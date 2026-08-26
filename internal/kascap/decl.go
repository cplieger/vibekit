// Package kascap declares which capability keys vibekit puts on the kiro-cli
// (KAS) ACP wire, on which call, why, and how KAS resolves each one.
//
// It exists because four things a reader needs had nowhere to live. The
// initialize handshake used to build its _meta.kiro block as a hand-written map
// literal in internal/bridge, and a literal can express only the keys vibekit
// DOES send:
//
//   - Which CALL carries a key. Both doors are live: most keys ride initialize,
//     and the ones KAS resolves per session ride session/new and session/load.
//     A key on the wrong door resolves to its absent default in silence.
//   - How KAS RESOLVES it. A client capability compared against true is not a
//     settings entry read through isSettingEnabled, and treating one as the
//     other costs a whole subsystem with nothing in any log to say so.
//   - Whether an ABSENT key resolves TRUE. semanticReview does, so vibekit
//     gets it by not sending it, and a literal has no way to write that down.
//   - That a key is deliberately WITHHELD. A literal has no row for a key it
//     omits, so a decision is indistinguishable from an oversight.
//
// The table in table.go is the record. Capabilities and SessionMeta are
// projections of it, and they are the whole exported surface: the declaration
// machinery stays unexported so no caller can mutate the table, and a consumer
// that needs to read it should gain an accessor returning a copy.
package kascap

import "github.com/cplieger/envx/v2"

// door names the ACP call that carries a capability key.
//
// The two doors have different lifetimes (one handshake per subprocess against
// one message per session), so a key's door is a real property of the key rather
// than an accident of where the code that sends it happens to sit. KAS decides
// it: a key its initialize handler stores on clientMeta rides the connection,
// and a key its session builders resolve out of the session call's own _meta
// rides the session. Getting it wrong costs the whole feature and says nothing
// anywhere, which is what happened to workflows.
type door string

const (
	// doorUnset is the zero value, and it is deliberately not a valid door: a
	// row that forgets to declare one must fail a well-formedness test rather
	// than default silently onto the connection door.
	doorUnset door = ""
	// doorConnection is the initialize handshake, sent once per kiro-cli
	// subprocess before any session exists.
	doorConnection door = "connection"
	// doorSession is the per-session door, sent on BOTH session/new and
	// session/load. Both, or a resumed chat quietly gets a different agent
	// from a fresh one: KAS resolves a session key from the call's own _meta
	// first and falls back to the value persisted at creation, so a session
	// created before a key existed never gains it on load unless the client
	// sends it there too.
	doorSession door = "session"
)

// resolver is how KAS reads a key, and it decides where the key sits in the
// payload as well as what its value has to look like.
//
// This is the distinction the old literal could not state. All three shapes
// were written as adjacent lines of one map, so the fact that they are read by
// three different mechanisms lived only in prose.
type resolver string

const (
	// resolverUnset is the zero value and is not a valid resolver.
	resolverUnset resolver = ""
	// resolverCapability is a key at the TOP level of _meta.kiro that KAS
	// tests for truth directly (resolveCapabilities does `=== true`; a few
	// sites read it for truthiness). It is not a settings entry and does not
	// go through isSettingEnabled, so its value is a bare bool.
	resolverCapability resolver = "capability"
	// resolverSetting is a key under _meta.kiro.settings that KAS reads
	// through isSettingEnabled, which returns val.enabled for an object and
	// false for anything else, an absent key included. So its value is the
	// object {"enabled": true} and never a bare true, which would resolve
	// false and silently disable the feature.
	resolverSetting resolver = "setting"
	// resolverObject is a key at the top level of _meta.kiro whose value is an
	// OBJECT KAS destructures rather than a boolean it compares. hooks is the
	// instance: KAS requires the value to be an object carrying a v2 member
	// and then checks that member, so `hooks: true` would enable nothing.
	resolverObject resolver = "object"
)

// Spawn carries the per-bridge facts the gated rows read. Every field is a
// decision the caller has already made for THIS subprocess, never a preference
// this package could look up for itself.
type Spawn struct {
	// Presets are the KAS policy-preset ids this session opens with, from the
	// active security profile (policyfile.Profile). EMPTY is meaningful and is
	// not a missing value: it means the Custom profile, where the permissions
	// files are the whole policy, so the key is withheld rather than sent empty.
	//
	// A slice on Spawn rather than a compiled row value because the profile is a
	// user setting that changes without a rebuild. It reaches the wire through the
	// policyPreset row's gate, on session/new and session/load alike, because KAS
	// re-reads the ids on each and persists neither. Both doors must therefore
	// resolve from the SAME source rather than each computing its own answer — a
	// resumed session picking up a profile the user has since changed is the
	// intended way a change reaches an existing chat, but the two doors disagreeing
	// for any other reason would be a posture nobody selected.
	Presets []string
	// SecretStorage is whether the caller has a credential store standing
	// behind the secretStorage capability. See that row's Because: declaring
	// the capability without a store is worse than declining it.
	SecretStorage bool
	// Hooks is whether this bridge opts into KAS's v2 hook engine.
	Hooks bool
	// ToolSearch is whether this session ships KAS's tool_search tool instead of
	// every MCP tool's full description. Presence-gated: absent already resolves
	// false at isSettingEnabled, so an off state has nothing to say on the wire.
	ToolSearch bool
	// Knowledge is whether this session gets the knowledge feature, and it gates
	// TWO rows — the `knowledge` capability and the `knowledge` setting — because
	// they are two thirds of one gate and splitting them is what shipped a
	// knowledge UI over a store the agent had no tool to query.
	//
	// Value-gated rather than presence-gated, unlike ToolSearch, and that
	// asymmetry is deliberate: the capability's resolver compares `=== true`, so
	// its key has to be present with a real boolean either way, and sending the
	// pair together keeps one field from meaning two different things about
	// presence.
	Knowledge bool
	// Memory is whether this session opts into kiro-cli's memory subsystem. It
	// gates the `userMemoryOptIn` row, which is ALWAYS PRESENT either way — the
	// row's value is the veto, so withholding it is the one state that must never
	// happen (see that row's because for the tri-state read).
	//
	// Value-gated for that reason, like Knowledge and unlike ToolSearch. The
	// second half of this switch is not on the wire at all: the child environment
	// carries KIRO_FEATURE_MEMORY_EXTERNAL_ENABLED, because the settings bridge
	// cannot reach the gate's eligibility term.
	Memory bool
}

// decl is one capability key vibekit can put on the wire, with everything a
// reader needs to judge it in one place.
type decl struct {
	// key is the wire key, unqualified. A resolverSetting row's key is its
	// name inside _meta.kiro.settings, not a dotted path, because the
	// resolver already says which container it lands in.
	key string

	// because is why vibekit sends this key, or why it withholds it. MANDATORY
	// and non-empty, enforced by TestEveryDeclHasABecause.
	//
	// This is the most valuable column and the reason the package exists. Each
	// entry is the rationale that used to sit as a comment beside the literal,
	// carried over verbatim: what the key buys, what breaks without it, what it
	// costs, and where the handler lives. A row whose because is a restatement
	// of its key teaches nothing and should be treated as missing.
	because string

	// env optionally names an environment variable an operator can set to stop
	// vibekit sending this key, so a capability that misbehaves in a deployment
	// can be switched off without waiting for a release.
	//
	// Read by buildDoor through envx.Bool, which means the fallback is this
	// row's compiled send: unset or empty leaves the row alone, and a value
	// envx cannot parse logs one Warn and leaves the row alone too. So a typo
	// in a compose file cannot silently disable a capability.
	//
	// DISABLE-ONLY, and structurally rather than by convention: the column may
	// sit only on a send:true row (TestEnvOverrideOnlyOnSentRows). A withheld
	// row carries no wire value by design, so an operator who could turn one on
	// would put a JSON null on the wire. It is also the honest shape for what
	// this is: an off switch for a shipped capability, not a rollout gate for an
	// unfinished one.
	//
	// Typed as an envx.Key because it IS a variable name: the type keeps a
	// value from reaching a getter's key position, and envx validates the
	// spelling on first read.
	env envx.Key

	// value is the wire value for an ungated row. Set explicitly on every such
	// row rather than derived from the resolver, so the table never sends a
	// nil that JSON renders as null; TestDeclIsWellFormed enforces it.
	// A gated row leaves this nil and its gate supplies the value.
	value any

	// gate, when non-nil, decides this row at spawn time. It returns the
	// value to send and whether to send the key at all, because those are two
	// different mechanisms and both are in use: secretStorage is always
	// present with a runtime VALUE, while hooks is present only when enabled.
	// Collapsing them into one boolean would lose the difference.
	gate func(Spawn) (value any, present bool)

	// door is the call that carries this key.
	door door

	// resolver is how KAS reads it, which also decides where it sits.
	resolver resolver

	// absentTrue records that KAS resolves an ABSENT key to TRUE.
	//
	// The column exists because there was previously nowhere to write this
	// down, and it inverts the reading of send: on such a row, NOT sending the
	// key is what enables the feature, and sending {"enabled": false} is what
	// turns it off. semanticReview is the instance.
	absentTrue bool

	// send is whether vibekit puts this key on the wire at all.
	//
	// A send:false row is a DECLARATION that vibekit deliberately withholds a
	// key, which is exactly what a map literal cannot express: a literal has no
	// row for a key it omits. Such a row must say why in because, which
	// TestNoSendWithoutReason enforces.
	send bool
}
