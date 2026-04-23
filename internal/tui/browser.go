package tui

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// OpenURL launches the user's default browser at rawURL. The URL is parsed
// and required to be http(s) before being handed to the platform launcher;
// this guards against accidentally passing a flag (e.g. "-arg") or a non-web
// scheme to `open`/`xdg-open`/`rundll32`.
func OpenURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("refusing to open non-http(s) url %q", rawURL)
	}
	safe := u.String()

	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", safe) //nolint:gosec // safe: scheme-validated http(s) URL
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", safe) //nolint:gosec // safe: scheme-validated http(s) URL
	default:
		cmd = exec.Command("xdg-open", safe) //nolint:gosec // safe: scheme-validated http(s) URL
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}
	return nil
}
