package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ajardin/kiroshi/internal/config"
)

// defaultSearch is the suggested query offered when the user leaves the search
// step blank. It mirrors the example in the README and matches the most common
// "my own open PRs" case.
const defaultSearch = "is:pr is:open author:@me archived:false"

// wizardStep enumerates the wizard's linear flow. The order is the field
// order: token, then search, then min reviews, then a validating spinner-less
// wait, ending in either an error (recoverable) or done (quit + save).
type wizardStep int

const (
	stepToken wizardStep = iota
	stepSearch
	stepMinReviews
	stepValidating
	stepError
	stepDone
)

// WizardResult is the outcome of a wizard run, extracted from the final model
// by RunWizard. Completed is false when the user aborted (esc / ctrl+c); in
// that case the other fields are meaningless and nothing should be written.
type WizardResult struct {
	Completed  bool
	Token      string
	Search     string
	MinReviews int
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

	login  string // login resolved by a successful token validation
	errMsg string // inline error shown on stepMinReviews / stepError

	// validate performs the live token check. Injected so tests and the CLI
	// can supply their own (the CLI closes over a context + gh client).
	validate func(token string) (login string, err error)

	width  int
	height int
}

// NewWizardModel builds a wizard that validates tokens through validate.
func NewWizardModel(validate func(token string) (login string, err error)) WizardModel {
	return WizardModel{step: stepToken, validate: validate}
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
		m.step = stepValidating
		return m, m.validateCmd()
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
	return func() tea.Msg {
		login, err := validate(token)
		return wizardValidateMsg{login: login, err: err}
	}
}

// result extracts the WizardResult from a (possibly aborted) model.
func (m WizardModel) result() WizardResult {
	if m.step != stepDone {
		return WizardResult{Completed: false}
	}
	mr, _ := m.parsedMinReviews() // already validated before leaving stepMinReviews
	return WizardResult{
		Completed:  true,
		Token:      strings.TrimSpace(m.token),
		Search:     m.resolvedSearch(),
		MinReviews: mr,
	}
}

// View implements tea.Model.
func (m WizardModel) View() string {
	title := lipgloss.NewStyle().Foreground(colYellow).Bold(true).Render("kiroshi setup")
	sub := lipgloss.NewStyle().Foreground(colDim).Render("Let's create your config file.")

	var body string
	switch m.step {
	case stepToken:
		body = m.fieldView("1/3", "GitHub token", maskValue(m.token), "scopes: repo, read:org", "")
	case stepSearch:
		body = m.fieldView("2/3", "Search query", m.search, defaultSearch, "")
	case stepMinReviews:
		body = m.fieldView("3/3", "Minimum approvals to ship", m.minReviewsStr, strconv.Itoa(config.DefaultMinReviews), m.errMsg)
	case stepValidating:
		body = lipgloss.NewStyle().Foreground(colCyan).Render("Validating token with GitHub…")
	case stepError:
		head := lipgloss.NewStyle().Foreground(colRed).Bold(true).Render("✗ token rejected")
		detail := lipgloss.NewStyle().Foreground(colText).Render(m.errMsg)
		hint := lipgloss.NewStyle().Foreground(colDim).Render("press any key to re-enter the token · esc to quit")
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
