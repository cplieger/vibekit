package server

import (
	"net/http"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// tabReader is the open-tab set as this endpoint uses it: one call that returns
// the set AND the version it reflects.
//
// One method, declared here because internal/tabs exports no interface and
// consumers name what they need. It is deliberately read-only: every tab mutation
// rides POST /api/command (invariant 1), so a wider role here would be surface
// for a write this package must not have.
type tabReader interface {
	// List returns the set in order plus the version it reflects, captured in ONE
	// critical section. That pairing is the contract, not an implementation
	// detail — see handleTabs.
	List() ([]vibekit.TabSubject, uint64)
}

// handleTabs serves the open-tab set.
//
//	GET /api/tabs -> {"tabs": [TabSubject], "version": N}
//
// READ ONLY, and that is the design rather than an omission. Every tab mutation
// goes through POST /api/command (invariant 1), so this endpoint has no PUT,
// PATCH or DELETE to add: a second mutation surface would mean a second place a
// failure's semantics are defined, for the one collection whose whole goal is to
// stop being special.
//
// THE SET AND THE VERSION COME FROM ONE Store.List CALL, and that is the load-
// bearing detail. They are one fact, captured in one critical section: a handler
// that read them separately could copy the tabs at version 10, let a mutation
// publish version 11, then answer with the old tabs stamped 11 — after which the
// client discards the very event the snapshot omitted, because its version rules
// read that frame as already applied. That is exactly the defect that killed an
// earlier revision's SSE-head watermark, where the snapshot and the event hub sat
// behind different locks.
//
// The version is the client's only watermark, and only an EVENT may advance it.
// This response supplies the baseline a client starts from (or re-lists to after
// a detected gap); the three rules that consume it are on
// vibekit.TabsChangedPayload.Version.
func (s *Server) handleTabs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpreply.MethodNotAllowed(w, http.MethodGet)
		return
	}
	if s.tabs == nil {
		// Unwired (a test server, or a build with no config dir). Answer the empty
		// collection rather than 404: a client that cannot read the arrangement
		// must still boot, and version 0 is the honest answer for a collection
		// nothing has ever written.
		webhttp.WriteJSON(w, vibekit.TabList{Tabs: []vibekit.TabSubject{}})
		return
	}
	open, version := s.tabs.List()
	if open == nil {
		// An empty set travels as [] rather than null: the field is not optional,
		// and a client decoding null where it expects an array is a runtime error
		// on the boot path.
		open = []vibekit.TabSubject{}
	}
	webhttp.WriteJSON(w, vibekit.TabList{Tabs: open, Version: version})
}
