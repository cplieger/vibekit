// Command wirecheck asserts wire-protocol compatibility between the Go
// server half (the web-terminal-engine module go.mod pins, used by the PTY
// shell in internal/hub) and the bundled TS client half (the Dockerfile-ARG-
// pinned npm artifact that static-src/shell.ts imports). The two halves are
// pinned INDEPENDENTLY — Renovate moves the Go module and the npm ARG in
// separate PRs, and a Go-only engine release publishes no npm package at all —
// so nothing but this gate proves the pair the image ships is one the engine
// considers compatible.
//
// The compatibility RULE is the engine's (terminal.WirePairIncompatibility —
// the same verdict its runtime handshake reaches, so this gate can never
// disagree with the close-4002 refusal). This program supplies the Go side
// from the engine's public constants; the client side arrives as flags the
// Dockerfile's wire-floor gate extracts from the vendored artifact.
//
// Without the gate a declared-incompatible pairing builds green, deploys,
// answers /api/health healthy, and then breaks only the shell tab — every
// terminal attempt closing with code 4002 while the rest of the app works, so
// the failure reads as a shell bug rather than a version mismatch. Fail the
// build instead.
//
// Exit 0: the pairing is declared-compatible. Exit 1: a declared floor is
// violated. Exit 2: usage error (a missing, malformed, or non-positive
// -client-rev / -client-min-server flag).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/cplieger/web-terminal-engine/v3/terminal"
)

// readManifest resolves the client half from the engine artifact's own published
// manifest, via the engine's exported decoder.
//
// Preferred over the -client-* flags because the alternative was scraping the
// vendored TypeScript with sed in the Dockerfile, which is the practice the
// engine published this manifest to end (web/src/wire-manifest.ts: it "breaks
// silently on any reformat"). The DECODING is the engine's — it owns the format,
// its schema check and its unusable-revisions check. What stays here is the
// POLICY: every failure is the usage error's exit 2, never a compatibility
// verdict, because a manifest the gate cannot read means the gate is broken and
// no pin should move. An unknown schema is named separately since its remedy is
// the opposite one: bump this gate.
func readManifest(path string, stderr io.Writer) (clientRev, clientMinServer int, ok bool) {
	m, err := terminal.ReadWireManifest(path)
	if err != nil {
		if errors.Is(err, terminal.ErrWireManifestSchema) {
			fmt.Fprintf(stderr, "wirecheck: %s: the manifest format moved ahead of this gate (fix the gate, do not bump a pin): %v\n", path, err)
		} else {
			fmt.Fprintf(stderr, "wirecheck: cannot read the engine's wire-compatibility manifest at %s (fix the gate, do not bump a pin): %v\n", path, err)
		}
		return 0, 0, false
	}
	return m.ProtocolVersion, m.MinimumServerProtocolVersion, true
}

func main() {
	manifest := flag.String("manifest", "", "path to the vendored engine artifact's wire-compatibility.json (preferred over the -client-* flags)")
	clientRev := flag.Int("client-rev", 0, "client WIRE_PROTOCOL_VERSION from the vendored npm artifact")
	clientMinServer := flag.Int("client-min-server", 0, "client MIN_SUPPORTED_SERVER_WIRE_VERSION from the vendored npm artifact")
	flag.Parse()
	rev, minServer := *clientRev, *clientMinServer
	if *manifest != "" {
		var ok bool
		if rev, minServer, ok = readManifest(*manifest, os.Stderr); !ok {
			os.Exit(2)
		}
	}
	os.Exit(run(rev, minServer, os.Stdout, os.Stderr))
}

// run performs the wire-floor gate against the engine's exported constants and
// returns the process exit code main hands to os.Exit — the contract the
// Dockerfile consumes: 0 declared-compatible, 1 floor violated (fail the
// build), 2 usage error (missing/non-positive flag values).
//
// The flags are validated here rather than left to the engine's comparator so
// a missing extraction is reported as the usage error it is (exit 2, "fix the
// gate") instead of a compatibility verdict (exit 1, "bump a pin").
func run(clientRev, clientMinServer int, stdout, stderr io.Writer) int {
	if clientRev <= 0 || clientMinServer <= 0 {
		fmt.Fprintln(stderr, "wirecheck: -client-rev and -client-min-server are required positive integers")
		return 2
	}
	if reason := terminal.WirePairIncompatibility(
		terminal.WireProtocolVersion, terminal.MinSupportedClientWireVersion,
		clientRev, clientMinServer,
	); reason != "" {
		fmt.Fprintf(stderr, "ERROR wire-floor-mismatch: %s\n%s\n", reason, remediation())
		return 1
	}
	fmt.Fprintf(stdout, "wirecheck ok: server wire rev %d (min client %d) <-> client wire rev %d (min server %d)\n",
		terminal.WireProtocolVersion, terminal.MinSupportedClientWireVersion, clientRev, clientMinServer)
	return 0
}

// remediation names this repo's engine pins. Which pin to move is build-layout
// knowledge the engine deliberately does not carry, so the app supplies it
// alongside the engine's reason.
func remediation() string {
	return "fix: bump go.mod's web-terminal-engine (Go half) or the Dockerfile's CPLIEGER_WEB_TERMINAL_ENGINE_VERSION ARG + static-src/package.json pin (TS half) so both halves resolve to a compatible pair"
}
