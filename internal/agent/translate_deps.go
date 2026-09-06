package agent

import (
	"reflect"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/translate"
)

// IsScheduled reports whether a run was launched by a schedule, reading the LEASE
// that already records it. The lease is granted between `new` and `invoke`, before
// the first lifecycle frame, so a run_start reaching translate sees the origin.
func (rs *Runs) IsScheduled(workflowID string) bool {
	l, ok := rs.lease(workflowID)
	return ok && l.Origin == runlease.OriginScheduled
}

// translateRoles is the translate wiring, named so it can be asserted rather than
// only executed. Every field is read HERE at construction, so its owner must already
// exist; requireWired names the missing field instead of panicking on the first frame.
func (rt *Runtime) translateRoles() *translate.Roles {
	return requireWired(&translate.Roles{
		Bus:   rt.bus,
		Chats: rt.chatStore,
		// The coordinator, not a buffer store: a frame folds into the OPEN TURN's
		// buffer, and a fold with no turn open has to open one.
		Buffers: rt.coord,
		Turns:   rt.coord,
		Lines:   rt.lines,
		// The ledger of steers this server sent — the discriminator between the
		// user's own words and a workflow reporting into the same buffer.
		Steers:       rt.steerLedger,
		PendingPerms: rt.bus,
		// rt, not the coordinator: BridgeRespond resolves the reply bridge from
		// the manager by chat id, which is this type's own reach.
		Respond:    rt,
		Push:       rt.coord,
		Sessions:   rt.coord,
		Terminals:  rt.agentTerms,
		HookStatus: rt.hookStatus,
		Catalog:    rt.catalog,
		WorkDir:    rt.lifecycle.workDir,
		MCP:        rt.mcpRegistry,
		Governance: rt.config,
		RunOrigin:  rt.runs,
		RunBounds:  rt.runs,
		// The coordinator, not rt: ending a turn needs the chat's bridge and its
		// in-flight prompt cancel, which are the coordinator's to reach.
		TurnInterrupt: rt.coord,
		Metering:      rt.coord,
	})
}

// requireWired panics unless every role in r has an owner. A panic, not a warning: a
// nil role is a mistake in this package's own constructor, and the alternative is a
// server that boots and dies on the first frame. Reflection so a new field is checked
// without anyone remembering a list.
func requireWired(r *translate.Roles) *translate.Roles {
	v := reflect.ValueOf(r).Elem()
	for i := range v.NumField() {
		name := v.Type().Field(i).Name
		switch f := v.Field(i); f.Kind() {
		case reflect.String:
			if f.String() == "" {
				panic("agent: translate role " + name + " is empty at construction")
			}
		case reflect.Interface:
			// A role assigned from a nil *T is a non-nil INTERFACE holding a nil
			// pointer, so only reaching through with Elem() catches it.
			if f.IsNil() {
				panic("agent: translate role " + name + " is nil at construction — its owner is " +
					"assigned after the roles literal in agent.New")
			}
			if e := f.Elem(); e.Kind() == reflect.Pointer && e.IsNil() {
				panic("agent: translate role " + name + " holds a nil " + e.Type().String() +
					" — its owner is assigned after the roles literal in agent.New")
			}
		default:
			panic("agent: translate role " + name + " has unchecked kind " + f.Kind().String())
		}
	}
	return r
}
