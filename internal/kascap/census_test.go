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

	// jsIdent matches one JavaScript identifier. Every anchor below is built
	// from it rather than from a spelled-out upstream name, because kiro-cli
	// 2.20.0 (KAS 0.53.3) began shipping a MANGLED bundle: 23.3 MB became
	// 11.3 MB and every local, parameter and module-level function name in it
	// was rewritten. resolveCapabilities became uTr, isSettingEnabled became
	// JE, the kiroMeta local became n. Nothing about the wire changed.
	//
	// So no anchor here may name an identifier. What minification cannot touch
	// is PROPERTY names, STRING literals and code STRUCTURE, and each source
	// below is re-anchored onto one of those three. Where a mangled name is
	// unavoidable the test DISCOVERS it (discoverOne) from a property name or
	// a body signature, then uses what it found.
	jsIdent = `[A-Za-z_$][A-Za-z0-9_$]*`
)

var (
	kiroCLIVersionRe = regexp.MustCompile(`(?m)^KIRO_CLI_VERSION="([^"]+)"`)

	// capResolverRe NAMES the mangled resolveCapabilities, the one function that
	// maps the whole _meta.kiro block onto a resolved struct.
	//
	// Anchored on two PROPERTY names a minifier may not rewrite, because both
	// are read back by other code: the field it assigns (resolvedCapabilities)
	// and the field it is handed (clientCapabilities). At 2.19.2 that site reads
	// `this.resolvedCapabilities = resolveCapabilities(params.clientCapabilities)`
	// and at 2.20.0 `this.resolvedCapabilities=uTr(t.clientCapabilities)`, so
	// one pattern covers both and returns whatever the function is called in
	// this build.
	capResolverRe = regexp.MustCompile(
		`resolvedCapabilities\s*=\s*(` + jsIdent + `)\(\s*` + jsIdent + `\.clientCapabilities\s*\)`,
	)

	// clientMetaReadRe matches a read off the agent's stored copy. this.clientMeta
	// is assigned from the initialize block and from nothing else, and it is a
	// property name, so it survives mangling.
	//
	// KNOWN NARROWING, and it is why this source is not trusted alone: a
	// minified build may alias the field into a local before reading it, and
	// then the read is spelled off the alias instead. Measured on 2.20.0,
	// `clientMeta?.backgroundProcesses === true` became
	// `y?.backgroundProcesses===!0`, so this pattern reports 2 keys where
	// 2.19.2 gave 4. An alias cannot be chased without scope analysis, so the
	// gap is recorded rather than closed: backgroundProcesses is declared in the
	// table, TestACPMethodsPresent covers the verbs, and requireNonEmpty cannot
	// see a subset. Do not read a key's absence from the census as proof that
	// KAS stopped reading it — check the bundle for the literal first.
	clientMetaReadRe = regexp.MustCompile(`\bclientMeta\??\.(` + jsIdent + `)`)

	// initBindRe is the ONE site where the agent server binds a local to the
	// client's _meta.kiro block, and its match position starts the bounded
	// window below. The receiver is wildcarded because it is the initialize
	// handler's parameter, which mangling renames (`params` at 2.19.2, `t` at
	// 2.20.0); the two property names either side of it are what make the site
	// identifiable, and capture group 1 is the local the window then reads off.
	//
	// The window matters because a bare sweep for the bound local cannot work
	// once names are mangled: at 2.20.0 the local is `n`, one character, which
	// appears everywhere. Scoping the sweep to 8 KiB after this unique binding
	// is what keeps a single-letter name usable.
	initBindRe = regexp.MustCompile(
		`(` + jsIdent + `)\s*=\s*` + jsIdent + `\.clientCapabilities\?\._meta\?\.kiro`,
	)

	// settingEnabledFnRe NAMES the mangled isSettingEnabled by its BODY, because
	// the function is module-local and its identifier does not survive.
	//
	// The body is the signature: take a container and a key, read the member,
	// and report its `.enabled` only when the member is a non-null object. That
	// shape is distinctive enough to identify one function and it is written in
	// syntax and property names rather than identifiers, so it reads the same
	// mangled or not. At 2.20.0 it resolves to JE.
	settingEnabledFnRe = regexp.MustCompile(
		`function\s+(` + jsIdent + `)\s*\(` + jsIdent + `,\s*` + jsIdent +
			`\)\s*\{\s*(?:let|var|const)\s+` + jsIdent + `\s*=\s*` + jsIdent +
			`\[` + jsIdent + `\];\s*return typeof\s+` + jsIdent +
			`\s*==\s*"object"\s*&&\s*` + jsIdent + `\s*!==\s*null\s*\?\s*` +
			jsIdent + `\.enabled\s*:\s*!1\s*\}`,
	)

	// settingEnabledRe is the direct settings gate, absent-means-false, for a
	// bundle that still spells the callee out. Kept beside the discovered name
	// above so an un-minified build keeps working unchanged.
	//
	// Anchored on the CALLEE and the quoted key, not on the first argument. That
	// argument varies by site — `settings`, `this.clientMeta?.settings ?? {}`,
	// `parsedProviderSettings.data` — and the bundle mixes quote styles, so the
	// old `isSettingEnabled\(settings,\s*"…"` form silently saw only 5 of the 7
	// literal gates. It missed the `goal` gate and `_providerPowers` entirely,
	// which is a large part of why four newly-declared keys had never appeared in
	// unclaimed.txt.
	settingEnabledRe = regexp.MustCompile(`isSettingEnabled\([^,()]*,\s*["'](` + jsIdent + `)["']\)`)

	// featureEnabledFnRe NAMES the mangled isFeatureEnabled wrapper. The wrapper
	// forwards to a METHOD of the model-config provider, and a method name is a
	// property name, so `.isFeatureEnabled(` survives mangling and identifies the
	// one-line function that calls it. At 2.20.0 it resolves to jp.
	featureEnabledFnRe = regexp.MustCompile(
		`function\s+(` + jsIdent + `)\s*\(` + jsIdent + `\)\s*\{\s*return\s+` +
			jsIdent + `\.isFeatureEnabled\(` + jsIdent + `\)\s*\}`,
	)

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
	//
	// Unanchored on the left so it matches both a bare call and a method call;
	// the wrapper's own call sites are swept separately under its discovered name.
	featureEnabledRe = regexp.MustCompile(`isFeatureEnabled\(["'](` + jsIdent + `)["']\)`)

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
	// settingDestructureRe below), while requiring the member access after it
	// leaves exactly the settings resolvers: measured on 2.19.2, 7 matches over 6
	// distinct keys and no noise, and the same 6 keys at 2.20.0. Wildcarding the
	// receiver already made this the ONE source mangling did not break.
	settingResolverRe = regexp.MustCompile(jsIdent + `\.data\.(` + jsIdent + `)\?\.enabled`)

	// settingDestructureRe is the resolver family's SECOND spelling. A resolver
	// with a compound value (resolveSpecPlan, resolveSessionEviction) binds the
	// member to a local before reading it, so `?.enabled` never follows the member
	// access and settingResolverRe cannot see it.
	//
	// Anchored on the BINDING STATEMENT, which is what lets the local be a
	// wildcard: a bare `<anything>.data.<key>` also matches an unrelated
	// persisted-state parse and would additionally report files, syncedAt, tools
	// and version as settings keys, while requiring `let|var|const <local> =`
	// in front and a `,` or `;` behind leaves exactly the two compound
	// resolvers. Measured: specPlan and sessionEviction, on 2.19.2 and 2.20.0
	// alike — the same two keys the old `const setting = parsed2.data.…` form
	// found before the local and the keyword were mangled.
	settingDestructureRe = regexp.MustCompile(
		`(?:let|var|const)\s+` + jsIdent + `\s*=\s*` + jsIdent + `\.data\.(` + jsIdent + `)[,;]`,
	)

	// absentTrueResolverRe is settingResolverRe's inverse-default subset: a
	// resolver whose fallback chain ends in a literal true, so an ABSENT key
	// resolves TRUE. This is the bundle-side witness for the absentTrue column.
	//
	// Both of its old anchors were casualties of mangling: the receiver was the
	// literal local `parsed2`, now wildcarded like settingResolverRe's, and the
	// fallback was the literal word `true`, which a minifier writes `!0`. Accept
	// either spelling or this source silently reports nothing, which would read
	// as "no key defaults to true" and quietly invert how send is judged on such
	// a row. Measured: semanticReview, and only semanticReview, on both bundles.
	absentTrueResolverRe = regexp.MustCompile(
		jsIdent + `\.data\.(` + jsIdent + `)\?\.enabled\s*\?\?[^;]*?\?\?\s*(?:true|!0)`,
	)
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
// yet. Check `ls ~/.local/share/kiro-cli/kas/` first though: a bump that has
// already shipped in the image leaves the pinned bundle unpacked there, and then
// the ~720 MB fetch below buys nothing. Fetch and unpack one only when the
// pinned version is genuinely absent, without touching the running install:
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
	raw, path := bundleSource(t, active)

	pinnedRaw, err := os.ReadFile(kasVersionPath)
	if err != nil {
		t.Fatalf("read %s (regenerate with: %s): %v", kasVersionPath, censusUpdateCmd, err)
	}
	pinned := strings.TrimSpace(string(pinnedRaw))

	// The stale-fixture check sits AFTER the bundle lookup, and the order is the
	// whole difference between a gate and an obstruction.
	//
	// It ran first until 2026-08-27, so a Renovate bump of the pin failed this
	// test on a machine that has no bundle and no way to get one — which is
	// every CI runner. The fixture can only be regenerated where the bundle is,
	// so failing where it is absent asks for work that cannot be done there,
	// and it blocked every kiro-cli PR on a red `go / validate` that said
	// nothing about whether the upgrade was safe. The comment above this
	// function already stated the intent this ordering now matches: local-only,
	// stage 1 gates CI.
	//
	// Where a bundle IS present the mismatch is still fatal, because that is
	// exactly the machine that can answer it, and the review it forces is the
	// point of the fixture.
	if pinned != active && !*updateCensus {
		t.Fatalf(`census fixture was generated against kiro-cli %s, active is %s.
A version bump can add, rename or drop a client capability, so this fixture has
to be RE-READ rather than diffed. Regenerate and review every added or dropped
line:
  %s`, pinned, active, censusUpdateCmd)
	}
	if *updateCensus && pinned != active {
		if err := os.WriteFile(kasVersionPath, []byte(active+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", kasVersionPath, err)
		}
		t.Logf("re-pinned %s to %s (read from %s)", kasVersionPath, active, path)
	}
	return raw
}

// bundleSource reads the pinned agent server's source, skipping when it is not
// installed locally, and knows nothing about the census fixture.
//
// Separate from loadBundle so a test can read the bundle WITHOUT inheriting the
// fixture's staleness gate. TestACPMethodsPresent is the caller that needs
// that: what it asserts is true or false about a bundle on its own terms, and
// making it wait on a fixture review would tie the one check that answers "is
// this upgrade safe" to the one that cannot run in CI.
func bundleSource(t *testing.T, active string) (src, path string) {
	t.Helper()
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
	read := pristineBundle(matches[0])
	raw, err := os.ReadFile(read)
	if err != nil {
		t.Fatalf("read bundle %s: %v", read, err)
	}
	return string(raw), read
}

// pristineBundle returns the .orig sibling of path when one exists, and path
// itself otherwise.
//
// The local kiro-cli patches rewrite acp-server.js IN PLACE and keep the
// unpatched bundle beside it as .orig; four of the nine live in that very file.
// Without this preference every census read and every -update regeneration
// treats a locally modified bundle as upstream — and a patched machine is the
// only kind that can produce these fixtures, since the glob finds nothing in CI.
func pristineBundle(path string) string {
	orig := path + ".orig"
	if fi, err := os.Stat(orig); err == nil && fi.Mode().IsRegular() {
		return orig
	}
	return path
}

// TestBundleSource_PrefersThePristineSibling pins the .orig preference, which is
// the difference between reading upstream and reading our own patches.
func TestBundleSource_PrefersThePristineSibling(t *testing.T) {
	const active = "9.9.9"
	live := fakeBundle(t, active, "patched")
	orig := live + ".orig"
	if err := os.WriteFile(orig, []byte("pristine"), 0o600); err != nil {
		t.Fatalf("write %s: %v", orig, err)
	}

	src, path := bundleSource(t, active)
	if src != "pristine" {
		t.Errorf("bundleSource(%q) read %q, want %q (the .orig sibling)", active, src, "pristine")
	}
	if path != orig {
		t.Errorf("bundleSource(%q) path = %q, want %q", active, path, orig)
	}
}

// TestBundleSource_FallsBackToTheLiveBundle covers the unpatched machine, where
// there is no .orig to prefer.
func TestBundleSource_FallsBackToTheLiveBundle(t *testing.T) {
	const active = "9.9.9"
	live := fakeBundle(t, active, "stock")

	src, path := bundleSource(t, active)
	if src != "stock" {
		t.Errorf("bundleSource(%q) read %q, want %q", active, src, "stock")
	}
	if path != live {
		t.Errorf("bundleSource(%q) path = %q, want %q", active, path, live)
	}
}

// fakeBundle writes content to a stand-in agent-server bundle for version active
// under a scratch HOME, and returns its path.
func fakeBundle(t *testing.T, active, content string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "share", "kiro-cli", "kas",
		active+"-0000", "node_modules", "@kiro", "agent", "dist", "server")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "acp-server.js")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Setenv("HOME", home)
	return path
}

// jsFuncBody returns the balanced-brace run that starts at the brace at index
// open, so a caller can scope a sweep to ONE function of the bundle.
//
// Scoping is what makes a mangled local usable at all: at 2.20.0 the capability
// block is bound to `n`, and a bundle-wide sweep for a one-character name is
// meaningless. String and template literals are skipped so a brace inside one
// cannot unbalance the count.
func jsFuncBody(src string, open int) string {
	depth := 0
	for i := open; i < len(src); i++ {
		switch c := src[i]; c {
		case '"', '\'', '`':
			for i++; i < len(src); i++ {
				if src[i] == '\\' {
					i++
					continue
				}
				if src[i] == c {
					break
				}
			}
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return src[open : i+1]
			}
		}
	}
	return ""
}

// discoverOne returns the single capture-group-1 match of re, and fails when
// there is not exactly one.
//
// Every dynamic anchor goes through here, because "exactly one" is the
// invariant that makes naming a mangled function trustworthy. Zero means the
// structural shape the pattern describes was reshaped upstream and the source
// is blind; more than one means the shape is no longer unique and the name it
// returns would be a guess. Both are reviews, and neither may pass silently:
// this census's one unfixable weakness is that it cannot detect a source
// matching a SUBSET, so the checks it CAN make have to be strict.
func discoverOne(t *testing.T, what string, re *regexp.Regexp, src string) string {
	t.Helper()
	seen := make(map[string]bool)
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		seen[m[1]] = true
	}
	names := slices.Sorted(maps.Keys(seen))
	if len(names) != 1 {
		t.Fatalf(`%s: found %d candidate names %v in the bundle, want exactly 1.
This anchor names a mangled upstream function from its surrounding structure,
so it holds only while that structure is unique. Re-read the pattern against
the bundle; do not regenerate the fixture, which would record the miss as the
new expectation.`, what, len(names), names)
	}
	return names[0]
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
	resolved := requireNonEmpty(t, "resolveCapabilities body", resolvedBlockKeys(t, src))
	stored := requireNonEmpty(t, "clientMeta reads", keysIn(clientMetaReadRe, src))
	adhoc := requireNonEmpty(t, "initialize-scope reads", initScopeKeys(t, src))

	all := slices.Concat(resolved, stored, adhoc)
	slices.Sort(all)
	return slices.Compact(all)
}

// resolvedBlockKeys returns the keys resolveCapabilities reads off the client's
// TOP-LEVEL _meta.kiro block.
//
// Three steps, because none of the names involved survives mangling: name the
// function from the property names either side of its call site, scope the sweep
// to its body, then find the local it binds the block to and read off that.
//
// The last step is the one with a trap in it. The function binds TWO _meta.kiro
// blocks — the top-level one off its own parameter, and the `fs` sub-block off
// `<param>.fs` — and only the first is in this census's scope. The sub-block's
// keys (readFile, writeFile, stat, readDirectory, delete) appear in neither the
// table nor the fixture, which is how we know the pre-mangling pattern never
// saw them either: it matched a local literally named kiroMeta, and the fs local
// was not it. So the binding must be anchored on the PARAMETER, not on any
// receiver, or this source silently grows five keys nobody withheld.
func resolvedBlockKeys(t *testing.T, src string) []string {
	t.Helper()
	name := discoverOne(t, "resolveCapabilities", capResolverRe, src)

	fnRe := regexp.MustCompile(`function\s+` + regexp.QuoteMeta(name) +
		`\s*\((` + jsIdent + `)\)\s*\{`)
	loc := fnRe.FindStringSubmatchIndex(src)
	if loc == nil {
		t.Fatalf(`resolveCapabilities resolved to %q, but no `+
			`"function %s(<one parameter>) {" declares it.
The name came from its call site, so it exists; the declaration is either
spelled another way (an arrow function, a method) or takes a different number
of parameters. Re-read the pattern rather than regenerating the fixture.`,
			name, name)
	}
	param := src[loc[2]:loc[3]]
	body := jsFuncBody(src, loc[1]-1)
	if body == "" {
		t.Fatalf("resolveCapabilities (%s): unbalanced braces from its declaration", name)
	}

	bindRe := regexp.MustCompile(`(` + jsIdent + `)\s*=\s*` +
		regexp.QuoteMeta(param) + `\??\._meta\?\.kiro`)
	local := discoverOne(t, "the top-level _meta.kiro local in "+name, bindRe, body)
	return keysIn(regexp.MustCompile(`\b`+regexp.QuoteMeta(local)+`\??\.(`+jsIdent+`)`), body)
}

// initScopeKeys returns the keys read off the local the initialize handler binds
// to the client's _meta.kiro block, within a bounded window after that binding.
func initScopeKeys(t *testing.T, src string) []string {
	t.Helper()
	local := discoverOne(t, "the initialize-scope _meta.kiro local", initBindRe, src)
	loc := initBindRe.FindStringIndex(src)
	end := min(loc[0]+capabilityWindow, len(src))
	window := src[loc[0]:end]
	return keysIn(regexp.MustCompile(`\b`+regexp.QuoteMeta(local)+`\??\.(`+jsIdent+`)`), window)
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
//
// The two gates are each swept twice, under the spelled-out callee AND under
// the name discovered for a mangled build, because a bundle carries one or the
// other and the union is correct for both.
func readSettingKeys(t *testing.T, src string) []string {
	t.Helper()
	settingFn := discoverOne(t, "isSettingEnabled", settingEnabledFnRe, src)
	featureFn := discoverOne(t, "the isFeatureEnabled wrapper", featureEnabledFnRe, src)

	direct := requireNonEmpty(t, "isSettingEnabled calls", slices.Concat(
		keysIn(settingEnabledRe, src),
		keysIn(regexp.MustCompile(`\b`+regexp.QuoteMeta(settingFn)+
			`\([^,()]*,\s*["'](`+jsIdent+`)["']\)`), src),
	))
	features := requireNonEmpty(t, "isFeatureEnabled calls", slices.Concat(
		keysIn(featureEnabledRe, src),
		keysIn(regexp.MustCompile(`\b`+regexp.QuoteMeta(featureFn)+
			`\(["'](`+jsIdent+`)["']\)`), src),
	))
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
