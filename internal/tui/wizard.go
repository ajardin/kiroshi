package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/config"
)

// defaultSearch is the suggested query offered when the user leaves the search
// step blank. It mirrors the example in the README and matches the most common
// "my own open PRs" case.
const defaultSearch = "is:pr is:open author:@me archived:false"

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

	login  string // login resolved by a successful token validation
	errMsg string // inline error shown on stepMinReviews / stepError

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

// Init implements tea.Model. The wizard has no startup command.
func (m WizardModel) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (m WizardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case wizardValidateMsg:
		if msg.err != nil {
			m.step = stepError
			m.errMsg = msg.err.Error()
			return m, nil
		}
		m.login = msg.login
		m.step = stepDone
		return m, tea.Quit
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m WizardModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		// Abort from anywhere: leave Completed false and quit.
		return m, tea.Quit
	default:
	}

	switch m.step {
	case stepToken:
		if msg.Type == tea.KeyEnter {
			m.step = stepSearch
			return m, nil
		}
		m.token = applyKey(m.token, msg)
		return m, nil
	case stepSearch:
		if msg.Type == tea.KeyEnter {
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
		if msg.Type == tea.KeyEnter {
			if strings.TrimSpace(m.jiraURL) == "" {
				// Blank URL = skip Jira entirely; go straight to validation.
				m.step = stepValidating
				return m, m.validateCmd()
			}
			m.step = stepJiraEmail
			return m, nil
		}
		m.jiraURL = applyKey(m.jiraURL, msg)
		return m, nil
	case stepJiraEmail:
		if msg.Type == tea.KeyEnter {
			m.step = stepJiraToken
			return m, nil
		}
		m.jiraEmail = applyKey(m.jiraEmail, msg)
		return m, nil
	case stepJiraToken:
		if msg.Type == tea.KeyEnter {
			m.step = stepValidating
			return m, m.validateCmd()
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

// applyKey edits a plain text buffer in response to a keypress: backspace
// trims the last byte, runes and space append. Anything else is a no-op.
func applyKey(buf string, msg tea.KeyMsg) string {
	switch msg.Type {
	case tea.KeyBackspace, tea.KeyDelete:
		if len(buf) > 0 {
			return buf[:len(buf)-1]
		}
		return buf
	case tea.KeyRunes:
		return buf + string(msg.Runes)
	case tea.KeySpace:
		return buf + " "
	default:
		return buf
	}
}

func (m WizardModel) handleMinReviewsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
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
			m.minReviewsStr = m.minReviewsStr[:len(m.minReviewsStr)-1]
			m.errMsg = ""
		}
		return m, nil
	case tea.KeyRunes:
		m.minReviewsStr += string(msg.Runes)
		m.errMsg = ""
		return m, nil
	default:
		return m, nil
	}
}

func (m WizardModel) handleRefreshKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
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
			m.refreshStr = m.refreshStr[:len(m.refreshStr)-1]
			m.errMsg = ""
		}
		return m, nil
	case tea.KeyRunes:
		m.refreshStr += string(msg.Runes)
		m.errMsg = ""
		return m, nil
	default:
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

// View implements tea.Model.
func (m WizardModel) View() string {
	title := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("kiroshi setup")
	sub := lipgloss.NewStyle().Foreground(colDim).Render("Let's create your config file.")

	var body string
	switch m.step {
	case stepToken:
		body = m.fieldView("1/7", "GitHub token", maskValue(m.token), "scopes: repo, read:org", "")
	case stepSearch:
		body = m.fieldView("2/7", "Search query", m.search, defaultSearch, "")
	case stepMinReviews:
		body = m.fieldView("3/7", "Minimum approvals to ship", m.minReviewsStr, strconv.Itoa(config.DefaultMinReviews), m.errMsg)
	case stepRefresh:
		body = m.fieldView("4/7", "Auto-refresh interval (optional)", m.refreshStr, "e.g. 5m · blank to disable", m.errMsg)
	case stepJiraURL:
		body = m.fieldView("5/7", "Jira base URL (optional)", m.jiraURL, "https://acme.atlassian.net · blank to skip", "")
	case stepJiraEmail:
		body = m.fieldView("6/7", "Jira account email", m.jiraEmail, "you@acme.com", "")
	case stepJiraToken:
		body = m.fieldView("7/7", "Jira API token", maskValue(m.jiraToken), "id.atlassian.com/manage-profile/security/api-tokens", "")
	case stepValidating:
		body = lipgloss.NewStyle().Foreground(colCyan).Render("Validating token with GitHub…")
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
	p := tea.NewProgram(m, tea.WithInput(in), tea.WithOutput(out), tea.WithAltScreen())
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
