// Package version holds the single-source-of-truth build version string for
// vibekit, stamped by the release image build.
//
// The -X path must be the FULL module path — `-ldflags "-X
// github.com/cplieger/vibekit/internal/version.Build=<tag>"`. A path that
// matches no package in the build is discarded silently: the linker reports
// nothing, the build succeeds, and Build keeps its default. This file
// documented the module-relative spelling for its whole life, and the
// Dockerfile copied it, so every released image was stamped "dev".
package version

// Build is the release tag the image build stamps in (e.g. "v0.8.3"). Stays
// "dev" for a plain go build, which is how a developer build identifies itself.
var Build = "dev"
