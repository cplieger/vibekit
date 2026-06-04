package gitexec

import (
	"strings"
	"testing"
)

func FuzzParseSCPStyle(f *testing.F) {
	f.Add("git@github.com:user/repo.git")
	f.Add("user@host:path")
	f.Add("")
	f.Add("https://github.com/user/repo")
	f.Add("git@host::path")
	f.Add("@host:path")

	f.Fuzz(func(t *testing.T, raw string) {
		host, _, ok := ParseSCPStyle(raw)
		if !ok {
			return
		}
		if host == "" {
			t.Fatal("ok=true but host is empty")
		}
		if strings.Contains(raw, "://") {
			t.Fatal("ok=true but input contains ://")
		}
		if !strings.Contains(raw, "@") {
			t.Fatal("ok=true but input has no @")
		}
		if strings.ContainsAny(host, "/?#") {
			t.Fatalf("ok=true but host contains forbidden char: %q", host)
		}
	})
}
