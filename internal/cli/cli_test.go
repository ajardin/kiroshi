package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantOut string
		wantErr bool
	}{
		{name: "default", args: nil, wantOut: "hello from kiroshi"},
		{name: "version", args: []string{"-version"}, wantOut: "kiroshi"},
		{name: "verbose", args: []string{"-verbose"}, wantOut: "hello from kiroshi"},
		{name: "help", args: []string{"-h"}},
		{name: "unknown flag", args: []string{"-nope"}, wantErr: true},
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

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var stdout, stderr bytes.Buffer
	err := Run(ctx, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected error on cancelled context, got nil")
	}
}
