package rpcerr

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/cplieger/vibekit/internal/vibekit"
)

// rpcErr builds the error shape a KAS failure arrives as.
func rpcErr(message, data string) error {
	e := &vibekit.RPCError{Code: -32603, Message: message}
	if data != "" {
		e.Data = json.RawMessage(data)
	}
	return e
}

// TestDetails pins that the `error.data` half is read at all, which is the
// whole reason RPCError grew a Data member: 127 measured -32603 errors put their
// text in data and set message to the literal "Internal error", so a reader of
// message alone discards the cause of every internal error KAS reports.
func TestDetails(t *testing.T) {
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
		{
			// A Zod array that parses but carries no message text: the join has
			// nothing to say, so what KAS actually sent is still better than "".
			"a Zod issue array with no messages falls back to the raw JSON",
			rpcErr("Internal error", `[{"path":["workspacePaths"]},{"message":""}]`),
			`[{"path":["workspacePaths"]},{"message":""}]`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := Details(c.err); got != c.want {
				t.Errorf("Details() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestText pins the composition, which is the part that was missing and
// the reason a chat user saw "ACP error -32603: Internal error" while the cause
// sat on the wire. Both directions matter: data wins when present, and the error
// string is the fallback rather than an empty result.
func TestText(t *testing.T) {
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
			// The 10-of-137 case, and the reason Details alone is the wrong
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
			if got := Text(c.err); got != c.want {
				t.Errorf("Text() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestTextBoundsAndSanitizes pins the two properties that make this text
// safe to put in a log line, an SSE payload and a banner. Both are reachable from
// the wire: the cap covers Details' raw-JSON fallback, which is unbounded on a
// Zod failure over a large params object, and the control-character stripping
// covers a provider message vibekit did not author.
func TestTextBoundsAndSanitizes(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("x", maxTextBytes*3)
	got := Text(rpcErr("Internal error", `{"details":"`+huge+`"}`))
	if len(got) > maxTextBytes {
		t.Errorf("Text() returned %d bytes, want <= %d", len(got), maxTextBytes)
	}

	// A newline would let upstream text forge a second log record, and U+202E
	// reorders the rest of a line in a viewer. Neither may survive.
	dirty := rpcErr("Internal error", `{"details":"first\nSECOND\u202ereordered"}`)
	got = Text(dirty)
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("Text() = %q, want no newline or carriage return", got)
	}
	if strings.Contains(got, "\u202e") {
		t.Errorf("Text() = %q, want the Bidi override removed", got)
	}
	if !strings.Contains(got, "first") || !strings.Contains(got, "SECOND") {
		t.Errorf("Text() = %q, want the real words preserved", got)
	}
}

// FuzzDetails pins that unwrapping an arbitrary `error.data` cannot panic and
// never invents content. Data is untrusted input from another process, and this
// is the only function that interprets it.
func FuzzDetails(f *testing.F) {
	f.Add(`{"details":"x"}`)
	f.Add(`[{"message":"a"}]`)
	f.Add(`[]`)
	f.Add(`null`)
	f.Add(`"a string"`)
	f.Add(`{`)
	f.Add(``)
	f.Add(`{"details":123}`)
	f.Fuzz(func(t *testing.T, data string) {
		got := Details(rpcErr("Internal error", data))
		if data == "" && got != "" {
			t.Fatalf("Details() = %q for absent data, want \"\"", got)
		}
		// The fallback returns the raw bytes, so the result is either derived from
		// the JSON or exactly it — never longer than the input plus the joiner
		// budget an issue array can add.
		if len(got) > len(data)+2*len(data) {
			t.Fatalf("Details() grew %d bytes of data into %d", len(data), len(got))
		}
	})
}

// FuzzText pins the bound on the value that actually reaches a user
// surface. Details may return the raw blob; this function is what promises a
// caller it is safe to interpolate, so the cap is its invariant, not that one's.
func FuzzText(f *testing.F) {
	f.Add(`{"details":"x"}`, "Internal error")
	f.Add(`[{"message":"a"}]`, "Internal error")
	f.Add(``, "workspacePaths is not iterable")
	f.Add(`{"details":"\u202e\n\u0000"}`, "Internal error")
	f.Fuzz(func(t *testing.T, data, message string) {
		got := Text(rpcErr(message, data))
		if len(got) > maxTextBytes {
			t.Fatalf("Text() = %d bytes, want <= %d", len(got), maxTextBytes)
		}
		if strings.ContainsAny(got, "\n\r") {
			t.Fatalf("Text() = %q, want no line break", got)
		}
	})
}
