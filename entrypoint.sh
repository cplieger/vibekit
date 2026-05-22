#!/bin/bash

CONFIG_DIR="/config"
TOOLS="$CONFIG_DIR/tools"
MANIFEST="$CONFIG_DIR/tools.json"
LOG="/tmp/setup-tools.log"

mkdir -p "$TOOLS/bin" "$TOOLS/go/bin" "$TOOLS/runtimes" \
    "$TOOLS/node/bin" "$TOOLS/python/bin" "$TOOLS/lib" \
    "$HOME/.local/share/kiro-cli" "$HOME/.ssh" \
    "$HOME/.kiro" "$HOME/.cache/go-build" "$HOME/.docker/cli-plugins" \
    || { printf "ERROR: failed to create required directories (is /config mounted and writable?)\n"; sleep 10; exit 1; }

# Seed tools.json from bundled default if not present
if [ ! -f "$MANIFEST" ]; then
    printf "Seeding tools.json from default\n"
    cp /opt/vibekit/tools.json.default "$MANIFEST"
fi

# First boot: foreground (blocks until tools ready)
# Subsequent: background (tools already present), respects auto_update setting
# kiro-cli is the one tool vibekit cannot launch without; it's downloaded
# below (not baked into the image for licensing reasons, not in tools.json
# so users can't accidentally remove it).
# shellcheck disable=SC2016
# SC2016: $vars inside KIRO_CLI_INSTALL are intentionally NOT expanded at
# assignment time — the whole string is `eval`d later and references must
# resolve in that scope ($_install_script, $HOME, $_rc).
KIRO_CLI_INSTALL='
  _install_script=$(mktemp)
  if ! curl -fsSL https://cli.kiro.dev/install -o "$_install_script"; then
    printf "ERROR: failed to download kiro-cli installer\n"
    rm -f "$_install_script"
    false
  elif [ ! -s "$_install_script" ]; then
    printf "ERROR: kiro-cli installer is empty (partial download?)\n"
    rm -f "$_install_script"
    false
  else
    bash "$_install_script"
    _rc=$?
    rm -f "$_install_script"
    if [ $_rc -eq 0 ]; then
      [ -f "$HOME/.local/bin/kiro-cli" ] && mv "$HOME/.local/bin/kiro-cli" "'"$TOOLS"'/bin/kiro-cli"
      mv "$HOME/.local/bin/kiro-cli-chat" "'"$TOOLS"'/bin/kiro-cli-chat" 2>/dev/null
      mv "$HOME/.local/bin/kiro-cli-term" "'"$TOOLS"'/bin/kiro-cli-term" 2>/dev/null
      true
    else
      false
    fi
  fi
'

if [ -f "$TOOLS/bin/kiro-cli" ]; then
    AUTO_UPDATE=$(jq -r '.auto_update // true' "$CONFIG_DIR/settings.json" 2>/dev/null || echo "true")
    if [ "$AUTO_UPDATE" = "true" ]; then
        printf "Tools installed, updating in background (log: %s)\n" "$LOG"
        bash /opt/vibekit/setup-tools.sh > "$LOG" 2>&1 &
    else
        printf "Tools installed, auto-update disabled\n"
    fi
else
    printf "First boot: installing kiro-cli\n"
    printf "  kiro-cli is proprietary AWS Content; by installing you accept\n"
    printf "  the AWS Customer Agreement. License: https://kiro.dev/license/\n"
    # Suppress the installer's "Next steps / Use the command" trailer
    # (irrelevant in a container where vibekit drives kiro-cli directly).
    # PIPESTATUS preserves the installer's exit code through the sed filter.
    eval "$KIRO_CLI_INSTALL" 2>&1 \
        | sed -e '/^Next steps:$/d' -e '/^Use the command "kiro-cli"/d'
    if [ "${PIPESTATUS[0]}" -ne 0 ]; then
        printf "WARNING: kiro-cli install failed\n"
    fi
    if [ -s "$MANIFEST" ] && jq -e '.binary + .go + .npm + .pip + .custom + .runtimes | length > 0' "$MANIFEST" >/dev/null 2>&1; then
        printf "First boot: installing additional tools (log: %s)\n" "$LOG"
        bash /opt/vibekit/setup-tools.sh 2>&1 | tee "$LOG" || \
            printf "WARNING: setup-tools.sh failed, check %s\n" "$LOG"
    fi
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
    for flag in chat.enableCheckpoint telemetry.enabled; do
        kiro-cli settings "$flag" false > /dev/null 2>&1 || true
    done
    # Auto-cleanup old conversations after 1 day (configurable in Settings → General).
    kiro-cli settings cleanup.periodDays 1 > /dev/null 2>&1 || true
    exec /app/vibekit
else
    printf "WARNING: kiro-cli not found, web UI not started\n"
    printf "Run 'bash /opt/vibekit/setup-tools.sh' then '/app/vibekit'\n"
    # Stay alive so the container doesn't restart-loop
    exec sleep infinity
fi
