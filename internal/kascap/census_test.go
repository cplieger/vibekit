package kascap

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// updateCensus rewrites the census fixture (and the version pin) from the local
// bundle. Spelled as a flag rather than the repo's UPDATE_GOLDEN env gate
// because regenerating this fixture is a review action, not a formatting one:
// every line it adds is a capability somebody has to judge.
var updateCensus = flag.Bool("update", false,
	"rewrite testdata/unclaimed.txt and testdata/kas-version.txt from the local agent-server bundle")

const (
	kasVersionPath  = "testdata/kas-version.txt"
	unclaimedPath   = "testdata/unclaimed.txt"
	entrypointPath  = "../../entrypoint.sh"
	censusUpdateCmd = "go test ./internal/kascap/ -run 'TestCapabilityCensus|TestAbsentTrueMatchesTheBundle' -update"

	// capabilityWindow bounds the slice of bundle read after the agent's own
	// initialize anchor. Measured against 2.18.0: the read set is complete at
	// 4 KiB and unchanged at 32 KiB, so 8 KiB is the stable value with margin.
	capabilityWindow = 8 << 10

	// initAnchor is the ONE site where the agent server binds a local to the
	// client's _meta.kiro block. It is what makes the window trustworthy: the
	// identifier kiroMeta is reused 14 times in this bundle for unrelated
	// payloads (prompt meta, session meta, tool meta), so an unanchored sweep
	// for it reports ~40 keys that are not client capabilities at all.
	initAnchor = "params.clientCapabilities?._meta?.kiro"
)

var (
	kiroCLIVersionRe = regexp.MustCompile(`(?m)^KIRO_CLI_VERSION="([^"]+)"`)

	// resolveCapabilitiesRe captures the body of src/platform/resolved-capabilities.ts's
	// resolveCapabilities, the one function that maps the whole _meta.kiro block
	// onto a resolved struct.
	resolveCapabilitiesRe = regexp.MustCompile(`(?s)function resolveCapabilities\(clientCapabilities\)\s*\{.*?\n\}`)

	// kiroMetaReadRe matches a member read off a kiroMeta local. Case matters:
	// rawKiroMeta and parseKiroMeta carry a capital K, so the word boundary plus
	// the lower-case k keeps them out.
	kiroMetaReadRe = regexp.MustCompile(`\bkiroMeta\??\.([A-Za-z_$][A-Za-z0-9_$]*)`)

	// clientMetaReadRe matches a read off the agent's stored copy. Noise-free
	// without any scoping: this.clientMeta is assigned from the initialize
	// block and from nothing else.
	clientMetaReadRe = regexp.MustCompile(`\bclientMeta\??\.([A-Za-z_$][A-Za-z0-9_$]*)`)

	// settingEnabledRe is the direct settings gate, absent-means-false.
	settingEnabledRe = regexp.MustCompile(`isSettingEnabled\(settings,\s*"([^"]+)"\)`)

	// settingResolverRe is the resolver family, which reads the same block but
	// applies a per-key default instead.
	settingResolverRe = regexp.MustCompile(`parsed2\.data\.([A-Za-z_$][A-Za-z0-9_$]*)\?\.enabled`)

	// absentTrueResolverRe is settingResolverRe's inverse-default subset: a
	// resolver whose fallback chain ends in a literal true, so an ABSENT key
	// resolves TRUE. This is the bundle-side witness for the absentTrue column.
	absentTrueResolverRe = regexp.MustCompile(`parsed2\.data\.([A-Za-z_$][A-Za-z0-9_$]*)\?\.enabled\s*\?\?[^;]*?\?\?\s*true`)
)

// activeKASVersion is the kiro-cli version this repo runs, read from the pin the
// image build and Renovate both use. Deliberately not "whatever is newest in the
// local cache": the census is only meaningful against the version vibekit ships.
func activeKASVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(entrypointPath)
	if err != nil {
		t.Fatalf("read %s: %v", entrypointPath, err)
	}
	m := kiroCLIVersionRe.FindSubmatch(raw)
	if m == nil {
		t.Fatalf(`no KIRO_CLI_VERSION="..." pin in %s.
The census resolves the active agent server from that pin; if the pin moved,
point this test at its new home rather than guessing from the local cache.`, entrypointPath)
	}
	return string(m[1])
}

// loadBundle returns the pinned agent server's source, skipping when it is not
// installed locally and failing when the pin has moved out from under the
// fixture. The two outcomes are deliberately different: an absent bundle is a
// machine without the runtime (CI, a fresh clone), while a moved pin is a real
// change to what vibekit talks to.
func loadBundle(t *testing.T) string {
	t.Helper()
	active := activeKASVersion(t)

	pinnedRaw, err := os.ReadFile(kasVersionPath)
	if err != nil {
		t.Fatalf("read %s (regenerate with: %s): %v", kasVersionPath, censusUpdateCmd, err)
	}
	pinned := strings.TrimSpace(string(pinnedRaw))
	if pinned != active && !*updateCensus {
		t.Fatalf(`census fixture was generated against kiro-cli %s, active is %s.
A version bump can add, rename or drop a client capability, so this fixture has
to be RE-READ rather than diffed. Regenerate and review every added or dropped
line:
  %s`, pinned, active, censusUpdateCmd)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory, so no local agent-server bundle: %v", err)
	}
	pattern := filepath.Join(home, ".local", "share", "kiro-cli", "kas",
		active+"-*", "node_modules", "@kiro", "agent", "dist", "server", "acp-server.js")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatalf("glob %s: %v", pattern, err)
	}
	if len(matches) == 0 {
		t.Skipf(`no agent-server bundle for kiro-cli %s under %s.
This is stage 2 of the capability gate and it is local-only by design; stage 1
(TestInitializeDeclaresExactly) needs no bundle and gates CI.`, active, pattern)
	}
	slices.Sort(matches)
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read bundle %s: %v", matches[0], err)
	}
	if *updateCensus && pinned != active {
		if err := os.WriteFile(kasVersionPath, []byte(active+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", kasVersionPath, err)
		}
		t.Logf("re-pinned %s to %s", kasVersionPath, active)
	}
	return string(raw)
}

// keysIn returns the sorted, deduplicated first capture group of every match.
func keysIn(re *regexp.Regexp, src string) []string {
	seen := make(map[string]bool)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		seen[m[1]] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// requireNonEmpty is the guard that keeps this census honest. Every source is
// a regex over a 21 MB third-party bundle, and the way each one dies is by
// matching nothing after an upstream reshape — which would quietly turn the
// census into a file of zero findings that passes forever.
func requireNonEmpty(t *testing.T, source string, keys []string) []string {
	t.Helper()
	if len(keys) == 0 {
		t.Fatalf(`census source %q extracted NOTHING from the bundle.
That is a broken extractor, not a clean result: the pattern this source relies
on was reshaped upstream. Fix the pattern; do not regenerate the fixture, which
would record the emptiness as the new expectation.`, source)
	}
	return keys
}

// readCapabilityKeys returns every top-level _meta.kiro key the pinned agent
// server reads from the client, from all three sites that read one.
func readCapabilityKeys(t *testing.T, src string) []string {
	t.Helper()
	body := resolveCapabilitiesRe.FindString(src)
	if body == "" {
		t.Fatal(`resolveCapabilities not found in the bundle.
It is the canonical map of the client's _meta.kiro block; if it was renamed or
inlined upstream, this census needs a new anchor rather than a regenerated
fixture.`)
	}
	resolved := requireNonEmpty(t, "resolveCapabilities body", keysIn(kiroMetaReadRe, body))
	stored := requireNonEmpty(t, "clientMeta reads", keysIn(clientMetaReadRe, src))

	if n := strings.Count(src, initAnchor); n != 1 {
		t.Fatalf(`the initialize anchor %q occurs %d times, want exactly 1.
The bounded window below is only trustworthy while that binding is unique.`, initAnchor, n)
	}
	at := strings.Index(src, initAnchor)
	end := min(at+capabilityWindow, len(src))
	adhoc := requireNonEmpty(t, "initialize-scope reads", keysIn(kiroMetaReadRe, src[at:end]))

	all := slices.Concat(resolved, stored, adhoc)
	slices.Sort(all)
	return slices.Compact(all)
}

// readSettingKeys returns every _meta.kiro.settings key the agent server reads,
// across both the direct gate and the resolver family.
func readSettingKeys(t *testing.T, src string) []string {
	t.Helper()
	direct := requireNonEmpty(t, "isSettingEnabled calls", keysIn(settingEnabledRe, src))
	resolved := requireNonEmpty(t, "settings resolvers", keysIn(settingResolverRe, src))
	all := slices.Concat(direct, resolved)
	slices.Sort(all)
	return slices.Compact(all)
}

// declaredKeys returns what the table accounts for on the connection door,
// split by container. A withheld row counts as DECLARED: recording a deliberate
// omission is the table's job, so such a key is not unclaimed.
func declaredKeys() (capabilities, settings map[string]bool) {
	capabilities = map[string]bool{
		// The settings container is not a row; it is derived from the rows.
		settingsKey: true,
	}
	settings = make(map[string]bool)
	for _, row := range table {
		if row.door != doorConnection {
			continue
		}
		if row.resolver == resolverSetting {
			settings[row.key] = true
			continue
		}
		capabilities[row.key] = true
	}
	return capabilities, settings
}

// TestCapabilityCensus reports every client-side key the pinned agent server
// reads that the table does not account for.
//
// It answers the question the table cannot answer about itself: not "are the
// keys we send correct" (stage 1's goldens pin that) but "what is KAS willing to
// read that we have never considered". Measured against 2.18.0 the answer is not
// zero, which is why the fixture is committed rather than asserted empty.
//
// The fixture is NOT a to-do list. Most entries are capabilities vibekit has no
// handler for and should not claim. An entry leaves the file by gaining a table
// row in either direction: send:true with an implementation, or send:false with
// a because saying why not.
//
// Local-only and version-pinned. Stage 1 gates CI; this stage needs the bundle.
//
// Regenerate with:
//
//	go test ./internal/kascap/ -run TestCapabilityCensus -update
func TestCapabilityCensus(t *testing.T) {
	src := loadBundle(t)
	declaredCaps, declaredSettings := declaredKeys()

	var unclaimed []string
	for _, key := range readCapabilityKeys(t, src) {
		if !declaredCaps[key] {
			unclaimed = append(unclaimed, "capability "+key)
		}
	}
	for _, key := range readSettingKeys(t, src) {
		if !declaredSettings[key] {
			unclaimed = append(unclaimed, "setting "+key)
		}
	}
	slices.Sort(unclaimed)

	got := censusHeader(activeKASVersion(t)) + strings.Join(unclaimed, "\n") + "\n"
	if *updateCensus {
		if err := os.WriteFile(unclaimedPath, []byte(got), 0o600); err != nil {
			t.Fatalf("write %s: %v", unclaimedPath, err)
		}
		t.Logf("wrote %s (%d unclaimed)", unclaimedPath, len(unclaimed))
	}
	wantRaw, err := os.ReadFile(unclaimedPath)
	if err != nil {
		t.Fatalf("read %s (regenerate with: %s): %v", unclaimedPath, censusUpdateCmd, err)
	}
	if string(wantRaw) == got {
		return
	}
	t.Errorf(`the capability census changed.
A key appearing here is one the agent server reads and the table does not
account for; a key disappearing means it was claimed or upstream stopped reading
it. Either way it is a review, not a diff to wave through. Regenerate with:
  %s
--- want
%s
+++ got
%s`, censusUpdateCmd, wantRaw, got)
}

// censusHeader is the fixture's preamble, regenerated with the body so the
// version it was read from travels with the findings.
func censusHeader(version string) string {
	return `# Client-side keys the kiro-cli agent server reads that internal/kascap does
# NOT account for: neither sent nor deliberately withheld.
#
# This is not a to-do list. Most entries are capabilities vibekit has no handler
# for and should not claim. An entry leaves this file by gaining a table row in
# either direction: send:true with an implementation, or send:false with a
# because saying why not.
#
# Read from kiro-cli ` + version + ` by TestCapabilityCensus.
# Regenerate: go test ./internal/kascap/ -run TestCapabilityCensus -update
`
}

// TestAbsentTrueMatchesTheBundle validates the absentTrue column against the
// agent server, in both directions.
//
// The column records a claim about SOMEBODY ELSE'S code — that an absent key
// resolves TRUE — so it is exactly the kind of claim that rots silently: the
// table would keep asserting it after an upstream default flipped, and the only
// symptom would be a feature quietly changing state. Both directions matter. A
// row claiming absentTrue that the bundle does not support is a false record,
// and a bundle resolver defaulting true whose key the table declares WITHOUT
// absentTrue is the more dangerous miss, because sending nothing then enables
// something nobody wrote down.
func TestAbsentTrueMatchesTheBundle(t *testing.T) {
	src := loadBundle(t)
	bundleTrue := requireNonEmpty(t, "inverse-default resolvers", keysIn(absentTrueResolverRe, src))

	for _, row := range table {
		if !row.absentTrue {
			continue
		}
		if !slices.Contains(bundleTrue, row.key) {
			t.Errorf(`%s declares absentTrue, but no resolver in kiro-cli %s defaults it to true.
The bundle's inverse-default resolvers are %v. Either the claim was always wrong
or upstream changed the default, and both make the row's because misleading.`,
				rowID(row), activeKASVersion(t), bundleTrue)
		}
	}

	declared := make(map[string]*decl)
	for i, row := range table {
		if row.resolver == resolverSetting {
			declared[row.key] = &table[i]
		}
	}
	for _, key := range bundleTrue {
		row, ok := declared[key]
		if !ok {
			// Not declared at all: TestCapabilityCensus owns that gap.
			continue
		}
		if !row.absentTrue {
			t.Errorf(`setting.%s is declared without absentTrue, but kiro-cli %s resolves an
ABSENT %s to TRUE. Withholding the key therefore ENABLES the feature, which
inverts how send reads on this row.`, key, activeKASVersion(t), key)
		}
	}
}
