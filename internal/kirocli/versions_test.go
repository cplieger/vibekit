package kirocli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestVersionDirCompleteRejectsEveryIncompleteShape pins what "complete" means.
// The sentinel is the ONLY thing that separates a finished install from a
// directory an interrupted one left behind, and it is checked against the
// directory's own name so a retained predecessor still reads as complete.
func TestVersionDirCompleteRejectsEveryIncompleteShape(t *testing.T) {
	tests := map[string]struct {
		setup func(t *testing.T, dir string)
		want  bool
	}{
		"complete set": {
			setup: func(t *testing.T, dir string) {
				writeSet(t, dir, pinnedVersion, "kiro-cli", "kiro-cli-chat")
				writeSentinelFile(t, dir, pinnedVersion)
			},
			want: true,
		},
		// THE vibekit delta, and the one place this verdict differs from
		// web-terminal-kiro's on the same fixture: `kiro-cli acp` is served by
		// the main binary and no Go path here invokes `chat`, so a directory
		// carrying the main dispatcher alone is COMPLETE. Flipping this to
		// false would gate readiness on a file vibekit never runs.
		"no sidecars at all is complete for this app": {
			setup: func(t *testing.T, dir string) {
				writeSet(t, dir, pinnedVersion, "kiro-cli")
				writeSentinelFile(t, dir, pinnedVersion)
			},
			want: true,
		},
		"binaries but no sentinel (interrupted install)": {
			setup: func(t *testing.T, dir string) {
				writeSet(t, dir, pinnedVersion, "kiro-cli", "kiro-cli-chat")
			},
		},
		"sentinel names another version": {
			setup: func(t *testing.T, dir string) {
				writeSet(t, dir, pinnedVersion, "kiro-cli", "kiro-cli-chat")
				writeSentinelFile(t, dir, prevVersion)
			},
		},
		"the main dispatcher is missing": {
			setup: func(t *testing.T, dir string) {
				writeSet(t, dir, pinnedVersion, "kiro-cli-chat", "kiro-cli-term")
				writeSentinelFile(t, dir, pinnedVersion)
			},
		},
		"the main dispatcher is a symlink": {
			setup: func(t *testing.T, dir string) {
				target := filepath.Join(t.TempDir(), "elsewhere")
				if err := writeFakeBinary(target, pinnedVersion); err != nil {
					t.Fatalf("writeFakeBinary: %v", err)
				}
				if err := os.Symlink(target, filepath.Join(dir, "kiro-cli")); err != nil {
					t.Fatalf("Symlink: %v", err)
				}
				writeSentinelFile(t, dir, pinnedVersion)
			},
		},
		"the main dispatcher is not executable": {
			setup: func(t *testing.T, dir string) {
				if err := os.WriteFile(filepath.Join(dir, "kiro-cli"), []byte("x"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				writeSentinelFile(t, dir, pinnedVersion)
			},
		},
		"sentinel is a symlink": {
			setup: func(t *testing.T, dir string) {
				writeSet(t, dir, pinnedVersion, "kiro-cli", "kiro-cli-chat")
				other := filepath.Join(t.TempDir(), "sentinel")
				if err := os.WriteFile(other, []byte(pinnedVersion+"\n"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				if err := os.Symlink(other, filepath.Join(dir, sentinelName)); err != nil {
					t.Fatalf("Symlink: %v", err)
				}
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			env := newFakeEnv(t)
			m := env.manager()
			dir := env.versionDir(pinnedVersion)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			tc.setup(t, dir)
			if got := m.versionDirComplete(pinnedVersion); got != tc.want {
				t.Errorf("versionDirComplete = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSelectActiveIgnoresPartialDirectories pins that a partially written
// version directory is never a selection candidate: it is not complete, so
// selection skips it and falls back to the complete predecessor.
func TestSelectActiveIgnoresPartialDirectories(t *testing.T) {
	env := newFakeEnv(t)
	env.placePartial(pinnedVersion)
	env.placeVersion(prevVersion)
	m := env.manager()

	sel, ok := m.selectActive(context.Background())
	if !ok {
		t.Fatal("selectActive found nothing, want the complete predecessor")
	}
	if sel.version != prevVersion {
		t.Errorf("selected %q, want %q -- a directory with no sentinel must not be selected", sel.version, prevVersion)
	}
}

// TestEnsurePrunesPartialsBeforeSelecting pins that Ensure removes the partial
// directory rather than leaving it to be re-probed forever, and that the
// staging trees of a previous crashed run go with it.
func TestEnsurePrunesPartialsBeforeSelecting(t *testing.T) {
	env := newFakeEnv(t)
	env.placePartial(oldVersion)
	orphan := filepath.Join(env.versionsRoot(), stagePrefix+"crashed")
	if err := os.MkdirAll(filepath.Join(orphan, "home"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	m := env.manager()

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if exists(env.versionDir(oldVersion)) {
		t.Error("the partial version directory survived Ensure")
	}
	if exists(orphan) {
		t.Error("the orphan staging tree survived Ensure")
	}
}

// TestSelectActiveExcludesATamperedBinaryUnderAnIntactSentinel pins finding 4:
// a directory name plus a sentinel is not proof. The selected candidate is
// probed and must answer with the version its own directory claims, so a binary
// replaced on the volume while `.complete` stayed intact is excluded --
// falling back to another complete version and leaving the pin unsatisfied so
// the caller reinstalls.
func TestSelectActiveExcludesATamperedBinaryUnderAnIntactSentinel(t *testing.T) {
	t.Run("excluded and the fallback serves when reinstall is impossible", func(t *testing.T) {
		env := newFakeEnv(t)
		tampered := env.placeVersion(pinnedVersion)
		env.placeVersion(prevVersion)
		// The sentinel still names the pin; only the binary was swapped.
		if err := writeFakeBinary(filepath.Join(tampered, mainBinary), "6.6.6"); err != nil {
			t.Fatalf("writeFakeBinary: %v", err)
		}
		env.installerFails = true
		m := env.manager()

		if err := m.Ensure(context.Background()); err == nil {
			t.Fatal("Ensure returned nil although the pin was tampered with and reinstall failed")
		}
		if got := m.PathEntry(); got != env.versionDir(prevVersion) {
			t.Errorf("PathEntry() = %q, want the untampered predecessor %q", got, env.versionDir(prevVersion))
		}
		if ready, reason := m.Ready(); !ready {
			t.Errorf("Ready() = false (%s), want true on the predecessor", reason)
		}
	})

	t.Run("triggers a reinstall that replaces the tampered directory", func(t *testing.T) {
		env := newFakeEnv(t)
		tampered := env.placeVersion(pinnedVersion)
		if err := writeFakeBinary(filepath.Join(tampered, mainBinary), "6.6.6"); err != nil {
			t.Fatalf("writeFakeBinary: %v", err)
		}
		m := env.manager()

		if err := m.Ensure(context.Background()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}
		if env.fetchCount() != 1 {
			t.Errorf("fetches = %d, want 1 -- a version-probe mismatch must trigger a reinstall", env.fetchCount())
		}
		out, err := probeFromFile(filepath.Join(tampered, mainBinary))
		if err != nil {
			t.Fatalf("probeFromFile: %v", err)
		}
		if got := parseVersion(string(out)); got != pinnedVersion {
			t.Errorf("reinstalled binary reports %q, want %q", got, pinnedVersion)
		}
		if ready, reason := m.Ready(); !ready {
			t.Errorf("Ready() = false (%s), want true after the reinstall", reason)
		}
	})
}

// TestSelectActiveIgnoresExistingDirectoriesWhenTheTreeWasTainted pins the
// taint contract: a sentinel is trivially forgeable, unlike a digest, so when
// the entrypoint reports the tools tree was writable by others, a pre-existing
// version directory may not be activated -- only one this process installed
// from a verified archive.
func TestSelectActiveIgnoresExistingDirectoriesWhenTheTreeWasTainted(t *testing.T) {
	env := newFakeEnv(t)
	env.placeVersion(pinnedVersion)
	m := env.manager(func(c *Config) { c.Tainted = true })

	if _, ok := m.selectActive(context.Background()); ok {
		t.Fatal("selectActive accepted a pre-existing directory on a tainted tree")
	}
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if env.fetchCount() != 1 {
		t.Errorf("fetches = %d, want 1 -- a tainted tree must force a digest-verified reinstall", env.fetchCount())
	}
	if ready, reason := m.Ready(); !ready {
		t.Errorf("Ready() = false (%s), want true after the verified reinstall", reason)
	}
}

// TestRetainedAndPrunedVersions pins ruling 3's invariant as a unit: the active
// version and its IMMEDIATE predecessor are retained, everything else goes, and
// the predecessor is never a victim. This is what makes a bad activation
// recoverable without a rollback journal, so it is asserted directly rather
// than only through Ensure.
func TestRetainedAndPrunedVersions(t *testing.T) {
	tests := map[string]struct {
		complete   []string
		active     string
		wantKeep   []string
		wantPruned []string
	}{
		"active plus predecessor retained, older pruned": {
			complete:   []string{"2.14.2", "2.14.1", "2.14.0", "2.13.9"},
			active:     "2.14.2",
			wantKeep:   []string{"2.14.2", "2.14.1"},
			wantPruned: []string{"2.14.0", "2.13.9"},
		},
		"predecessor is never pruned when the fallback is active": {
			complete:   []string{"2.14.1", "2.14.0"},
			active:     "2.14.1",
			wantKeep:   []string{"2.14.1", "2.14.0"},
			wantPruned: []string{},
		},
		"only the active version present": {
			complete:   []string{"2.14.2"},
			active:     "2.14.2",
			wantKeep:   []string{"2.14.2"},
			wantPruned: []string{},
		},
		"a version newer than the active one is pruned (the pin moved down)": {
			complete:   []string{"2.15.0", "2.14.2", "2.14.1"},
			active:     "2.14.2",
			wantKeep:   []string{"2.14.2", "2.14.1"},
			wantPruned: []string{"2.15.0"},
		},
		"numeric ordering, not lexical": {
			complete:   []string{"2.14.2", "2.9.9", "2.10.0"},
			active:     "2.14.2",
			wantKeep:   []string{"2.14.2", "2.10.0"},
			wantPruned: []string{"2.9.9"},
		},
		"nothing is pruned when no version is active": {
			complete:   []string{"2.14.2", "2.14.1", "2.14.0"},
			active:     "",
			wantKeep:   nil,
			wantPruned: nil,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			keep := retainedVersions(tc.complete, tc.active)
			slices.Sort(keep)
			want := slices.Clone(tc.wantKeep)
			slices.Sort(want)
			if !slices.Equal(keep, want) {
				t.Errorf("retainedVersions = %v, want %v", keep, want)
			}
			pruned := versionsToPrune(tc.complete, tc.active)
			slices.Sort(pruned)
			wantPruned := slices.Clone(tc.wantPruned)
			slices.Sort(wantPruned)
			if !slices.Equal(pruned, wantPruned) {
				t.Errorf("versionsToPrune = %v, want %v", pruned, wantPruned)
			}
			for _, v := range pruned {
				if slices.Contains(keep, v) {
					t.Errorf("%q is both retained and pruned", v)
				}
			}
		})
	}
}

// TestEnsurePrunesOnlyBeyondTheRetainedPair pins the invariant through Ensure:
// on a boot that already has the pin, the predecessor survives and the older
// version is removed, and pruning happens only after the switch.
func TestEnsurePrunesOnlyBeyondTheRetainedPair(t *testing.T) {
	env := newFakeEnv(t)
	env.placeVersion(pinnedVersion)
	env.placeVersion(prevVersion)
	env.placeVersion(oldVersion)
	m := env.manager()

	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	got := env.versionDirs()
	slices.Sort(got)
	want := []string{prevVersion, pinnedVersion}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("version directories = %v, want %v (active + one predecessor)", got, want)
	}
}

// TestEnsureFailedInstallPrunesNothing pins that a failed install never touches
// the fallback set. The versions on the volume are exactly what makes the
// failure survivable, so pruning runs only after a successful publish.
func TestEnsureFailedInstallPrunesNothing(t *testing.T) {
	env := newFakeEnv(t)
	env.placeVersion(prevVersion)
	env.placeVersion(oldVersion)
	env.installerFails = true
	m := env.manager()

	if err := m.Ensure(context.Background()); err == nil {
		t.Fatal("Ensure returned nil although the installer produced nothing")
	}
	got := env.versionDirs()
	slices.Sort(got)
	want := []string{oldVersion, prevVersion}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("version directories = %v, want %v -- a failed install must prune nothing", got, want)
	}
}

// TestParseVersionTakesTheLastFieldOfTheFirstLine pins the --version parse: the
// same rule the shell helper used, so extra banner or trailing lines cannot
// change the answer.
func TestParseVersionTakesTheLastFieldOfTheFirstLine(t *testing.T) {
	tests := map[string]string{
		"kiro-cli 2.14.2\n":                     "2.14.2",
		"kiro-cli 2.14.2":                       "2.14.2",
		"kiro-cli 2.14.2\nextra 9.9.9\n":        "2.14.2",
		"  kiro-cli   2.14.2  \n":               "2.14.2",
		"2.14.2\n":                              "2.14.2",
		"":                                      "",
		"\n":                                    "",
		"   \n":                                 "",
		"\nkiro-cli 2.14.2\n":                   "",
		"kiro-cli version 2.14.2 (build abc)\n": "abc)",
	}
	for out, want := range tests {
		t.Run(strings.ReplaceAll(out, "\n", "\\n"), func(t *testing.T) {
			if got := parseVersion(out); got != want {
				t.Errorf("parseVersion(%q) = %q, want %q", out, got, want)
			}
		})
	}
}

// FuzzParseVersion pins the parser's invariants on arbitrary --version output:
// it never panics, the result is always a field of the first line, and it never
// contains whitespace. A version string is compared against a directory name
// and a sentinel, so a parse that could return whitespace or a later line's
// content would be a real integrity problem.
func FuzzParseVersion(f *testing.F) {
	for _, seed := range []string{
		"kiro-cli 2.14.2\n", "", "\n\n\n", "   ", "kiro-cli\t2.14.2\r\n",
		"a b c\nd e f\n", "2.14.2", "\x00 \x00", strings.Repeat("x ", 500),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, out string) {
		got := parseVersion(out)
		if got == "" {
			return
		}
		if strings.ContainsAny(got, " \t\r\n\v\f") {
			t.Fatalf("parseVersion(%q) = %q, which contains whitespace", out, got)
		}
		first, _, _ := strings.Cut(out, "\n")
		if !slices.Contains(strings.Fields(first), got) {
			t.Fatalf("parseVersion(%q) = %q, which is not a field of the first line %q", out, got, first)
		}
	})
}

// TestCompareVersionsOrdersNumerically pins that version ordering is numeric
// per segment, because the retained-predecessor invariant is defined by "the
// newest version below the active one".
func TestCompareVersionsOrdersNumerically(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"2.14.2", "2.14.1", 1},
		{"2.14.1", "2.14.2", -1},
		{"2.14.2", "2.14.2", 0},
		{"2.10.0", "2.9.9", 1},
		{"2.14", "2.14.0", 0},
		{"2.14.1", "2.14", 1},
		{"2.14.2", "not-a-version", -1},
	}
	for _, tc := range tests {
		got := compareVersions(tc.a, tc.b)
		if (got > 0) != (tc.want > 0) || (got < 0) != (tc.want < 0) {
			t.Errorf("compareVersions(%q, %q) = %d, want sign of %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func writeSet(t *testing.T, dir, version string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := writeFakeBinary(filepath.Join(dir, n), version); err != nil {
			t.Fatalf("writeFakeBinary(%s): %v", n, err)
		}
	}
}

func writeSentinelFile(t *testing.T, dir, version string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, sentinelName), []byte(version+"\n"), 0o600); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}
}

// TestArchTargetMapsGOARCH pins the architecture resolution the production path
// uses (Config.Arch empty), including the refusal: an architecture with no
// published archive must fail at construction rather than build a URL that
// 404s after boot.
func TestArchTargetMapsGOARCH(t *testing.T) {
	tests := map[string]struct {
		want string
		ok   bool
	}{
		"amd64": {want: archAMD64, ok: true},
		"arm64": {want: archARM64, ok: true},
		"386":   {},
		"arm":   {},
		"":      {},
	}
	for goarch, tc := range tests {
		t.Run(goarch, func(t *testing.T) {
			got, err := archTarget(goarch)
			switch {
			case tc.ok && err != nil:
				t.Fatalf("archTarget(%q): %v", goarch, err)
			case !tc.ok && !errors.Is(err, ErrUnsupportedArch):
				t.Fatalf("archTarget(%q) error = %v, want ErrUnsupportedArch", goarch, err)
			case tc.ok && got != tc.want:
				t.Errorf("archTarget(%q) = %q, want %q", goarch, got, tc.want)
			}
		})
	}
}

// TestSelectActiveExcludesAnUnparseableVersionAnswer pins that a binary whose
// --version output carries no version is excluded rather than trusted. An empty
// parse must never be treated as "matches the directory name", because that
// would let a broken or replaced binary activate.
func TestSelectActiveExcludesAnUnparseableVersionAnswer(t *testing.T) {
	tests := map[string]struct {
		out string
		err error
	}{
		"empty output":       {out: ""},
		"blank output":       {out: "   \n"},
		"probe failed":       {err: errors.New("timed out")},
		"leading blank line": {out: "\nkiro-cli " + pinnedVersion + "\n"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			env := newFakeEnv(t)
			env.placeVersion(pinnedVersion)
			env.onProbe = func(string) ([]byte, error) { return []byte(tc.out), tc.err }
			m := env.manager()

			if sel, ok := m.selectActive(context.Background()); ok {
				t.Errorf("selectActive accepted %q (selected %q)", tc.out, sel.version)
			}
		})
	}
}
