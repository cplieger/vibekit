# check=error=true

# --- Builder stage: compile Go server and TypeScript ---
FROM debian:trixie-slim@sha256:020c0d20b9880058cbe785a9db107156c3c75c2ac944a6aa7ab59f2add76a7bd AS builder

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

# Compile the tool catalog: the mise registry (name -> install source,
# descriptions; MIT) joined with the aqua registry (binary asset
# templates + checksums; MIT) and vibekit's catalog-overlays.json
# (featured set, LSP shims, manual entries). The result is a single
# read-only JSON the tools engine loads; both refs are Renovate-pinned
# so catalog updates arrive as ordinary dependency PRs.
# renovate: datasource=github-releases depName=jdx/mise
ARG MISE_REGISTRY_REF=v2026.7.6
# renovate: datasource=github-releases depName=aquaproj/aqua-registry
ARG AQUA_REGISTRY_REF=v4.538.0
# hadolint ignore=DL3062
RUN mkdir -p /tmp/registries && \
    curl -fsSL "https://codeload.github.com/jdx/mise/tar.gz/refs/tags/${MISE_REGISTRY_REF}" \
      | tar -xz -C /tmp/registries && \
    curl -fsSL "https://codeload.github.com/aquaproj/aqua-registry/tar.gz/refs/tags/${AQUA_REGISTRY_REF}" \
      | tar -xz -C /tmp/registries && \
    go run ./cmd/toolcatalog \
      -mise "/tmp/registries/mise-${MISE_REGISTRY_REF#v}/registry" \
      -aqua "/tmp/registries/aqua-registry-${AQUA_REGISTRY_REF#v}/pkgs" \
      -overlay catalog-overlays.json \
      -refs "mise=${MISE_REGISTRY_REF},aqua=${AQUA_REGISTRY_REF}" \
      -out /tmp/tool-catalog.json && \
    # MIT requires the copyright + permission notice to travel with
    # copies/substantial portions; the compiled catalog embeds data
    # derived from both registries, so ship both license texts.
    mkdir -p /tmp/catalog-licenses && \
    cp "/tmp/registries/mise-${MISE_REGISTRY_REF#v}/LICENSE" /tmp/catalog-licenses/LICENSE.mise && \
    cp "/tmp/registries/aqua-registry-${AQUA_REGISTRY_REF#v}/LICENSE" /tmp/catalog-licenses/LICENSE.aqua-registry && \
    rm -rf /tmp/registries

# Fetch ansi_up (the only vendor JS dependency now that xterm.js is gone).
RUN mkdir -p static/vendor && \
    curl -fsSL "https://registry.npmjs.org/ansi_up/-/ansi_up-${ANSI_UP_VERSION}.tgz" \
      | tar -xz -C static/vendor --strip-components=1 package/ansi_up.js

# Fetch @cplieger/actions TS source from npm registry. The lib publishes
# TS only (no precompiled JS) — same pattern as @cplieger/reactive and
# @cplieger/web-terminal-engine, matching how local TS files in static-src/ are
# treated. Extracted to static-src/node_modules/@cplieger/actions/ so
# tsc's bundler resolution finds the package + its types relative to
# static-src/tsconfig.json.
# renovate: datasource=npm depName=@cplieger/actions
ARG CPLIEGER_ACTIONS_VERSION=2.0.13
RUN mkdir -p static-src/node_modules/@cplieger/actions && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/actions/-/actions-${CPLIEGER_ACTIONS_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/actions --strip-components=1

# Fetch @cplieger/fetch TS source (same TS-only pattern). api-client.ts imports
# createFetch/requestRaw from it; resolved via the importmap at runtime
# (/vendor/cplieger-fetch/index.js).
# renovate: datasource=npm depName=@cplieger/fetch
ARG CPLIEGER_FETCH_VERSION=1.1.3
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
# imports `render` from it (the reset primitives), and it is the peer the UI
# package builds on; resolved via the importmap at runtime
# (/vendor/cplieger-web-terminal-engine/index.js).
# renovate: datasource=npm depName=@cplieger/web-terminal-engine
ARG CPLIEGER_WEB_TERMINAL_ENGINE_VERSION=2.4.0
RUN mkdir -p static-src/node_modules/@cplieger/web-terminal-engine && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/web-terminal-engine/-/web-terminal-engine-${CPLIEGER_WEB_TERMINAL_ENGINE_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/web-terminal-engine --strip-components=1

# Fetch @cplieger/web-terminal-ui TS source (same TS-only pattern). shell.ts
# imports createTerminal + presetTouch from it; it is the reference touch-first
# terminal UI built on the engine (a peer dependency). Extracted side by side
# under static-src/node_modules/@cplieger so tsc's bundler resolution finds the
# engine when compiling the UI's `@cplieger/web-terminal-engine` import. Resolved
# via the importmap at runtime (/vendor/cplieger-web-terminal-ui/index.js +
# /presets.js), and its css/ bundle is concatenated into style.css below.
# renovate: datasource=npm depName=@cplieger/web-terminal-ui
ARG CPLIEGER_WEB_TERMINAL_UI_VERSION=3.5.0
RUN mkdir -p static-src/node_modules/@cplieger/web-terminal-ui && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/web-terminal-ui/-/web-terminal-ui-${CPLIEGER_WEB_TERMINAL_UI_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/web-terminal-ui --strip-components=1

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
ARG CPLIEGER_UI_PRIMITIVES_VERSION=2.1.2
RUN mkdir -p static-src/node_modules/@cplieger/ui-primitives && \
    curl -fsSL "https://registry.npmjs.org/@cplieger/ui-primitives/-/ui-primitives-${CPLIEGER_UI_PRIMITIVES_VERSION}.tgz" \
      | tar -xz -C static-src/node_modules/@cplieger/ui-primitives --strip-components=1

# Compile TypeScript then build Go (static files embedded via go:embed).
# BUILD_VERSION is stamped into internal/version.Build via -ldflags so the
# running binary can report what tag it was built from. Defaults to "dev"
# for local test builds; CI sets it to the date-sha tag.
#
# Step 1: tsc --project compiles app TS (build + service-worker configs).
# Step 2: compile @cplieger/actions's TS source into static/vendor/cplieger-actions/
# so the browser can fetch the lib's compiled JS via the importmap entry.
# The lib uses internal relative imports (./registry.js, ./api.js, etc.)
# which are preserved as relative paths in the emit and resolve naturally
# within /vendor/cplieger-actions/ at runtime.
ARG BUILD_VERSION=dev
RUN mapfile -t wt_ui_ts < <(find static-src/node_modules/@cplieger/web-terminal-ui/src -name '*.ts') && \
    /tmp/package/lib/tsc --project static-src/tsconfig.build.json && \
    /tmp/package/lib/tsc --project static-src/tsconfig.sw.json && \
    /tmp/package/lib/tsc \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-reactive \
        --rootDir static-src/node_modules/@cplieger/reactive/src \
        --skipLibCheck \
        --strict \
        static-src/node_modules/@cplieger/reactive/src/*.ts && \
    /tmp/package/lib/tsc \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-actions \
        --rootDir static-src/node_modules/@cplieger/actions/src \
        --skipLibCheck \
        --strict \
        static-src/node_modules/@cplieger/actions/src/*.ts && \
    /tmp/package/lib/tsc \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-web-terminal-engine \
        --rootDir static-src/node_modules/@cplieger/web-terminal-engine/src \
        --skipLibCheck \
        --strict \
        static-src/node_modules/@cplieger/web-terminal-engine/src/*.ts && \
    /tmp/package/lib/tsc \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-ui-primitives \
        --rootDir static-src/node_modules/@cplieger/ui-primitives/src \
        --skipLibCheck \
        --strict \
        static-src/node_modules/@cplieger/ui-primitives/src/*.ts \
        static-src/node_modules/@cplieger/ui-primitives/src/toast/*.ts && \
    /tmp/package/lib/tsc \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-fetch \
        --rootDir static-src/node_modules/@cplieger/fetch/src \
        --skipLibCheck \
        --strict \
        static-src/node_modules/@cplieger/fetch/src/*.ts && \
    /tmp/package/lib/tsc \
        --module ESNext \
        --target ESNext \
        --moduleResolution bundler \
        --outDir static/vendor/cplieger-web-terminal-ui \
        --rootDir static-src/node_modules/@cplieger/web-terminal-ui/src \
        --skipLibCheck \
        --strict \
        "${wt_ui_ts[@]}"

# Concatenate per-feature CSS splits into the served bundle, then append the
# @cplieger/web-terminal-ui CSS bundle (its own css/MANIFEST order) wrapped in an
# `@layer web-terminal-ui { ... }` block. That layer is declared FIRST in
# static-src/css/00-header.css (lowest priority), so the UI package's standalone
# full-page tokens/reset never clobber vibekit's design system — vibekit's
# layered + unlayered rules win every conflict, while the terminal-specific
# `.term` / `.wt-*` / `.term-input` classes (which vibekit has no rule for) apply.
# Behavior: skip blank lines and #-comments, cat each listed file
# (paths relative to each manifest's dir) into the output.
RUN set -eu; \
    : > static/style.css; \
    while IFS= read -r line || [ -n "$line" ]; do \
        case "$line" in ''|\#*) continue ;; esac; \
        cat "static-src/css/${line}" >> static/style.css; \
    done < static-src/css/MANIFEST; \
    printf '@layer web-terminal-ui {\n' >> static/style.css; \
    while IFS= read -r line || [ -n "$line" ]; do \
        case "$line" in ''|\#*) continue ;; esac; \
        cat "static-src/node_modules/@cplieger/web-terminal-ui/css/${line}" >> static/style.css; \
    done < static-src/node_modules/@cplieger/web-terminal-ui/css/MANIFEST; \
    printf '}\n' >> static/style.css

RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w -X vibekit/internal/version.Build=${BUILD_VERSION}" \
    -o /app/vibekit .

# --- Final stage: minimal runtime ---
FROM debian:trixie-slim@sha256:020c0d20b9880058cbe785a9db107156c3c75c2ac944a6aa7ab59f2add76a7bd

ENV DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Baked-in dependencies — the minimal stable runtime surface that
# vibekit and kiro-cli rely on. Everything else (Node, Python, Go,
# Java, Rust, all LSPs, all forge CLIs) is installed on demand by the
# in-process tools engine (internal/tools) into the persistent
# /config/tools/ volume, discovered through the compiled catalog.
#
# What's here and why:
#   - ca-certificates: HTTPS trust for every download
#   - curl: entrypoint kiro-cli download + manual install commands
#   - git: vibekit's gitexec, checkpoint system, file history,
#          forge integrations
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
ENV PATH="/config/tools/bin:/config/tools/npm/bin:/config/tools/python/bin:/config/tools/go/bin:/config/home/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
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
# Upstream registry license texts (MIT) for the data compiled into the
# catalog — the notice must accompany substantial portions.
COPY --from=builder /tmp/catalog-licenses/ /opt/vibekit/licenses/

WORKDIR /workspace
EXPOSE 9847

HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=60s \
    CMD curl -sf http://127.0.0.1:9847/api/health || exit 1
ENTRYPOINT ["/opt/vibekit/entrypoint.sh"]
