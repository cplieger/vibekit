#!/bin/bash
# Reads /config/tools.json, checks for updates, and installs enabled tools.
#
# Schema additions over the original:
#
#   enabled        bool   whether to install. Missing -> true (back-compat
#                          with manually-authored entries). The pre-populated
#                          tools.json.default ships every entry with
#                          "enabled": false so the user picks via the UI.
#
#   auto_update    bool   whether to bump the version on each run. Missing
#                          -> true. Lets users pin a specific version with
#                          one toggle in the UI.
#
#   requires       []str  list of section.name dependencies that must be
#                          installed before this entry. Used by the API
#                          enable handler for transitive auto-enable.
#
#   shims          {n:c}  map of shim_name -> shell command line. After
#                          install, each shim_name is created as a wrapper
#                          script in $BIN that exec's the given command.
#                          Lets us expose tsgo as typescript-language-server
#                          (the name kiro-cli looks for) and pyrefly as
#                          pyright/pyright-langserver, without baking
#                          shim logic into the install command itself.
#
#   uninstall      str    optional override for cleanup. When DELETE
#                          /api/tools/{section}/{name} fires, clear_tool()
#                          uses this if present, otherwise falls back to
#                          per-section defaults.
#
#   method (lsp)   str    only for the lsp section: which install pipeline
#                          handles this entry (binary / npm / go / gem).
#                          Lets us group all language servers under one
#                          UI section while reusing existing install code.
#
# FUTURE ENHANCEMENT: today this script only runs on container start
# (foreground on first boot, background thereafter, both gated by
# config.auto_update). Adding a daily timer (e.g. cron at /etc/cron.d
# or a vibekit-side scheduler) would close the gap for long-running
# containers that never restart. The check_update + install loop is
# already idempotent so the same script can serve both triggers.

set -uo pipefail

# Paths are overridable via env so the API DELETE handler and tests can
# point the same helper functions at a different tree / fixture.
TOOLS="${VK_TOOLS_DIR:-/config/tools}"
BIN="$TOOLS/bin"
RUNTIMES="$TOOLS/runtimes"
GOBIN="$TOOLS/go/bin"
MANIFEST="${VK_MANIFEST:-/config/tools.json}"

export GOROOT="$RUNTIMES/go"
export GOPATH="$TOOLS/go"
export GOBIN
export PATH="$BIN:$GOBIN:$RUNTIMES/go/bin:$RUNTIMES/node/bin:$TOOLS/node/bin:$TOOLS/python/bin:$RUNTIMES/uv/bin:$RUNTIMES/ruby/bin:$RUNTIMES/rust/bin:$RUNTIMES/java/bin:$PATH"

# When sourced (DELETE handler reusing clear_tool, or tests probing
# helpers) we skip the eager mkdir + manifest existence check and the
# install loop at the bottom. Direct execution does the full run.
_vk_sourced=0
if [ "${BASH_SOURCE[0]}" != "${0}" ]; then
    _vk_sourced=1
fi

if [ "$_vk_sourced" = "0" ]; then
    mkdir -p "$BIN" "$GOBIN" "$RUNTIMES" "$TOOLS/node/bin" \
        "$TOOLS/python/bin" "$TOOLS/lib"

    if [ ! -f "$MANIFEST" ]; then
        printf "ERROR: %s not found\n" "$MANIFEST"
        exit 1
    fi

    printf "[%s] Tool setup starting\n" "$(date -Iseconds)"
fi

# --- Architecture detection ---
# Resolved once at script start; consumed via expand() placeholders.
# These cover the three common naming conventions used by upstream
# release artifacts. Add more pairs only when a real entry needs them.

UNAME_M=$(uname -m)
case "$UNAME_M" in
    aarch64|arm64)
        ARCH_X64_OR_ARM64="arm64"
        ARCH_AMD64_OR_ARM64="arm64"
        ARCH_X86_64_OR_AARCH64="aarch64"
        ARCH_X64_OR_AARCH64="aarch64"
        ARCH_X86_64_OR_ARM64="arm64"
        ;;
    x86_64|amd64)
        ARCH_X64_OR_ARM64="x64"
        ARCH_AMD64_OR_ARM64="amd64"
        ARCH_X86_64_OR_AARCH64="x86_64"
        ARCH_X64_OR_AARCH64="x64"
        ARCH_X86_64_OR_ARM64="x86_64"
        ;;
    *)
        printf "WARNING: unrecognized uname -m: %s; defaulting arch placeholders to x64/amd64/x86_64\n" "$UNAME_M"
        ARCH_X64_OR_ARM64="x64"
        ARCH_AMD64_OR_ARM64="amd64"
        ARCH_X86_64_OR_AARCH64="x86_64"
        ARCH_X64_OR_AARCH64="x64"
        ARCH_X86_64_OR_ARM64="x86_64"
        ;;
esac

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
    cmd="${cmd//\$\{ARCH_X64_OR_ARM64\}/$ARCH_X64_OR_ARM64}"
    cmd="${cmd//\$\{ARCH_AMD64_OR_ARM64\}/$ARCH_AMD64_OR_ARM64}"
    cmd="${cmd//\$\{ARCH_X86_64_OR_AARCH64\}/$ARCH_X86_64_OR_AARCH64}"
    cmd="${cmd//\$\{ARCH_X64_OR_AARCH64\}/$ARCH_X64_OR_AARCH64}"
    cmd="${cmd//\$\{ARCH_X86_64_OR_ARM64\}/$ARCH_X86_64_OR_ARM64}"
    printf '%s' "$cmd"
}

has_bin() {
    local name="$1"
    [ -x "$BIN/$name" ] || [ -x "$GOBIN/$name" ] || \
        [ -x "$TOOLS/node/bin/$name" ] || [ -x "$TOOLS/python/bin/$name" ] || \
        [ -x "$RUNTIMES/uv/bin/$name" ] || [ -x "$RUNTIMES/ruby/bin/$name" ] || \
        [ -x "$RUNTIMES/rust/bin/$name" ] || [ -x "$RUNTIMES/java/bin/$name" ]
}

# Returns 0 if the given top-level section is missing or empty.
section_empty() {
    local section="$1"
    local count
    count=$(jq -r "(.${section} // {}) | length" "$MANIFEST" 2>/dev/null || echo 0)
    [ "$count" = "0" ]
}

# Returns 0 if the entry at jq_path has enabled != false. Missing field
# defaults to true so user-authored manifests keep working without an
# explicit flag.
entry_enabled() {
    local jq_path="$1"
    local enabled
    # NB: jq's `//` treats `false` as empty, so `.enabled // true` would
    # wrongly yield true for an explicit false. Test `!= false` instead:
    # true when the field is true OR absent, false only when explicitly false.
    enabled=$(jq -r "${jq_path}.enabled != false" "$MANIFEST")
    [ "$enabled" = "true" ]
}

# Returns 0 if the entry at jq_path has auto_update != false. Missing
# field defaults to true (auto-update by default).
entry_auto_update() {
    local jq_path="$1"
    local au
    # Same `//`-with-false caveat as entry_enabled; use != false.
    au=$(jq -r "${jq_path}.auto_update != false" "$MANIFEST")
    [ "$au" = "true" ]
}

# Create wrapper scripts in $BIN for every entry in the .shims object.
# Each shim's value is the shell line to exec. We wrap it in /bin/sh
# so the shim works in any caller's shell and doesn't depend on PATH
# alignment when kiro-cli spawns the LSP.
write_shims() {
    local jq_path="$1"
    local shim_count
    shim_count=$(jq -r "(${jq_path}.shims // {}) | length" "$MANIFEST" 2>/dev/null || echo 0)
    if [ "$shim_count" = "0" ]; then
        return 0
    fi
    while IFS=$'\t' read -r shim_name shim_cmd; do
        [ -z "$shim_name" ] && continue
        # shellcheck disable=SC2016 # we're writing literal strings to a script
        printf '#!/bin/sh\nexec %s "$@"\n' "$shim_cmd" > "$BIN/$shim_name"
        chmod 755 "$BIN/$shim_name"
        printf "    shim: %s -> %s\n" "$shim_name" "$shim_cmd"
    done < <(jq -r "${jq_path}.shims // {} | to_entries[] | \"\(.key)\t\(.value)\"" "$MANIFEST")
}

# Remove shims associated with an entry. Called by clear_tool().
remove_shims() {
    local jq_path="$1"
    while IFS= read -r shim_name; do
        [ -z "$shim_name" ] && continue
        rm -f "$BIN/$shim_name"
    done < <(jq -r "${jq_path}.shims // {} | keys[]" "$MANIFEST" 2>/dev/null)
}

# Check for a newer version and update the manifest. Returns 0 if changed.
# Honors auto_update: false (the per-entry pin).
check_update() {
    local jq_path="$1" name="$2" current="$3" method="$4"
    local latest=""

    # Per-tool pin: skip the version bump entirely. The entry stays at
    # its current version on every boot until the user toggles auto_update
    # back on or edits the manifest.
    if ! entry_auto_update "$jq_path"; then
        return 1
    fi

    case "$method" in
        manual|null) return 1 ;;
    esac

    # Skip values that look like git commit hashes (a single run of
    # 7-40 hex chars with no separators) — those aren't comparable.
    # Everything else (v1.2.3, 1.2.3, jdk-21.0.5+11, 2025-01-06,
    # 7.0.0-dev.20260527.2) is a legitimate upstream version tag and
    # may auto-update.
    case "$current" in
        *[!0-9a-f]*) ;;            # contains a non-hex char -> a real version
        [0-9a-f]*[0-9a-f]) return 1 ;;  # pure hex -> commit hash, skip
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
            # npm view requires the npm CLI which is itself an opt-in
            # tool. If npm isn't present yet, skip the update silently.
            command -v npm >/dev/null 2>&1 || return 1
            latest=$(npm view "$name" version 2>/dev/null) || true
            ;;
        pypi)
            latest=$(curl -fsSL --connect-timeout 10 --max-time 15 \
                "https://pypi.org/pypi/${name}/json" 2>/dev/null \
                | jq -r '.info.version // empty') || true
            ;;
        rubygems)
            latest=$(curl -fsSL --connect-timeout 10 --max-time 15 \
                "https://rubygems.org/api/v1/gems/${name}.json" 2>/dev/null \
                | jq -r '.version // empty') || true
            ;;
    esac

    if [ -z "$latest" ]; then
        printf "    update: fetch failed\n"
        return 1
    fi

    # Security: the version string is substituted into the install
    # command which is later eval'd. A malicious or malformed upstream
    # tag containing shell metacharacters would execute on next boot.
    # Allow only the characters real version tags use; reject anything
    # else and keep the pinned version.
    case "$latest" in
        *[!a-zA-Z0-9._+-]*)
            printf "    update: rejected upstream version %q (illegal chars), keeping pinned\n" "$latest"
            return 1
            ;;
    esac

    if [ "$current" = "$latest" ]; then
        return 1
    fi

    printf "    update: %s -> %s\n" "$current" "$latest"
    # Atomic rewrite: validate jq output before replacing the manifest.
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

# Clear binaries (and shims) for a tool so install re-downloads, or so a
# Delete request fully removes the install footprint. Honors a custom
# .uninstall field if the entry provides one.
clear_tool() {
    local section="$1" name="$2"
    local jq_path=".${section}[\"$name\"]"

    # Custom uninstall override always wins.
    local uninstall
    uninstall=$(jq -r "${jq_path}.uninstall // empty" "$MANIFEST" 2>/dev/null)
    if [ -n "$uninstall" ]; then
        local version
        version=$(jq -r "${jq_path}.version // \"\"" "$MANIFEST" 2>/dev/null)
        eval "$(expand "$uninstall" "$version")" || true
        remove_shims "$jq_path"
        return 0
    fi

    case "$section" in
        runtimes)
            rm -rf "${RUNTIMES:?}/$name"
            ;;
        binary|custom)
            rm -f "$BIN/$name"
            ;;
        go)
            for bin in $(jq -r "${jq_path}.binaries[]?" "$MANIFEST" 2>/dev/null); do
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
        lsp)
            # Delegate to the entry's install method.
            local lsp_method
            lsp_method=$(jq -r "${jq_path}.method // \"binary\"" "$MANIFEST")
            case "$lsp_method" in
                binary) rm -f "$BIN/$name" ;;
                npm)    rm -rf "$TOOLS/node/lib/node_modules/$name"
                        rm -f "$TOOLS/node/bin/$name" ;;
                go)     for bin in $(jq -r "${jq_path}.binaries[]?" "$MANIFEST" 2>/dev/null); do
                            rm -f "$GOBIN/$bin"
                        done ;;
                gem)    if command -v gem >/dev/null 2>&1; then
                            gem uninstall "$name" --all --executables --force 2>/dev/null || true
                        fi ;;
            esac
            ;;
        apt)
            ;; # apt packages managed by system, no clear needed
    esac
    remove_shims "$jq_path"
}

# Resolve the requires chain: every entry listed in .requires must be
# enabled before we install this entry. We don't auto-enable here (the
# API handler does that, then writes back to the manifest); we just
# refuse to install an entry whose deps aren't satisfied yet.
requires_satisfied() {
    local jq_path="$1"
    local req
    while IFS= read -r req; do
        [ -z "$req" ] && continue
        # req format: "section.name" e.g. "runtimes.node"
        local sec="${req%%.*}"
        local n="${req#*.}"
        local req_enabled
        # Match entry_enabled's semantics: enabled unless explicitly false.
        # `has` guards a missing entry (null != false is true in jq, which
        # would otherwise falsely report a non-existent dep as satisfied).
        req_enabled=$(jq -r "(.${sec}[\"${n}\"] != null) and (.${sec}[\"${n}\"].enabled != false)" "$MANIFEST" 2>/dev/null)
        if [ "$req_enabled" != "true" ]; then
            printf "    skipped: requires %s (not enabled)\n" "$req"
            return 1
        fi
    done < <(jq -r "${jq_path}.requires[]?" "$MANIFEST" 2>/dev/null)
    return 0
}

# Run an install command captured from the manifest, with placeholders
# expanded. Used by sections that store install scripts inline.
run_install() {
    local jq_path="$1" version="$2" install_field="${3:-install}"
    local install_cmd
    install_cmd=$(jq -r "${jq_path}.${install_field}" "$MANIFEST")
    if [ "$install_cmd" = "null" ] || [ -z "$install_cmd" ]; then
        printf "    error: no install command\n"
        return 1
    fi
    eval "$(expand "$install_cmd" "$version")"
}

# --- Process each section: update then install ---
#
# When this script is SOURCED rather than executed (the API DELETE
# handler sources it to reuse clear_tool(); tests source it to probe
# the helper functions), stop here — only the function and variable
# definitions above are wanted, not the install loop.
if [ "$_vk_sourced" = "1" ]; then
    # shellcheck disable=SC2317 # reachable when sourced; shellcheck can't see that
    return 0 2>/dev/null || true
fi

# Order matters: runtimes first, then everything that may depend on
# them. lsp last because it can require any runtime.

printf "\n=== Runtimes ===\n"
if section_empty runtimes; then
    printf "  (none configured)\n"
else
    for name in $(jq -r '.runtimes | keys[]' "$MANIFEST"); do
        jq_path=".runtimes[\"$name\"]"
        version=$(jq -r "${jq_path}.version" "$MANIFEST")
        method=$(jq -r "${jq_path}.update.method // \"manual\"" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if ! entry_enabled "$jq_path"; then
            printf "    disabled\n"
            continue
        fi
        if check_update "$jq_path" "$name" "$version" "$method"; then
            clear_tool runtimes "$name"
            version=$(jq -r "${jq_path}.version" "$MANIFEST")
        fi
        # Each runtime declares its own "installed" probe. Default: a
        # binary at $RUNTIMES/<name>/bin/<name>. Override via .probe.
        probe=$(jq -r "${jq_path}.probe // \"\"" "$MANIFEST")
        if [ -z "$probe" ]; then
            probe="$RUNTIMES/$name/bin/$name"
        fi
        if [ ! -e "$probe" ]; then
            printf "    install: %s\n" "$version"
            run_install "$jq_path" "$version" || printf "    error: runtime install failed\n"
        else
            printf "    installed\n"
        fi
        write_shims "$jq_path"
    done
fi

printf "\n=== Binary tools ===\n"
if section_empty binary; then
    printf "  (none configured)\n"
else
    for name in $(jq -r '.binary | keys[]' "$MANIFEST"); do
        jq_path=".binary[\"$name\"]"
        version=$(jq -r "${jq_path}.version" "$MANIFEST")
        method=$(jq -r "${jq_path}.update.method // \"manual\"" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if ! entry_enabled "$jq_path"; then
            printf "    disabled\n"
            continue
        fi
        if check_update "$jq_path" "$name" "$version" "$method"; then
            clear_tool binary "$name"
            version=$(jq -r "${jq_path}.version" "$MANIFEST")
        fi
        if ! has_bin "$name"; then
            printf "    install: %s\n" "$version"
            run_install "$jq_path" "$version" || printf "    error: install failed\n"
        else
            printf "    installed\n"
        fi
        write_shims "$jq_path"
    done
fi

printf "\n=== Go tools ===\n"
if section_empty go; then
    printf "  (none configured)\n"
else
    for name in $(jq -r '.go | keys[]' "$MANIFEST"); do
        jq_path=".go[\"$name\"]"
        version=$(jq -r "${jq_path}.version" "$MANIFEST")
        method=$(jq -r "${jq_path}.update.method // \"manual\"" "$MANIFEST")
        first_bin=$(jq -r "${jq_path}.binaries[0]" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if ! entry_enabled "$jq_path"; then
            printf "    disabled\n"
            continue
        fi
        if ! requires_satisfied "$jq_path"; then
            continue
        fi
        if check_update "$jq_path" "$name" "$version" "$method"; then
            clear_tool go "$name"
            version=$(jq -r "${jq_path}.version" "$MANIFEST")
        fi
        if ! has_bin "$first_bin"; then
            if ! command -v go >/dev/null 2>&1; then
                printf "    skipped: go runtime not installed\n"
                continue
            fi
            pkg=$(jq -r "${jq_path}.package" "$MANIFEST")
            pkg="${pkg//\$\{VERSION\}/$version}"
            printf "    install: %s\n" "$version"
            go install "$pkg"
        else
            printf "    installed\n"
        fi
        write_shims "$jq_path"
    done
fi

printf "\n=== Node.js tools ===\n"
if section_empty npm; then
    printf "  (none configured)\n"
elif ! command -v npm >/dev/null 2>&1; then
    printf "  npm not found, skipping (enable runtimes.node first)\n"
else
    npm_changed=false
    for name in $(jq -r '.npm | keys[]' "$MANIFEST"); do
        jq_path=".npm[\"$name\"]"
        if ! entry_enabled "$jq_path"; then continue; fi
        version=$(jq -r "${jq_path}.version" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if check_update "$jq_path" "$name" "$version" "npm"; then
            npm_changed=true
        fi
    done
    first_npm=""
    for n in $(jq -r '.npm | keys[]' "$MANIFEST"); do
        if entry_enabled ".npm[\"$n\"]"; then first_npm="$n"; break; fi
    done
    if [ -n "$first_npm" ]; then
        if [ "$npm_changed" = true ]; then
            clear_tool npm "$first_npm"
        fi
        if ! has_bin "$first_npm"; then
            printf "    installing npm packages...\n"
            npm_args=""
            for name in $(jq -r '.npm | keys[]' "$MANIFEST"); do
                if ! entry_enabled ".npm[\"$name\"]"; then continue; fi
                version=$(jq -r ".npm[\"$name\"].version" "$MANIFEST")
                npm_args="$npm_args ${name}@${version}"
            done
            # shellcheck disable=SC2086
            npm install --prefix "$TOOLS/node" -g $npm_args
        else
            printf "    all installed\n"
        fi
    fi
fi

printf "\n=== Python tools (pip) ===\n"
if section_empty pip; then
    printf "  (none configured)\n"
elif ! command -v pip3 >/dev/null 2>&1 && ! command -v uv >/dev/null 2>&1; then
    printf "  no python package manager found, skipping (enable runtimes.uv first)\n"
else
    pip_changed=false
    for name in $(jq -r '.pip | keys[]' "$MANIFEST"); do
        jq_path=".pip[\"$name\"]"
        if ! entry_enabled "$jq_path"; then continue; fi
        version=$(jq -r "${jq_path}.version" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if check_update "$jq_path" "$name" "$version" "pypi"; then
            pip_changed=true
        fi
    done
    first_pip=""
    for n in $(jq -r '.pip | keys[]' "$MANIFEST"); do
        if entry_enabled ".pip[\"$n\"]"; then first_pip="$n"; break; fi
    done
    if [ -n "$first_pip" ]; then
        if [ "$pip_changed" = true ]; then
            clear_tool pip "$first_pip"
        fi
        if ! has_bin "$first_pip"; then
            printf "    installing pip packages...\n"
            pip_args=""
            for name in $(jq -r '.pip | keys[]' "$MANIFEST"); do
                if ! entry_enabled ".pip[\"$name\"]"; then continue; fi
                version=$(jq -r ".pip[\"$name\"].version" "$MANIFEST")
                pip_args="$pip_args ${name}==${version}"
            done
            if command -v uv >/dev/null 2>&1; then
                # shellcheck disable=SC2086
                uv pip install --prefix "$TOOLS/python" --system $pip_args
            else
                # shellcheck disable=SC2086
                pip3 install --no-cache-dir --prefix "$TOOLS/python" $pip_args
                for f in "$TOOLS/python/local/bin"/*; do
                    [ -f "$f" ] && ln -sf "$f" "$TOOLS/python/bin/$(basename "$f")"
                done
            fi
        else
            printf "    all installed\n"
        fi
    fi
fi

printf "\n=== Custom tools ===\n"
if section_empty custom; then
    printf "  (none configured)\n"
else
    for name in $(jq -r '.custom | keys[]' "$MANIFEST"); do
        jq_path=".custom[\"$name\"]"
        version=$(jq -r "${jq_path}.version" "$MANIFEST")
        method=$(jq -r "${jq_path}.update.method // \"manual\"" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if ! entry_enabled "$jq_path"; then
            printf "    disabled\n"
            continue
        fi
        if ! requires_satisfied "$jq_path"; then
            continue
        fi
        if check_update "$jq_path" "$name" "$version" "$method"; then
            clear_tool custom "$name"
            version=$(jq -r "${jq_path}.version" "$MANIFEST")
        fi
        if ! has_bin "$name"; then
            printf "    install: %s\n" "$version"
            run_install "$jq_path" "$version" || printf "    error: install failed\n"
        else
            printf "    installed\n"
        fi
        write_shims "$jq_path"
    done
fi

printf "\n=== Cargo tools ===\n"
if section_empty cargo; then
    printf "  (none configured)\n"
elif ! command -v cargo > /dev/null 2>&1; then
    printf "  cargo not found, skipping (enable runtimes.rust first)\n"
else
    for name in $(jq -r '.cargo | keys[]' "$MANIFEST"); do
        jq_path=".cargo[\"$name\"]"
        if ! entry_enabled "$jq_path"; then
            printf "  %s: disabled\n" "$name"
            continue
        fi
        version=$(jq -r "${jq_path}.version" "$MANIFEST")
        printf "  %s (%s):\n" "$name" "$version"
        if ! has_bin "$name"; then
            printf "    install: %s\n" "$version"
            cargo install "$name" --version "${version#v}" --root "$TOOLS" 2>&1 | tail -1
        else
            printf "    installed\n"
        fi
        write_shims "$jq_path"
    done
fi

printf "\n=== Language servers (lsp) ===\n"
if section_empty lsp; then
    printf "  (none configured)\n"
else
    for name in $(jq -r '.lsp | keys[]' "$MANIFEST"); do
        jq_path=".lsp[\"$name\"]"
        version=$(jq -r "${jq_path}.version" "$MANIFEST")
        method=$(jq -r "${jq_path}.update.method // \"manual\"" "$MANIFEST")
        install_method=$(jq -r "${jq_path}.method // \"binary\"" "$MANIFEST")
        printf "  %s (%s, via %s):\n" "$name" "$version" "$install_method"
        if ! entry_enabled "$jq_path"; then
            printf "    disabled\n"
            continue
        fi
        if ! requires_satisfied "$jq_path"; then
            continue
        fi
        if check_update "$jq_path" "$name" "$version" "$method"; then
            clear_tool lsp "$name"
            version=$(jq -r "${jq_path}.version" "$MANIFEST")
        fi
        # The "primary" binary the LSP exposes, as kiro-cli sees it.
        # For shimmed entries this is the shim name; for direct
        # installs it's the binary itself. Default to the entry name.
        primary=$(jq -r "${jq_path}.primary // \"$name\"" "$MANIFEST")
        if has_bin "$primary"; then
            printf "    installed\n"
            write_shims "$jq_path"
            continue
        fi
        printf "    install: %s\n" "$version"
        case "$install_method" in
            binary)
                run_install "$jq_path" "$version" || printf "    error: install failed\n"
                ;;
            npm)
                if ! command -v npm >/dev/null 2>&1; then
                    printf "    skipped: npm not available\n"
                    continue
                fi
                pkg=$(jq -r "${jq_path}.package // \"$name\"" "$MANIFEST")
                npm install --prefix "$TOOLS/node" -g "${pkg}@${version#v}" 2>&1 | tail -3
                # If the entry declares extra packages (e.g. typescript-
                # language-server requires the typescript compiler too),
                # install them in the same call.
                while IFS= read -r extra; do
                    [ -z "$extra" ] && continue
                    npm install --prefix "$TOOLS/node" -g "$extra" 2>&1 | tail -2
                done < <(jq -r "${jq_path}.npm_extras[]?" "$MANIFEST" 2>/dev/null)
                ;;
            go)
                if ! command -v go >/dev/null 2>&1; then
                    printf "    skipped: go not available\n"
                    continue
                fi
                pkg=$(jq -r "${jq_path}.package" "$MANIFEST")
                pkg="${pkg//\$\{VERSION\}/$version}"
                go install "$pkg"
                ;;
            gem)
                if ! command -v gem >/dev/null 2>&1; then
                    printf "    skipped: gem not available (enable runtimes.ruby first)\n"
                    continue
                fi
                gem install "$name" -v "${version#v}" --no-document 2>&1 | tail -3
                ;;
            *)
                printf "    error: unknown install method '%s'\n" "$install_method"
                ;;
        esac
        write_shims "$jq_path"
    done
fi

printf "\n=== System packages (apt) ===\n"
if section_empty apt; then
    printf "  (none configured)\n"
else
    apt_list=""
    for name in $(jq -r '.apt | keys[]' "$MANIFEST"); do
        jq_path=".apt[\"$name\"]"
        if ! entry_enabled "$jq_path"; then
            printf "  %s: disabled\n" "$name"
            continue
        fi
        printf "  %s\n" "$name"
        if ! command -v "$name" > /dev/null 2>&1 && ! dpkg -s "$name" >/dev/null 2>&1; then
            apt_list="$apt_list $name"
        else
            printf "    installed\n"
        fi
    done
    if [ -n "$apt_list" ]; then
        printf "    installing:%s\n" "$apt_list"
        # shellcheck disable=SC2086
        apt-get update -qq && apt-get install -y -qq --no-install-recommends $apt_list 2>&1 | tail -3
        rm -rf /var/lib/apt/lists/*
    fi
fi

printf "\n[%s] Tool setup complete\n" "$(date -Iseconds)"
