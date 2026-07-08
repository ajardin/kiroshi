package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/ajardin/kiroshi/internal/config"
)

// send applies a key message and unwraps the result back to a WizardModel,
// running the returned command synchronously so msg-producing cmds (the token
// validation) feed back through Update — mirroring applyCmd for the dashboard.
func send(t *testing.T, m WizardModel, msg tea.Msg) (WizardModel, tea.Cmd) {
	t.Helper()
	// Validation now batches the validate cmd with the spinner tick; unwrap and
	// apply each sub-cmd so the wizardValidateMsg still feeds back through Update.
	if batch, ok := msg.(tea.BatchMsg); ok {
		var last tea.Cmd
		for _, c := range batch {
			if c == nil {
				continue
			}
			m, last = send(t, m, c())
		}
		return m, last
	}
	updated, cmd := m.Update(msg)
	out, ok := updated.(WizardModel)
	if !ok {
		t.Fatalf("Update returned %T, want WizardModel", updated)
	}
	return out, cmd
}

func TestApplyKey_BackspaceTrimsRuneNotByte(t *testing.T) {
	t.Parallel()

	got := applyKey("café", tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got != "caf" {
		t.Errorf("applyKey backspace = %q, want %q (must trim the whole rune)", got, "caf")
	}
	if got = applyKey("", tea.KeyPressMsg{Code: tea.KeyBackspace}); got != "" {
		t.Errorf("applyKey backspace on empty = %q, want empty", got)
	}
}

func typeRunes(t *testing.T, m WizardModel, s string) WizardModel {
	t.Helper()
	m, _ = send(t, m, tea.KeyPressMsg{Text: s})
	return m
}

func enter(t *testing.T, m WizardModel) (WizardModel, tea.Cmd) {
	t.Helper()
	return send(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
}

func okValidator(string) (string, error) { return "octocat", nil }

func okJiraValidator(_, _, _ string) error { return nil }

func TestWizard_HappyPath(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	m = typeRunes(t, m, "ghp_token")
	m, _ = enter(t, m) // -> search

	m = typeRunes(t, m, "is:pr author:@me")
	m, _ = enter(t, m) // -> min reviews

	m = typeRunes(t, m, "3")
	m, _ = enter(t, m) // -> refresh interval

	m, _ = enter(t, m) // refresh left blank -> jira url

	m, cmd := enter(t, m) // jira url left blank -> validating, fires validateCmd
	if m.step != stepValidating {
		t.Fatalf("step = %v, want stepValidating", m.step)
	}
	if cmd == nil {
		t.Fatal("expected a validation command")
	}

	// Run the validation command and feed its message back in.
	m, _ = send(t, m, cmd())
	if m.step != stepDone {
		t.Fatalf("step = %v, want stepDone", m.step)
	}

	res := m.result()
	if !res.Completed {
		t.Fatal("result not completed")
	}
	if res.Token != "ghp_token" || res.Search != "is:pr author:@me" || res.MinReviews != 3 {
		t.Errorf("unexpected result: %+v", res)
	}
	if res.JiraBaseURL != "" || res.JiraEmail != "" || res.JiraToken != "" {
		t.Errorf("expected blank Jira config, got %+v", res)
	}
}

func TestWizard_JiraConfigured(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	m = typeRunes(t, m, "ghp_token")
	m, _ = enter(t, m) // -> search
	m, _ = enter(t, m) // search blank -> min reviews
	m, _ = enter(t, m) // min reviews blank -> refresh interval
	m, _ = enter(t, m) // refresh blank -> jira url

	m = typeRunes(t, m, "https://acme.atlassian.net")
	m, _ = enter(t, m) // -> jira email
	if m.step != stepJiraEmail {
		t.Fatalf("step = %v, want stepJiraEmail", m.step)
	}

	m = typeRunes(t, m, "me@acme.com")
	m, _ = enter(t, m) // -> jira token

	m = typeRunes(t, m, "jira-secret")
	m, cmd := enter(t, m) // -> validating
	if m.step != stepValidating {
		t.Fatalf("step = %v, want stepValidating", m.step)
	}
	m, _ = send(t, m, cmd())

	res := m.result()
	if res.JiraBaseURL != "https://acme.atlassian.net" || res.JiraEmail != "me@acme.com" || res.JiraToken != "jira-secret" {
		t.Errorf("unexpected Jira config: %+v", res)
	}
}

func TestWizard_JiraURLRequiresHTTPS(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	m = typeRunes(t, m, "ghp_token")
	m, _ = enter(t, m) // -> search
	m, _ = enter(t, m) // search blank -> min reviews
	m, _ = enter(t, m) // min reviews blank -> refresh interval
	m, _ = enter(t, m) // refresh blank -> jira url

	m = typeRunes(t, m, "http://acme.atlassian.net")
	m, _ = enter(t, m)
	if m.step != stepJiraURL {
		t.Fatalf("step = %v, want to stay on stepJiraURL for an http URL", m.step)
	}
	if !strings.Contains(m.errMsg, "https") {
		t.Errorf("errMsg = %q, want an https hint", m.errMsg)
	}
	if !strings.Contains(m.View().Content, "https") {
		t.Error("view should surface the https error inline")
	}
}

func TestWizard_JiraValidationFails(t *testing.T) {
	t.Parallel()

	badJira := func(_, _, _ string) error { return errors.New("jira unauthorized") }
	m := NewWizardModel(okValidator, badJira)
	m = typeRunes(t, m, "ghp_token")
	m, _ = enter(t, m) // -> search
	m, _ = enter(t, m) // -> min reviews
	m, _ = enter(t, m) // -> refresh interval
	m, _ = enter(t, m) // -> jira url

	m = typeRunes(t, m, "https://acme.atlassian.net")
	m, _ = enter(t, m) // -> jira email
	m = typeRunes(t, m, "me@acme.com")
	m, _ = enter(t, m) // -> jira token
	m = typeRunes(t, m, "bad-token")
	m, cmd := enter(t, m) // -> validating

	m, _ = send(t, m, cmd())
	if m.step != stepError {
		t.Fatalf("step = %v, want stepError", m.step)
	}
	if !strings.Contains(m.errMsg, "jira") {
		t.Errorf("errMsg = %q, want it to mention jira", m.errMsg)
	}
}

func TestWizard_TokenStepShowsLeastPrivilegeHint(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	view := m.View().Content
	if !strings.Contains(view, "fine-grained PAT") {
		t.Errorf("token step view should hint at a fine-grained PAT, got %q", view)
	}
	if !strings.Contains(view, "Pull requests / Contents / Members") {
		t.Errorf("token step view should list the read-only permissions, got %q", view)
	}
}

func TestWizard_JiraTokenIsMasked(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	m.step = stepJiraToken
	m = typeRunes(t, m, "jira-secret")
	view := m.View().Content
	if strings.Contains(view, "jira-secret") {
		t.Error("raw Jira token leaked into the view")
	}
}

func TestWizard_BlankFieldsUseDefaults(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	m = typeRunes(t, m, "tok")
	m, _ = enter(t, m)    // token -> search
	m, _ = enter(t, m)    // search left blank -> min reviews
	m, _ = enter(t, m)    // min reviews left blank -> refresh
	m, _ = enter(t, m)    // refresh left blank -> jira url
	m, cmd := enter(t, m) // jira url left blank -> validating
	m, _ = send(t, m, cmd())

	res := m.result()
	if res.Search != defaultSearch {
		t.Errorf("search = %q, want default %q", res.Search, defaultSearch)
	}
	if res.MinReviews != 2 {
		t.Errorf("min reviews = %d, want default 2", res.MinReviews)
	}
	if res.RefreshInterval != 0 {
		t.Errorf("refresh interval = %v, want default 0 (disabled)", res.RefreshInterval)
	}
}

func TestWizard_RefreshIntervalParsed(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	m = typeRunes(t, m, "tok")
	m, _ = enter(t, m) // -> search
	m, _ = enter(t, m) // -> min reviews
	m, _ = enter(t, m) // -> refresh
	m = typeRunes(t, m, "5m")
	m, _ = enter(t, m)    // -> jira url
	m, cmd := enter(t, m) // jira url blank -> validating
	m, _ = send(t, m, cmd())

	if res := m.result(); res.RefreshInterval != 5*time.Minute {
		t.Errorf("refresh interval = %v, want 5m", res.RefreshInterval)
	}
}

func TestWizard_InvalidRefreshStays(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	m = typeRunes(t, m, "tok")
	m, _ = enter(t, m) // -> search
	m, _ = enter(t, m) // -> min reviews
	m, _ = enter(t, m) // -> refresh
	m = typeRunes(t, m, "abc")
	m, cmd := enter(t, m)

	if m.step != stepRefresh {
		t.Errorf("step = %v, want to stay on stepRefresh", m.step)
	}
	if cmd != nil {
		t.Error("no validation command should fire on invalid input")
	}
	if m.errMsg == "" {
		t.Error("expected an inline error message")
	}
}

func TestWizard_InvalidMinReviewsStays(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	m = typeRunes(t, m, "tok")
	m, _ = enter(t, m)
	m, _ = enter(t, m) // -> min reviews
	m = typeRunes(t, m, "abc")
	m, cmd := enter(t, m)

	if m.step != stepMinReviews {
		t.Errorf("step = %v, want to stay on stepMinReviews", m.step)
	}
	if cmd != nil {
		t.Error("no validation command should fire on invalid input")
	}
	if m.errMsg == "" {
		t.Error("expected an inline error message")
	}
}

func TestWizard_TokenRejectedRecovers(t *testing.T) {
	t.Parallel()

	boom := func(string) (string, error) { return "", errors.New("bad credentials") }
	m := NewWizardModel(boom, okJiraValidator)
	m = typeRunes(t, m, "nope")
	m, _ = enter(t, m)       // -> search
	m, _ = enter(t, m)       // -> min reviews
	m, _ = enter(t, m)       // -> refresh
	m, _ = enter(t, m)       // -> jira url
	m, cmd := enter(t, m)    // jira url blank -> validating
	m, _ = send(t, m, cmd()) // validation fails

	if m.step != stepError {
		t.Fatalf("step = %v, want stepError", m.step)
	}
	if !strings.Contains(m.errMsg, "bad credentials") {
		t.Errorf("errMsg = %q, want it to mention the failure", m.errMsg)
	}

	// Any key returns to the token step to retry.
	m, _ = send(t, m, tea.KeyPressMsg{Text: "x"})
	if m.step != stepToken {
		t.Errorf("step = %v, want stepToken after retry", m.step)
	}
}

// backspaceAll erases the whole current buffer by sending backspaces, the way
// a user would blank a seeded field.
func backspaceAll(t *testing.T, m WizardModel, n int) WizardModel {
	t.Helper()
	for range n {
		m, _ = send(t, m, tea.KeyPressMsg{Code: tea.KeyBackspace})
	}
	return m
}

func existingConfig() *config.Config {
	return &config.Config{
		GitHubToken:     "ghp_old",
		Search:          "is:pr involves:@me",
		MinReviews:      3,
		RefreshInterval: 5 * time.Minute,
		JiraBaseURL:     "https://acme.atlassian.net",
		JiraEmail:       "me@acme.com",
		JiraToken:       "jira-old",
	}
}

func TestWizard_ReconfigureBlankInputsKeepEverything(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator).WithExistingConfig(existingConfig())
	m, _ = enter(t, m)    // token blank -> keep ghp_old
	m, _ = enter(t, m)    // search (seeded) -> min reviews
	m, _ = enter(t, m)    // min reviews (seeded) -> refresh
	m, _ = enter(t, m)    // refresh (seeded) -> jira url
	m, _ = enter(t, m)    // jira url (seeded) -> jira email
	m, _ = enter(t, m)    // jira email (seeded) -> jira token
	m, cmd := enter(t, m) // jira token blank -> keep jira-old, validating
	m, _ = send(t, m, cmd())

	res := m.result()
	if !res.Completed {
		t.Fatal("result not completed")
	}
	want := existingConfig()
	if res.Token != want.GitHubToken {
		t.Errorf("token = %q, want kept %q", res.Token, want.GitHubToken)
	}
	if res.Search != want.Search || res.MinReviews != want.MinReviews || res.RefreshInterval != want.RefreshInterval {
		t.Errorf("seeded values not kept: %+v", res)
	}
	if res.JiraBaseURL != want.JiraBaseURL || res.JiraEmail != want.JiraEmail || res.JiraToken != want.JiraToken {
		t.Errorf("jira values not kept: %+v", res)
	}
}

func TestWizard_ReconfigureNewTokenReplaces(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator).WithExistingConfig(existingConfig())
	m = typeRunes(t, m, "ghp_new")
	m, _ = enter(t, m)    // -> search
	m, _ = enter(t, m)    // -> min reviews
	m, _ = enter(t, m)    // -> refresh
	m, _ = enter(t, m)    // -> jira url
	m, _ = enter(t, m)    // -> jira email
	m, _ = enter(t, m)    // -> jira token
	m, cmd := enter(t, m) // -> validating
	m, _ = send(t, m, cmd())

	if res := m.result(); res.Token != "ghp_new" {
		t.Errorf("token = %q, want replacement %q", res.Token, "ghp_new")
	}
}

func TestWizard_ReconfigureKeptTokenIsValidated(t *testing.T) {
	t.Parallel()

	var validated string
	spy := func(token string) (string, error) {
		validated = token
		return "octocat", nil
	}
	m := NewWizardModel(spy, okJiraValidator).WithExistingConfig(existingConfig())
	m, _ = enter(t, m) // token blank -> keep
	m, _ = enter(t, m)
	m, _ = enter(t, m)
	m, _ = enter(t, m)
	m, _ = enter(t, m) // jira url
	m, _ = enter(t, m) // jira email
	m, cmd := enter(t, m)
	_, _ = send(t, m, cmd())

	if validated != "ghp_old" {
		t.Errorf("validated token = %q, want the kept token to go through live validation", validated)
	}
}

func TestWizard_ReconfigureSentinelClearsJira(t *testing.T) {
	t.Parallel()

	cfg := existingConfig()
	m := NewWizardModel(okValidator, okJiraValidator).WithExistingConfig(cfg)
	m, _ = enter(t, m) // token blank -> keep
	m, _ = enter(t, m) // search
	m, _ = enter(t, m) // min reviews
	m, _ = enter(t, m) // refresh

	m = backspaceAll(t, m, len(cfg.JiraBaseURL))
	m = typeRunes(t, m, "-")
	m, cmd := enter(t, m) // sentinel -> validating, jira cleared
	if m.step != stepValidating {
		t.Fatalf("step = %v, want stepValidating after the \"-\" sentinel", m.step)
	}
	m, _ = send(t, m, cmd())

	res := m.result()
	if res.JiraBaseURL != "" || res.JiraEmail != "" || res.JiraToken != "" {
		t.Errorf("jira config not cleared: %+v", res)
	}
}

func TestWizard_ReconfigureBlankJiraURLKeepsCurrent(t *testing.T) {
	t.Parallel()

	cfg := existingConfig()
	m := NewWizardModel(okValidator, okJiraValidator).WithExistingConfig(cfg)
	m, _ = enter(t, m) // token
	m, _ = enter(t, m) // search
	m, _ = enter(t, m) // min reviews
	m, _ = enter(t, m) // refresh

	m = backspaceAll(t, m, len(cfg.JiraBaseURL))
	m, _ = enter(t, m) // blank -> keep current URL, on to email
	if m.step != stepJiraEmail {
		t.Fatalf("step = %v, want stepJiraEmail (blank keeps the current URL)", m.step)
	}
	if m.jiraURL != cfg.JiraBaseURL {
		t.Errorf("jiraURL = %q, want restored %q", m.jiraURL, cfg.JiraBaseURL)
	}
}

func TestWizard_ReconfigureHints(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator).WithExistingConfig(existingConfig())
	if view := m.View().Content; !strings.Contains(view, "keep the current token") {
		t.Errorf("token step should hint that blank keeps the token, got %q", view)
	}
	if view := m.View().Content; !strings.Contains(view, "update your config") {
		t.Errorf("subtitle should mention updating, got %q", view)
	}

	m.step = stepJiraURL
	m.jiraURL = ""
	if view := m.View().Content; !strings.Contains(view, `"-" to remove Jira`) {
		t.Errorf("jira url step should document the removal sentinel, got %q", view)
	}

	m.step = stepJiraToken
	if view := m.View().Content; !strings.Contains(view, "keep the current token") {
		t.Errorf("jira token step should hint that blank keeps the token, got %q", view)
	}
}

func TestWizard_EscAborts(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	m = typeRunes(t, m, "tok")
	m, cmd := send(t, m, tea.KeyPressMsg{Code: tea.KeyEsc})

	if cmd == nil {
		t.Error("esc should return tea.Quit")
	}
	if res := m.result(); res.Completed {
		t.Error("aborted wizard must not report Completed")
	}
}

func TestWizard_TokenIsMasked(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator, okJiraValidator)
	m = typeRunes(t, m, "secret")
	view := m.View().Content
	if strings.Contains(view, "secret") {
		t.Error("raw token leaked into the view")
	}
	if !strings.Contains(view, "••••••") {
		t.Error("expected the token to be masked with bullets")
	}
}
