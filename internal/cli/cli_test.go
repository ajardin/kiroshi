package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func TestRun(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"`)

	tests := []struct {
		name    string
		args    []string
		wantOut string
		wantErr bool
	}{
		{name: "default", args: []string{"-config", cfgPath}, wantOut: "kiroshi ready"},
		{name: "version", args: []string{"-version"}, wantOut: "kiroshi"},
		{name: "verbose", args: []string{"-verbose", "-config", cfgPath}, wantOut: "kiroshi ready"},
		{name: "help", args: []string{"-h"}},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: true},
		{name: "missing config file", args: []string{"-config", filepath.Join(t.TempDir(), "nope.toml")}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout, stderr bytes.Buffer
			err := Run(t.Context(), tt.args, &stdout, &stderr)

			if (err != nil) != tt.wantErr {
				t.Fatalf("Run() err = %v, wantErr = %v (stderr=%q)", err, tt.wantErr, stderr.String())
			}
			if tt.wantErr || tt.wantOut == "" {
				return
			}
			if !strings.Contains(stdout.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want substring %q", stdout.String(), tt.wantOut)
			}
		})
	}
}

func TestRun_CancelledContext(t *testing.T) {
	t.Parallel()

	cfgPath := writeConfig(t, `github_token = "t"
search = "s"`)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var stdout, stderr bytes.Buffer
	err := Run(ctx, []string{"-config", cfgPath}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}
