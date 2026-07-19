package forges

// Test scaffolding for the CLI-native auth/discovery layer: fake forge
// CLIs as shell stubs on an isolated PATH. Every stub is a /bin/sh
// script (absolute shebang, so the restricted PATH doesn't matter) that
// prints canned output and/or records its argv, stdin, and selected env
// into files the test asserts on. This exercises the REAL subprocess
// plumbing (cliexec: LookPath, sanitized env, stdin piping, output
// capture) with no network and no real CLIs.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stubPath creates an isolated bin dir, points PATH at it (and only
// it), and returns the dir. t.Setenv also blocks t.Parallel, which
// these tests must not use anyway (process-global PATH/HOME).
func stubPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	return dir
}

// stubCLI writes an executable /bin/sh script named name into dir.
// The script restores a standard internal PATH: the test's restricted
// PATH (stub dir only) governs which BINARY LookPath resolves, but the
// script body still needs coreutils like cat.
func stubCLI(t *testing.T, dir, name, script string) {
	t.Helper()
	body := "#!/bin/sh\nPATH=/usr/bin:/bin\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatalf("write %s stub: %v", name, err)
	}
}

// recordingScript returns a stub body that appends `argv:<args>`,
// `stdin:<stdin>`, and `env:<$GITEA_SERVER_TOKEN>` lines to recPath on
// every invocation, then exits 0.
func recordingScript(recPath string) string {
	return `printf 'argv:%s\n' "$*" >> ` + recPath + `
if [ ! -t 0 ]; then printf 'stdin:%s\n' "$(cat)" >> ` + recPath + `; fi
printf 'env:%s\n' "$GITEA_SERVER_TOKEN" >> ` + recPath
}

// readRecord returns recPath's contents ("" when the stub never ran).
func readRecord(t *testing.T, recPath string) string {
	t.Helper()
	data, err := os.ReadFile(recPath)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read record: %v", err)
	}
	return string(data)
}

// recordLines filters the record to lines with the given prefix,
// prefix stripped.
func recordLines(record, prefix string) []string {
	var out []string
	for line := range strings.SplitSeq(record, "\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			out = append(out, v)
		}
	}
	return out
}
