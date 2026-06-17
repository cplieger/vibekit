package composition

import (
	"path/filepath"
	"testing"
	"time"
)

// Test_gk_vibekit_u29_envDuration kills:
//   - config.go:50 CONDITIONALS_NEGATION (`raw == ""` -> `!=`): a valid,
//     non-empty duration must be parsed and returned, not the fallback.
//   - config.go:54 CONDITIONALS_NEGATION (`err != nil` -> `==`): an
//     unparseable value must return the (non-zero) fallback, not the
//     zero duration from a failed parse.
func Test_gk_vibekit_u29_envDuration(t *testing.T) {
	const key = "GK_VIBEKIT_U29_ENVDUR"
	const fallback = 10 * time.Second

	t.Run("valid_parses_not_fallback", func(t *testing.T) {
		// Original: raw!="" so it skips the early return and parses 5s.
		// Mutant (50): `raw != ""` is true for a set value -> returns fallback.
		t.Setenv(key, "5s")
		got := envDuration(key, fallback)
		if got != 5*time.Second {
			t.Errorf("envDuration(%q) = %v, want %v", "5s", got, 5*time.Second)
		}
	})

	t.Run("invalid_returns_fallback_not_zero", func(t *testing.T) {
		// Original: ParseDuration fails, err!=nil -> returns fallback.
		// Mutant (54): `err == nil` is false on a parse error -> returns d (0).
		t.Setenv(key, "not-a-duration")
		got := envDuration(key, fallback)
		if got != fallback {
			t.Errorf("envDuration(invalid) = %v, want fallback %v", got, fallback)
		}
	})

	t.Run("empty_returns_fallback", func(t *testing.T) {
		t.Setenv(key, "")
		got := envDuration(key, fallback)
		if got != fallback {
			t.Errorf("envDuration(empty) = %v, want fallback %v", got, fallback)
		}
	})
}

// Test_gk_vibekit_u29_checkDirWritable kills:
//   - validate_config.go:53 CONDITIONALS_NEGATION (os.Stat `err != nil` -> `==`)
//   - validate_config.go:63 CONDITIONALS_NEGATION (os.CreateTemp `err != nil` -> `==`)
//
// A valid, writable directory must yield nil. Under either mutant the matching
// `err == nil` branch fires on the no-error path and returns an error instead.
func Test_gk_vibekit_u29_checkDirWritable(t *testing.T) {
	t.Run("writable_dir_returns_nil", func(t *testing.T) {
		dir := t.TempDir()
		if err := checkDirWritable(dir, "GK_ENV"); err != nil {
			t.Errorf("checkDirWritable(writable dir) = %v, want nil", err)
		}
	})

	t.Run("missing_dir_returns_error", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "gk-does-not-exist")
		if err := checkDirWritable(dir, "GK_ENV"); err == nil {
			t.Error("checkDirWritable(missing dir) = nil, want error")
		}
	})
}
