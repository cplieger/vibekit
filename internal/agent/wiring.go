package agent

import "reflect"

// requireCollaborators panics unless every collaborator field of every collaborator
// the Runtime built is populated. A collaborator binds its own collaborators BY
// VALUE at construction, so a field still nil at that literal stays nil forever;
// this has shipped as a nil-receiver panic three times, which is why it gets a
// structural check via reflection rather than a hand-edited checklist. Only
// pointer, interface and func fields are checked. A genuinely optional
// collaborator carries `wiring:"optional"` on the FIELD.
func requireCollaborators(h *Runtime) {
	// Keyed literals: a positional list silently swaps meaning if fieldalignment
	// ever reorders these.
	for _, c := range []struct {
		v    any
		name string
	}{
		{name: "runs", v: h.runs},
		{name: "config", v: h.config},
		{name: "inbound", v: h.inbound},
		{name: "agentTerms", v: h.agentTerms},
		{name: "mcpRegistry", v: h.mcpRegistry},
		{name: "runRoutes", v: h.runRoutes},
		{name: "utility", v: h.utility},
		{name: "replay", v: h.replay},
		// Every collaborator constructed inside New with fields taken from h.
		{name: "coord", v: h.coord},
	} {
		requirePopulated(c.name, c.v)
	}
}

// requirePopulated panics on the first nil pointer, interface or func field of v.
func requirePopulated(owner string, v any) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.IsNil() {
		panic("agent: collaborator " + owner + " was never constructed")
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
			// nil state to check. Maps are excluded: several collaborators
			// initialise theirs lazily on first write.
		}
	}
}
