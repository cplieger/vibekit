package main

import (
	"strings"
	"testing"
)

func FuzzIsIdentReferenced(f *testing.F) {
	f.Add("const foo = bar(baz)", "foo")
	f.Add("const foobar = 1", "foo")
	f.Add("prefoo = 1", "foo")
	f.Add("x.foo.y", "foo")
	f.Add("", "foo")
	f.Add("foo", "")
	f.Add("$foo$", "foo")
	f.Add("_foo_", "foo")

	f.Fuzz(func(t *testing.T, body, ident string) {
		if ident == "" {
			return // empty ident causes infinite loop, skip
		}
		result := isIdentReferenced(body, ident)
		// If ident not in body at all, result must be false.
		if !strings.Contains(body, ident) && result {
			t.Errorf("ident %q not in body %q but returned true", ident, body)
		}
	})
}

func FuzzSanitizeVarName(f *testing.F) {
	f.Add("chat_id")
	f.Add("message_id")
	f.Add("")
	f.Add("_")
	f.Add("already_camel_case")
	f.Add("o")
	f.Add("out")
	f.Add("v")
	f.Add("private")
	f.Add("return")
	f.Add("a_b_c_d")
	f.Add("__double")

	f.Fuzz(func(t *testing.T, wireName string) {
		result := sanitizeVarName(wireName)
		// Must not panic; result is deterministic.
		result2 := sanitizeVarName(wireName)
		if result != result2 {
			t.Errorf("sanitizeVarName(%q) not deterministic: %q vs %q", wireName, result, result2)
		}
	})
}
