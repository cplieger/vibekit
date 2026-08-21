package mcp

// D80: validation reports every error.
//
// Validate used to return on the first failure, so a spec with three problems
// took three submit-fix-submit round trips. These tests pin both halves of the
// replacement: independent checks ACCUMULATE, and the transport chain still
// short-circuits because an unknown transport means the validator map has no
// entry and the per-transport check cannot run.

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// fieldsOf collects the attributed field names of an error, sorted and
// de-duplicated, so a case can assert which INPUTS a form would mark without
// depending on the order the checks happen to run in.
func fieldsOf(t *testing.T, err error) []string {
	t.Helper()
	seen := map[string]struct{}{}
	for _, fe := range FieldErrors(err) {
		seen[fe.Field] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	slices.Sort(out)
	return out
}

// TestValidate_AccumulatesIndependentFields is the item's headline promise: one
// response names every problem.
func TestValidate_AccumulatesIndependentFields(t *testing.T) {
	cases := []struct {
		srv        *Server
		name       string
		wantFields []string
		wantMsgs   []string
	}{
		{
			// The pasted-block case D80 exists for: a README block whose name,
			// url and headers are all wrong, none of which the user typed.
			name: "PastedRemoteBlockWithThreeBadFields",
			srv: &Server{
				Transport: TransportHTTP,
				Name:      "1bad name",
				URL:       "ftp://example.test/mcp",
				Headers:   []KeyPair{{Name: "bad header", Value: "x"}},
			},
			wantFields: []string{"headers", "name", "url"},
			wantMsgs: []string{
				"name must be",
				"url scheme must be http or https",
				`headers[0]: bad name`,
			},
		},
		{
			// The two shapes the grouped checks used to collapse. A stdio record
			// carrying BOTH url and headers reported one error attributed only to
			// url, so the headers box was never marked — and these are independent
			// presence facts with nothing sequencing them.
			name: "StdioCarryingBothURLAndHeaders",
			srv: &Server{
				Transport: TransportStdio,
				Name:      "ok",
				Command:   "bash",
				URL:       "https://leaked.example/mcp",
				Headers:   []KeyPair{{Name: "Authorization", Value: "Bearer t"}},
			},
			wantFields: []string{"headers", "url"},
			wantMsgs: []string{
				"stdio transport cannot have url",
				"stdio transport cannot have headers",
			},
		},
		{
			// The remote counterpart: command, args and env together reported one
			// error attributed only to command. This is the shape a pasted stdio
			// block hits when its transport is switched to remote.
			name: "RemoteCarryingCommandArgsAndEnv",
			srv: &Server{
				Transport: TransportHTTP,
				Name:      "ok",
				URL:       "https://x.example/mcp",
				Command:   "npx",
				Args:      []string{"-y", "server"},
				Env:       []KeyPair{{Name: "TOKEN", Value: "t"}},
			},
			wantFields: []string{"args", "command", "env"},
			wantMsgs: []string{
				"remote transport cannot have command",
				"remote transport cannot have args",
				"remote transport cannot have env",
			},
		},
		{
			name: "StdioNameAndToolListsAndOAuth",
			srv: &Server{
				Transport:         TransportStdio,
				Name:              "-nope",
				Command:           "bash",
				OAuthClientID:     "abc",
				OAuthClientSecret: "def",
				DisabledTools:     []string{"ok", "bad\x01"},
				AutoApprove:       []string{"also\x02bad"},
			},
			wantFields: []string{"auto_approve", "disabled_tools", "name", "oauth_client_id", "oauth_client_secret"},
			wantMsgs: []string{
				"disabled_tools[1]: control character",
				"auto_approve[0]: control character",
				"stdio transport cannot have oauth_client_id",
				"stdio transport cannot have oauth_client_secret",
			},
		},
		{
			// Two entries of one record, each wrong for its own reason: entry 1
			// being a duplicate must not hide entry 2's control character.
			name: "EveryBadHeaderEntryIsNamed",
			srv: &Server{
				Transport: TransportHTTP,
				Name:      "ok",
				URL:       "https://example.test/mcp",
				Headers: []KeyPair{
					{Name: "Authorization", Value: "a"},
					{Name: "authorization", Value: "b"},
					{Name: "X-Other", Value: "c\x00d"},
				},
			},
			wantFields: []string{"headers"},
			wantMsgs: []string{
				`headers[1]: duplicate name`,
				"headers[2]: value contains a control character",
			},
		},
		{
			name: "EveryBadArgIsNamed",
			srv: &Server{
				Transport: TransportStdio,
				Name:      "ok",
				Command:   "bash",
				Args:      []string{"fine", "b\x01d", "also\x02bad"},
			},
			wantFields: []string{"args"},
			wantMsgs: []string{
				"args[1] contains a control character",
				"args[2] contains a control character",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.srv)
			if err == nil {
				t.Fatal("Validate accepted a record with several bad fields")
			}
			if got := fieldsOf(t, err); !slices.Equal(got, tc.wantFields) {
				t.Errorf("attributed fields = %v, want %v", got, tc.wantFields)
			}
			// Every message survives verbatim: the aggregation changed, the
			// wording did not, which is what keeps the existing substring
			// assertions in validate_test.go meaningful.
			for _, want := range tc.wantMsgs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error text %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

// TestValidate_TransportChainShortCircuits is the other half. An unknown
// transport means transportValidators has no entry, so the per-transport check
// cannot run — there is nothing to accumulate. What MUST still accumulate beside
// it are the checks that do not depend on the transport at all.
func TestValidate_TransportChainShortCircuits(t *testing.T) {
	cases := []struct {
		srv        *Server
		name       string
		wantFields []string
		// notWant is the message a per-transport check would have produced had
		// the chain wrongly continued past an unknown transport.
		notWant string
	}{
		{
			name: "EmptyTransportStopsTheChain",
			srv: &Server{
				Name: "1bad", Transport: "",
				DisabledTools: []string{"x\x01"},
			},
			wantFields: []string{"disabled_tools", "name", "transport"},
			notWant:    "command required",
		},
		{
			name: "UnknownTransportStopsTheChain",
			srv: &Server{
				Name: "ok", Transport: Transport("carrier-pigeon"),
				DisabledTools: []string{"x\x01"},
			},
			wantFields: []string{"disabled_tools", "transport"},
			notWant:    "command required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.srv)
			if err == nil {
				t.Fatal("Validate accepted an unusable transport")
			}
			if got := fieldsOf(t, err); !slices.Equal(got, tc.wantFields) {
				t.Errorf("attributed fields = %v, want %v", got, tc.wantFields)
			}
			if strings.Contains(err.Error(), tc.notWant) {
				t.Errorf("per-transport check ran past an unusable transport: %q", err.Error())
			}
		})
	}
}

// TestValidate_DependentChecksWithinAFieldStayOrdered pins the second kind of
// necessary sequence: the checks that have nothing to say about a value that is
// not there.
func TestValidate_DependentChecksWithinAFieldStayOrdered(t *testing.T) {
	t.Run("MissingCommandIsTheOnlyCommandError", func(t *testing.T) {
		err := Validate(&Server{Name: "ok", Transport: TransportStdio, Command: "   "})
		if err == nil {
			t.Fatal("Validate accepted an empty stdio command")
		}
		if !strings.Contains(err.Error(), "command required for stdio transport") {
			t.Errorf("error text %q does not name the missing command", err.Error())
		}
		if strings.Contains(err.Error(), "command too long") {
			t.Error("a length check ran against a command that is not there")
		}
	})
	t.Run("UnparseableURLIsTheOnlyURLError", func(t *testing.T) {
		err := Validate(&Server{Name: "ok", Transport: TransportHTTP, URL: "not a url"})
		if err == nil {
			t.Fatal("Validate accepted an unparseable url")
		}
		if strings.Contains(err.Error(), "url scheme must be") {
			t.Error("the scheme check read a url that never parsed")
		}
	})
	t.Run("ControlCharInURLIsNotReportedAsSyntax", func(t *testing.T) {
		err := Validate(&Server{Name: "ok", Transport: TransportHTTP, URL: "https://ex\x01.test/"})
		if err == nil {
			t.Fatal("Validate accepted a control character in a url")
		}
		if !strings.Contains(err.Error(), "url contains a control character") {
			t.Errorf("error text %q does not name the control character", err.Error())
		}
		if strings.Contains(err.Error(), "must be an absolute http(s) URL") {
			t.Error("url.Parse ran on a control-bearing value and reported syntax instead")
		}
	})
}

// TestValidate_SentinelsSurviveTheJoin is the constraint the HTTP layer depends
// on: errors.Is must still reach a sentinel through the aggregation, or every
// store error would route to 400.
func TestValidate_SentinelsSurviveTheJoin(t *testing.T) {
	var errs fieldErrs
	errs.addf("name", "something about the name")
	errs.merge(ErrNameConflict)
	joined := errs.join()
	if !errors.Is(joined, ErrNameConflict) {
		t.Error("errors.Is cannot reach a sentinel through errors.Join")
	}
	if len(FieldErrors(joined)) != 1 {
		t.Errorf("FieldErrors found %d entries in a join holding one", len(FieldErrors(joined)))
	}
}

// TestFieldErrors_WalksAWrappedJoin covers the shape the paste path produces:
// ImportServers wraps a whole joined validation with the offending server's name,
// so the walk has to descend through fmt.Errorf's single-error unwrap as well as
// through the join.
func TestFieldErrors_WalksAWrappedJoin(t *testing.T) {
	inner := Validate(&Server{
		Transport: TransportHTTP, Name: "1bad", URL: "ftp://x.test/",
	})
	if inner == nil {
		t.Fatal("fixture record validated clean")
	}
	wrapped := errors.Join(errors.New("server \"1bad\""), inner)
	got := fieldsOf(t, wrapped)
	if !slices.Equal(got, []string{"name", "url"}) {
		t.Errorf("fields through a wrapped join = %v, want [name url]", got)
	}
}

// TestFieldErrors_Bounded pins that accumulation cannot be turned into an
// unbounded allocation by a hostile payload: a paste naming thousands of bad tool
// names describes one mistake and must not build thousands of messages.
//
// Both layers that enforce the cap get their own case, and both assert it
// EXACTLY. A one-sided "no more than" check passes for a cap that admits one
// extra entry, and each layer masks the other unless it is handed more failures
// than the layer beneath it can produce.
func TestFieldErrors_Bounded(t *testing.T) {
	t.Run("accumulating_a_hostile_record", func(t *testing.T) {
		tools := make([]string, 0, 400)
		for range 400 {
			tools = append(tools, "bad\x01name")
		}
		// Both lists, so more failures arrive at the outer accumulator than one
		// sub-validator can hand it.
		err := Validate(&Server{
			Name: "ok", Transport: TransportStdio, Command: "bash",
			DisabledTools: tools, AutoApprove: tools,
		})
		if err == nil {
			t.Fatal("Validate accepted 800 control-bearing tool names")
		}
		joined, ok := err.(interface{ Unwrap() []error })
		if !ok {
			t.Fatalf("Validate returned %T, want a joined error", err)
		}
		if n := len(joined.Unwrap()); n != maxFieldErrors {
			t.Errorf("Validate accumulated %d failures, want exactly the %d cap", n, maxFieldErrors)
		}
		if n := len(FieldErrors(err)); n != maxFieldErrors {
			t.Errorf("FieldErrors returned %d entries, want exactly the %d cap", n, maxFieldErrors)
		}
	})

	t.Run("flattening_an_oversized_tree", func(t *testing.T) {
		// FieldErrors is exported and walks whatever it is handed, so its own
		// bound has to hold for a tree larger than any this package builds.
		leaves := make([]error, 0, maxFieldErrors*2)
		for i := range maxFieldErrors * 2 {
			leaves = append(leaves, &FieldError{Field: "name", Msg: fmt.Sprintf("bad %d", i)})
		}
		if n := len(FieldErrors(errors.Join(leaves...))); n != maxFieldErrors {
			t.Errorf("FieldErrors of a %d-leaf tree returned %d entries, want exactly the %d cap",
				len(leaves), n, maxFieldErrors)
		}
	})
}

// TestValidate_CleanRecordsStayClean is the guard against an accumulator that
// reports a phantom: every shape validate_test.go accepts must still produce nil,
// including the empty tool lists and absent optional fields.
func TestValidate_CleanRecordsStayClean(t *testing.T) {
	cases := []*Server{
		{
			Name: "ok", Transport: TransportStdio, Command: "bash", Args: []string{"-c", "echo"},
			Env: []KeyPair{{Name: "FOO", Value: "bar"}},
		},
		{
			Name: "ok", Transport: TransportHTTP, URL: "https://x.test/mcp",
			Headers: []KeyPair{{Name: "Authorization", Value: "Bearer x"}},
		},
		{Name: "ok", Transport: TransportSSE, URL: "http://x.test/sse"},
		{
			Name: "ok", Transport: TransportHTTP, URL: "https://x.test/mcp",
			OAuthClientID: "cid", OAuthClientSecret: "csec",
		},
	}
	for _, srv := range cases {
		t.Run(string(srv.Transport)+"/"+srv.Name, func(t *testing.T) {
			if err := Validate(srv); err != nil {
				t.Errorf("Validate rejected a clean record: %v", err)
			}
		})
	}
}
