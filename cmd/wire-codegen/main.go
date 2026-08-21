// Command wire-codegen generates TypeScript interfaces, validating decoders,
// and an SSE event→decoder registry from Go wire types using the wiregen
// library (AST-based; github.com/cplieger/wiregen/v2). Output lands in
// static-src/wire/ and feeds the client's typed SSE/REST decoding.
//
// The contract itself — the registered types, enums, name overrides and SSE
// event table — lives in internal/wirespec. This command is the driver.
//
// Run: go run ./cmd/wire-codegen   (from the vibekit repo root)
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/cplieger/vibekit/internal/wirespec"
)

func main() {
	os.Exit(run())
}

// run generates the wire artifacts and returns the process exit code, so the
// signal context's cancel runs on every path (os.Exit in main itself would skip
// the defer).
//
// The context is signal.NotifyContext rather than context.Background, and that
// is the whole reason wiregen's Generate takes one. Loading the registered
// packages runs the `go` command as a subprocess, which on a cold module cache
// can spend minutes fetching — and an unbounded subprocess is exactly what
// Ctrl-C is for. With a plain background context an interrupt killed this
// process and orphaned the load; with this one the load is cancelled and
// Generate returns, which is how the staged output gets cleaned up rather than
// left behind. SIGTERM is included because this also runs from the Docker build
// and from CI, where the signal is not a keyboard.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r := wirespec.Registry()
	outDir := filepath.Join("static-src", "wire")
	if err := r.Generate(ctx, outDir); err != nil {
		// An interrupt is not a failure to report as one: name the signal so a
		// cancelled run does not read as a broken registry.
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "wire-codegen: interrupted")
			return 130
		}
		fmt.Fprintf(os.Stderr, "wire-codegen: %v\n", err)
		return 1
	}
	fmt.Println("wire-codegen: generated " + outDir + "/{types,decoders,registry}.gen.ts")
	return 0
}
