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

// defaultSearch applies when the search step is left blank. `involves:@me`
// covers both panes: PRs you authored and PRs you are asked to review.
const defaultSearch = "is:pr is:open involves:@me archived:false"

// wizardStep enumerates the wizard's linear flow: the input steps in field
// order, then validation, ending in a recoverable error or done.
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

// inputSteps is the number of steps the user types into, for the "[n/N]" tag.
const inputSteps = int(stepJiraToken) + 1

// WizardResult is the outcome of a wizard run. Completed is false when the
// user aborted, and then nothing should be written.
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

type wizardValidateMsg struct {
	login string
	err   error
}

// WizardModel is the interactive config-setup form. Like the dashboard's
// filter it hand-rolls one text buffer per step rather than pulling in
// bubbles/textinput.
type WizardModel struct {
	step wizardStep
	// The raw typed buffers; blank accepts the placeholder default, and a blank
	// jiraURL skips the other Jira steps.
	token         string
	search        string
	minReviewsStr string
	refreshStr    string
	jiraURL       string
	jiraEmail     string
	jiraToken     string

	// reconfigure marks a re-run over an existing config. The masked token
	// steps can't show a prefilled value, so the existing secrets are kept
	// aside: a blank entry keeps them.
	reconfigure       bool
	existingToken     string
	existingJiraURL   string
	existingJiraToken string

	login     string
	errMsg    string
	spinFrame int

	validate     func(token string) (login string, err error)
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
// step but the masked token ones with cfg's current values.
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

// Init implements tea.Model.
func (m WizardModel) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m WizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case spinMsg:
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

// handlePaste appends pasted text to the active step's buffer.
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
	}
	return m, nil
}

func (m WizardModel) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc":
		return m, tea.Quit
	}

	switch m.step {
	case stepToken:
		if msg.Code == tea.KeyEnter {
			// A kept token is still validated live, catching an expired one.
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
		var done bool
		m.minReviewsStr, m.errMsg, done = editNumeric(m.minReviewsStr, m.errMsg, msg, checked(parseMinReviews))
		if done {
			m.step = stepRefresh
		}
		return m, nil
	case stepRefresh:
		var done bool
		m.refreshStr, m.errMsg, done = editNumeric(m.refreshStr, m.errMsg, msg, checked(parseRefreshInterval))
		if done {
			m.step = stepJiraURL
		}
		return m, nil
	case stepJiraURL:
		if msg.Code == tea.KeyEnter {
			trimmed := strings.TrimSpace(m.jiraURL)
			if trimmed == "-" {
				// Removing Jira drops the kept values too, so a retry after a
				// failed validation doesn't resurrect them.
				m.jiraURL, m.jiraEmail, m.jiraToken = "", "", ""
				m.existingJiraURL, m.existingJiraToken = "", ""
				return m.startValidation()
			}
			if trimmed == "" {
				if m.existingJiraURL != "" {
					m.jiraURL = m.existingJiraURL
					m.errMsg = ""
					m.step = stepJiraEmail
					return m, nil
				}
				return m.startValidation()
			}
			if err := config.ValidateJiraBaseURL(trimmed); err != nil {
				m.errMsg = err.Error()
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
			if strings.TrimSpace(m.jiraToken) == "" && m.existingJiraToken != "" {
				m.jiraToken = m.existingJiraToken
			}
			return m.startValidation()
		}
		m.jiraToken = applyKey(m.jiraToken, msg)
		return m, nil
	case stepError:
		m.step = stepToken
		m.errMsg = ""
		return m, nil
	default:
		return m, nil
	}
}

// startValidation checks the credentials live before anything is written.
func (m WizardModel) startValidation() (tea.Model, tea.Cmd) {
	m.step = stepValidating
	m.spinFrame = 0
	return m, tea.Batch(m.validateCmd(), spinnerCmd())
}

// trimLastRune removes the trailing rune, not byte, so backspacing over é or
// an emoji never leaves invalid UTF-8.
func trimLastRune(s string) string {
	if s == "" {
		return s
	}
	_, size := utf8.DecodeLastRuneInString(s)
	return s[:len(s)-size]
}

// applyKey edits a text buffer: backspace trims the last rune, printable
// input appends (Key.Text is empty for special keys).
func applyKey(buf string, msg tea.KeyPressMsg) string {
	switch msg.Code {
	case tea.KeyBackspace, tea.KeyDelete:
		return trimLastRune(buf)
	default:
		return buf + msg.Text
	}
}

// editNumeric applies msg to buf, the buffer of a step whose value never
// contains a space. Enter reports done only when check accepts buf; otherwise
// the error shows inline.
func editNumeric(buf, errMsg string, msg tea.KeyPressMsg, check func(string) error) (newBuf, newErr string, done bool) {
	switch msg.Code {
	case tea.KeyEnter:
		if err := check(buf); err != nil {
			return buf, err.Error(), false
		}
		return buf, "", true
	case tea.KeyBackspace, tea.KeyDelete:
		if buf != "" {
			return trimLastRune(buf), "", false
		}
	default:
		if msg.Text != "" && msg.Text != " " {
			return buf + msg.Text, "", false
		}
	}
	return buf, errMsg, false
}

// checked adapts a parser into editNumeric's check.
func checked[T any](parse func(string) (T, error)) func(string) error {
	return func(s string) error {
		_, err := parse(s)
		return err
	}
}

// parseRefreshInterval treats a blank buffer as disabled (zero).
func parseRefreshInterval(buf string) (time.Duration, error) {
	s := strings.TrimSpace(buf)
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

// parseMinReviews treats a blank buffer as config.DefaultMinReviews.
func parseMinReviews(buf string) (int, error) {
	s := strings.TrimSpace(buf)
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

func (m WizardModel) resolvedSearch() string {
	if s := strings.TrimSpace(m.search); s != "" {
		return s
	}
	return defaultSearch
}

// validateCmd checks the values result will write, trimmed: a pasted token
// with a trailing newline must not fail here and then be saved anyway.
func (m WizardModel) validateCmd() tea.Cmd {
	res, validate, validateJira := m.values(), m.validate, m.validateJira
	return func() tea.Msg {
		login, err := validate(res.Token)
		if err != nil {
			return wizardValidateMsg{err: err}
		}
		if res.JiraBaseURL != "" {
			if jerr := validateJira(res.JiraBaseURL, res.JiraEmail, res.JiraToken); jerr != nil {
				return wizardValidateMsg{err: fmt.Errorf("jira: %w", jerr)}
			}
		}
		return wizardValidateMsg{login: login}
	}
}

// result extracts the WizardResult from a possibly aborted model.
func (m WizardModel) result() WizardResult {
	if m.step != stepDone {
		return WizardResult{}
	}
	res := m.values()
	res.Completed = true
	return res
}

// values resolves the typed buffers. The numeric ones were validated before
// leaving their steps.
func (m WizardModel) values() WizardResult {
	mr, _ := parseMinReviews(m.minReviewsStr)
	ri, _ := parseRefreshInterval(m.refreshStr)
	return WizardResult{
		Token:           strings.TrimSpace(m.token),
		Search:          m.resolvedSearch(),
		MinReviews:      mr,
		RefreshInterval: ri,
		JiraBaseURL:     strings.TrimSpace(m.jiraURL),
		JiraEmail:       strings.TrimSpace(m.jiraEmail),
		JiraToken:       strings.TrimSpace(m.jiraToken),
	}
}

// View implements tea.Model.
func (m WizardModel) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	return v
}

func (m WizardModel) render() string {
	title := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("kiroshi setup")
	subText := "Let's create your config file."
	if m.reconfigure {
		subText = "Let's update your config file."
	}
	sub := lipgloss.NewStyle().Foreground(colDim).Render(subText)

	// fieldView wraps the hint in "(default: …)", so it reads as the outcome
	// of a blank entry.
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
		body = m.fieldView("GitHub token", maskValue(m.token), tokenHint)
	case stepSearch:
		body = m.fieldView("Search query", m.search, defaultSearch)
	case stepMinReviews:
		body = m.fieldView("Minimum approvals to ship", m.minReviewsStr, strconv.Itoa(config.DefaultMinReviews))
	case stepRefresh:
		body = m.fieldView("Auto-refresh interval (optional)", m.refreshStr, "e.g. 5m · blank to disable")
	case stepJiraURL:
		body = m.fieldView("Jira base URL (optional)", m.jiraURL, jiraURLHint)
	case stepJiraEmail:
		body = m.fieldView("Jira account email", m.jiraEmail, "you@acme.com")
	case stepJiraToken:
		body = m.fieldView("Jira API token", maskValue(m.jiraToken), jiraTokenHint)
	case stepValidating:
		frame := spinFrames[m.spinFrame%len(spinFrames)]
		label := "Validating token with GitHub…"
		if strings.TrimSpace(m.jiraURL) != "" {
			label = "Validating credentials with GitHub and Jira…"
		}
		body = lipgloss.NewStyle().Foreground(colCyan).Render(frame + " " + label)
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

// fieldView renders the current step's prompt: the value with a cursor, or
// the placeholder when empty, then any inline error.
func (m WizardModel) fieldView(label, value, placeholder string) string {
	stepTag := lipgloss.NewStyle().Foreground(colCyan).Render(fmt.Sprintf("[%d/%d]", int(m.step)+1, inputSteps))
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
	if m.errMsg != "" {
		out += "\n " + lipgloss.NewStyle().Foreground(colRed).Render("✗ "+m.errMsg)
	}
	return out
}

// maskValue renders a secret as bullets.
func maskValue(s string) string {
	if s == "" {
		return ""
	}
	return strings.Repeat("•", len([]rune(s)))
}

// RunWizard executes the setup wizard to completion against in and out.
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
