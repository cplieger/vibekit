package kascap

import (
	"strings"
	"testing"
)

// rowID is a row's identity: a key alone is not unique, because "knowledge" is
// deliberately BOTH a top-level capability and a settings entry (the knowledge
// gate has three parts and two of them are on this wire). The resolver is what
// separates them, and it also decides which container the key lands in, so
// (resolver, key) is the identity every structural check uses.
func rowID(d decl) string { return string(d.resolver) + "." + d.key }

// TestEveryDeclHasABecause is the package's reason to exist, expressed as a
// gate: a row that cannot say why the key is on the wire (or why it is not)
// carries no more information than the map literal this table replaced.
func TestEveryDeclHasABecause(t *testing.T) {
	if len(table) == 0 {
		t.Fatal("table is empty; every check in this file would pass forever")
	}
	for _, row := range table {
		t.Run(rowID(row), func(t *testing.T) {
			if strings.TrimSpace(row.because) == "" {
				t.Errorf("%s has no because; state what the key buys and what breaks without it", rowID(row))
			}
			// A because that only restates the key teaches nothing. Compared
			// case-insensitively against the key alone, which is the one
			// mechanical form of "says nothing" available here.
			if strings.EqualFold(strings.TrimSpace(row.because), row.key) {
				t.Errorf("%s's because only restates its key", rowID(row))
			}
		})
	}
}

// withheldReasonFloor is a character floor on a withheld row's because. It is
// deliberately low: it rules out a token ("unused", "TODO", "not needed") and
// nothing more. It is not a prose rule, and a row that needs fewer words than
// this to explain a deliberate omission almost certainly has not explained it.
const withheldReasonFloor = 40

// TestNoSendWithoutReason gates the rows a map literal could never carry: a
// key vibekit deliberately WITHHOLDS.
//
// Two properties, and the second is the one that catches drift. A withheld row
// must explain the omission at more than token length, and it must carry no
// wire value or gate — a value the table never sends is dead state, and the way
// this table would rot is somebody flipping send to false and leaving the value
// behind for a later reader to trust.
func TestNoSendWithoutReason(t *testing.T) {
	withheld := 0
	for _, row := range table {
		if row.send {
			continue
		}
		withheld++
		t.Run(rowID(row), func(t *testing.T) {
			if len(strings.TrimSpace(row.because)) < withheldReasonFloor {
				t.Errorf("%s is withheld on a %d-character reason; say what withholding causes",
					rowID(row), len(strings.TrimSpace(row.because)))
			}
			if row.value != nil {
				t.Errorf("%s is withheld but declares a wire value (%v); a value that is never sent will drift",
					rowID(row), row.value)
			}
			if row.gate != nil {
				t.Errorf("%s is withheld but declares a gate; the gate can never run", rowID(row))
			}
		})
	}
	if withheld == 0 {
		t.Error(`no row declares send:false, so this gate is vacuous.
The table's stated purpose includes recording the keys vibekit deliberately
withholds; if the last such row was deleted, that information was lost with it.`)
	}
}

// doorCollisions reports every row identity that appears on more than one door.
// Taking the table as a parameter is what lets the test below check the CHECK
// on a deliberately-broken table, rather than asserting a property that is
// currently vacuous.
func doorCollisions(rows []decl) []string {
	doors := make(map[string]map[door]bool)
	for _, row := range rows {
		if doors[rowID(row)] == nil {
			doors[rowID(row)] = make(map[door]bool)
		}
		doors[rowID(row)][row.door] = true
	}
	var bad []string
	for id, seen := range doors {
		if len(seen) > 1 {
			bad = append(bad, id)
		}
	}
	return bad
}

// TestSessionDoorKeysAbsentFromConnectionDoor pins that a key rides exactly one
// door.
//
// The failure it guards is a MOVE that only half happens: a key relocated to
// the session door while its connection row stays behind is declared twice, and
// because both projections build from the same table nothing else would notice.
//
// No row rides the session door today, so the first subtest alone would be
// vacuous. The second runs the same check over a deliberately-broken table and
// requires it to complain, which is what makes the green result mean something
// before T5 adds a real session row.
func TestSessionDoorKeysAbsentFromConnectionDoor(t *testing.T) {
	t.Run("the real table", func(t *testing.T) {
		if bad := doorCollisions(table); len(bad) > 0 {
			t.Errorf("these rows are declared on both doors: %v", bad)
		}
	})

	t.Run("the check catches a half-finished move", func(t *testing.T) {
		moved := decl{
			key:      "userInput",
			door:     doorSession,
			resolver: resolverCapability,
			value:    true,
			send:     true,
			because:  "a synthetic duplicate of the connection-door row, to prove this check fires",
		}
		bad := doorCollisions(append(append([]decl{}, table...), moved))
		if len(bad) != 1 || bad[0] != "capability.userInput" {
			t.Errorf("doorCollisions did not report the planted duplicate; got %v, want [capability.userInput]", bad)
		}
	})
}

// TestDeclIsWellFormed pins the structural invariants the zero values are
// designed to expose: an unset door or resolver is not a default, and a row
// that is sent must have something to send.
func TestDeclIsWellFormed(t *testing.T) {
	seen := make(map[string]bool)
	for _, row := range table {
		t.Run(rowID(row), func(t *testing.T) {
			if row.door == doorUnset {
				t.Errorf("%s declares no door", rowID(row))
			}
			if row.resolver == resolverUnset {
				t.Errorf("%s declares no resolver", rowID(row))
			}
			if row.send && row.value == nil && row.gate == nil {
				t.Errorf("%s is sent with neither a value nor a gate, so it would send JSON null", rowID(row))
			}
			if row.send && row.value != nil && row.gate != nil {
				t.Errorf("%s declares both a value and a gate; the gate always wins, so the value is a lie", rowID(row))
			}
			if seen[rowID(row)+string(row.door)] {
				t.Errorf("%s is declared twice on the same door", rowID(row))
			}
			seen[rowID(row)+string(row.door)] = true
		})
	}
}

// TestSettingRowsCarryTheEnabledObject pins the shape KAS demands of a settings
// entry: isSettingEnabled returns val.enabled for an object and false for
// anything else, so a bare true here resolves FALSE and silently turns the
// feature off. That is the failure mode this column was added to prevent, and
// it is invisible on the wire.
func TestSettingRowsCarryTheEnabledObject(t *testing.T) {
	checked := 0
	for _, row := range table {
		if row.resolver != resolverSetting || !row.send {
			continue
		}
		checked++
		t.Run(rowID(row), func(t *testing.T) {
			obj, ok := row.value.(map[string]any)
			if !ok {
				t.Fatalf("%s's value is %T, not an object; isSettingEnabled resolves that to false", rowID(row), row.value)
			}
			if obj["enabled"] != true {
				t.Errorf("%s's value is %v, want enabled:true", rowID(row), obj)
			}
		})
	}
	if checked == 0 {
		t.Error("no settings row is sent; this gate checked nothing")
	}
}

// TestEnvOverrideOnlyOnSentRows pins what makes the env column disable-only.
//
// A withheld row carries no wire value (TestNoSendWithoutReason enforces that),
// so an operator able to turn one ON would put a JSON null on the wire —
// resolving false at every settings site and enabling nothing at every
// capability site, for a key that now LOOKS declared. Restricting the column to
// send:true rows makes that unreachable rather than merely discouraged.
func TestEnvOverrideOnlyOnSentRows(t *testing.T) {
	for _, row := range table {
		if row.env == "" || row.send {
			continue
		}
		t.Errorf(`%s is withheld but names env=%q. The override's fallback is the row's own
send, so on a withheld row the only reachable change is turning it ON — and a
withheld row has no value to send. Give the row a value and send:true, or drop
the env name.`, rowID(row), row.env)
	}
}

// TestEnvOverrideIsWired proves the lookup exists and decides the wire, across
// every state an operator can put the variable in.
//
// The old tripwire in this slot asserted the opposite (that nothing read the
// column) and was deleted with the lookup that replaced it. Its successor has to
// exercise the projection rather than the column, because the failure that
// matters is a name that reaches no code: a row could carry an env nobody reads
// and every structural check above would still pass.
//
// The malformed case is the one worth having. envx.Bool falls back to the row's
// compiled send and logs one Warn, so a typo in a compose file leaves the
// capability ON. That fail direction is deliberate: the variable exists to
// disable a capability on purpose, and a mistyped value is not a purpose.
func TestEnvOverrideIsWired(t *testing.T) {
	row := findRow(t, resolverSetting, "workflows")
	if row.env == "" {
		t.Fatalf("setting.workflows carries no env name; this test pins the lookup through it")
	}
	build := Capabilities
	if row.door == doorSession {
		build = SessionMeta
	}

	// Cannot be t.Parallel: every case sets a process-wide variable.
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{"unset leaves the compiled default", "", true},
		{"false stops sending", "false", false},
		{"0 stops sending", "0", false},
		{"off stops sending", "off", false},
		{"no stops sending", "no", false},
		{"mixed case is tolerated", "FALSE", false},
		{"surrounding space is tolerated", "  false  ", false},
		{"true keeps sending", "true", true},
		{"1 keeps sending", "1", true},
		{"a typo keeps sending", "flase", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(string(row.env), tc.value)
			settings, _ := build(Spawn{})[settingsKey].(map[string]any)
			_, present := settings["workflows"]
			if present != tc.want {
				t.Errorf("%s=%q: workflows present = %v, want %v", row.env, tc.value, present, tc.want)
			}
		})
	}
}

// findRow returns the one row with this identity, failing when it is absent so a
// renamed key surfaces as a missing row rather than a vacuous pass.
func findRow(t *testing.T, r resolver, key string) decl {
	t.Helper()
	for _, row := range table {
		if row.resolver == r && row.key == key {
			return row
		}
	}
	t.Fatalf("no %s.%s row in the table", r, key)
	return decl{}
}

// neutralizeEnvOverrides pins every env-bearing row to its compiled send for the
// duration of the test, by setting each variable EMPTY (which envx reads as
// unset).
//
// Any test that asserts on a projection needs this, because an env override
// makes the payload depend on the ambient environment: a developer or runner
// carrying VIBEKIT_AGENT_WORKFLOWS=false would fail a golden for a reason that
// has nothing to do with the table. Driven off the table so a new env row is
// covered without editing this helper. Callers must not use t.Parallel.
func neutralizeEnvOverrides(t *testing.T) {
	t.Helper()
	for _, row := range table {
		if row.env != "" {
			t.Setenv(string(row.env), "")
		}
	}
}

// TestBuildersDoNotAliasTheTable pins that a caller cannot corrupt the table
// through a payload it was handed. The settings objects are built once at package
// init, so without the clone in buildDoor one caller's mutation would change
// every later handshake in the process.
//
// Both doors, because both now carry a settings object and both build through the
// same clone. A door tested on one side only would let a regression survive on
// the other.
func TestBuildersDoNotAliasTheTable(t *testing.T) {
	neutralizeEnvOverrides(t)
	for _, tc := range []struct {
		name  string
		build func(Spawn) map[string]any
		key   string
	}{
		{"connection", Capabilities, "knowledge"},
		{"session", SessionMeta, "workflows"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := settingsOf(t, tc.build(Spawn{}))
			entry, ok := first[tc.key].(map[string]any)
			if !ok {
				t.Fatalf("no %s settings object; got %T", tc.key, first[tc.key])
			}
			entry["enabled"] = false
			delete(first, tc.key)

			second := settingsOf(t, tc.build(Spawn{}))
			again, ok := second[tc.key].(map[string]any)
			if !ok {
				t.Fatalf("mutating one payload's settings object removed %s from the next one", tc.key)
			}
			if again["enabled"] != true {
				t.Errorf("mutating one payload flipped the next one's %s setting to %v", tc.key, again["enabled"])
			}
		})
	}
}

// settingsOf returns a payload's settings container, failing when it is absent —
// which would otherwise make every assertion above pass over an empty map.
func settingsOf(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	settings, ok := payload[settingsKey].(map[string]any)
	if !ok {
		t.Fatalf("no settings object in the payload; got %T", payload[settingsKey])
	}
	return settings
}
