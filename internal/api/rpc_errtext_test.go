package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// rpcErr builds the error shape a KAS failure arrives as.
func rpcErr(message, data string) error {
	e := &RPCError{Code: -32603, Message: message}
	if data != "" {
		e.Data = json.RawMessage(data)
	}
	return e
}

// TestRPCDetails pins that the `error.data` half is read at all, which is the
// whole reason RPCError grew a Data member: 127 measured -32603 errors put their
// text in data and set message to the literal "Internal error", so a reader of
// message alone discards the cause of every internal error KAS reports.
func TestRPCDetails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"an error carrying no data", rpcErr("boom", ""), ""},
		{"a non-RPC error", errors.New("boom"), ""},
		{"the details shape", rpcErr("Internal error", `{"details":"the real cause"}`), "the real cause"},
		{
			// The other measured shape: a Zod issue array. Its messages are what a
			// caller wants; the paths are noise.
			"a Zod issue array",
			rpcErr("Internal error", `[{"message":"workspacePaths is required","path":["workspacePaths"]},{"message":"inputs must be an object","path":["inputs"]}]`),
			"workspacePaths is required; inputs must be an object",
		},
		{
			// Neither shape: the raw JSON still beats "" because it is what KAS said.
			"an unrecognised shape falls back to the raw JSON",
			rpcErr("Internal error", `{"weird":true}`),
			`{"weird":true}`,
		},
		{"an empty details string falls through rather than winning", rpcErr("Internal error", `{"details":""}`), `{"details":""}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := RPCDetails(c.err); got != c.want {
				t.Errorf("RPCDetails() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRPCErrorText pins the composition, which is the part that was missing and
// the reason a chat user saw "ACP error -32603: Internal error" while the cause
// sat on the wire. Both directions matter: data wins when present, and the error
// string is the fallback rather than an empty result.
func TestRPCErrorText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil is empty rather than a literal", nil, ""},
		{
			// The 127-of-137 case. Before this function existed, this rendered as
			// the message: "Internal error".
			"data wins over a boilerplate message",
			rpcErr("Internal error", `{"details":"The monthly usage limit has been reached"}`),
			"The monthly usage limit has been reached",
		},
		{
			// The 10-of-137 case, and the reason RPCDetails alone is the wrong
			// helper for a caller: it answers "" here.
			"a message-only error falls back to the message",
			rpcErr("workspacePaths is not iterable", ""),
			"workspacePaths is not iterable",
		},
		{"an ordinary Go error passes through", errors.New("boom"), "boom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := RPCErrorText(c.err); got != c.want {
				t.Errorf("RPCErrorText() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRPCErrorTextBoundsAndSanitizes pins the two properties that make this text
// safe to put in a log line, an SSE payload and a banner. Both are reachable from
// the wire: the cap covers RPCDetails' raw-JSON fallback, which is unbounded on a
// Zod failure over a large params object, and the control-character stripping
// covers a provider message vibekit did not author.
func TestRPCErrorTextBoundsAndSanitizes(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("x", MaxRPCErrorTextBytes*3)
	got := RPCErrorText(rpcErr("Internal error", `{"details":"`+huge+`"}`))
	if len(got) > MaxRPCErrorTextBytes {
		t.Errorf("RPCErrorText() returned %d bytes, want <= %d", len(got), MaxRPCErrorTextBytes)
	}

	// A newline would let upstream text forge a second log record, and U+202E
	// reorders the rest of a line in a viewer. Neither may survive.
	dirty := rpcErr("Internal error", `{"details":"first\nSECOND\u202ereordered"}`)
	got = RPCErrorText(dirty)
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("RPCErrorText() = %q, want no newline or carriage return", got)
	}
	if strings.Contains(got, "\u202e") {
		t.Errorf("RPCErrorText() = %q, want the Bidi override removed", got)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "SECOND") {
		t.Errorf("RPCErrorText() = %q, want the real words preserved", got)
	}
}

// FuzzRPCDetails pins that unwrapping an arbitrary `error.data` cannot panic and
// never invents content. Data is untrusted input from another process, and this
// is the only function that interprets it.
func FuzzRPCDetails(f *testing.F) {
	f.Add(`{"details":"x"}`)
	f.Add(`[{"message":"a"}]`)
	f.Add(`[]`)
	f.Add(`null`)
	f.Add(`"a string"`)
	f.Add(`{`)
	f.Add(``)
	f.Add(`{"details":123}`)
	f.Fuzz(func(t *testing.T, data string) {
		got := RPCDetails(rpcErr("Internal error", data))
		if data == "" && got != "" {
			t.Fatalf("RPCDetails() = %q for absent data, want \"\"", got)
		}
		// The fallback returns the raw bytes, so the result is either derived from
		// the JSON or exactly it — never longer than the input plus the joiner
		// budget an issue array can add.
		if len(got) > len(data)+2*len(data) {
			t.Fatalf("RPCDetails() grew %d bytes of data into %d", len(data), len(got))
		}
	})
}

// FuzzRPCErrorText pins the bound on the value that actually reaches a user
// surface. RPCDetails may return the raw blob; this function is what promises a
// caller it is safe to interpolate, so the cap is its invariant, not that one's.
func FuzzRPCErrorText(f *testing.F) {
	f.Add(`{"details":"x"}`, "Internal error")
	f.Add(`[{"message":"a"}]`, "Internal error")
	f.Add(``, "workspacePaths is not iterable")
	f.Add(`{"details":"\u202e\n\u0000"}`, "Internal error")
	f.Fuzz(func(t *testing.T, data, message string) {
		got := RPCErrorText(rpcErr(message, data))
		if len(got) > MaxRPCErrorTextBytes {
			t.Fatalf("RPCErrorText() = %d bytes, want <= %d", len(got), MaxRPCErrorTextBytes)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Fatalf("RPCErrorText() = %q, want no line break", got)
		}
	})
}

// TestModelServed pins the entitlement predicate, including both fail-open cases.
// They are the ones a naive implementation gets wrong, and each wrong answer is
// worse than the defect the check exists to prevent: refusing every model when a
// backend advertises no catalog, or refusing an inherited empty value that means
// "use the backend default".
func TestModelServed(t *testing.T) {
	t.Parallel()
	served := []string{"claude-sonnet-4", "claude-haiku-4", "auto"}
	cases := []struct {
		name   string
		id     string
		served []string
		want   bool
	}{
		{"a served id", "claude-sonnet-4", served, true},
		{"an unserved id", "claude-opus-9", served, false},
		{"an empty id means inherit the default", "", served, true},
		{"the auto sentinel is never validated", ModelAuto, []string{"only-this"}, true},
		{"an empty served set means entitlement is unknowable", "anything", nil, true},
		{"an empty served set plus an empty id", "", nil, true},
		{
			// The case that decides whether the check is safe to add at all. The
			// display list drops [Deprecated]/[Legacy] entries, so a check against
			// it would refuse a model the account can still run. Callers must pass
			// the UNFILTERED set, and this pins that a present-but-different id is
			// refused so the distinction is not silently lost.
			"an id absent from the set is refused even when the set looks complete",
			"claude-legacy-1", served, false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := ModelServed(c.id, c.served); got != c.want {
				t.Errorf("ModelServed(%q, %v) = %v, want %v", c.id, c.served, got, c.want)
			}
		})
	}
}

// FuzzModelServed pins that the predicate agrees with a trivial oracle for every
// input, so no id can pass by a normalisation or comparison quirk. An entitlement
// check that answers yes for an id the account does not have is the whole bug.
func FuzzModelServed(f *testing.F) {
	f.Add("claude-sonnet-4", "claude-sonnet-4,claude-haiku-4")
	f.Add("", "a,b")
	f.Add("x", "")
	f.Add("AUTO", "auto")
	f.Fuzz(func(t *testing.T, id, csv string) {
		var served []string
		if csv != "" {
			served = strings.Split(csv, ",")
		}
		// The oracle is a MAP rather than slices.Contains, deliberately: production
		// uses slices.Contains, and an oracle built from the same primitive would
		// agree with a bug in it.
		set := make(map[string]struct{}, len(served))
		for _, sv := range served {
			set[sv] = struct{}{}
		}
		_, member := set[id]
		want := id == "" || id == ModelAuto || len(served) == 0 || member
		got := ModelServed(id, served)
		if got != want {
			t.Fatalf("ModelServed(%q, %v) = %v, want %v", id, served, got, want)
		}
		// Exact comparison only: a case-folded or trimmed match would let an id the
		// backend never advertised through.
		if got && id != "" && id != ModelAuto && len(served) > 0 && !member {
			t.Fatalf("ModelServed accepted %q which is not exactly in %v", id, served)
		}
	})
}
