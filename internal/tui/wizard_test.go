package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// send applies a key message and unwraps the result back to a WizardModel,
// running the returned command synchronously so msg-producing cmds (the token
// validation) feed back through Update — mirroring applyCmd for the dashboard.
func send(t *testing.T, m WizardModel, msg tea.Msg) (WizardModel, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(msg)
	out, ok := updated.(WizardModel)
	if !ok {
		t.Fatalf("Update returned %T, want WizardModel", updated)
	}
	return out, cmd
}

func typeRunes(t *testing.T, m WizardModel, s string) WizardModel {
	t.Helper()
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
	return m
}

func enter(t *testing.T, m WizardModel) (WizardModel, tea.Cmd) {
	t.Helper()
	return send(t, m, tea.KeyMsg{Type: tea.KeyEnter})
}

func okValidator(string) (string, error) { return "octocat", nil }

func TestWizard_HappyPath(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator)
	m = typeRunes(t, m, "ghp_token")
	m, _ = enter(t, m) // -> search

	m = typeRunes(t, m, "is:pr author:@me")
	m, _ = enter(t, m) // -> min reviews

	m = typeRunes(t, m, "3")
	m, cmd := enter(t, m) // -> validating, fires validateCmd
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
}

func TestWizard_BlankFieldsUseDefaults(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator)
	m = typeRunes(t, m, "tok")
	m, _ = enter(t, m) // search left blank
	m, _ = enter(t, m) // min reviews left blank
	m, cmd := enter(t, m)
	m, _ = send(t, m, cmd())

	res := m.result()
	if res.Search != defaultSearch {
		t.Errorf("search = %q, want default %q", res.Search, defaultSearch)
	}
	if res.MinReviews != 2 {
		t.Errorf("min reviews = %d, want default 2", res.MinReviews)
	}
}

func TestWizard_InvalidMinReviewsStays(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator)
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
	m := NewWizardModel(boom)
	m = typeRunes(t, m, "nope")
	m, _ = enter(t, m)
	m, _ = enter(t, m)
	m, cmd := enter(t, m)
	m, _ = send(t, m, cmd()) // validation fails

	if m.step != stepError {
		t.Fatalf("step = %v, want stepError", m.step)
	}
	if !strings.Contains(m.errMsg, "bad credentials") {
		t.Errorf("errMsg = %q, want it to mention the failure", m.errMsg)
	}

	// Any key returns to the token step to retry.
	m, _ = send(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if m.step != stepToken {
		t.Errorf("step = %v, want stepToken after retry", m.step)
	}
}

func TestWizard_EscAborts(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator)
	m = typeRunes(t, m, "tok")
	m, cmd := send(t, m, tea.KeyMsg{Type: tea.KeyEsc})

	if cmd == nil {
		t.Error("esc should return tea.Quit")
	}
	if res := m.result(); res.Completed {
		t.Error("aborted wizard must not report Completed")
	}
}

func TestWizard_TokenIsMasked(t *testing.T) {
	t.Parallel()

	m := NewWizardModel(okValidator)
	m = typeRunes(t, m, "secret")
	view := m.View()
	if strings.Contains(view, "secret") {
		t.Error("raw token leaked into the view")
	}
	if !strings.Contains(view, "••••••") {
		t.Error("expected the token to be masked with bullets")
	}
}
