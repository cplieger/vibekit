package translate

// A runtime census of the `_meta.kiro` fields KAS sends and vibekit drops.
//
// KAS owns this payload's shape and nothing documents it. Every field
// below is decoded into a Go struct, so a field KAS starts sending is
// discarded by encoding/json with no error and no log line —
// `ACPModeUpdateWire` is the standing evidence the class is real: it
// read `modeId` where KAS sends `currentModeId`, so agent-initiated mode
// changes were silently never persisted.
//
// This is the runtime half of a pair. internal/kascap's census answers
// the client→agent direction statically; it cannot see this direction,
// since what KAS chooses to send is a property of a running session
// rather than of the bundle's initialize handler.
//
// Three properties keep a diagnostic from becoming a defect of its own.
// It never materializes a value: one byte of each field is read to name
// its JSON type. It is bounded twice, per frame and per process, and
// reaching the process bound latches the whole probe off rather than
// growing a map. And it cannot fail a turn: the only operations are a
// length check, one map decode whose error is discarded, byte
// comparisons, and a lock/unlock with the log emitted after the unlock.
//
// Always on: censusMeta costs 1391 ns/op and 553 B/op against
// HandleAssistantChunk's 112764 ns/op and 845747 B/op — 1.2% of the time
// on the hottest path in the app. A sample budget would be actively
// wrong here on top of unnecessary: the fields most worth discovering
// ride rare frames (a refusal, a policy denial, a checkpoint), which is
// exactly what a per-frame budget spends itself before reaching.

import (
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"

	"github.com/cplieger/runesafe/v2"
)

// maxCensusObjectBytes skips the probe for an oversized `_meta.kiro`
// object. The map decode is O(keys), so without this a hostile or
// malformed block carrying many keys turns one frame into that many map
// inserts. 64 KiB is orders of magnitude above every real block measured
// on this wire.
const maxCensusObjectBytes = 64 << 10

// maxCensusKeys bounds the distinct (name, type) pairs held for the life
// of the process. Both halves of a key are backend-controlled, so an
// unbounded map is a memory sink; reaching the cap disables the probe
// rather than continuing.
const maxCensusKeys = 128

// maxCensusNameBytes bounds one reported field name: backend-controlled
// text going to a logfmt line, sanitized and capped like any other
// untrusted string on a human-read surface.
const maxCensusNameBytes = 64

// censusLedger remembers which (name, type) pairs have been reported, so
// a stream of per-turn frames reports each novel shape once.
//
// A mutex and a plain map, not an unlocked module-level set: a
// concurrent map write in Go is a fatal, unrecoverable runtime error, and
// BridgeCoordinator.Forward runs one goroutine per bridge, so N open
// chats means N concurrent decoders.
//
// A plain map rather than sync.Map: the write path runs once per novel
// key, and len() has to be exact for the cap below, which sync.Map
// cannot give without a second counter.
type censusLedger struct {
	// reported leads: govet fieldalignment wants the pointer-bearing field ahead
	// of the mutex, and the bool last.
	reported map[string]struct{}
	mu       sync.Mutex
	off      bool
}

var census = &censusLedger{reported: make(map[string]struct{}, maxCensusKeys)}

// claim reports whether name is novel, recording it if so. It also answers false
// once the ledger is full, which is what latches the probe off.
func (l *censusLedger) claim(name string) (novel, full bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.off {
		return false, false
	}
	if _, seen := l.reported[name]; seen {
		return false, false
	}
	if len(l.reported) >= maxCensusKeys {
		l.off = true
		return false, true
	}
	l.reported[name] = struct{}{}
	return true, false
}

// disabled reports whether the ledger has latched off, so the hot path can skip
// the decode entirely rather than paying for it and discarding the result.
func (l *censusLedger) disabled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.off
}

// knownKeys returns the cached lowercased member-name set for a type.
//
// Cached because censusMeta runs on every frame and the walk below
// allocates a map and reflects over every field — a per-frame
// recomputation is the one way this diagnostic could show up in a
// profile.
//
// The returned map is shared and must be treated as read-only.
func knownKeys(t reflect.Type) map[string]struct{} {
	if cached, ok := knownKeyCache.Load(t); ok {
		if set, isSet := cached.(map[string]struct{}); isSet {
			return set
		}
	}
	derived := knownKeysOf(t)
	knownKeyCache.Store(t, derived)
	return derived
}

// knownKeyCache memoizes knownKeysOf per type. A sync.Map rather than a guarded
// map because this one is read-mostly with a fixed key set (two types today), the
// exact shape sync.Map is for — unlike the ledger, whose cap needs an exact len().
var knownKeyCache sync.Map

// knownKeysOf returns the lowercased JSON member names a struct type
// consumes, including those of any embedded or nested struct reached by
// a field with no json tag of its own.
//
// Derived from the struct tags rather than hand-listed, so the known set
// cannot drift from the parser it describes.
//
// Lowercased because encoding/json matches object members
// case-insensitively: a frame sending `MessageId` is consumed by the
// `messageId` field, so a case-sensitive comparison would report a field
// that was read.
func knownKeysOf(t reflect.Type) map[string]struct{} {
	out := make(map[string]struct{})
	collectKnownKeys(t, out, 0)
	return out
}

// maxCensusDepth bounds the tag walk. A wire struct is a handful of levels deep;
// the bound exists so a recursive type cannot spin rather than as a real limit.
const maxCensusDepth = 8

func collectKnownKeys(t reflect.Type, out map[string]struct{}, depth int) {
	if depth > maxCensusDepth {
		return
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch {
		case name == "-":
			continue
		case name != "":
			out[strings.ToLower(name)] = struct{}{}
		default:
			// No tag: encoding/json matches the FIELD name for an ordinary field,
			// and promotes an embedded struct's members into this object.
			if f.Anonymous {
				collectKnownKeys(f.Type, out, depth+1)
				continue
			}
			out[strings.ToLower(f.Name)] = struct{}{}
		}
	}
}

// jsonKindOf names a raw value's JSON type from its first byte, which is
// all the probe ever reads. Naming the type and never the value is what
// makes the report safe to log: a field's contents cannot leak from code
// that does not decode them.
func jsonKindOf(raw json.RawMessage) string {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return "object"
		case '[':
			return "array"
		case '"':
			return "string"
		case 't', 'f':
			return "bool"
		case 'n':
			return "null"
		default:
			return "number"
		}
	}
	return "empty"
}

// censusMeta reports, once per process, every member of one `_meta.kiro`
// object that the given wire type does not consume.
//
// declined names members the type deliberately does not read: `preview`
// and `rawOutput` are skipped on purpose, so without listing them the
// probe would fire on its first frame and mute itself before reporting
// anything real.
func censusMeta(label string, raw json.RawMessage, target reflect.Type, declined ...string) {
	if len(raw) == 0 || len(raw) > maxCensusObjectBytes || census.disabled() {
		return
	}
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		// Not an object, or malformed. The caller's own decode reports that; a
		// diagnostic must not report it twice.
		return
	}
	known := knownKeys(target)
	for name, value := range members {
		if _, ok := known[strings.ToLower(name)]; ok {
			continue
		}
		if declines(declined, name) {
			continue
		}
		kind := jsonKindOf(value)
		key := label + "." + strings.ToLower(name) + ":" + kind
		novel, full := census.claim(key)
		if full {
			slog.Warn("wire census: field budget spent, probe disabled for this process",
				"limit", maxCensusKeys)
			return
		}
		if !novel {
			continue
		}
		slog.Warn("wire census: UNKNOWN _meta.kiro field, dropped — KAS may have added one",
			"frame", label,
			"field", runesafe.SanitizeSingleLineBounded(name, maxCensusNameBytes),
			"type", kind)
	}
}

// declines reports whether name is one the caller deliberately does not read.
//
// A linear scan over the caller's own short list rather than a merge into the
// cached known set, because that set is SHARED: adding to it would mutate a map
// every other caller reads, and copying it per frame would put back the
// allocation the cache exists to remove. Today the longest list has one member.
func declines(declined []string, name string) bool {
	for _, d := range declined {
		if strings.EqualFold(d, name) {
			return true
		}
	}
	return false
}

// censusMeteringUnit reports a metering unit label vibekit does not sum.
//
// The one place a value is reported rather than a name, because here the
// label is the discovery: the field name `unit` is known, so no
// field-name probe can see that KAS started billing in a new dimension.
//
// Safe to log: a unit is a low-cardinality dimension name from the
// backend's own billing vocabulary, never a quantity or an identifier.
// It is sanitized and capped anyway, since it is still backend-controlled
// text on a log line.
func censusMeteringUnit(unit string) {
	if unit == "" || unit == meteringUnitCredit || census.disabled() {
		return
	}
	safe := runesafe.SanitizeSingleLineBounded(unit, maxCensusNameBytes)
	novel, full := census.claim("meteringUsage.unit=" + safe)
	if full {
		slog.Warn("wire census: field budget spent, probe disabled for this process",
			"limit", maxCensusKeys)
		return
	}
	if !novel {
		return
	}
	slog.Warn("wire census: UNKNOWN metering unit, not counted as spend — "+
		"KAS may have added a billing dimension", "unit", safe)
}
