package mcp

import "testing"

func TestNewServer_stdio(t *testing.T) {
	s, err := NewServer(TransportStdio, "test",
		WithCommand("echo", "hello"),
		WithEnv([]KeyPair{{Name: "A", Value: "B"}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "test" {
		t.Fatalf("got name %q", s.Name)
	}
}

func TestNewServer_http(t *testing.T) {
	s, err := NewServer(TransportHTTP, "test-http",
		WithURL("http://localhost:8080"),
		WithOAuthClientID("cid"),
		WithHeaders([]KeyPair{{Name: "X-Foo", Value: "bar"}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "test-http" {
		t.Fatalf("got name %q", s.Name)
	}
}
