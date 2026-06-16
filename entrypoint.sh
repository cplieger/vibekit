#!/bin/bash

CONFIG_DIR="/config"
TOOLS="$CONFIG_DIR/tools"
MANIFEST="$CONFIG_DIR/tools.json"
LOG="/tmp/setup-tools.log"

# kiro-cli is pinned via Renovate against the public install manifest at
# https://desktop-release.q.us-east-1.amazonaws.com/index.json. Bumping
# either literal triggers a reinstall on next container start (see the
# version-drift check below). The in-binary auto-update is disabled so
# what runs always matches the version baked into the image tag and the
# SHA we verified at install time. KIRO_CLI_SHA256 is the sha256 of the
# x86_64-linux headless zip; on aarch64 the hash is logged but not
# enforced (Renovate tracks one arch).
# renovate: datasource=custom.kiro-cli depName=kiro-cli
KIRO_CLI_VERSION="2.7.1"
KIRO_CLI_SHA256="abdf9ea163229151db558dd6a5cb4f3ebf8822d53bc1e1653af16c0bc7ccb64f"

mkdir -p "$TOOLS/bin" "$TOOLS/go/bin" "$TOOLS/runtimes" \
    "$TOOLS/node/bin" "$TOOLS/python/bin" "$TOOLS/lib" \
    "$HOME/.local/share/kiro-cli" "$HOME/.ssh" \
    "$KIRO_HOME" "$HOME/.cache/go-build" "$HOME/.docker/cli-plugins" \
    || { printf "ERROR: failed to create required directories (is /config mounted and writable?)\n"; sleep 10; exit 1; }

# Seed tools.json from bundled default if not present
if [ ! -f "$MANIFEST" ]; then
    printf "Seeding tools.json from default\n"
    cp /opt/vibekit/tools.json.default "$MANIFEST"
fi

# kiro-cli is the one tool vibekit cannot launch without; it's downloaded
# below (not baked into the image for licensing reasons, not in tools.json
# so users can't accidentally remove it). Pinned via KIRO_CLI_VERSION /
# KIRO_CLI_SHA256 above; Renovate keeps both in lockstep with the public
# install manifest.
install_kiro_cli() {
    printf "Installing kiro-cli %s\n" "$KIRO_CLI_VERSION"
    printf "  kiro-cli is proprietary AWS Content; by installing you accept\n"
    printf "  the AWS Customer Agreement. License: https://kiro.dev/license/\n"

    # Direct download of the versioned zip per the docs:
    # https://kiro.dev/docs/cli/installation/ ("With a zip file"). We pin
    # the version (not /latest/) so a given image tag is reproducible and
    # verify the sha256 before running install.sh.
    local arch zip_url tmpdir zip
    case "$(uname -m)" in
        x86_64)  arch="x86_64-linux"  ;;
        aarch64) arch="aarch64-linux" ;;
        *)
            printf "ERROR: unsupported architecture: %s\n" "$(uname -m)" >&2
            return 1
            ;;
    esac
    zip_url="https://desktop-release.q.us-east-1.amazonaws.com/${KIRO_CLI_VERSION}/kirocli-${arch}.zip"

    tmpdir=$(mktemp -d) || return 1
    zip="$tmpdir/kirocli.zip"

    if ! curl --proto '=https' --tlsv1.2 -fsSL "$zip_url" -o "$zip"; then
        printf "ERROR: failed to download kiro-cli zip from %s\n" "$zip_url" >&2
        rm -rf "$tmpdir"
        return 1
    fi
    if [ ! -s "$zip" ]; then
        printf "ERROR: kiro-cli zip is empty (partial download?)\n" >&2
        rm -rf "$tmpdir"
        return 1
    fi

    # Verify SHA-256. KIRO_CLI_SHA256 is the x86_64-linux hash from the
    # install manifest, kept in lockstep with KIRO_CLI_VERSION by Renovate.
    # On aarch64 we log the hash for the audit trail but do not enforce.
    local actual
    actual=$(sha256sum "$zip" | awk '{print $1}')
    printf "kiro-cli zip SHA-256: %s (url=%s)\n" "$actual" "$zip_url"
    if [ "$arch" = "x86_64-linux" ]; then
        if [ "$actual" != "$KIRO_CLI_SHA256" ]; then
            printf "ERROR: kiro-cli SHA-256 mismatch\n" >&2
            printf "  expected: %s\n" "$KIRO_CLI_SHA256" >&2
            printf "  actual:   %s\n" "$actual" >&2
            printf "  refusing install; bump KIRO_CLI_VERSION/KIRO_CLI_SHA256 together\n" >&2
            rm -rf "$tmpdir"
            return 1
        fi
        printf "kiro-cli SHA-256 verified against pinned hash\n"
    else
        printf "kiro-cli SHA-256 unverified on %s (no pinned hash for this arch)\n" "$arch"
    fi

    if ! unzip -q "$zip" -d "$tmpdir"; then
        printf "ERROR: failed to extract kiro-cli zip\n" >&2
        rm -rf "$tmpdir"
        return 1
    fi

    # Run upstream install.sh. Don't gate on its exit code — the kiro-cli
    # installer touches shell profiles and other side surfaces that
    # legitimately fail in our minimal root container; what matters is
    # whether the binary it drops at $HOME/.local/bin/kiro-cli reports
    # the version we pinned. Capture install.sh output to a tempfile so
    # we can surface it on failure.
    local install_log install_rc
    install_log=$(mktemp)
    "$tmpdir/kirocli/install.sh" --no-confirm < /dev/null > "$install_log" 2>&1
    install_rc=$?
    rm -rf "$tmpdir"

    if [ ! -f "$HOME/.local/bin/kiro-cli" ]; then
        printf "ERROR: install.sh did not produce %s/.local/bin/kiro-cli (rc=%d)\n" \
            "$HOME" "$install_rc" >&2
        printf "install.sh output:\n" >&2
        cat "$install_log" >&2
        rm -f "$install_log"
        return 1
    fi
    local installed
    installed=$("$HOME/.local/bin/kiro-cli" --version 2>/dev/null | awk '{print $NF}')
    if [ "$installed" != "$KIRO_CLI_VERSION" ]; then
        printf "ERROR: installed binary reports version %s, wanted %s (install.sh rc=%d)\n" \
            "${installed:-unknown}" "$KIRO_CLI_VERSION" "$install_rc" >&2
        printf "install.sh output:\n" >&2
        cat "$install_log" >&2
        rm -f "$install_log"
        return 1
    fi
    rm -f "$install_log"

    # Promote to the canonical $TOOLS/bin/ location so PATH ordering
    # (which puts /config/tools/bin first) and any in-process
    # absolute-path references resolve to the freshly installed binary.
    mv -f "$HOME/.local/bin/kiro-cli" "$TOOLS/bin/kiro-cli" || return 1
    mv -f "$HOME/.local/bin/kiro-cli-chat" "$TOOLS/bin/kiro-cli-chat" 2>/dev/null || true
    mv -f "$HOME/.local/bin/kiro-cli-term" "$TOOLS/bin/kiro-cli-term" 2>/dev/null || true
}

# Reinstall when either the binary is missing or the on-disk version
# drifts from KIRO_CLI_VERSION. The binary lives on the persistent
# /config volume, so a freshly bumped image needs this drift check to
# actually pick up the new version on restart.
needs_kiro_cli_install() {
    if [ ! -f "$TOOLS/bin/kiro-cli" ]; then
        return 0
    fi
    local current
    current=$("$TOOLS/bin/kiro-cli" --version 2>/dev/null | awk '{print $NF}')
    if [ "$current" != "$KIRO_CLI_VERSION" ]; then
        printf "kiro-cli version drift: installed=%s pinned=%s; reinstalling\n" \
            "${current:-unknown}" "$KIRO_CLI_VERSION"
        return 0
    fi
    return 1
}

if needs_kiro_cli_install; then
    if ! install_kiro_cli; then
        printf "WARNING: kiro-cli install failed\n"
    fi
fi

# Run setup-tools.sh FOREGROUND on every boot. This ensures LSPs and
# other on-demand tools are on PATH before kiro-cli spawns its first
# bridge (kiro-cli scans PATH for language servers at code-intelligence
# init time — if an LSP isn't present yet, it won't be detected).
#
# Performance: setup-tools.sh skips already-installed tools (just a
# file-existence check per entry). The only cost on a warm boot is the
# version-update network probes (~1s each for entries with auto_update
# enabled). This is acceptable vs. the alternative of LSPs being
# silently missing on the first chat after a restart.
if [ -s "$MANIFEST" ] && jq -e '.binary + .go + .npm + .pip + .custom + .runtimes + .lsp | length > 0' "$MANIFEST" >/dev/null 2>&1; then
    printf "Running setup-tools.sh (log: %s)\n" "$LOG"
    bash /opt/vibekit/setup-tools.sh 2>&1 | tee "$LOG" || \
        printf "WARNING: setup-tools.sh failed, check %s\n" "$LOG"
fi

# Launch the ACP web UI server (foreground)
# Falls back to sleep if kiro-cli isn't available yet
if command -v kiro-cli > /dev/null 2>&1; then
    # Enable the experimental kiro-cli features we surface in the UI.
    # Defaults: on for features vibekit renders natively, off for the
    # duplicate-of-vibekit and telemetry toggles:
    #
    #   chat.enableCheckpoint       — off. vibekit has its own
    #     shadow-git checkpoint system (internal/checkpoint/) wired
    #     into the Restore buttons, diff endpoint, and per-file Undo.
    #     Leaving kiro-cli's parallel system on doubled the shadow-git
    #     disk cost with no user-visible benefit.
    #   telemetry.enabled           — off. self-hosted tool shouldn't
    #     phone home without the user opting in. Users can flip it on
    #     from Settings → General if they want to share usage data.
    #   toolSearch.enabled          — off, matching kiro-cli's default.
    #     Loading MCP tools on demand saves context tokens for users
    #     with many MCP servers but adds a round-trip per tool
    #     discovery; the trade-off is opt-in. We seed the value
    #     explicitly so the UI checkbox reflects the actual state
    #     (without seeding, the get-empty fallback would show the
    #     toggle as on while reality was off).
    #
    # chat.enableContextUsageIndicator was removed from this loop; it
    # only affects kiro-cli's own TUI prompt line, which we never
    # render — vibekit draws the context ring from the kiro/metadata
    # ACP notification regardless of the flag.
    #
    # Settings writes are idempotent so running this on every boot
    # keeps new containers consistent without requiring the user to
    # discover the toggles first.
    for flag in chat.enableTodoList chat.enableKnowledge \
                chat.enableSubagent chat.enablePromptHints \
                hooks.showStatus; do
        kiro-cli settings "$flag" true > /dev/null 2>&1 || true
    done
    for flag in chat.enableCheckpoint telemetry.enabled toolSearch.enabled; do
        kiro-cli settings "$flag" false > /dev/null 2>&1 || true
    done
    # Disable in-binary auto-update: KIRO_CLI_VERSION above is the
    # source of truth, kept current by Renovate against the public
    # install manifest. Letting kiro-cli silently replace itself
    # would invalidate the pinned SHA, break image-tag reproducibility,
    # and bypass the version-drift reinstall path. Bumps land via
    # Renovate PR → image rebuild → restart picks them up.
    kiro-cli settings "app.disableAutoupdates" true > /dev/null 2>&1 || true
    # Auto-cleanup old conversations after 1 day (configurable in Settings → General).
    kiro-cli settings cleanup.periodDays 1 > /dev/null 2>&1 || true
    exec /app/vibekit
else
    printf "WARNING: kiro-cli not found, web UI not started\n"
    printf "Run 'bash /opt/vibekit/setup-tools.sh' then '/app/vibekit'\n"
    # Stay alive so the container doesn't restart-loop
    exec sleep infinity
fi
