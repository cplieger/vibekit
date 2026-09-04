# check=error=true

# --- Builder stage: compile Go server and TypeScript ---
FROM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132 AS builder

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl && rm -rf /var/lib/apt/lists/*

# Go for building the web server
# renovate: datasource=golang-version depName=golang
ARG GO_VERSION=1.27.1
RUN ARCH=$(dpkg --print-architecture) && \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" \
    | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

# tsc — the TypeScript 7 native compiler (a Go binary, no Node needed). Now
# that TS7 has shipped stable, the native compiler is the `typescript`
# package's own `tsc`, distributed per-platform under
# @typescript/typescript-linux-<arch> (published in lockstep with the
# `typescript` metapackage at the same version). We fetch just that platform
# tarball and run its bundled `tsc` at build time.
# renovate: datasource=npm depName=typescript
ARG TS_VERSION=7.0.2
# Arch-aware fetch: native per-arch runners build arm64 on real arm64
# hardware, so the tsc binary must match the build arch. dpkg reports
# arm64/amd64; the npm platform package uses arm64/x64. A hardcoded x64
# here fails the arm64 build with "cannot execute binary file: Exec
# format error".
RUN TS_ARCH=$([ "$(dpkg --print-architecture)" = "arm64" ] && echo "arm64" || echo "x64") && \
    curl -fsSL \
    "https://registry.npmjs.org/@typescript/typescript-linux-${TS_ARCH}/-/typescript-linux-${TS_ARCH}-${TS_VERSION}.tgz" \
    | tar -xz -C /tmp

# Build Go server
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . ./

# Bake the published tool catalog as the first-boot/offline fallback.
# The catalog (~700 tools joined from the mise + aqua registries by
# cplieger/tool-catalog's daily publisher; both registries' MIT license
# texts travel INSIDE the JSON) is DATA on a daily upstream cadence —
# the runtime engine refreshes it at boot and on a schedule
# (VIBEKIT_TOOL_CATALOG_REFRESH), so this baked copy only serves a
# container that has never reached the publisher. vibekit's
# bundled-tools.json (this app's own tools) is not compiled in:
# the engine re-applies it to EVERY loaded catalog (baked, cached,
# fetched), so it ships beside the binary instead. The verify pass
# re-gates whatever the fetch returned: every required-tools.txt name
# must resolve for linux amd64+arm64 or the build fails, and the
# runtime refresh re-runs the same check before every swap.
ARG TOOL_CATALOG_URL=https://github.com/cplieger/tool-catalog/releases/latest/download/tool-catalog.json
# `go tool`, not `go run <pkg>@<version>`: the pkg@version form resolves OUTSIDE
# the main module, so it discarded the copy `go mod download` had already put in
# the layer above and re-resolved toolbelt over the network -- including a query
# for the module's latest version to report a deprecation. GOPROXY falls back to
# `direct` on a proxy error, direct means VCS, and this stage installs no git, so
# a proxy hiccup failed the build with "git: executable file not found in $PATH"
# rather than anything about the catalog (main, 2026-08-22). The tool directive in
# go.mod pins the version instead, which also collapses the two pins this step
# used to carry -- go.mod and an ARG that had to agree with nothing enforcing it.
# Runs with GOPROXY=off, so it cannot reach for the network at all.
RUN curl --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 20 --max-time 300 --retry 3 --retry-delay 5 -fsSL -o /tmp/tool-catalog.json "${TOOL_CATALOG_URL}" && \
    GOFLAGS=-mod=readonly GOPROXY=off go tool toolcatalog \
      verify -catalog /tmp/tool-catalog.json -require required-tools.txt \
      -overlay bundled-tools.json

# Fetch @cplieger/actions TS source from npm registry. The lib publishes
# TS only (no precompiled JS) — same pattern as @cplieger/reactive and
# @cplieger/web-terminal-engine, matching how local TS files in static-src/ are
# treated. Extracted to static-src/node_modules/@cplieger/actions/ so
# tsc's typecheck and esbuild's bundle resolution find the package + its
# types relative to static-src/tsconfig.json.
# renovate: datasource=npm depName=@cplieger/actions
ARG CPLIEGER_ACTIONS_VERSION=3.1.5
RUN mkdir -p static-src/node_modules/@cplieger/actions && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/actions/-/actions-${CPLIEGER_ACTIONS_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/actions --strip-components=1

# Fetch @cplieger/fetch TS source (same TS-only pattern). api-client.ts imports
# createFetch/requestRaw from it; bundled into app.js by cmd/bundle.
# renovate: datasource=npm depName=@cplieger/fetch
ARG CPLIEGER_FETCH_VERSION=2.1.2
RUN mkdir -p static-src/node_modules/@cplieger/fetch && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/fetch/-/fetch-${CPLIEGER_FETCH_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/fetch --strip-components=1

# Fetch @cplieger/reactive TS source (same TS-only pattern). @cplieger/actions
# imports it, and the app imports it directly; bundled into app.js by
# cmd/bundle.
# renovate: datasource=npm depName=@cplieger/reactive
ARG CPLIEGER_REACTIVE_VERSION=2.1.0
RUN mkdir -p static-src/node_modules/@cplieger/reactive && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/reactive/-/reactive-${CPLIEGER_REACTIVE_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/reactive --strip-components=1

# Fetch @cplieger/web-terminal-engine TS source (same TS-only pattern). shell.ts
# imports `render` from it (the reset primitives), and it is the peer the UI
# package builds on; bundled into app.js by cmd/bundle.
# renovate: datasource=npm depName=@cplieger/web-terminal-engine
ARG CPLIEGER_WEB_TERMINAL_ENGINE_VERSION=5.0.9
RUN mkdir -p static-src/node_modules/@cplieger/web-terminal-engine && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/web-terminal-engine/-/web-terminal-engine-${CPLIEGER_WEB_TERMINAL_ENGINE_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/web-terminal-engine --strip-components=1

# Fetch @cplieger/web-terminal-ui TS source (same TS-only pattern). shell.ts
# imports createTerminal + presetTouch from it; it is the reference touch-first
# terminal UI built on the engine (a peer dependency). Extracted side by side
# under static-src/node_modules/@cplieger so resolution finds the engine when
# compiling the UI's `@cplieger/web-terminal-engine` import. Bundled into
# app.js by cmd/bundle; its css/ bundle (MANIFEST.touch) is concatenated into
# style.css by the same tool.
# renovate: datasource=npm depName=@cplieger/web-terminal-ui
ARG CPLIEGER_WEB_TERMINAL_UI_VERSION=7.0.6
RUN mkdir -p static-src/node_modules/@cplieger/web-terminal-ui && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/web-terminal-ui/-/web-terminal-ui-${CPLIEGER_WEB_TERMINAL_UI_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/web-terminal-ui --strip-components=1

# Fetch @cplieger/ui-primitives TS source (same TS-only pattern). The app's
# headless UI modules (toast, tooltip, confirm, popover, focus-trap, theme,
# view-transition, announce) import its per-primitive subpaths; bundled into
# app.js by cmd/bundle. Its base stylesheet (css/ui-primitives.css) is
# concatenated into static/style.css by cmd/bundle (via a MANIFEST entry),
# then skinned by static-src/css/04-uip-skin.css.
#
# The theme adoption (static-src/theme.ts's createTheme storage adapter + the
# index.html themeInitSnippetFromJSON anti-FOUC snippet) needs
# @cplieger/ui-primitives >= 2.1.0, which ships the createTheme storage-adapter
# API. This ARG and static-src/package.json's @cplieger/ui-primitives pin
# track the same exact version.
# renovate: datasource=npm depName=@cplieger/ui-primitives
ARG CPLIEGER_UI_PRIMITIVES_VERSION=3.0.7
RUN mkdir -p static-src/node_modules/@cplieger/ui-primitives && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/ui-primitives/-/ui-primitives-${CPLIEGER_UI_PRIMITIVES_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/ui-primitives --strip-components=1

# @cplieger/keyenc encodes the client's composite keys (row signatures, the
# persisted banner-dismissal and pending-path keys, the action idempotency
# keys) so no field's content can forge a different field split. This ARG and
# static-src/package.json's @cplieger/keyenc pin track the same exact version.
# renovate: datasource=npm depName=@cplieger/keyenc
ARG CPLIEGER_KEYENC_VERSION=1.0.7
RUN mkdir -p static-src/node_modules/@cplieger/keyenc && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/keyenc/-/keyenc-${CPLIEGER_KEYENC_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/keyenc --strip-components=1

# Build the browser client, then the Go server (static files embedded via
# go:embed). BUILD_VERSION is stamped into internal/version.Build via
# -ldflags so the running binary can report what tag it was built from.
# Defaults to "dev" for local test builds; CI sets it to the date-sha tag.
#
# Step 1: tsc --noEmit is the TYPE gate over the app + service-worker
# configs (esbuild transpiles without typechecking, so tsc keeps failing
# the build on type errors exactly as before).
# Step 2: cmd/bundle (esbuild via its Go API — a Go library, no Node, no
# npm) bundles static-src/app.ts into /app.js + hashed chunks under
# /chunks/ (the dynamic import() sites and the code they share with the
# entry), bundles sw.ts into /sw.js, and assembles static/style.css from
# the two CSS manifests (@cplieger/web-terminal-ui's MANIFEST.touch first
# — library-before-consumer source order is the override mechanism — then
# static-src/css/MANIFEST). The @cplieger/* library sources fetched above
# are bundled in at build time, so nothing is served from /vendor/ and the
# page needs no importmap.
ARG BUILD_VERSION=dev
# Wire-floor gate (cross-language compatibility): go.mod's engine module and
# the ARG-pinned npm client version move INDEPENDENTLY (Renovate bumps them in
# separate PRs, and a Go-only engine release publishes no npm package), so
# their pairing is governed by the engine's exported wire-compatibility floors,
# not by version strings looking alike. Assert both directional floors at build
# time — a declared-incompatible pairing would close every shell attempt with
# code 4002 while /api/health stays green and the rest of the app works, so the
# outage reads as a shell bug rather than a version mismatch. Fail HERE instead.
# Client constants come from the vendored artifact's PUBLISHED MANIFEST
# (wire-compatibility.json, a package-root file the engine renders from its own
# TypeScript constants); server constants come from the engine's public Go API
# inside scripts/wirecheck. Neither half is scraped from source. This replaced a
# `sed` extraction of wire-compatibility.ts, which is the practice the engine
# published the manifest to end -- it breaks on any reformat of that line, and a
# reformat is not a wire change, so the gate would have failed for the wrong
# reason. The manifest is decoded by the engine's own terminal.ReadWireManifest,
# so its schema has one home rather than one per consumer.
# BUILT, not `go run`: the gate's exit code is its contract (0 compatible,
# 1 floor violated, 2 the gate itself is broken), and `go run` discards it --
# it prints "exit status 2" and exits 1 itself, collapsing "fix the gate" into
# "bump a pin". Dropping the DL3062 ignore with it: that rule fires on an
# unpinned `go run`/`go install <pkg>`, which is meaningless for a local path,
# and `go build ./scripts/wirecheck` does not trip it. An unneeded ignore
# suppresses a real future warning on this step.
RUN --mount=type=cache,target=/root/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=tmpfs,target=/tmp/wirecheck-bin \
    WIRE_MANIFEST=static-src/node_modules/@cplieger/web-terminal-engine/wire-compatibility.json && \
    test -f "$WIRE_MANIFEST" || { echo "wire-floor-gate: $WIRE_MANIFEST missing from the vendored engine artifact (fix the gate, do not bump a pin)" >&2; exit 2; } && \
    go build -o /tmp/wirecheck-bin/wirecheck ./scripts/wirecheck && \
    /tmp/wirecheck-bin/wirecheck -manifest "$WIRE_MANIFEST"

# hadolint ignore=DL3062
RUN /tmp/package/lib/tsc --project static-src/tsconfig.build.json --noEmit && \
    /tmp/package/lib/tsc --project static-src/tsconfig.sw.json --noEmit && \
    go run ./cmd/bundle

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X vibekit/internal/version.Build=${BUILD_VERSION}" \
    -o /app/vibekit .

# --- Final stage: minimal runtime ---
FROM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132

ENV DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Baked-in dependencies — the minimal stable runtime surface that
# vibekit and kiro-cli rely on. Everything else (Node, Python, Go,
# Java, Rust, all LSPs, all forge CLIs) is installed on demand by the
# in-process tools engine (the cplieger/toolbelt library, wired in
# internal/composition) into the persistent /config/tools/ volume,
# discovered through the compiled catalog.
#
# What's here and why:
#   - ca-certificates: HTTPS trust for every download
#   - curl: entrypoint kiro-cli download + manual install commands
#   - git: vibekit's gitexec, file history, forge integrations
#          (the checkpoint system does NOT use git — it is a
#          content-addressed blob/event store)
#   - openssh-client: git over ssh, gh ssh
#   - unzip: kiro-cli installer (it's a zip) + zip-format tools
#   - xz-utils: Node/shellcheck tarball extract (.tar.xz)
#   - jq: entrypoint.sh JSON parsing + a generally useful agent tool
#
# Notably NOT here. Each is installable at runtime from Settings -> Tools:
# nodejs and python3 as catalog entries (node, uv), and the rest as `apt:`
# entries now that the engine has an apt source -- so this list is a starting
# point rather than a ceiling. Removing them drops ~190 MB off the compressed
# image (212 MB -> ~22 MB):
#   nodejs, npm, python3, python3-pip, python3-venv, wget, gcc,
#   libc6-dev, make, openssl, rsync.
# kiro-cli itself is downloaded on first boot by entrypoint.sh
# (licensing prevents us from baking it into the image).
# PKG_REFRESH busts the cache for this layer. Without it BuildKit restores the
# layer verbatim on every rebuild and `apt-get upgrade` never runs again, so the
# image keeps shipping whatever packages were current when the layer was first
# built (measured 2026-08: 11 days stale, with Debian security updates already
# out for util-linux, unzip and jq). The central release/CI/scan builds pass
# today's UTC date. The `echo` is load-bearing: BuildKit keys a RUN on the build
# args it actually CONSUMES, so a merely-declared ARG would change nothing.
ARG PKG_REFRESH=static
# Re-declared after the ARG above: hadolint >= 2.15.0 drops a stage's SHELL
# dialect at the next ARG/ENV and shellchecks the rest of the stage as POSIX
# sh. Docker-side a no-op (same shell, no layer); it keeps the SC checks live.
SHELL ["/bin/bash", "-o", "pipefail", "-c"]
# libatomic1 is a RUNTIME dependency of the Node.js the tools engine
# installs, not a build tool: node's official linux-x64 binaries link
# libatomic.so.1 from v25 onward (measured — v24.18.0 does not, v26.7.0
# does). Nothing else in this list pulls it, so without it every
# npm-sourced tool fails with a bare `npm failed: exit status 127` and
# `node: error while loading shared libraries: libatomic.so.1` — which
# took out pyright, typescript and typescript-language-server together.
# hadolint ignore=DL3008
RUN echo "OS package refresh: ${PKG_REFRESH}" \
    && apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    jq \
    libatomic1 \
    openssh-client \
    tini \
    unzip \
    xz-utils \
    && rm -rf /var/lib/apt/lists/*

# Every dev tool — Go, Node, Python, Java, Rust, LSPs, forge CLIs —
# is installed at runtime by the tools engine (internal/tools) into
# the persistent /config/tools/ tree: versioned trees under
# opt/<name>/, every binary symlinked (or shimmed) into the single
# bin/ dir, npm and python package trees under npm/ and python/.
# PATH exposes those plus the package-manager bin dirs so a freshly
# installed tool is visible the moment its job finishes.
#
# /config is the single persistent volume for all container state:
#   /config/tools/      — bin/ (PATH), opt/<tool>/, npm/, python/, go/
#   /config/home/       — auth, ssh, gitconfig, build cache
#   /config/home/.kiro/ — kiro-cli per-user state (sessions, settings,
#                         steering, agents, logs). KIRO_HOME MUST equal
#                         $HOME/.kiro: the v3 engine (KAS) ignores KIRO_HOME
#                         and hardcodes os.homedir()/.kiro, while the Rust
#                         wrapper (settings CLI) honors KIRO_HOME — pointing
#                         KIRO_HOME inside HOME is the only way vibekit, the
#                         wrapper, and KAS agree on one directory. (Verified
#                         against the KAS 2.12 bundle: zero KIRO_HOME reads;
#                         home-dir comes from --home-dir or os.homedir().)
#   /config/*.json      — config.json (vibekit prefs), tools.json (v2
#                         manifest), tools-state.json (engine state), mcp.json
#   /config/chats/      — chat history
#
# GOROOT is intentionally absent: the go bin symlink resolves into its
# versioned dist tree under opt/go/ and the toolchain derives GOROOT
# from its own resolved location.
#
# tools/bin is the engine's SINGLE PATH dir, and the npm/ and python/ trees are
# install roots behind it, not PATH entries: toolbelt's linkPMBins symlinks every
# npm and uv-installed binary into tools/bin, installGo runs with GOBIN=tools/bin,
# installCargo writes there via --root, and installManual asserts its probe landed
# there ("install command finished but %s is not in the bin dir"). So npm/bin and
# python/bin were redundant on PATH — every binary they hold is already reachable one
# directory earlier. They were also real exposure: they sit ahead of /usr/bin, the
# entrypoint never creates or repairs them, and a binary planted while such a tree
# was group/other-writable is executed by root. Removing them removes that path
# rather than policing it (see vibekit.md invariant 6). Inherited pre-v2 volumes are
# the one regressing case — a binary in npm/bin with no tools/bin symlink stops
# resolving; it surfaces immediately as "command not found", and the remedy is to
# symlink it into tools/bin.
# tools/go/bin STAYS: it is GOPATH/bin (see ENV GOPATH below), where a hand-run
# `go install` lands when the engine's GOBIN is not in play. Its residual exposure is
# accepted rather than hardened — deleting a user's own go-installed tools is the
# productivity harm invariant 6 forbids, and the threat presupposes an actor who
# already holds /config/home/.ssh and the auth tokens.
ENV PATH="/config/tools/bin:/config/tools/go/bin:/config/home/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
ENV GOPATH="/config/tools/go"
ENV GOBIN="/config/tools/bin"
ENV HOME="/config/home"
ENV KIRO_HOME="/config/home/.kiro"
# The encoding every child process resolves. Unset, glibc's default C locale
# applies and git octal-escapes every non-ASCII path in status/diff/log output.
# C.UTF-8 is a glibc BUILT-IN, so this needs no `locales` package — and a base
# image on musl would make the line a claim the image cannot honour.
ENV LANG="C.UTF-8"
RUN mkdir -p /config/home/.kiro && chmod 777 /config/home /config/home/.kiro

# Repoint root's pw_dir to /config/home so OpenSSH (which resolves "~"
# via getpwuid, NOT $HOME) reads and writes ~/.ssh/known_hosts under
# the persisted volume. Without this, every container recreation wipes
# the host-key cache.
RUN sed -i 's|^root:x:0:0:root:/root:|root:x:0:0:root:/config/home:|' /etc/passwd

# Copy compiled web server from builder
COPY --from=builder /app /app

# Install artifacts under /opt/vibekit/ so they don't clutter / in the
# file browser. The blacklist in internal/filebrowse/paths.go already
# hides /opt, so users never see these.
COPY --chmod=755 entrypoint.sh /opt/vibekit/entrypoint.sh
COPY --from=builder /tmp/tool-catalog.json /opt/vibekit/tool-catalog.json
COPY bundled-tools.json /opt/vibekit/bundled-tools.json

WORKDIR /workspace
EXPOSE 9847

# start-period=300s: a fresh-volume first boot downloads and verifies kiro-cli
# (~528 MB zip), so a slow registry or link can legitimately take minutes. The
# budget is unchanged by the install moving into the server: the listener now
# binds first and /api/health answers 503 "kiro-cli installing" for that same
# window, where before there was nothing listening at all. Either way the
# container is not healthy until the pinned version is installed and runnable, so
# the start period still has to cover the download. 60s marked such containers
# unhealthy while the install was progressing normally.
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=300s \
    CMD ["curl", "-sf", "http://127.0.0.1:9847/api/health"]
# tini is PID 1 so orphans get reaped.
#
# vibekit spawns git, kiro-cli and the agent's own terminals, and each of those can
# leave a grandchild whose parent exits first. An orphan reparents to PID 1, and
# PID 1 was vibekit — a Go program that waits the children it started and nothing
# else, so an orphan's exit status was never collected and the process stayed on the
# table as a zombie forever. Measured before this: 1,172 zombies of 1,246 processes,
# 610 of them `git`, all with ppid 1, against a `pids.max` of `max` as the only
# thing between that and a hard `fork` failure.
#
# Reaping is not an application concern and an in-process SIGCHLD/Wait4 loop fights
# the Go runtime's own child bookkeeping, so it belongs in PID 1. `--` separates
# tini's arguments from the entrypoint's; entrypoint.sh keeps its own `exec`, so the
# server still runs as tini's direct child and signals still reach it.
ENTRYPOINT ["/usr/bin/tini", "--", "/opt/vibekit/entrypoint.sh"]
