package permissions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	cfgsettings "github.com/cplieger/vibekit/internal/settings"
)

// writeSettings writes a config.json file in a fresh temp dir and
// returns its config dir path. Keeps the tests fast and isolated.
func writeSettings(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatalf("write settings: %v", err)
		}
	}
	return dir
}

// --- SupervisedDefault ---
//
// Every branch is a documented "fail-closed to false" fallback. The
// function is called on every new-chat auto-create, so missing-file,
// malformed-JSON, missing-key, wrong-type, and both true/false values
// need pinning so a future refactor that accidentally flips a branch
// to fail-open is caught immediately.

func TestSupervisedDefault(t *testing.T) {
	tests := []struct {
		name     string
		settings string // JSON body; "" means no file written
		useEmpty bool   // true → pass "" as configDir (empty-dir short-circuit)
		noFile   bool   // true → use TempDir with no config.json
		want     bool
	}{
		{"EmptyConfigDirReturnsFalse", "", true, false, false},
		{"MissingSettingsReturnsFalse", "", false, true, false},
		{"MalformedJSONReturnsFalse", `{not json`, false, false, false},
		{"MissingKeyReturnsFalse", `{"other_field":true}`, false, false, false},
		{"WrongTypeReturnsFalse", `{"supervised_default":42}`, false, false, false},
		{"TrueReturnsTrue", `{"supervised_default":true}`, false, false, true},
		{"FalseReturnsFalse", `{"supervised_default":false}`, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var dir string
			switch {
			case tt.useEmpty:
				dir = ""
			case tt.noFile:
				dir = t.TempDir()
			default:
				dir = writeSettings(t, tt.settings)
			}
			if got := SupervisedDefault(context.Background(), dir); got != tt.want {
				t.Errorf("SupervisedDefault() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSupervisedDefault_NonENOENTReadErrorFailsClosed(t *testing.T) {
	// Regression: when config.json exists but cannot be read (here
	// simulated by making it a directory, which returns EISDIR — a
	// non-ENOENT error), SupervisedDefault must land on its documented
	// fail-CLOSED default, false. A regression that flipped the branch
	// to fail-open would silently gate every write on approval.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "config.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if SupervisedDefault(context.Background(), dir) {
		t.Error("SupervisedDefault(dir-as-settings) = true, want false (fail-closed)")
	}
}

func TestReadSettingsBytes_EmptyConfigDirReturnsNil(t *testing.T) {
	// Regression (ops-perm-05): empty configDir must short-circuit
	// to (nil, nil) instead of filepath.Join("", "config.json")
	// → "config.json" which would read the process's PWD. Every
	// reader built on cfgsettings.ReadBytes then picks its own
	// fallback.
	data, err := cfgsettings.ReadBytes(context.Background(), "")
	if err != nil {
		t.Errorf("cfgsettings.ReadBytes(\"\") err = %v, want nil", err)
	}
	if data != nil {
		t.Errorf("cfgsettings.ReadBytes(\"\") data = %v, want nil", data)
	}
	// SupervisedDefault inherits the short-circuit; pin the fallback.
	if SupervisedDefault(context.Background(), "") {
		t.Error("SupervisedDefault(context.Background(), \"\") = true, want false")
	}
}
