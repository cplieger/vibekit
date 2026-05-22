package main

import (
	"os"
	"testing"
)

// envOr is a startup-time helper used at three call sites
// (KIRO_CLI_PATH, KIRO_WORK_DIR, KIRO_CONFIG_DIR) plus VAPID_SUBJECT.
// The "empty env var treated as unset" branch is the subtle one:
// users who accidentally export `KIRO_CLI_PATH=""` expect the
// fallback, not a broken exec path. These tests pin all three
// precedence branches (unset / empty / set) so a refactor to
// `os.LookupEnv` (which would return true for an empty string)
// can't silently break the third case.

func TestEnvOr_UnsetReturnsFallback(t *testing.T) {
	const key = "VIBEKIT_ENVOR_TEST_UNSET"
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}

	got := envOr(key, "fallback")

	if got != "fallback" {
		t.Errorf("envOr(%q, %q) = %q, want %q", key, "fallback", got, "fallback")
	}
}

func TestEnvOr_SetNonEmptyReturnsValue(t *testing.T) {
	const key = "VIBEKIT_ENVOR_TEST_SET"
	t.Setenv(key, "actual")

	got := envOr(key, "fallback")

	if got != "actual" {
		t.Errorf("envOr(%q, %q) with env=%q = %q, want %q", key, "fallback", "actual", got, "actual")
	}
}

func TestEnvOr_SetEmptyReturnsFallback(t *testing.T) {
	// Contract: "" is treated as unset so users who accidentally
	// export KIRO_CLI_PATH= don't get a broken empty exec path.
	// Without this test, a refactor to `if _, ok := os.LookupEnv(k); ok`
	// would pass the other two cases but silently break this one.
	const key = "VIBEKIT_ENVOR_TEST_EMPTY"
	t.Setenv(key, "")

	got := envOr(key, "fallback")

	if got != "fallback" {
		t.Errorf("envOr(%q, %q) with env=%q = %q, want %q (empty string must fall back, not return \"\")",
			key, "fallback", "", got, "fallback")
	}
}

func TestEnvOr_FallbackCanBeEmpty(t *testing.T) {
	// An empty fallback is a legitimate caller choice; envOr must
	// not inject any default of its own.
	const key = "VIBEKIT_ENVOR_TEST_EMPTY_FALLBACK"
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}

	got := envOr(key, "")

	if got != "" {
		t.Errorf("envOr(%q, \"\") = %q, want \"\"", key, got)
	}
}
