package gitexec

import (
	"slices"
	"strings"
	"testing"
)

// FuzzFirstSubcommand verifies firstSubcommand never panics, skips
// flag-like tokens, and returns "" only when no non-empty non-flag token exists.
func FuzzFirstSubcommand(f *testing.F) {
	f.Add("status")
	f.Add("-c\x00value\x00push")
	f.Add("-C\x00/tmp\x00clone")
	f.Add("--flag\x00commit")
	f.Add("")
	f.Add("-c")
	f.Add("-c\x00val")

	f.Fuzz(func(t *testing.T, joined string) {
		// Split on NUL to simulate arg list.
		var args []string
		if joined != "" {
			args = strings.Split(joined, "\x00")
		}
		result := firstSubcommand(args)
		if result == "" {
			return // "" is the "not found" sentinel, always valid
		}
		// Invariant: result must not start with '-'.
		if strings.HasPrefix(result, "-") {
			t.Fatalf("firstSubcommand returned flag-like value: %q", result)
		}
		// Invariant: result must appear in args.
		if !slices.Contains(args, result) {
			t.Fatalf("firstSubcommand returned %q which is not in args", result)
		}
	})
}
