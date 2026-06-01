// Package version holds the single-source-of-truth build version string
// for vibekit. Populated at build time via `-ldflags "-X
// vibekit/internal/version.Build=<tag>"` in the Dockerfile. When
// built outside the container (`go build` / `go test`) it stays "dev"
// so callers can tell development from release.
//
// Anything that needs the version — the ACP handshake, GET /api/version,
// the Settings → General panel — imports this package instead of
// hardcoding the string.
package version

// Build is the build tag injected by the image build (e.g. "2026.04.21").
// Stays "dev" for developer builds.
var Build = "dev"
