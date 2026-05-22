package permissions

import (
	"slices"
	"testing"
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
			got := shellFields(tt.input)
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
	if hasWriteOption(`grep "hello -o world" file.txt`) {
		t.Error("hasWriteOption matched -o embedded inside a quoted argument")
	}
	if hasWriteOption(`grep 'contains -o flag' file.txt`) {
		t.Error("hasWriteOption matched -o embedded inside a single-quoted argument")
	}
	// Actual -o flag (quoted or not) should still match.
	if !hasWriteOption(`curl -o output.txt`) {
		t.Error("hasWriteOption failed to match real -o flag")
	}
	if !hasWriteOption(`curl "-o" output.txt`) {
		t.Error("hasWriteOption failed to match quoted -o flag (still a flag to the program)")
	}
}
