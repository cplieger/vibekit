# check=error=true

# --- Builder stage: compile Go server and TypeScript ---
FROM --platform=$BUILDPLATFORM debian:trixie-slim@sha256:b6e2a152f22a40ff69d92cb397223c906017e1391a73c952b588e51af8883bf8 AS builder

SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# hadolint ignore=DL3008
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates curl && rm -rf /var/lib/apt/lists/*

# Go for building the web server
# renovate: datasource=golang-version depName=golang
ARG TARGETARCH
ARG TARGETOS=linux
ARG BUILDARCH
ARG GO_VERSION=1.26.3
RUN curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${BUILDARCH}.tar.gz" \
    | tar -C /usr/local -xz
ENV PATH="/usr/local/go/bin:${PATH}"

# tsgo for TypeScript compilation (native binary, no Node needed). Tracks
# the `beta` dist-tag on @typescript/native-preview — Microsoft's curated
# stabler channel — rather than the daily `latest` channel. The linux-x64
# platform tarball below is published in lockstep with the metapackage at
# the same version string, so the URL resolves identically. See
# .github/renovate.json for the followTag rule.
# renovate: datasource=npm depName=@typescript/native-preview
ARG TSGO_VERSION=7.0.0-dev.20260421.2
RUN curl -fsSL \
    "https://registry.npmjs.org/@typescript/native-preview-linux-x64/-/native-preview-linux-x64-${TSGO_VERSION}.tgz" \
    | tar -xz -C /tmp

# xterm.js terminal emulator (ESM files, no Node needed). Versions tracked
# by Renovate via the ARG comments below.
# renovate: datasource=npm depName=@xterm/xterm
ARG XTERM_VERSION=6.0.0
# renovate: datasource=npm depName=@xterm/addon-fit
ARG XTERM_FIT_VERSION=0.11.0
# renovate: datasource=npm depName=@xterm/addon-webgl
ARG XTERM_WEBGL_VERSION=0.19.0
# renovate: datasource=npm depName=@xterm/addon-web-links
ARG XTERM_WEBLINKS_VERSION=0.12.0
# renovate: datasource=npm depName=ansi_up
ARG ANSI_UP_VERSION=6.0.6

# Build Go server
WORKDIR /build/web
COPY web/go.mod web/go.sum ./
RUN go mod download
COPY web/ ./

# Fetch the xterm.js tarballs and extract only the files we actually serve
# (the compiled .mjs addons + the core .mjs/.css) straight into the embed
# path. One RUN = one layer, no intermediate staging dirs.
RUN mkdir -p static/vendor/xterm && \
    curl -fsSL "https://registry.npmjs.org/@xterm/xterm/-/xterm-${XTERM_VERSION}.tgz" \
      | tar -xz -C static/vendor/xterm --strip-components=2 \
          package/lib/xterm.mjs package/css/xterm.css && \
    curl -fsSL "https://registry.npmjs.org/@xterm/addon-fit/-/addon-fit-${XTERM_FIT_VERSION}.tgz" \
      | tar -xz -C static/vendor/xterm --strip-components=2 package/lib/addon-fit.mjs && \
    curl -fsSL "https://registry.npmjs.org/@xterm/addon-webgl/-/addon-webgl-${XTERM_WEBGL_VERSION}.tgz" \
      | tar -xz -C static/vendor/xterm --strip-components=2 package/lib/addon-webgl.mjs && \
    curl -fsSL "https://registry.npmjs.org/@xterm/addon-web-links/-/addon-web-links-${XTERM_WEBLINKS_VERSION}.tgz" \
      | tar -xz -C static/vendor/xterm --strip-components=2 package/lib/addon-web-links.mjs && \
    curl -fsSL "https://registry.npmjs.org/ansi_up/-/ansi_up-${ANSI_UP_VERSION}.tgz" \
      | tar -xz -C static/vendor --strip-components=1 package/ansi_up.js

# Compile TypeScript then build Go (static files embedded via go:embed).
# BUILD_VERSION is stamped into internal/version.Build via -ldflags so the
# running binary can report what tag it was built from. Defaults to "dev"
# for local test builds; CI sets it to the date-sha tag.
ARG BUILD_VERSION=dev
RUN /tmp/package/lib/tsgo --project static-src/tsconfig.json \
    && /tmp/package/lib/tsgo --project static-src/tsconfig.sw.json

# Concatenate per-feature CSS splits into the served bundle.
# Behavior: skip blank lines and #-comments, cat each listed file
# (paths relative to manifest dir) into the output.
RUN set -eu; \
    : > static/style.css; \
    while IFS= read -r line || [ -n "$line" ]; do \
        case "$line" in ''|\#*) continue ;; esac; \
        cat "static-src/css/${line}" >> static/style.css; \
    done < static-src/css/MANIFEST

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags="-s -w -X vibekit/internal/version.Build=${BUILD_VERSION}" \
    -o /app/vibekit .

# --- Final stage: minimal runtime ---
FROM debian:trixie-slim@sha256:b6e2a152f22a40ff69d92cb397223c906017e1391a73c952b588e51af8883bf8

ENV DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-o", "pipefail", "-c"]

# Baked-in dependencies. These are load-bearing for the app to function
# and are intentionally not user-configurable via tools.json:
#   - node + npm: required for `npx`-based MCP servers (first-class vibekit
#     feature); version tracks Debian trixie (Node.js 20.x).
#   - everything else: stable utility surface that kiro-cli and the web
#     server shell out to (git, jq, curl, etc.).
# kiro-cli itself is downloaded on first boot by entrypoint.sh (licensing
# prevents us from baking it into the image).
# hadolint ignore=DL3008
RUN apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    wget \
    git \
    unzip \
    xz-utils \
    jq \
    nodejs \
    npm \
    python3 \
    python3-pip \
    python3-venv \
    gcc \
    libc6-dev \
    make \
    openssh-client \
    openssl \
    rsync \
    && rm -rf /var/lib/apt/lists/*

# Go, Node, and all dev tools installed at runtime via setup-tools.sh.
# /config is the single persistent volume for all container state:
#   /config/tools/   — runtimes, Go/Node/Python binaries, caches
#   /config/home/    — auth, ssh, gitconfig, build cache
#   /config/kiro/    — kiro-cli per-user state (auth.db, sessions, settings,
#                      steering, agents). Pointed at via KIRO_HOME so vibekit
#                      and kiro-cli agree on the location regardless of HOME.
#   /config/*.json   — config.json (vibekit prefs), tools.json, mcp.json, etc.
#   /config/chats/   — chat history
ENV PATH="/config/tools/bin:/config/tools/go/bin:/config/tools/runtimes/go/bin:/config/tools/runtimes/node/bin:/config/tools/node/bin:/config/tools/python/bin:/config/home/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
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
