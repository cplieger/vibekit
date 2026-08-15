# check=error=true

# --- Builder stage: compile Go server and TypeScript ---
FROM debian:trixie-slim@sha256:3a39a0592364683e6bab97937b72cad5a8fa6dcbbee90edb3bb48c7f8e94f258 AS builder

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl && rm -rf /var/lib/apt/lists/*

# Go for building the web server
# renovate: datasource=golang-version depName=golang
ARG GO_VERSION=1.26.6
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

# ansi_up: lightweight ANSI→HTML converter for agent-terminal <pre> panels.
# renovate: datasource=npm depName=ansi_up
ARG ANSI_UP_VERSION=6.0.6

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
# catalog-overlays.json (display-copy patches) is no longer compiled in:
# the engine re-applies it to EVERY loaded catalog (baked, cached,
# fetched), so it ships beside the binary instead. The verify pass
# re-gates whatever the fetch returned: every required-tools.txt name
# must resolve for linux amd64+arm64 or the build fails, and the
# runtime refresh re-runs the same check before every swap.
ARG TOOL_CATALOG_URL=https://github.com/cplieger/tool-catalog/releases/latest/download/tool-catalog.json
# renovate: datasource=go depName=github.com/cplieger/toolbelt/v2
ARG TOOLBELT_TOOLCATALOG_VERSION=v2.4.12
# hadolint ignore=DL3062
RUN curl --proto '=https' --proto-redir '=https' --tlsv1.2 --connect-timeout 20 --max-time 300 --retry 3 --retry-delay 5 -fsSL -o /tmp/tool-catalog.json "${TOOL_CATALOG_URL}" && \
    go run "github.com/cplieger/toolbelt/v2/cmd/toolcatalog@${TOOLBELT_TOOLCATALOG_VERSION}" \
      verify -catalog /tmp/tool-catalog.json -require required-tools.txt

# Fetch ansi_up (the only third-party JS dependency now that xterm.js is
# gone). Extracted as a full package into static-src/node_modules/ so the
# bundler resolves the bare `ansi_up` specifier like the @cplieger/* libs;
# it is bundled into app.js, not served standalone.
RUN mkdir -p static-src/node_modules/ansi_up && \
    curl -fsSL "https://registry.npmjs.org/ansi_up/-/ansi_up-${ANSI_UP_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/ansi_up --strip-components=1

# Fetch @cplieger/actions TS source from npm registry. The lib publishes
# TS only (no precompiled JS) — same pattern as @cplieger/reactive and
# @cplieger/web-terminal-engine, matching how local TS files in static-src/ are
# treated. Extracted to static-src/node_modules/@cplieger/actions/ so
# tsc's typecheck and esbuild's bundle resolution find the package + its
# types relative to static-src/tsconfig.json.
# renovate: datasource=npm depName=@cplieger/actions
ARG CPLIEGER_ACTIONS_VERSION=3.1.1
RUN mkdir -p static-src/node_modules/@cplieger/actions && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/actions/-/actions-${CPLIEGER_ACTIONS_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/actions --strip-components=1

# Fetch @cplieger/fetch TS source (same TS-only pattern). api-client.ts imports
# createFetch/requestRaw from it; bundled into app.js by cmd/bundle.
# renovate: datasource=npm depName=@cplieger/fetch
ARG CPLIEGER_FETCH_VERSION=2.1.0
RUN mkdir -p static-src/node_modules/@cplieger/fetch && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/fetch/-/fetch-${CPLIEGER_FETCH_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/fetch --strip-components=1

# Fetch @cplieger/reactive TS source (same TS-only pattern). @cplieger/actions
# imports it, and the app imports it directly; bundled into app.js by
# cmd/bundle.
# renovate: datasource=npm depName=@cplieger/reactive
ARG CPLIEGER_REACTIVE_VERSION=1.2.5
RUN mkdir -p static-src/node_modules/@cplieger/reactive && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/reactive/-/reactive-${CPLIEGER_REACTIVE_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/reactive --strip-components=1

# Fetch @cplieger/web-terminal-engine TS source (same TS-only pattern). shell.ts
# imports `render` from it (the reset primitives), and it is the peer the UI
# package builds on; bundled into app.js by cmd/bundle.
# renovate: datasource=npm depName=@cplieger/web-terminal-engine
ARG CPLIEGER_WEB_TERMINAL_ENGINE_VERSION=3.10.2
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
ARG CPLIEGER_WEB_TERMINAL_UI_VERSION=5.6.0
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
ARG CPLIEGER_UI_PRIMITIVES_VERSION=3.0.1
RUN mkdir -p static-src/node_modules/@cplieger/ui-primitives && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/ui-primitives/-/ui-primitives-${CPLIEGER_UI_PRIMITIVES_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/ui-primitives --strip-components=1

# @cplieger/keyenc encodes the client's composite keys (row signatures, the
# persisted banner-dismissal and pending-path keys, the action idempotency
# keys) so no field's content can forge a different field split. This ARG and
# static-src/package.json's @cplieger/keyenc pin track the same exact version.
# renovate: datasource=npm depName=@cplieger/keyenc
ARG CPLIEGER_KEYENC_VERSION=1.0.2
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
# npm) bundles static-src/app.ts into /app.js + hashed lazy chunks under
# /chunks/ (the dynamic import() sites), bundles sw.ts into /sw.js,
# assembles static/style.css from the two CSS manifests
# (@cplieger/web-terminal-ui's MANIFEST.touch first — library-before-
# consumer source order is the override mechanism — then
# static-src/css/MANIFEST), and writes precompressed .gz siblings the
# server hands to gzip-accepting clients. The @cplieger/* library sources
# fetched above are bundled in at build time, so nothing is served from
# /vendor/ and the page needs no importmap.
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
FROM debian:trixie-slim@sha256:3a39a0592364683e6bab97937b72cad5a8fa6dcbbee90edb3bb48c7f8e94f258

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
# Notably NOT here (opt-in via Settings -> Tools):
#   nodejs, npm, python3, python3-pip, python3-venv, wget, gcc,
#   libc6-dev, make, openssl, rsync. Removing these drops ~190 MB
#   off the compressed image (212 MB -> ~22 MB).
# kiro-cli itself is downloaded on first boot by entrypoint.sh
# (licensing prevents us from baking it into the image).
# hadolint ignore=DL3008
RUN apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    git \
    jq \
    openssh-client \
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
RUN mkdir -p /config/home/.kiro && chmod 777 /config/home /config/home/.kiro

# Repoint root's pw_dir to /config/home so OpenSSH (which resolves "~"
# via getpwuid, NOT $HOME) reads and writes ~/.ssh/known_hosts under
# the persisted volume. Without this, every container recreation wipes
# the host-key cache.
RUN sed -i 's|^root:x:0:0:root:/root:|root:x:0:0:root:/config/home:|' /etc/passwd

# Copy compiled web server from builder
COPY --from=builder /app /app

# Install artifacts under /opt/vibekit/ so they don't clutter / in the
# file browser. The blacklist in internal/filehandler/paths.go already
# hides /opt, so users never see these.
COPY --chmod=755 entrypoint.sh /opt/vibekit/entrypoint.sh
COPY --from=builder /tmp/tool-catalog.json /opt/vibekit/tool-catalog.json
COPY catalog-overlays.json /opt/vibekit/catalog-overlays.json

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
ENTRYPOINT ["/opt/vibekit/entrypoint.sh"]
