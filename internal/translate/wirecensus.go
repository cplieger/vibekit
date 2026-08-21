package translate

// A runtime census of the `_meta.kiro` fields KAS sends and vibekit drops.
//
// KAS owns this payload's shape and nothing documents it. Every field below is
// decoded into a Go struct, so a field KAS starts sending is discarded by
// encoding/json with no error, no log line and no way to notice — which is
// exactly how each of the ones vibekit DOES read was found: by hand-probing the
// wire. `ACPModeUpdateWire` is the standing evidence that the class is real
// rather than theoretical: it read `modeId` where KAS sends `currentModeId`, so
// agent-initiated mode changes were silently never persisted.
//
// This is the runtime half of a pair. internal/kascap's census answers the
// client→agent direction statically — which `_meta.kiro` keys the SHIPPED bundle
// is willing to read that vibekit's table does not account for. It cannot see
// this direction, because what KAS chooses to SEND is a property of a running
// session rather than of the bundle's initialize handler.
//
// Three properties keep a diagnostic from becoming a defect of its own. It never
// materializes a VALUE: one byte of each field is read to name its JSON type, so
// there is nothing for a value to leak into. It is bounded twice, per frame and
// per process, and reaching the process bound latches the whole probe off rather
// than growing a map. And it cannot fail a turn: the only operations are a length
// check, one map decode whose error is discarded, byte comparisons, and a
// lock/unlock with the log emitted after the unlock.
//
// ALWAYS ON, decided on a measurement rather than a feeling. Benchmarked on this
// container (13th-gen i5, Go 1.27): censusMeta costs 1391 ns/op and 553 B/op
// against HandleAssistantChunk's 112764 ns/op and 845747 B/op — 1.2% of the time
// and 0.065% of the bytes on the hottest path in the app. So there is no gate and
// no sampling. A sample budget would be actively wrong here on top of being
// unnecessary: the fields most worth discovering ride RARE frames (a refusal, a
// policy denial, a checkpoint), which is exactly what a per-frame budget spends
// itself before reaching. Re-measure with `go test -bench 'HandleAssistantChunk|
// CensusMeta' -benchmem ./internal/translate/` before reconsidering.

import (
	"encoding/json"
	"log/slog"
	"reflect"
	"strings"
	"sync"

	"github.com/cplieger/runesafe/v2"
)

// maxCensusObjectBytes skips the probe for an oversized `_meta.kiro` object.
//
// The map decode is O(keys) and happens BEFORE any guard inside the loop could
// help, so without this a hostile or malformed block carrying a hundred thousand
// keys turns one frame into a hundred thousand map inserts. 64 KiB is orders of
// magnitude above every real block measured on this wire (the largest carries a
// checkpoint's three URIs and a workflow descriptor, well under 1 KiB).
const maxCensusObjectBytes = 64 << 10

// maxCensusKeys bounds the distinct (name, type) pairs held for the life of the
// process. Both halves of a key are backend-controlled, so an unbounded map is a
// memory sink; reaching the cap disables the probe rather than continuing, which
// is strictly better than continuing to scan for findings nobody will read.
const maxCensusKeys = 128

// maxCensusNameBytes bounds one reported field name. A name is backend-controlled
// text going to a logfmt line, so it is sanitized and capped like any other
// untrusted string on a human-read surface (see displayText).
const maxCensusNameBytes = 64

// censusLedger remembers which (name, type) pairs have been reported, so a stream
// of per-turn frames reports each novel shape once rather than on every frame.
//
// A mutex and a plain map, NOT the unlocked module-level set the upstream
// equivalent uses. That set is safe in Python, where a racing pair of threads can
// only duplicate a log line; in Go a concurrent map write is a fatal,
// unrecoverable runtime error that no recover can catch, and it would kill the
// process mid-turn. The concurrency is real rather than hypothetical:
// BridgeCoordinator.Forward runs one goroutine per bridge and each calls these
// handlers directly, so N open chats means N concurrent decoders.
//
// A plain map rather than sync.Map because the write path runs once per novel key
// (contention is irrelevant) and len() has to be exact for the cap above, which
// sync.Map cannot give without a second counter.
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
// Cached because censusMeta runs on EVERY frame — agent_message_chunk arrives per
// token — and the walk below allocates a map and reflects over every field. A
// per-frame recomputation of a value that cannot change for the life of the
// process is the one way this diagnostic could show up in a profile.
//
// The returned map is shared and must be treated as read-only, which is why
// censusMeta copies before adding its declined names.
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

// knownKeysOf returns the lowercased JSON member names a struct type consumes,
// including those of any embedded or nested struct reached by a field with no
// json tag of its own.
//
// DERIVED from the struct tags rather than hand-listed, so the known set cannot
// drift from the parser it describes — a hand-written set goes stale the moment
// someone adds a field, and it goes stale silently in the direction that produces
// a false report.
//
// LOWERCASED because encoding/json matches object members case-insensitively: a
// frame sending `MessageId` IS consumed by the `messageId` field, so comparing
// case-sensitively would report a field that was read. That is the subtle half of
// this function and the reason it has its own test.
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

// jsonKindOf names a raw value's JSON type from its first byte, which is all the
// probe ever reads of a value. Naming the TYPE and never the value is what makes
// the report safe to log: a field's contents cannot leak from code that does not
// decode them.
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

// censusMeta reports, once per process, every member of one `_meta.kiro` object
// that the given wire type does not consume.
//
// declined names members the type deliberately does NOT read, and it is not
// optional: `preview` and `rawOutput` are skipped on purpose (whole file bodies
// and an unmodelled tool payload), so without them the probe fires on its first
// frame and gets muted before it can report anything real.
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
// The one place a VALUE is reported rather than a name, because here the label IS
// the discovery: the field name `unit` is known, so no field-name probe can see
// that KAS started billing in a new dimension. `unit=cacheRead` is the finding;
// `unit:string` says nothing, since the unit is always a string.
//
// Safe to log for the same reason the upstream equivalent gives: a unit is a
// low-cardinality dimension name drawn from the backend's own billing vocabulary,
// never a quantity, an alias or an identifier. It is sanitized and capped anyway,
// because it is still backend-controlled text on a log line.
//
// It matters here more than a name would: persistTurnSummary sums an entry whose
// unit is empty or "credit", so an unrecognised unit is either added to the user's
// credit total or silently dropped, and neither is visible today.
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
