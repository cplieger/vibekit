package eval

import (
	"strings"
	"testing"
)

func FuzzHasSafePrefix(f *testing.F) {
	f.Add("git status", "git status")
	f.Add("git status --short", "git status")
	f.Add("git", "git status")
	f.Add("", "")
	f.Add("ls", "ls")
	f.Add("ls -la", "ls")
	f.Add("\x00prefix", "prefix")
	f.Add("git\tstatus", "git")

	f.Fuzz(func(t *testing.T, command, prefix string) {
		result := HasSafePrefix(command, prefix)

		// Identity: HasSafePrefix(s, s) == true for all s.
		if command == prefix && !result {
			t.Errorf("HasSafePrefix(%q, %q) = false; identity violated", command, prefix)
		}

		// If prefix is longer than command, result must be false
		// (unless they're equal, handled above).
		if len(prefix) > len(command) && result {
			t.Errorf("HasSafePrefix(%q, %q) = true; prefix longer than command", command, prefix)
		}

		// If true and command != prefix, verify boundary character.
		if result && command != prefix {
			if !strings.HasPrefix(command, prefix) {
				t.Errorf("HasSafePrefix(%q, %q) = true but HasPrefix is false", command, prefix)
			}
			if len(command) > len(prefix) {
				next := command[len(prefix)]
				if next != ' ' && next != '\t' {
					t.Errorf("HasSafePrefix(%q, %q) = true but boundary char is %q", command, prefix, next)
				}
			}
		}
	})
}

func FuzzExtractBaseCommand(f *testing.F) {
	f.Add("ls -la")
	f.Add("git commit -m 'msg'")
	f.Add(`"quoted" arg`)
	f.Add("")
	f.Add("   ")
	f.Add("\x00\x01")

	f.Fuzz(func(t *testing.T, command string) {
		result := ExtractBaseCommand(command)
		fields := ShellFields(command)

		// Result must equal ShellFields(s)[0] when non-empty.
		if len(fields) > 0 {
			if result != fields[0] {
				t.Errorf("ExtractBaseCommand(%q) = %q; want ShellFields[0] = %q", command, result, fields[0])
			}
		} else {
			if result != "" {
				t.Errorf("ExtractBaseCommand(%q) = %q; want empty (no fields)", command, result)
			}
		}
	})
}
