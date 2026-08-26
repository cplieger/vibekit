package kascap

import (
	"flag"
	"maps"
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
	//
	// Anchored on the CALLEE and the quoted key, not on the first argument. That
	// argument varies by site — `settings`, `this.clientMeta?.settings ?? {}`,
	// `parsedProviderSettings.data` — and the bundle mixes quote styles, so the
	// old `isSettingEnabled\(settings,\s*"…"` form silently saw only 5 of the 7
	// literal gates. It missed the `goal` gate and `_providerPowers` entirely,
	// which is a large part of why four newly-declared keys had never appeared in
	// unclaimed.txt.
	settingEnabledRe = regexp.MustCompile(`isSettingEnabled\([^,()]*,\s*["']([A-Za-z_$][A-Za-z0-9_$]*)["']\)`)

	// featureEnabledRe is the FEATURE-FLAG gate, and it is the reader shape this
	// census was blind to for its whole life.
	//
	// isFeatureEnabled(key) resolves through the model-config provider rather than
	// straight off _meta.kiro.settings, which is why it does not look like a
	// settings read. It is one: the bundle bridges the two with
	// `isSettingEnabled(initSettings, feature)` and `isSettingEnabled(clientSettings,
	// feature)` — a VARIABLE key, so those two sites are invisible to
	// settingEnabledRe above — so a key consulted here can be supplied by the
	// client exactly like a direct gate.
	//
	// Measured cost of the blindness on 2.19.2: 15 keys read only through this
	// shape, of which 12 had never appeared in unclaimed.txt. One of them,
	// memoryEnable, is a key the table WITHHOLDS on the strength of this very
	// site, so the census was failing to see the evidence for a decision it
	// already recorded.
	featureEnabledRe = regexp.MustCompile(`isFeatureEnabled\(["']([A-Za-z_$][A-Za-z0-9_$]*)["']\)`)

	// settingResolverRe is the resolver family, which reads the same block but
	// applies a per-key default instead.
	//
	// The RECEIVER is a wildcard, and that is the second blind spot this census
	// had. It was anchored on the literal local name `parsed2`, which covers the
	// resolvers that take a parsed settings block as a parameter and misses every
	// one that names its own — `sessionSettings.data.disableAutoCompaction?.enabled`
	// and `initializeSettings.data.disableAutoCompaction?.enabled` are the measured
	// case, a LIVE key with two readers that never once appeared in unclaimed.txt.
	//
	// The `?.enabled` suffix is what keeps the wildcard honest. A bare
	// `<anything>.data.<key>` also matches an unrelated persisted-state parse (see
	// settingDestructureRe below, which is why THAT one stays anchored), while
	// requiring the member access after it leaves exactly the settings resolvers:
	// measured on 2.19.2, 7 matches over 6 distinct keys and no noise.
	settingResolverRe = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*\.data\.([A-Za-z_$][A-Za-z0-9_$]*)\?\.enabled`)

	// settingDestructureRe is the resolver family's SECOND spelling. A resolver
	// with a compound value (resolveSpecPlan, resolveSessionEviction) binds the
	// member to a local before reading it, so `?.enabled` never follows the member
	// access and settingResolverRe cannot see it.
	//
	// Anchored on the binding statement rather than a bare `parsed2.data.X`: an
	// unrelated persisted-state parse reuses the local name `parsed2`, so the bare
	// form additionally reports files, syncedAt, tools and version as settings
	// keys. Measured against 2.18.0, this form adds exactly specPlan and
	// sessionEviction.
	settingDestructureRe = regexp.MustCompile(`const setting = parsed2\.data\.([A-Za-z_$][A-Za-z0-9_$]*)`)

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
//
// A Renovate bump arrives BEFORE the version is deployed anywhere, so the pin
// failure normally has to be answered against a bundle that is not on the volume
// yet. Fetch and unpack one without touching the running install:
//
//	curl -fsSLO https://desktop-release.q.us-east-1.amazonaws.com/<version>/kirocli-x86_64-linux.zip
//	unzip -q kirocli-x86_64-linux.zip
//	env -u KIRO_KAS_SERVER_PATH -u KIRO_KAS_NODE_PATH HOME=<scratch> \
//	  ./kirocli/bin/kiro-cli-chat acp --agent-engine v3 </dev/null
//	HOME=<scratch> go test ./internal/kascap/ -run 'TestCapabilityCensus|TestAbsentTrueMatchesTheBundle' -update
//
// The `env -u` is the load-bearing part and the reason for this paragraph.
// Inside vibekit and web-terminal-kiro the agent's OWN environment exports
// KIRO_KAS_SERVER_PATH and KIRO_KAS_NODE_PATH, a spawned kiro-cli honours them
// over its own embedded bundle, and every redirect this glob relies on (HOME,
// XDG_DATA_HOME, even the uid) is then irrelevant. Measured on the 2.19.1 →
// 2.19.2 bump: the new binary reported `kiro-cli 2.19.2`, served KAS 0.48.0 out
// of 2.19.1's tree, unpacked nothing, and left this test skipping — which reads
// as an unchanged capability surface rather than as a bundle that was never
// read. With the two variables cleared the same binary unpacked KAS 0.52.1.
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
	return slices.Sorted(maps.Keys(seen))
}

// requireNonEmpty is the guard that keeps this census honest. Every source is
// a regex over a 21 MB third-party bundle, and the way each one dies is by
// matching nothing after an upstream reshape — which would quietly turn the
// census into a file of zero findings that passes forever.
//
// It catches a source that matches NOTHING. It does NOT catch one that matches a
// SUBSET, and that is the failure this census actually had: settingEnabledRe was
// anchored on a first argument that only 5 of 7 gate sites use, so it stayed
// comfortably non-empty while missing the `goal` and `_providerPowers` gates.
// A subset match is invisible to any self-check here, because the file has no
// independent idea of how many keys there should be — the only real defence is
// re-reading the patterns against the bundle on a version bump, which is what the
// version pin forces.
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

// readSettingKeys returns the _meta.kiro.settings keys the agent server reads
// through the four STATIC shapes: the direct isSettingEnabled gate, the
// isFeatureEnabled flag gate, a resolver reading `?.enabled` inline, and a
// resolver that destructures first.
//
// It cannot see a read whose key is a VARIABLE, and that limit is inherent
// rather than a pattern to fix: `isSettingEnabled(initSettings, feature)` and
// `isSettingEnabled(clientSettings, feature)` bridge the initialize settings
// block onto the feature-flag provider with a variable key, so which keys flow
// through them is decided by the caller. That bridge is exactly why
// featureEnabledRe belongs here — a key consulted as a literal at the
// isFeatureEnabled end is client-supplyable at the isSettingEnabled end, so
// omitting the shape hid twelve reachable keys.
func readSettingKeys(t *testing.T, src string) []string {
	t.Helper()
	direct := requireNonEmpty(t, "isSettingEnabled calls", keysIn(settingEnabledRe, src))
	features := requireNonEmpty(t, "isFeatureEnabled calls", keysIn(featureEnabledRe, src))
	resolved := requireNonEmpty(t, "settings resolvers", keysIn(settingResolverRe, src))
	destructured := requireNonEmpty(t, "destructuring resolvers", keysIn(settingDestructureRe, src))
	all := slices.Concat(direct, features, resolved, destructured)
	slices.Sort(all)
	return slices.Compact(all)
}

// declaredKeys returns what the table accounts for, split by container. A
// withheld row counts as DECLARED: recording a deliberate omission is the
// table's job, so such a key is not unclaimed.
//
// Deliberately door-BLIND. The read side of this census is a set of regexes over
// the whole bundle and cannot tell which call a key was read from, so filtering
// the declared side by door would report a key vibekit sends on the session door
// as one it never considered — a permanent false finding in a fixture whose
// entries are supposed to be decisions somebody still owes. Which door a key
// belongs on is the goldens' question, and a key on the wrong one is
// TestSessionDoorKeysAbsentFromConnectionDoor's.
func declaredKeys() (capabilities, settings map[string]bool) {
	capabilities = map[string]bool{
		// The settings container is not a row; it is derived from the rows.
		settingsKey: true,
	}
	settings = make(map[string]bool)
	for _, row := range table {
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
