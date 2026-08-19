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
	"fmt"
	"os"
	"path/filepath"

	"github.com/cplieger/vibekit/internal/wirespec"
)

func main() {
	r := wirespec.Registry()

	outDir := filepath.Join("static-src", "wire")
	if err := r.Generate(outDir); err != nil {
		fmt.Fprintf(os.Stderr, "wire-codegen: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("wire-codegen: generated " + outDir + "/{types,decoders,registry}.gen.ts")
}
