package mcp

// Tests for prewarm.go: the npx package extractor that decides which
// packages to `npm install -g` on boot and on config change.

import (
	"strings"
	"testing"
	"unicode/utf8"

	"pgregory.net/rapid"
)

// TestExtractNpxPackage consolidates the 12 single-case tests into a
// table-driven test with t.Run sub-tests for each scenario.
func TestExtractNpxPackage(t *testing.T) {
	cases := []struct {
		name string
		srv  *Server
		want string
	}{
		{
			name: "NotNpx",
			srv: &Server{
				Transport: TransportStdio, Command: "node",
				Args: []string{"script.js"}, Prewarm: true, Enabled: true,
			},
			want: "",
		},
		{
			name: "NotStdio",
			srv: &Server{
				Transport: TransportHTTP, Command: "npx",
				Args: []string{"@scope/pkg"}, Prewarm: true, Enabled: true,
			},
			want: "",
		},
		{
			name: "PrewarmOff",
			srv: &Server{
				Transport: TransportStdio, Command: "npx",
				Args: []string{"-y", "@scope/pkg"}, Prewarm: false, Enabled: true,
			},
			want: "",
		},
		{
			name: "Disabled",
			srv: &Server{
				Transport: TransportStdio, Command: "npx",
				Args: []string{"-y", "@scope/pkg"}, Prewarm: true, Enabled: false,
			},
			want: "",
		},
		{
			name: "WithMinusY",
			srv: &Server{
				Transport: TransportStdio, Command: "npx",
				Args: []string{"-y", "@scope/pkg"}, Prewarm: true, Enabled: true,
			},
			want: "@scope/pkg",
		},
		{
			name: "WithLongYes",
			srv: &Server{
				Transport: TransportStdio, Command: "npx",
				Args: []string{"--yes", "some-pkg"}, Prewarm: true, Enabled: true,
			},
			want: "some-pkg",
		},
		{
			name: "WithoutYesFlag",
			srv: &Server{
				Transport: TransportStdio, Command: "npx",
				Args: []string{"some-pkg"}, Prewarm: true, Enabled: true,
			},
			want: "some-pkg",
		},
		{
			name: "SkipsBlankArgs",
			srv: &Server{
				Transport: TransportStdio, Command: "npx",
				Args: []string{"", "-y", "  ", "target"}, Prewarm: true, Enabled: true,
			},
			want: "target",
		},
		{
			name: "WhitespaceCommand",
			srv: &Server{
				Transport: TransportStdio, Command: "  npx  ",
				Args: []string{"pkg"}, Prewarm: true, Enabled: true,
			},
			want: "pkg",
		},
		{
			name: "RejectsFlagInjectionViaRegistry",
			srv: &Server{
				Transport: TransportStdio, Command: "npx",
				Args: []string{"-y", "--registry=http://evil.example", "legit-pkg"}, Prewarm: true, Enabled: true,
			},
			want: "",
		},
		{
			name: "RejectsShortFlagInjection",
			srv: &Server{
				Transport: TransportStdio, Command: "npx",
				Args: []string{"-p=foo", "--quiet", "real-pkg"}, Prewarm: true, Enabled: true,
			},
			want: "",
		},
		{
			name: "RejectsDashPrefixedAlone",
			srv: &Server{
				Transport: TransportStdio, Command: "npx",
				Args: []string{"-y", "--registry=http://evil.example"}, Prewarm: true, Enabled: true,
			},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractNpxPackage(tc.srv); got != tc.want {
				t.Errorf("extractNpxPackage() = %q, want %q", got, tc.want)
			}
		})
	}
}

// FuzzExtractNpxPackage targets the security-sensitive npx package
// extractor with random Args slices. The function must either return a
// string matching the safe-package regex or "" (rejected). Any other
// output indicates a bypass vector.
func FuzzExtractNpxPackage(f *testing.F) {
	// Seed corpus from known interesting inputs.
	f.Add("-y", "@scope/pkg")
	f.Add("--yes", "some-pkg")
	f.Add("-y", "--registry=http://evil.example")
	f.Add("-p=foo", "real-pkg")
	f.Add("-y", "legit@npm:malicious")
	f.Add("-y", "pkg@https://evil.example/tar.tgz")
	f.Add("", "")
	f.Add("-y", "")
	f.Add("--yes", "@modelcontextprotocol/server-github")

	f.Fuzz(func(t *testing.T, arg1, arg2 string) {
		s := &Server{
			Transport: TransportStdio,
			Command:   "npx",
			Args:      []string{arg1, arg2},
			Prewarm:   true,
			Enabled:   true,
		}
		got := extractNpxPackage(s)
		if got == "" {
			return // rejected — valid outcome
		}
		// If non-empty, it must match the safe-package regex.
		if !npmPkgSpecRe.MatchString(got) {
			t.Errorf("extractNpxPackage returned %q which does not match npmPkgSpecRe", got)
		}
	})
}

func TestTailOutput_ShortStays(t *testing.T) {
	if got := tailOutput([]byte("short"), 1024); got != "short" {
		t.Errorf("got %q, want 'short'", got)
	}
}

func TestTailOutput_LongTruncated(t *testing.T) {
	got := tailOutput([]byte("0123456789"), 4)
	if got != "…6789" {
		t.Errorf("got %q, want '…6789'", got)
	}
}

// Regression: Q3. tailOutput previously sliced mid-UTF-8-rune when
// the cut landed inside a multi-byte character; the invalid
// continuation-byte prefix then went through slog/Loki as a broken
// sequence. npm emits box-drawing / emoji in progress output, so this
// matters in practice. The fix advances past continuation bytes so
// the tail always starts on a rune boundary.
func TestTailOutput_AdvancesPastUTF8ContinuationBytes(t *testing.T) {
	// "αβγ" is 6 bytes: 0xCE 0xB1 0xCE 0xB2 0xCE 0xB3. Cut at n=5
	// lands on the 0xB1 continuation byte; the function should skip
	// forward to the start of the next rune.
	in := []byte("αβγ")
	got := tailOutput(in, 5)
	rest := strings.TrimPrefix(got, "…")
	if !utf8.ValidString(rest) {
		t.Errorf("tail not valid UTF-8 after ellipsis: %q", got)
	}
}

// SEC-u12c1-001 regression: reject npm package specs that carry
// URL / git / file / npm-alias suffixes. These bypass the
// leading-dash guard because they put the redirect inside the
// @-suffix (e.g. `legit@npm:malicious`). Any such form returned from
// extractNpxPackage would be fed to `npm install -g` and run
// attacker-served preinstall/postinstall scripts inside the vibekit
// container.
func TestExtractNpxPackage_RejectsUnsafePackageSpecs(t *testing.T) {
	cases := []struct {
		name string
		arg  string
	}{
		{"npm_alias", "legit@npm:malicious"},
		{"https_tarball", "pkg@https://evil.example/tar.tgz"},
		{"git_spec", "pkg@git+https://evil.example/repo.git"},
		{"github_shorthand", "pkg@github:user/repo"},
		{"file_spec", "pkg@file:/tmp/local.tgz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{
				Transport: TransportStdio, Command: "npx",
				Args:    []string{"-y", tc.arg},
				Enabled: true, Prewarm: true,
			}
			if got := extractNpxPackage(s); got != "" {
				t.Errorf("extractNpxPackage(%q) = %q, want empty (unsafe spec must be rejected)",
					tc.arg, got)
			}
		})
	}
}

// SEC-u12c1-001 positive control: legitimate package specs still
// pass the tightened regex. Prevents false-positive regressions on
// routine adds.
func TestExtractNpxPackage_AcceptsSafePackageSpecs(t *testing.T) {
	cases := []string{
		"package-name",
		"@scope/package-name",
		"@scope/package-name@1.2.3",
		"package@latest",
		"package@^1.0.0",
		"@modelcontextprotocol/server-github",
	}
	for _, arg := range cases {
		t.Run(arg, func(t *testing.T) {
			s := &Server{
				Transport: TransportStdio, Command: "npx",
				Args:    []string{"-y", arg},
				Enabled: true, Prewarm: true,
			}
			if got := extractNpxPackage(s); got != arg {
				t.Errorf("extractNpxPackage(%q) = %q, want %q",
					arg, got, arg)
			}
		})
	}
}

// u12c2-f1: ringBuffer bounds memory for rogue npm postinstall
// output. Three contracts: writes under cap accumulate, writes
// over cap keep only the tail, and the byte count reported by
// Write matches the input length (io.Writer contract).
func TestRingBuffer(t *testing.T) {
	cases := []struct {
		name   string
		want   string
		writes []string
		cap    int
		wantN  int
	}{
		{
			name:   "AccumulatesUnderCap",
			cap:    10,
			writes: []string{"hello"},
			want:   "hello",
			wantN:  5,
		},
		{
			name:   "TrimsHeadWhenOverCap",
			cap:    5,
			writes: []string{"abcde", "f"},
			want:   "bcdef",
			wantN:  1,
		},
		{
			name:   "KeepsTailAcrossLargeWrite",
			cap:    4,
			writes: []string{"0123456789"},
			want:   "6789",
			wantN:  10,
		},
		{
			name:   "MultipleOverflowsKeepLatestTail",
			cap:    3,
			writes: []string{"aaa", "bbb", "ccc", "ddd"},
			want:   "ddd",
		},
		{
			name:   "ZeroLengthWriteIsNoop",
			cap:    10,
			writes: []string{"hello", ""},
			want:   "hello",
			wantN:  0,
		},
		{
			name:   "EmptyBytesIsEmpty",
			cap:    10,
			writes: nil,
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &ringBuffer{cap: tc.cap}
			var lastN int
			for _, w := range tc.writes {
				n, err := r.Write([]byte(w))
				if err != nil {
					t.Fatalf("Write(%q): %v", w, err)
				}
				lastN = n
			}
			if tc.wantN != 0 && lastN != tc.wantN {
				t.Errorf("last Write returned %d, want %d", lastN, tc.wantN)
			}
			if got := string(r.Bytes()); got != tc.want {
				t.Errorf("Bytes() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRingBuffer_Property verifies the core invariant via property-based
// testing: after any sequence of writes, Bytes() returns the last
// min(totalWritten, cap) bytes of the concatenated input.
func TestRingBuffer_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cap := rapid.IntRange(1, 256).Draw(t, "cap")
		nWrites := rapid.IntRange(0, 20).Draw(t, "nWrites")

		r := &ringBuffer{cap: cap}
		var concat []byte

		for range nWrites {
			chunk := rapid.SliceOfN(rapid.Byte(), 0, 128).Draw(t, "chunk")
			n, err := r.Write(chunk)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			if n != len(chunk) {
				t.Fatalf("Write returned %d, want %d", n, len(chunk))
			}
			concat = append(concat, chunk...)
		}

		got := r.Bytes()
		if len(got) > cap {
			t.Fatalf("len(Bytes()) = %d exceeds cap %d", len(got), cap)
		}
		wantLen := min(len(concat), cap)
		want := concat[len(concat)-wantLen:]
		if string(got) != string(want) {
			t.Fatalf("Bytes() = %q, want suffix %q", got, want)
		}
	})
}

// u12c2-f5: extractNpxPackage returns "" (silent skip, NOT a
// panic or the npx binary name) when Args doesn't contain a
// real package spec. Multiple inputs reach the same final return:
// Args with only -y/--yes, empty Args, or only blank entries.
func TestExtractNpxPackage_NoPackageAfterYes_returnsEmpty(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "only_minus_y", args: []string{"-y"}},
		{name: "only_long_yes", args: []string{"--yes"}},
		{name: "empty_args", args: []string{}},
		{name: "nil_args", args: nil},
		{name: "all_blank", args: []string{"", "  "}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{
				Transport: TransportStdio, Command: "npx", Args: tc.args,
				Enabled: true, Prewarm: true,
			}
			if got := extractNpxPackage(s); got != "" {
				t.Errorf("extractNpxPackage(args=%v) = %q, want empty",
					tc.args, got)
			}
		})
	}
}
