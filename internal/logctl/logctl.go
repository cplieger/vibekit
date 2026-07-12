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

	"github.com/cplieger/slogx"
	"github.com/cplieger/vibekit/internal/settings"
)

// levelVar is the shared LevelVar the installed handler follows: slogx.Setup
// wires it into the default logger (in Install) and SetDebug flips it at
// runtime, so a PATCH to debug_logs re-levels subsequent log calls in place.
var levelVar *slog.LevelVar

// Install wires the shared LevelVar into slog's default logger and
// reads the initial level from configDir/config.json. Call exactly
// once at startup, before any other slog calls that matter.
//
// The handler is installed at info first (via slogx.Setup, which returns the
// LevelVar the handler follows), then promoted to debug only when debug_logs
// is present and true. Parse failures (corrupt JSON, wrong type on debug_logs)
// or a legitimately-missing config.json (first boot) leave it at info, so a
// broken settings file never accidentally drops the user into debug mode.
func Install(ctx context.Context, configDir string) {
	levelVar = slogx.Setup(slogx.Options{})
	if on, ok := settings.Field[bool](ctx, configDir, settings.KeyDebugLogs, settings.KeyDebugLogs); on && ok {
		levelVar.Set(slog.LevelDebug)
	}
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
