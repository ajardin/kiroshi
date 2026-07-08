package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/config"
)

// defaultSearch is the suggested query offered when the user leaves the search
// step blank. It mirrors the example in the README. `involves:@me` casts the
// widest useful net — PRs you authored AND ones you're asked to review — which
// is what feeds both dashboard panes (the `tab` toggle splits them by author).
const defaultSearch = "is:pr is:open involves:@me archived:false"

// wizardStep enumerates the wizard's linear flow. The order is the field
// order: token, search, min reviews, refresh interval, then the three optional
// Jira steps, then a validating spinner-less wait, ending in either an error
// (recoverable) or done (quit + save).
type wizardStep int

const (
	stepToken wizardStep = iota
	stepSearch
	stepMinReviews
	stepRefresh
	stepJiraURL
	stepJiraEmail
	stepJiraToken
	stepValidating
	stepError
	stepDone
)

// WizardResult is the outcome of a wizard run, extracted from the final model
// by RunWizard. Completed is false when the user aborted (esc / ctrl+c); in
// that case the other fields are meaningless and nothing should be written.
type WizardResult struct {
	Completed       bool
	Token           string
	Search          string
	MinReviews      int
	RefreshInterval time.Duration
	JiraBaseURL     string
	JiraEmail       string
	JiraToken       string
}

// wizardValidateMsg carries the result of the live token check back into the
// Bubble Tea update loop.
type wizardValidateMsg struct {
	login string
	err   error
}

// WizardModel is the interactive config-setup form. It reuses the dashboard
// palette and a hand-rolled text buffer per step (mirroring the list's filter
// input) rather than pulling in bubbles/textinput. The token step is masked.
type WizardModel struct {
	step  wizardStep
	token string
	// search and minReviewsStr hold the raw typed buffers; empty means "accept
	// the placeholder default".
	search        string
	minReviewsStr string
	// refreshStr is the raw typed auto-refresh interval ("5m"); blank = disabled.
	refreshStr string
	// jira* hold the optional Jira config buffers. A blank jiraURL skips Jira
	// setup entirely (the email/token steps are not shown).
	jiraURL   string
	jiraEmail string
	jiraToken string

	// reconfigure marks a re-run over an existing config (seeded via
	// WithExistingConfig). The masked token steps can't show a prefilled
	// value, so the existing secrets are kept aside: a blank entry keeps
	// them, non-blank input replaces them. existingJiraURL backs the
	// blank-keeps / "-"-removes convention on the Jira URL step.
	reconfigure       bool
	existingToken     string
	existingJiraURL   string
	existingJiraToken string

	login     string // login resolved by a successful token validation
	errMsg    string // inline error shown on stepMinReviews / stepError
	spinFrame int    // animates the stepValidating spinner

	// validate performs the live token check. Injected so tests and the CLI
	// can supply their own (the CLI closes over a context + gh client).
	validate func(token string) (login string, err error)
	// validateJira performs the live Jira credential check, called only when the
	// user configured a Jira base URL. Injected for the same reason as validate.
	validateJira func(baseURL, email, token string) error

	width  int
	height int
}

// NewWizardModel builds a wizard that validates the GitHub token through
// validate and (when Jira is configured) the Jira credentials through
// validateJira.
func NewWizardModel(validate func(token string) (login string, err error), validateJira func(baseURL, email, token string) error) WizardModel {
	return WizardModel{step: stepToken, validate: validate, validateJira: validateJira}
}

// WithExistingConfig switches the wizard to reconfigure mode, seeding every
// step with cfg's current values. It reuses the same buffers the
// validation-failure retry path keeps, so the form re-walks prefilled. The
// two token buffers stay empty (they are masked, so a prefill would be
// unreadable); the existing secrets are kept aside and a blank entry keeps
// them.
func (m WizardModel) WithExistingConfig(cfg *config.Config) WizardModel {
	m.reconfigure = true
	m.existingToken = cfg.GitHubToken
	m.search = cfg.Search
	m.minReviewsStr = strconv.Itoa(cfg.MinReviews)
	if cfg.RefreshInterval > 0 {
		m.refreshStr = shortDuration(cfg.RefreshInterval)
	}
	m.jiraURL = cfg.JiraBaseURL
	m.jiraEmail = cfg.JiraEmail
	m.existingJiraURL = cfg.JiraBaseURL
	m.existingJiraToken = cfg.JiraToken
	return m
}

// Init implements tea.Model. The wizard has no startup command.
func (m WizardModel) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m WizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case spinMsg:
		// Self-terminating: only keep ticking while the validation runs.
		if m.step != stepValidating {
			return m, nil
		}
		m.spinFrame++
		return m, spinnerCmd()
	case wizardValidateMsg:
		if msg.err != nil {
			m.step = stepError
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.login = msg.login
		m.step = stepDone
		return m, tea.Quit
	case tea.PasteMsg:
		return m.handlePaste(msg.Content)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// handlePaste inserts pasted text into the active step's buffer. v1 delivered
// bracketed paste as a runes KeyMsg, so pasting a token or URL just worked;
// v2 emits a dedicated message. Non-input steps ignore paste.
func (m WizardModel) handlePaste(text string) (tea.Model, tea.Cmd) {
	switch m.step {
	case stepToken:
		m.token += text
	case stepSearch:
		m.search += text
	case stepMinReviews:
		m.minReviewsStr += text
		m.errMsg = ""
	case stepRefresh:
		m.refreshStr += text
		m.errMsg = ""
	case stepJiraURL:
		m.jiraURL += text
		m.errMsg = ""
	case stepJiraEmail:
		m.jiraEmail += text
	case stepJiraToken:
		m.jiraToken += text
	default:
	}
	return m, nil
}

func (m WizardModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		// Abort from anywhere: leave Completed false and quit.
		return m, tea.Quit
	}

	switch m.step {
	case stepToken:
		if msg.Code == tea.KeyEnter {
			// Reconfigure: a blank entry keeps the stored token (it still goes
			// through live validation, catching an expired token).
			if strings.TrimSpace(m.token) == "" && m.existingToken != "" {
				m.token = m.existingToken
			}
			m.step = stepSearch
			return m, nil
		}
		m.token = applyKey(m.token, msg)
		return m, nil
	case stepSearch:
		if msg.Code == tea.KeyEnter {
			m.step = stepMinReviews
			return m, nil
		}
		m.search = applyKey(m.search, msg)
		return m, nil
	case stepMinReviews:
		return m.handleMinReviewsKey(msg)
	case stepRefresh:
		return m.handleRefreshKey(msg)
	case stepJiraURL:
		if msg.Code == tea.KeyEnter {
			trimmed := strings.TrimSpace(m.jiraURL)
			if trimmed == "-" {
				// "-" sentinel: drop the whole Jira trio (existing values too, so
				// a validation-failure retry doesn't resurrect them) and validate.
				m.jiraURL, m.jiraEmail, m.jiraToken = "", "", ""
				m.existingJiraURL, m.existingJiraToken = "", ""
				m.step = stepValidating
				m.spinFrame = 0
				return m, tea.Batch(m.validateCmd(), spinnerCmd())
			}
			if trimmed == "" {
				// Reconfigure with Jira set up: blank keeps the current URL and
				// walks the remaining Jira steps ("-" is the way to remove it).
				if m.existingJiraURL != "" {
					m.jiraURL = m.existingJiraURL
					m.errMsg = ""
					m.step = stepJiraEmail
					return m, nil
				}
				// Blank URL = skip Jira entirely; go straight to validation.
				m.step = stepValidating
				m.spinFrame = 0
				return m, tea.Batch(m.validateCmd(), spinnerCmd())
			}
			// Basic auth ships email:token on every request; refuse a scheme
			// that would send them in cleartext (Jira Cloud is always https).
			if !strings.HasPrefix(trimmed, "https://") {
				m.errMsg = "URL must start with https://"
				return m, nil
			}
			m.errMsg = ""
			m.step = stepJiraEmail
			return m, nil
		}
		m.jiraURL = applyKey(m.jiraURL, msg)
		m.errMsg = ""
		return m, nil
	case stepJiraEmail:
		if msg.Code == tea.KeyEnter {
			m.step = stepJiraToken
			return m, nil
		}
		m.jiraEmail = applyKey(m.jiraEmail, msg)
		return m, nil
	case stepJiraToken:
		if msg.Code == tea.KeyEnter {
			// Reconfigure: blank keeps the stored Jira token (validated live,
			// same as the GitHub one).
			if strings.TrimSpace(m.jiraToken) == "" && m.existingJiraToken != "" {
				m.jiraToken = m.existingJiraToken
			}
			m.step = stepValidating
			m.spinFrame = 0
			return m, tea.Batch(m.validateCmd(), spinnerCmd())
		}
		m.jiraToken = applyKey(m.jiraToken, msg)
		return m, nil
	case stepError:
		// Any non-abort key returns to the token step to retry.
		m.step = stepToken
		m.errMsg = ""
		return m, nil
	default:
		return m, nil
	}
}

// trimLastRune removes the trailing rune from s — not the trailing byte, so
// backspacing over a multi-byte character (é, CJK, emoji) never leaves the
// buffer with invalid UTF-8.
func trimLastRune(s string) string {
	if s == "" {
		return s
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-size]
}

// applyKey edits a plain text buffer in response to a keypress: backspace
// trims the last rune, printable input appends. Anything else is a no-op —
// Key.Text is empty for special keys, so appending it covers that for free
// (it carries the space too, v1's separate KeySpace case).
func applyKey(buf string, msg tea.KeyPressMsg) string {
	switch msg.Code {
	case tea.KeyBackspace, tea.KeyDelete:
		return trimLastRune(buf)
	default:
		return buf + msg.Text
	}
}

func (m WizardModel) handleMinReviewsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEnter:
		if _, err := m.parsedMinReviews(); err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		m.errMsg = ""
		m.step = stepRefresh
		return m, nil
	case tea.KeyBackspace, tea.KeyDelete:
		if len(m.minReviewsStr) > 0 {
			m.minReviewsStr = trimLastRune(m.minReviewsStr)
			m.errMsg = ""
		}
		return m, nil
	default:
		// Bare space excluded to match v1, where space arrived as KeySpace
		// (not KeyRunes) and this handler dropped it.
		if msg.Text != "" && msg.Text != " " {
			m.minReviewsStr += msg.Text
			m.errMsg = ""
		}
		return m, nil
	}
}

func (m WizardModel) handleRefreshKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Code {
	case tea.KeyEnter:
		if _, err := m.parsedRefreshInterval(); err != nil {
			m.errMsg = err.Error()
			return m, nil
		}
		m.errMsg = ""
		m.step = stepJiraURL
		return m, nil
	case tea.KeyBackspace, tea.KeyDelete:
		if len(m.refreshStr) > 0 {
			m.refreshStr = trimLastRune(m.refreshStr)
			m.errMsg = ""
		}
		return m, nil
	default:
		// Same v1 space parity as handleMinReviewsKey.
		if msg.Text != "" && msg.Text != " " {
			m.refreshStr += msg.Text
			m.errMsg = ""
		}
		return m, nil
	}
}

// parsedRefreshInterval resolves the typed buffer: blank disables auto-refresh
// (zero), otherwise it must parse as a non-negative Go duration ("5m", "1h").
func (m WizardModel) parsedRefreshInterval() (time.Duration, error) {
	s := strings.TrimSpace(m.refreshStr)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("interval must be a duration like 5m or 1h")
	}
	if d < 0 {
		return 0, fmt.Errorf("interval must be >= 0")
	}
	return d, nil
}

// parsedMinReviews resolves the typed buffer: empty falls back to the package
// default, otherwise it must parse as an integer >= 0.
func (m WizardModel) parsedMinReviews() (int, error) {
	s := strings.TrimSpace(m.minReviewsStr)
	if s == "" {
		return config.DefaultMinReviews, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("min reviews must be a whole number")
	}
	if n < 0 {
		return 0, fmt.Errorf("min reviews must be >= 0")
	}
	return n, nil
}

// resolvedSearch returns the typed query, or the suggested default when blank.
func (m WizardModel) resolvedSearch() string {
	if s := strings.TrimSpace(m.search); s != "" {
		return s
	}
	return defaultSearch
}

func (m WizardModel) validateCmd() tea.Cmd {
	token, validate := m.token, m.validate
	jiraURL, jiraEmail, jiraToken, validateJira := strings.TrimSpace(m.jiraURL), m.jiraEmail, m.jiraToken, m.validateJira
	return func() tea.Msg {
		login, err := validate(token)
		if err != nil {
			return wizardValidateMsg{err: err}
		}
		if jiraURL != "" {
			if jerr := validateJira(jiraURL, jiraEmail, jiraToken); jerr != nil {
				return wizardValidateMsg{err: fmt.Errorf("jira: %w", jerr)}
			}
		}
		return wizardValidateMsg{login: login}
	}
}

// result extracts the WizardResult from a (possibly aborted) model.
func (m WizardModel) result() WizardResult {
	if m.step != stepDone {
		return WizardResult{Completed: false}
	}
	mr, _ := m.parsedMinReviews()      // already validated before leaving stepMinReviews
	ri, _ := m.parsedRefreshInterval() // already validated before leaving stepRefresh
	return WizardResult{
		Completed:       true,
		Token:           strings.TrimSpace(m.token),
		Search:          m.resolvedSearch(),
		MinReviews:      mr,
		RefreshInterval: ri,
		JiraBaseURL:     strings.TrimSpace(m.jiraURL),
		JiraEmail:       strings.TrimSpace(m.jiraEmail),
		JiraToken:       strings.TrimSpace(m.jiraToken),
	}
}

// View implements tea.Model. Like the dashboard's View, it declares the
// altscreen request on the returned view (a program option until v1).
func (m WizardModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

// render composes the wizard frame as a styled string.
func (m WizardModel) render() string {
	title := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("kiroshi setup")
	subText := "Let's create your config file."
	if m.reconfigure {
		subText = "Let's update your config file."
	}
	sub := lipgloss.NewStyle().Foreground(colDim).Render(subText)

	// fieldView wraps the placeholder in "(default: …)", so the reconfigure
	// hints are phrased to read as the blank-input outcome.
	tokenHint := "fine-grained PAT, read-only: Pull requests / Contents / Members" //nolint:gosec // G101: helper text, not a credential
	if m.existingToken != "" {
		tokenHint = "keep the current token"
	}
	jiraURLHint := "https://acme.atlassian.net · blank to skip"
	if m.existingJiraURL != "" {
		jiraURLHint = `keep current · type "-" to remove Jira`
	}
	jiraTokenHint := "id.atlassian.com/manage-profile/security/api-tokens" //nolint:gosec // G101: helper text, not a credential
	if m.existingJiraToken != "" {
		jiraTokenHint = "keep the current token"
	}

	var body string
	switch m.step {
	case stepToken:
		body = m.fieldView("1/7", "GitHub token", maskValue(m.token), tokenHint, "")
	case stepSearch:
		body = m.fieldView("2/7", "Search query", m.search, defaultSearch, "")
	case stepMinReviews:
		body = m.fieldView("3/7", "Minimum approvals to ship", m.minReviewsStr, strconv.Itoa(config.DefaultMinReviews), m.errMsg)
	case stepRefresh:
		body = m.fieldView("4/7", "Auto-refresh interval (optional)", m.refreshStr, "e.g. 5m · blank to disable", m.errMsg)
	case stepJiraURL:
		body = m.fieldView("5/7", "Jira base URL (optional)", m.jiraURL, jiraURLHint, m.errMsg)
	case stepJiraEmail:
		body = m.fieldView("6/7", "Jira account email", m.jiraEmail, "you@acme.com", "")
	case stepJiraToken:
		body = m.fieldView("7/7", "Jira API token", maskValue(m.jiraToken), jiraTokenHint, "")
	case stepValidating:
		frame := spinFrames[m.spinFrame%len(spinFrames)]
		body = lipgloss.NewStyle().Foreground(colCyan).Render(frame + " Validating token with GitHub…")
	case stepError:
		head := lipgloss.NewStyle().Foreground(colRed).Bold(true).Render("✗ validation failed")
		detail := lipgloss.NewStyle().Foreground(colText).Render(m.errMsg)
		hint := lipgloss.NewStyle().Foreground(colDim).Render("press any key to start over · esc to quit")
		body = head + "\n" + detail + "\n\n" + hint
	case stepDone:
		body = lipgloss.NewStyle().Foreground(colGreen).Render(fmt.Sprintf("✓ validated as @%s", m.login))
	}

	footer := lipgloss.NewStyle().Foreground(colMuted).Render("enter to continue · esc to cancel")
	return "\n " + title + "  " + sub + "\n\n " + indentBlock(body, " ") + "\n\n " + footer + "\n"
}

// fieldView renders a single prompt: a step counter, label, the current value
// with a trailing cursor (or a muted placeholder when empty), and an optional
// inline error.
func (m WizardModel) fieldView(step, label, value, placeholder, errMsg string) string {
	stepTag := lipgloss.NewStyle().Foreground(colCyan).Render("[" + step + "]")
	lbl := lipgloss.NewStyle().Foreground(colText).Bold(true).Render(label)

	var val string
	if value == "" {
		ph := lipgloss.NewStyle().Foreground(colMuted).Render(placeholder)
		cursor := lipgloss.NewStyle().Foreground(colText).Render("_")
		val = cursor + "  " + lipgloss.NewStyle().Foreground(colMuted).Render("(default: ") + ph + lipgloss.NewStyle().Foreground(colMuted).Render(")")
	} else {
		val = lipgloss.NewStyle().Foreground(colText).Render(value + "_")
	}

	out := stepTag + " " + lbl + "\n " + val
	if errMsg != "" {
		out += "\n " + lipgloss.NewStyle().Foreground(colRed).Render("✗ "+errMsg)
	}
	return out
}

// maskValue renders a secret as bullets so the token never appears on screen.
func maskValue(s string) string {
	if s == "" {
		return ""
	}
	return strings.Repeat("•", len([]rune(s)))
}

// RunWizard executes the setup wizard to completion against in/out and returns
// the collected result. It mirrors Run, but recovers the final model so the
// caller can read the values the user entered.
func RunWizard(m WizardModel, in io.Reader, out io.Writer) (WizardResult, error) {
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out))
	final, err := p.Run()
	if err != nil {
		return WizardResult{}, err
	}
	fm, ok := final.(WizardModel)
	if !ok {
		return WizardResult{}, fmt.Errorf("unexpected wizard model type %T", final)
	}
	return fm.result(), nil
}
