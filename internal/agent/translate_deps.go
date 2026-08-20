package agent

// The two translate roles that are genuinely ADAPTERS rather than forwards.
//
// Eight forwards used to live here — ChatRecords, ParentACPSession, WorkDir,
// PendingPermsAdd, NotifyPush, BufferStore, LineTracker, IsHookStatusEnabled —
// because translate.Roles bundled them into two composites only a type holding
// every collaborator could satisfy. Roles is flat now and each field names its
// owner, so those eight are deleted rather than moved.
//
// What remains needs a body: mcpRecorder narrows five unexported registry
// methods to an exported contract, and Output renders a terminal's raw
// ring on demand.

import (
	"context"
	"reflect"

	"github.com/cplieger/vibekit/internal/runlease"
	"github.com/cplieger/vibekit/internal/translate"
	"github.com/cplieger/vibekit/internal/vibekit"
)

// MCPRecorder returns the Runtime's MCP state recorder.
func (rt *Runtime) MCPRecorder() translate.MCPRecorder {
	return &mcpRecorder{reg: rt.mcpRegistry}
}

// mcpRecorder adapts the registry's five unexported record* methods to the
// exported MCPRecorder contract translate consumes. It holds the REGISTRY, not the
// Runtime: it was hubMCPRecorder holding a whole *Runtime to reach one field.
type mcpRecorder struct{ reg *mcpRegistry }

func (r *mcpRecorder) RecordConnected(ctx context.Context, serverName string, tools []string, prompts []vibekit.MCPPromptInfo, resources []vibekit.MCPResourceInfo) {
	r.reg.recordConnected(ctx, serverName, tools, prompts, resources)
}

func (r *mcpRecorder) RecordOAuth(ctx context.Context, serverName, oauthURL string) {
	r.reg.recordOAuth(ctx, serverName, oauthURL)
}

func (r *mcpRecorder) RecordInitFailure(ctx context.Context, serverName, errMsg string) {
	r.reg.recordInitFailure(ctx, serverName, errMsg)
}

func (r *mcpRecorder) RecordDisabled(ctx context.Context, serverName string) {
	r.reg.recordDisabled(ctx, serverName)
}

func (r *mcpRecorder) SignalReady() {
	r.reg.signalReady()
}

// IsScheduled reports whether a run was launched by a schedule.
//
// The run's LEASE is already the record of that — it is what gates the deny-fast
// permission floor — so this exports the fact rather than tracking it twice.
// Granted between `new` and `invoke` in launch, which is before the first
// lifecycle frame can arrive, so a run_start reaching translate always sees the
// origin its launch recorded.
func (rs *Runs) IsScheduled(workflowID string) bool {
	l, ok := rs.lease(workflowID)
	return ok && l.Origin == runlease.OriginScheduled
}

// translateRoles is the translate wiring, named so it can be asserted rather
// than only executed. Every role points at its OWNER.
//
// Ten of these were Runtime forwards behind two composites (translate's
// StreamingAccess at 8 members and PermissionAccess at 5). A composite spanning
// the chat store, the event bus, the buffer store, the line tracker, the terminal
// registry and the bridge lookup can only be satisfied by something holding all
// six — so the runtime grew ten methods it had no other use for and became the one
// type that qualified. That is how the god object was built, one convenience
// interface at a time.
//
// Every field is read HERE, at construction, so each owner must already exist.
// requireWired is what holds that, and it is production code rather than a test
// for a reason: a test that rebuilds this value after New has returned cannot see
// WHEN New read the fields, which is the entire bug. Checking at the call site
// catches it at construction, with the field's name, instead of as a nil-receiver
// panic on the first session update.
func (rt *Runtime) translateRoles() *translate.Roles {
	return requireWired(&translate.Roles{
		Bus:          rt.bus,
		Chats:        rt.chatStore,
		Buffers:      rt.bridge.assistantBufs,
		Lines:        rt.lines,
		PendingPerms: rt.bus,
		Push:         rt.coord,
		Sessions:     rt.coord,
		Terminals:    rt.agentTerms,
		HookStatus:   rt.hookStatus,
		WorkDir:      rt.lifecycle.workDir,
		MCP:          rt.MCPRecorder(),
		Governance:   rt.config,
		RunOrigin:    rt.runs,
		RunBounds:    rt.runs,
	})
}

// requireWired panics unless every role in r has an owner.
//
// A PANIC, not a logged warning, and not an error: a nil role is a programming
// mistake in this package's own constructor, fixable only by editing the
// constructor, and the alternative is a server that boots and then dies on the
// first frame of the first turn. It is the same refusal agent.New already makes for
// a nil lifetime context, and it fires at process start in every test.
//
// Reflection rather than a hand-written field list, because a list has to be
// edited exactly when a role is added — which is the moment the mistake is
// available to make. This has now been made twice, in two constructors: roles
// bound to an owner BY VALUE capture whatever the field holds at the literal,
// whereas the forwards they replaced read h per call and so tolerated any order.
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
			// Two nils, and the second is the one that actually shipped. A role
			// assigned from a nil *T is a non-nil INTERFACE holding a nil pointer,
			// so IsNil() on the field is false and the check passed while the
			// receiver was nil — the same typed-nil trap Bridge normalizes for.
			// Reaching through with Elem() is what catches it.
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
