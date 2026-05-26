// Package logctl owns the process-wide slog handler so settings
// toggles can flip the log level at runtime without restarting the
// container. Install() must be called once at startup; SetDebug
// adjusts the level on a shared slog.LevelVar that the handler
// follows.
//
// Level choices:
//   - debug=false → slog.LevelInfo (default; production chatty-ish
//     but not noisy)
//   - debug=true  → slog.LevelDebug (surfaces the slog.Debug lines
//     in translateACPEvent's default case, fs handler details,
//     and bridge stdin/stdout sampling)
//
// The setting reads from <configDir>/config.json `debug_logs`
// (bool). Install reads it once to pick the boot level; the handler
// writes Info or Debug based on whatever the LevelVar says at log
// time, so the PATCH endpoint that flips the bool also flips the
// level for subsequent calls.
package logctl

import (
	"context"
	"log/slog"
	"os"

	"vibekit/internal/settings"
)

var levelVar slog.LevelVar

// Install wires the shared LevelVar into slog's default logger and
// reads the initial level from configDir/config.json. Call exactly
// once at startup, before any other slog calls that matter.
//
// Parse failures (corrupt JSON, wrong type on debug_logs) fall back
// to info level so a broken settings file never accidentally drops
// the user into debug mode. The failure is logged at warn level
// after the handler is wired so operators get a breadcrumb instead
// of a silent fallback. A legitimately-missing config.json (first
// boot) is not an error and produces no warn.
func Install(configDir string) {
	on, ok := settings.Field[bool](context.Background(), configDir, "debug_logs", "debug_logs")
	if on && ok {
		levelVar.Set(slog.LevelDebug)
	} else {
		levelVar.Set(slog.LevelInfo)
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: &levelVar})
	slog.SetDefault(slog.New(handler))
}

// SetDebug flips the active log level at runtime. Called by the
// settings PATCH handler when the user toggles Debug logs.
func SetDebug(on bool) {
	if on {
		levelVar.Set(slog.LevelDebug)
	} else {
		levelVar.Set(slog.LevelInfo)
	}
}
