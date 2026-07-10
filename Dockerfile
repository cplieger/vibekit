# check=error=true

# --- Builder stage: compile Go server and TypeScript ---
FROM debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2 AS builder

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl && rm -rf /var/lib/apt/lists/*

# Go for building the web server
# renovate: datasource=golang-version depName=golang
ARG GO_VERSION=1.26.5
RUN ARCH=$(dpkg --print-architecture) && \
    curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${ARCH}.tar.gz" \
    | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

# tsgo for TypeScript compilation (native binary, no Node needed). Tracks
# the `latest` dist-tag on @typescript/native-preview — Microsoft's curated
# stabler channel — rather than the daily `latest` channel. The per-arch
# platform tarballs below are published in lockstep with the metapackage at
# the same version string, so the URL resolves identically. See
# .github/renovate.json for the followTag rule.
# renovate: datasource=npm depName=@typescript/native-preview
ARG TSGO_VERSION=7.0.0-dev.20260707.2
# Arch-aware fetch: native per-arch runners build arm64 on real arm64
# hardware, so the tsgo binary must match the build arch. dpkg reports
# arm64/amd64; tsgo's npm platform package uses arm64/x64. A hardcoded x64
# here fails the arm64 build with "tsgo: cannot execute binary file: Exec
# format error".
RUN TSGO_ARCH=$([ "$(dpkg --print-architecture)" = "arm64" ] && echo "arm64" || echo "x64") && \
    curl -fsSL \
    "https://registry.npmjs.org/@typescript/native-preview-linux-${TSGO_ARCH}/-/native-preview-linux-${TSGO_ARCH}-${TSGO_VERSION}.tgz" \
    | tar -xz -C /tmp

# ansi_up: lightweight ANSI→HTML converter for agent-terminal <pre> panels.
# renovate: datasource=npm depName=ansi_up
ARG ANSI_UP_VERSION=6.0.6

# Build Go server
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . ./

# Fetch ansi_up (the only vendor JS dependency now that xterm.js is gone).
RUN mkdir -p static/vendor && \
    curl -fsSL "https://registry.npmjs.org/ansi_up/-/ansi_up-${ANSI_UP_VERSION}.tgz" \
      | tar -xz -C static/vendor --strip-components=1 package/ansi_up.js

# Fetch @cplieger/actions TS source from npm registry. The lib publishes
# TS only (no precompiled JS) — same pattern as @cplieger/reactive and
# @cplieger/web-terminal-engine, matching how local TS files in static-src/ are
# treated. Extracted to static-src/node_modules/@cplieger/actions/ so
# tsgo's bundler resolution finds the package + its types relative to
# static-src/tsconfig.json.
# renovate: datasource=npm depName=@cplieger/actions
ARG CPLIEGER_ACTIONS_VERSION=2.0.11
RUN mkdir -p static-src/node_modules/@cplieger/actions && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/actions/-/actions-${CPLIEGER_ACTIONS_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/actions --strip-components=1

# Fetch @cplieger/fetch TS source (same TS-only pattern). api-client.ts imports
# createFetch/requestRaw from it; resolved via the importmap at runtime
# (/vendor/cplieger-fetch/index.js).
# renovate: datasource=npm depName=@cplieger/fetch
ARG CPLIEGER_FETCH_VERSION=1.1.1
RUN mkdir -p static-src/node_modules/@cplieger/fetch && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/fetch/-/fetch-${CPLIEGER_FETCH_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/fetch --strip-components=1

# Fetch @cplieger/reactive TS source (same TS-only pattern). @cplieger/actions
# imports it, and the app imports it directly; both resolve it via the
# importmap at runtime (/vendor/cplieger-reactive/index.js).
# renovate: datasource=npm depName=@cplieger/reactive
ARG CPLIEGER_REACTIVE_VERSION=1.2.4
RUN mkdir -p static-src/node_modules/@cplieger/reactive && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/reactive/-/reactive-${CPLIEGER_REACTIVE_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/reactive --strip-components=1

# Fetch @cplieger/web-terminal-engine TS source (same TS-only pattern). shell.ts
# imports render/keyboard/scroll/connection from it; resolved via the importmap
# at runtime (/vendor/cplieger-web-terminal-engine/index.js).
# renovate: datasource=npm depName=@cplieger/web-terminal-engine
ARG CPLIEGER_WEB_TERMINAL_ENGINE_VERSION=2.3.2
RUN mkdir -p static-src/node_modules/@cplieger/web-terminal-engine && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/web-terminal-engine/-/web-terminal-engine-${CPLIEGER_WEB_TERMINAL_ENGINE_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/web-terminal-engine --strip-components=1

# Fetch @cplieger/ui-primitives TS source (same TS-only pattern). The app's
# headless UI modules (toast, tooltip, confirm, popover, focus-trap, theme,
# view-transition, announce) import its per-primitive subpaths; resolved via
# the importmap at runtime (/vendor/cplieger-ui-primitives/...). Its base
# stylesheet (css/ui-primitives.css) is concatenated into static/style.css by
# the CSS-bundle step below (via a MANIFEST entry), then skinned by
# static-src/css/04-uip-skin.css.
#
# The theme adoption (static-src/theme.ts's createTheme storage adapter + the
# index.html themeInitSnippetFromJSON anti-FOUC snippet) needs
# @cplieger/ui-primitives >= 2.1.0, which ships the createTheme storage-adapter
# API. This ARG and static-src/package.json's @cplieger/ui-primitives pin both
# track 2.1.0.
# renovate: datasource=npm depName=@cplieger/ui-primitives
ARG CPLIEGER_UI_PRIMITIVES_VERSION=2.1.1
RUN mkdir -p static-src/node_modules/@cplieger/ui-primitives && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/ui-primitives/-/ui-primitives-${CPLIEGER_UI_PRIMITIVES_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/ui-primitives --strip-components=1

# Compile TypeScript then build Go (static files embedded via go:embed).
# BUILD_VERSION is stamped into internal/version.Build via -ldflags so the
# running binary can report what tag it was built from. Defaults to "dev"
# for local test builds; CI sets it to the date-sha tag.
#
# Step 1: tsgo --project compiles app TS (build + service-worker configs).
# Step 2: compile @cplieger/actions's TS source into static/vendor/cplieger-actions/
# so the browser can fetch the lib's compiled JS via the importmap entry.
# The lib uses internal relative imports (./registry.js, ./api.js, etc.)
# which are preserved as relative paths in the emit and resolve naturally
# within /vendor/cplieger-actions/ at runtime.
ARG BUILD_VERSION=dev
RUN /tmp/package/lib/tsgo --project static-src/tsconfig.build.json && \
    /tmp/package/lib/tsgo --project static-src/tsconfig.sw.json && \
    /tmp/package/lib/tsgo \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-reactive \
        --rootDir static-src/node_modules/@cplieger/reactive/src \
        --skipLibCheck \
        --strict \
        static-src/node_modules/@cplieger/reactive/src/*.ts && \
    /tmp/package/lib/tsgo \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-actions \
        --rootDir static-src/node_modules/@cplieger/actions/src \
        --skipLibCheck \
        --strict \
        static-src/node_modules/@cplieger/actions/src/*.ts && \
    /tmp/package/lib/tsgo \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-web-terminal-engine \
        --rootDir static-src/node_modules/@cplieger/web-terminal-engine/src \
        --skipLibCheck \
        --strict \
        static-src/node_modules/@cplieger/web-terminal-engine/src/*.ts && \
    /tmp/package/lib/tsgo \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-ui-primitives \
        --rootDir static-src/node_modules/@cplieger/ui-primitives/src \
        --skipLibCheck \
        --strict \
        static-src/node_modules/@cplieger/ui-primitives/src/*.ts \
        static-src/node_modules/@cplieger/ui-primitives/src/toast/*.ts && \
    /tmp/package/lib/tsgo \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-fetch \
        --rootDir static-src/node_modules/@cplieger/fetch/src \
        --skipLibCheck \
        --strict \
        static-src/node_modules/@cplieger/fetch/src/*.ts

# Concatenate per-feature CSS splits into the served bundle.
# Behavior: skip blank lines and #-comments, cat each listed file
# (paths relative to manifest dir) into the output.
RUN set -eu; \
    : > static/style.css; \
    while IFS= read -r line || [ -n "$line" ]; do \
        case "$line" in ''|\#*) continue ;; esac; \
        cat "static-src/css/${line}" >> static/style.css; \
    done < static-src/css/MANIFEST

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X vibekit/internal/version.Build=${BUILD_VERSION}" \
    -o /app/vibekit .

# --- Final stage: minimal runtime ---
FROM debian:trixie-slim@sha256:28de0877c2189802884ccd20f15ee41c203573bd87bb6b883f5f46362d24c5c2

ENV DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Baked-in dependencies — the minimal stable runtime surface that
# vibekit and kiro-cli rely on. Everything else (Node, Python, Go,
# Java, Ruby, Rust, all LSPs, all forge CLIs) is installed on demand
# via setup-tools.sh into the persistent /config/tools/ volume. See
# tools.json for the curated list users can enable from the UI.
#
# What's here and why:
#   - ca-certificates: HTTPS trust for every download
#   - curl: every install script + entrypoint kiro-cli download
#   - git: vibekit's gitexec, checkpoint system, file history,
#          forge integrations
#   - openssh-client: git over ssh, gh ssh
#   - unzip: kiro-cli installer (it's a zip)
#   - xz-utils: Node tarball extract (.tar.xz) and other archives
#   - jq: setup-tools.sh + entrypoint.sh manifest parsing
#
# Notably NOT here (was here, now opt-in via tools.json):
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

# Every dev tool — Go, Node, Python, Java, Ruby, Rust, LSPs, forge
# CLIs — is installed at runtime via setup-tools.sh into the
# persistent /config/tools/ tree. PATH is pre-shaped to expose all
# the runtime install locations so a freshly-enabled tool is on
# PATH the moment setup-tools.sh finishes its install loop.
#
# /config is the single persistent volume for all container state:
#   /config/tools/   — runtimes, Go/Node/Python binaries, caches
#   /config/home/    — auth, ssh, gitconfig, build cache
#   /config/kiro/    — kiro-cli per-user state (auth.db, sessions, settings,
#                      steering, agents). Pointed at via KIRO_HOME so vibekit
#                      and kiro-cli agree on the location regardless of HOME.
#   /config/*.json   — config.json (vibekit prefs), tools.json, mcp.json, etc.
#   /config/chats/   — chat history
ENV PATH="/config/tools/bin:/config/tools/go/bin:/config/tools/runtimes/go/bin:/config/tools/runtimes/node/bin:/config/tools/node/bin:/config/tools/python/bin:/config/tools/runtimes/uv/bin:/config/tools/runtimes/ruby/bin:/config/tools/runtimes/rust/bin:/config/tools/runtimes/java/bin:/config/home/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
ENV GOROOT="/config/tools/runtimes/go"
ENV GOPATH="/config/tools/go"
ENV GOBIN="/config/tools/go/bin"
ENV HOME="/config/home"
ENV KIRO_HOME="/config/kiro"
RUN mkdir -p /config/home /config/kiro && chmod 777 /config/home /config/kiro

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
COPY --chmod=755 setup-tools.sh /opt/vibekit/setup-tools.sh
COPY tools.json /opt/vibekit/tools.json.default

WORKDIR /workspace
EXPOSE 9847

HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=60s \
    CMD curl -sf http://127.0.0.1:9847/api/health || exit 1
ENTRYPOINT ["/opt/vibekit/entrypoint.sh"]
