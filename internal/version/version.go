// Package version exposes build-time identifiers for the kiroshi binary.
//
// The version, commit and date package variables are meant to be overridden at
// build time via -ldflags -X. When left at their defaults, String falls back
// to runtime/debug.ReadBuildInfo so `go install` and `go run` builds still
// carry usable metadata.
package version

import (
	"fmt"
	"runtime/debug"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

// String returns a human-readable identifier of the current kiroshi build,
// combining semantic version, VCS commit and build date.
func String() string {
	v, c, d := version, commit, date
	if v == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok {
			if info.Main.Version != "" && info.Main.Version != "(devel)" {
				v = info.Main.Version
			}
			for _, s := range info.Settings {
				switch s.Key {
				case "vcs.revision":
					if c == "none" && len(s.Value) >= 7 {
						c = s.Value[:7]
					}
				case "vcs.time":
					if d == "unknown" {
						d = s.Value
					}
				}
			}
		}
	}
	return fmt.Sprintf("kiroshi %s (commit %s, built %s)", v, c, d)
}
