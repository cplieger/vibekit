#!/bin/bash
# Reads /config/tools.json, checks for updates, and installs missing tools.
# Single script for both first boot and subsequent restarts.
set -uo pipefail

TOOLS="/config/tools"
BIN="$TOOLS/bin"
RUNTIMES="$TOOLS/runtimes"
GOBIN="$TOOLS/go/bin"
MANIFEST="/config/tools.json"

export GOROOT="$RUNTIMES/go"
export GOPATH="$TOOLS/go"
export GOBIN
export PATH="$BIN:$GOBIN:$RUNTIMES/go/bin:$RUNTIMES/node/bin:$TOOLS/node/bin:$TOOLS/python/bin:$PATH"

mkdir -p "$BIN" "$GOBIN" "$RUNTIMES" "$TOOLS/node/bin" \
    "$TOOLS/python/bin" "$TOOLS/lib"

if [ ! -f "$MANIFEST" ]; then
    printf "ERROR: %s not found\n" "$MANIFEST"
    exit 1
fi

printf "[%s] Tool setup starting\n" "$(date -Iseconds)"

# --- GitHub auth for rate limits ---

GH_AUTH=""
if command -v gh >/dev/null 2>&1; then
    GH_AUTH=$(gh auth token 2>/dev/null) || true
fi

gh_curl() {
    if [ -n "$GH_AUTH" ]; then
        curl -fsSL --connect-timeout 10 --max-time 15 \
            -H "Authorization: Bearer $GH_AUTH" "$@" 2>/dev/null
    else
        curl -fsSL --connect-timeout 10 --max-time 15 "$@" 2>/dev/null
    fi
}

# --- Helpers ---

expand() {
    local cmd="$1" version="$2"
    local version_nopfx="${version#v}"
    cmd="${cmd//\$\{VERSION\}/$version}"
    cmd="${cmd//\$\{VERSION_NOPFX\}/$version_nopfx}"
    cmd="${cmd//\$\{BIN\}/$BIN}"
    cmd="${cmd//\$\{TOOLS\}/$TOOLS}"
    cmd="${cmd//\$\{RUNTIMES\}/$RUNTIMES}"
    cmd="${cmd//\$\{GOBIN\}/$GOBIN}"
    cmd="${cmd//\$\{HOME\}/$HOME}"
    printf '%s' "$cmd"
}

has_bin() {
    local name="$1"
    [ -f "$BIN/$name" ] || [ -f "$GOBIN/$name" ] || \
        [ -f "$TOOLS/node/bin/$name" ] || [ -f "$TOOLS/python/bin/$name" ]
}

# Returns 0 if the given top-level section is missing or empty.
section_empty() {
    local section="$1"
    local count
    count=$(jq -r "(.${section} // {}) | length" "$MANIFEST" 2>/dev/null || echo 0)
    [ "$count" = "0" ]
}

# Check for a newer version and update the manifest. Returns 0 if changed.
check_update() {
    local jq_path="$1" name="$2" current="$3" method="$4"
    local latest=""

    case "$method" in
        manual|null) return 1 ;;
    esac

    # Skip non-semver (commit hashes)
    case "$current" in
        v*|[0-9]*) ;;
        *) return 1 ;;
    esac

    case "$method" in
        github)
            local repo
            repo=$(jq -r "${jq_path}.update.repo" "$MANIFEST")
            latest=$(gh_curl \
                "https://api.github.com/repos/${repo}/releases/latest" \
                | jq -r '.tag_name // empty') || true
            ;;
        gomod)
            local mod
            mod=$(jq -r "${jq_path}.update.module" "$MANIFEST")
            latest=$(curl -fsSL --connect-timeout 10 --max-time 15 \
                "https://proxy.golang.org/${mod}/@latest" 2>/dev/null \
                | jq -r '.Version // empty') || true
            ;;
        url)
            local url prefix raw
            url=$(jq -r "${jq_path}.update.url" "$MANIFEST")
            prefix=$(jq -r "${jq_path}.update.strip_prefix // empty" "$MANIFEST")
            raw=$(curl -fsSL --connect-timeout 10 --max-time 15 \
                "$url" 2>/dev/null | head -1) || true
            if [ -n "$raw" ]; then
                latest="${raw#"$prefix"}"
            fi
            ;;
        npm)
            latest=$(npm view "$name" version 2>/dev/null) || true
            ;;
        pypi)
            latest=$(curl -fsSL --connect-timeout 10 --max-time 15 \
                "https://pypi.org/pypi/${name}/json" 2>/dev/null \
                | jq -r '.info.version // empty') || true
            ;;
    esac

    if [ -z "$latest" ]; then
        printf "    update: fetch failed\n"
        return 1
    fi

    if [ "$current" = "$latest" ]; then
        return 1
    fi

    printf "    update: %s -> %s\n" "$current" "$latest"
    # Atomic rewrite: validate jq output before replacing the manifest.
    # Without this, a jq failure (silent under set +e) would leave an
    # empty $tmp and truncate the manifest to a newline, breaking every
    # subsequent run.
    local tmp
    if ! tmp=$(jq --arg v "$latest" "${jq_path}.version = \$v" "$MANIFEST"); then
        printf "    update: jq rewrite failed, keeping pinned version\n"
        return 1
    fi
    if [ -z "$tmp" ] || ! printf '%s' "$tmp" | jq empty >/dev/null 2>&1; then
        printf "    update: jq produced invalid output, keeping pinned version\n"
        return 1
    fi
    printf '%s\n' "$tmp" > "${MANIFEST}.tmp" && mv "${MANIFEST}.tmp" "$MANIFEST"
    return 0
}

# Clear binaries for a tool so install re-downloads the new version.
clear_tool() {
    local section="$1" name="$2"
    case "$section" in
        runtimes)
            rm -rf "${RUNTIMES:?}/$name"
            ;;
        binary|custom)
            rm -f "$BIN/$name"
            ;;
        go)
            for bin in $(jq -r ".go[\"$name\"].binaries[]" "$MANIFEST"); do
                rm -f "$GOBIN/$bin"
            done
            ;;
        npm)
            rm -rf "$TOOLS/node/lib" "$TOOLS/node/bin"
            mkdir -p "$TOOLS/node/bin"
            ;;
        pip)
            rm -rf "$TOOLS/python/lib" "$TOOLS/python/bin"
            mkdir -p "$TOOLS/python/bin"
            ;;
        cargo)
            rm -f "$TOOLS/bin/$name"
            ;;
        apt)
            ;; # apt packages managed by system, no clear needed
    esac
}

# --- Process each section: update then install ---

printf "\n=== Runtimes ===\n"
if section_empty runtimes; then
    printf "  (none configured)\n"
else
    for name in $(jq -r '.runtimes | keys[]' "$MANIFEST"); do
        version=$(jq -r ".runtimes[\"$name\"].version" "$MANIFEST")
        method=$(jq -r ".runtimes[\"$name\"].update.method" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if check_update ".runtimes[\"$name\"]" "$name" "$version" "$method"; then
            clear_tool runtimes "$name"
            version=$(jq -r ".runtimes[\"$name\"].version" "$MANIFEST")
        fi
        if [ ! -f "$RUNTIMES/$name/bin/$name" ]; then
            install_cmd=$(jq -r ".runtimes[\"$name\"].install" "$MANIFEST")
            printf "    install: %s\n" "$version"
            eval "$(expand "$install_cmd" "$version")"
        else
            printf "    installed\n"
        fi
    done
fi

printf "\n=== Binary tools ===\n"
if section_empty binary; then
    printf "  (none configured)\n"
else
    for name in $(jq -r '.binary | keys[]' "$MANIFEST"); do
        version=$(jq -r ".binary[\"$name\"].version" "$MANIFEST")
        method=$(jq -r ".binary[\"$name\"].update.method" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if check_update ".binary[\"$name\"]" "$name" "$version" "$method"; then
            clear_tool binary "$name"
            version=$(jq -r ".binary[\"$name\"].version" "$MANIFEST")
        fi
        if ! has_bin "$name"; then
            install_cmd=$(jq -r ".binary[\"$name\"].install" "$MANIFEST")
            printf "    install: %s\n" "$version"
            eval "$(expand "$install_cmd" "$version")"
        else
            printf "    installed\n"
        fi
    done
fi

printf "\n=== Go tools ===\n"
if section_empty go; then
    printf "  (none configured)\n"
else
    for name in $(jq -r '.go | keys[]' "$MANIFEST"); do
        version=$(jq -r ".go[\"$name\"].version" "$MANIFEST")
        method=$(jq -r ".go[\"$name\"].update.method" "$MANIFEST")
        first_bin=$(jq -r ".go[\"$name\"].binaries[0]" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if check_update ".go[\"$name\"]" "$name" "$version" "$method"; then
            clear_tool go "$name"
            version=$(jq -r ".go[\"$name\"].version" "$MANIFEST")
        fi
        if ! has_bin "$first_bin"; then
            pkg=$(jq -r ".go[\"$name\"].package" "$MANIFEST")
            pkg="${pkg//\$\{VERSION\}/$version}"
            printf "    install: %s\n" "$version"
            go install "$pkg"
        else
            printf "    installed\n"
        fi
    done
fi

printf "\n=== Node.js tools ===\n"
if section_empty npm; then
    printf "  (none configured)\n"
else
    npm_changed=false
    for name in $(jq -r '.npm | keys[]' "$MANIFEST"); do
        version=$(jq -r ".npm[\"$name\"].version" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if check_update ".npm[\"$name\"]" "$name" "$version" "npm"; then
            npm_changed=true
        fi
    done
    first_npm=$(jq -r '.npm | keys[0]' "$MANIFEST")
    if [ "$npm_changed" = true ]; then
        clear_tool npm "$first_npm"
    fi
    if ! has_bin "$first_npm"; then
        printf "    installing npm packages...\n"
        npm_args=""
        for name in $(jq -r '.npm | keys[]' "$MANIFEST"); do
            version=$(jq -r ".npm[\"$name\"].version" "$MANIFEST")
            npm_args="$npm_args ${name}@${version}"
        done
        # shellcheck disable=SC2086 # word-splitting intentional: $npm_args is a space-separated package@version list
        npm install --prefix "$TOOLS/node" -g $npm_args
    else
        printf "    all installed\n"
    fi
fi

printf "\n=== Python tools ===\n"
if section_empty pip; then
    printf "  (none configured)\n"
else
    pip_changed=false
    for name in $(jq -r '.pip | keys[]' "$MANIFEST"); do
        version=$(jq -r ".pip[\"$name\"].version" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if check_update ".pip[\"$name\"]" "$name" "$version" "pypi"; then
            pip_changed=true
        fi
    done
    first_pip=$(jq -r '.pip | keys[0]' "$MANIFEST")
    if [ "$pip_changed" = true ]; then
        clear_tool pip "$first_pip"
    fi
    if ! has_bin "$first_pip"; then
        printf "    installing pip packages...\n"
        pip_args=""
        for name in $(jq -r '.pip | keys[]' "$MANIFEST"); do
            version=$(jq -r ".pip[\"$name\"].version" "$MANIFEST")
            pip_args="$pip_args ${name}==${version}"
        done
        # shellcheck disable=SC2086 # word-splitting intentional: $pip_args is a space-separated package==version list
        pip3 install --no-cache-dir --prefix "$TOOLS/python" $pip_args
        # Debian pip uses a local/ prefix; symlink binaries into the expected path
        for f in "$TOOLS/python/local/bin"/*; do
            [ -f "$f" ] && ln -sf "$f" "$TOOLS/python/bin/$(basename "$f")"
        done
    else
        printf "    all installed\n"
    fi
fi

printf "\n=== Custom tools ===\n"
if section_empty custom; then
    printf "  (none configured)\n"
else
    for name in $(jq -r '.custom | keys[]' "$MANIFEST"); do
        version=$(jq -r ".custom[\"$name\"].version" "$MANIFEST")
        method=$(jq -r ".custom[\"$name\"].update.method" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if check_update ".custom[\"$name\"]" "$name" "$version" "$method"; then
            clear_tool custom "$name"
            version=$(jq -r ".custom[\"$name\"].version" "$MANIFEST")
        fi
        if ! has_bin "$name"; then
            install_cmd=$(jq -r ".custom[\"$name\"].install" "$MANIFEST")
            printf "    install: %s\n" "$version"
            eval "$(expand "$install_cmd" "$version")"
        else
            printf "    installed\n"
        fi
    done
fi

printf "\n=== Cargo tools ===\n"
if section_empty cargo; then
    printf "  (none configured)\n"
elif ! command -v cargo > /dev/null 2>&1; then
    printf "  cargo not found, skipping\n"
else
    for name in $(jq -r '.cargo | keys[]' "$MANIFEST"); do
        version=$(jq -r ".cargo[\"$name\"].version" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if ! has_bin "$name"; then
            printf "    install: %s\n" "$version"
            cargo install "$name" --version "${version#v}" --root "$TOOLS" 2>&1 | tail -1
        else
            printf "    installed\n"
        fi
    done
fi

printf "\n=== System packages (apt) ===\n"
if section_empty apt; then
    printf "  (none configured)\n"
else
    apt_list=""
    for name in $(jq -r '.apt | keys[]' "$MANIFEST"); do
        printf "  %s\n" "$name"
        if ! command -v "$name" > /dev/null 2>&1; then
            apt_list="$apt_list $name"
        else
            printf "    installed\n"
        fi
    done
    if [ -n "$apt_list" ]; then
        printf "    installing:%s\n" "$apt_list"
        # shellcheck disable=SC2086 # word-splitting intentional: $apt_list is a space-separated package list
        apt-get update -qq && apt-get install -y -qq --no-install-recommends $apt_list 2>&1 | tail -3
        rm -rf /var/lib/apt/lists/*
    fi
fi

printf "\n[%s] Tool setup complete\n" "$(date -Iseconds)"
