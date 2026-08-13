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

// TestEnvOverrideIsNotWiredYet is a tripwire, not an assertion about the
// design. The env column is declared (a row can name an environment variable
// that overrides send) but build.go performs NO lookup, so a populated env
// today is a silent no-op. This test fails the moment a row populates it, which
// is the point at which the lookup has to be implemented.
func TestEnvOverrideIsNotWiredYet(t *testing.T) {
	for _, row := range table {
		if row.env != "" {
			t.Errorf(`%s sets env=%q, but nothing reads it: build.go performs no environment lookup,
so this row's send is still decided at compile time. Implement the lookup in
buildDoor (and delete this test) before relying on the override.`, rowID(row), row.env)
		}
	}
}

// TestCapabilitiesDoesNotAliasTheTable pins that a caller cannot corrupt the
// table through a payload it was handed. The settings objects are built once at
// package init, so without the clone in buildDoor one caller's mutation would
// change every later handshake in the process.
func TestCapabilitiesDoesNotAliasTheTable(t *testing.T) {
	first := Capabilities(Spawn{})
	settings, ok := first[settingsKey].(map[string]any)
	if !ok {
		t.Fatalf("no settings object in the payload; got %T", first[settingsKey])
	}
	knowledge, ok := settings["knowledge"].(map[string]any)
	if !ok {
		t.Fatalf("no knowledge settings object; got %T", settings["knowledge"])
	}
	knowledge["enabled"] = false
	delete(settings, "workflows")

	second := Capabilities(Spawn{})
	secondSettings, ok := second[settingsKey].(map[string]any)
	if !ok {
		t.Fatalf("no settings object in the second payload; got %T", second[settingsKey])
	}
	if _, present := secondSettings["workflows"]; !present {
		t.Error("mutating one payload's settings object removed workflows from the next one")
	}
	if got := secondSettings["knowledge"].(map[string]any)["enabled"]; got != true {
		t.Errorf("mutating one payload flipped the next one's knowledge setting to %v", got)
	}
}
