package permissions

import (
	"slices"
	"testing"

	"github.com/cplieger/vibekit/internal/permissions/eval"
)

func TestShellFields(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{"empty", "", nil},
		{"whitespace only", "   ", nil},
		{"single token", "ls", []string{"ls"}},
		{"simple split", "ls -la /tmp", []string{"ls", "-la", "/tmp"}},
		{"double quotes", `grep "hello world" file.txt`, []string{"grep", "hello world", "file.txt"}},
		{"single quotes", `echo 'hello world'`, []string{"echo", "hello world"}},
		{"mixed quotes", `cmd "arg one" 'arg two' plain`, []string{"cmd", "arg one", "arg two", "plain"}},
		{"quotes mid-token", `--output="my file.txt"`, []string{"--output=my file.txt"}},
		{"adjacent quotes", `"a""b"`, []string{"ab"}},
		{"unterminated double", `echo "hello`, []string{"echo", "hello"}},
		{"unterminated single", `echo 'hello`, []string{"echo", "hello"}},
		{"tabs as separators", "cmd\t-o\tfile", []string{"cmd", "-o", "file"}},
		{"write option not inside quotes", `curl -o output.txt`, []string{"curl", "-o", "output.txt"}},
		{"write option inside quotes is arg", `grep "-o" file`, []string{"grep", "-o", "file"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := eval.ShellFields(tt.input)
			if !slices.Equal(got, tt.want) {
				t.Errorf("shellFields(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestHasWriteOption_QuotedArgNotMatched(t *testing.T) {
	// In shell, quoting doesn't change flag semantics — `grep "-o" file`
	// still passes -o as a flag to grep. So shellFields correctly strips
	// quotes and the write-option check still matches. The real value of
	// shellFields is preventing false positives from split-mid-argument:
	// `grep "hello -o world"` should NOT match -o because it's inside a
	// single argument, not a standalone flag.
	if eval.HasWriteOption(`grep "hello -o world" file.txt`) {
		t.Error("hasWriteOption matched -o embedded inside a quoted argument")
	}
	if eval.HasWriteOption(`grep 'contains -o flag' file.txt`) {
		t.Error("hasWriteOption matched -o embedded inside a single-quoted argument")
	}
	// Actual -o flag (quoted or not) should still match.
	if !eval.HasWriteOption(`curl -o output.txt`) {
		t.Error("hasWriteOption failed to match real -o flag")
	}
	if !eval.HasWriteOption(`curl "-o" output.txt`) {
		t.Error("hasWriteOption failed to match quoted -o flag (still a flag to the program)")
	}
}

func TestExtractBaseCommand_WhitespaceVariants(t *testing.T) {
	// F4: base extraction must split on any IFS whitespace, not
	// just a single space, so `cat\t/etc/passwd` and `cat  foo`
	// both resolve to "cat" consistently.
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"ls", "ls"},
		{"ls -la", "ls"},
		{"  ls -la", "ls"},
		{"ls\t-la", "ls"},
		{"ls  -la", "ls"},
		{"\tls", "ls"},
	}
	for _, tt := range cases {
		got := eval.ExtractBaseCommand(tt.in)
		if got != tt.want {
			t.Errorf("ExtractBaseCommand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHasWriteOption_ComprehensiveMatrix(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		// TokenExact: --output
		{"git diff --output /tmp/x HEAD", true},
		{"git log --output /tmp/x", true},
		// TokenPrefix: --output=
		{"git diff --output=/tmp/x HEAD", true},
		{"cat --out-file=/tmp/x foo", true},
		{"cat --write=/tmp/x foo", true},
		{"cat --write-file=/tmp/x foo", true},
		// ShortPrefix: -o with trailing value
		{"-o /tmp/x", true},
		{"cat -o/tmp/x foo", true},
		{"cat -o foo", true},
		// ShortPrefix: -O
		{"wget -O /tmp/x https://example.com", true},
		{"wget -O/tmp/x https://example.com", true},
		// -o exactly 2 chars alone
		{"cat foo -o", true},
		// False positives that must NOT match
		{"git diff --output-format=json HEAD", false},
		{"ls --only-dirs", false},
		{"cat foo", false},
		{"git diff HEAD", false},
		{"ls -la", false},
		{"echo hello", false},
		// --output as substring of another flag
		{"cmd --outputter=yes", false},
	}
	for _, tc := range cases {
		got := eval.HasWriteOption(tc.command)
		if got != tc.want {
			t.Errorf("HasWriteOption(%q) = %v, want %v", tc.command, got, tc.want)
		}
	}
}
