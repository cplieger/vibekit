package agent

import "reflect"

// requireCollaborators panics unless every collaborator field of every plane the
// Runtime built is populated.
//
// This guards a bug this constructor has now shipped THREE times, in three
// different types, and the third time is what made it worth a structural answer
// rather than a third fix. The pattern: a plane binds its collaborators BY VALUE
// at construction, so a field still nil at that moment stays nil forever — where
// the forwarding methods these planes replaced read h per call and so tolerated
// any assignment order. Splitting the god object is exactly what made order
// load-bearing, and nothing in the language notices.
//
// The failure mode is what makes it worth catching here: a nil collaborator is
// not a compile error and not a startup error, it is a nil-receiver panic on the
// first request that reaches that path. The run plane's utility thunk, three
// translate roles and the inbound ladder's coordinator each shipped that way.
//
// A panic rather than an error, on the same terms as the nil lifetime context New
// already refuses: this is a mistake in this package's own wiring, fixable only by
// editing this package, and it fires in every test at process start. See
// requireWired for the translate-role half of the same idea.
//
// Reflection over the struct rather than a hand-written checklist, because a
// checklist must be edited exactly when a collaborator is added — the moment the
// mistake is available to make. Only pointer, interface and func fields are
// checked; a value field has no nil to be.
//
// A genuinely optional collaborator carries `wiring:"optional"` on the FIELD, so
// the exemption sits where a reader of that field will see it and a newly added
// field cannot inherit one by accident. There are real ones: scheduling is off
// rather than half-present when no store is wired, and the secret store and the
// ignore matcher are both absent when no config dir is set.
func requireCollaborators(h *Runtime) {
	for _, plane := range []struct {
		name string
		v    any
	}{
		{"runs", h.runs},
		{"config", h.config},
		{"inbound", h.inbound},
		{"agentTerms", h.agentTerms},
		{"mcpRegistry", h.mcpRegistry},
		{"runRoutes", h.runRoutes},
		{"utility", h.utility},
		{"replay", h.replay},
		// The coordinator is here because it was the FOURTH site to capture a nil
		// this way, and the guard missed it: the list held the planes I thought of
		// rather than everything that binds a collaborator at construction. Any
		// type built inside New with fields taken from h belongs in it.
		{"coord", h.coord},
	} {
		requirePopulated(plane.name, plane.v)
	}
}

// requirePopulated panics on the first nil pointer, interface or func field of v.
func requirePopulated(owner string, v any) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.IsNil() {
		panic("agent: plane " + owner + " was never constructed")
	}
	s := rv.Elem()
	for i := range s.NumField() {
		f := s.Field(i)
		if s.Type().Field(i).Tag.Get("wiring") == "optional" {
			continue
		}
		switch f.Kind() {
		case reflect.Pointer, reflect.Interface, reflect.Func:
			if f.IsNil() {
				panic("agent: " + owner + "." + s.Type().Field(i).Name +
					" is nil after New — its collaborator is assigned later in the " +
					"constructor than the literal that binds it")
			}
		default:
			// A value field (a mutex, a map, a string, an embedded struct) has no
			// nil state to check. Maps are deliberately excluded: several planes
			// initialise theirs lazily on first write.
		}
	}
}
