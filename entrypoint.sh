#!/bin/bash

CONFIG_DIR="/config"
TOOLS="$CONFIG_DIR/tools"

# kiro-cli is pinned via Renovate against the public install manifest at
# https://desktop-release.q.us-east-1.amazonaws.com/index.json. Bumping
# either literal triggers a reinstall on next container start (see the
# version-drift check below). The in-binary auto-update is disabled so
# what runs always matches the version baked into the image tag and the
# SHA we verified at install time. KIRO_CLI_SHA256 (x86_64) and
# KIRO_CLI_SHA256_ARM64 (aarch64) are the per-arch sha256 of the headless
# zip, BOTH enforced at install; the kiro-cli packageRule in
# cplieger/.github groups all three literals into one Renovate PR.
# renovate: datasource=custom.kiro-cli depName=kiro-cli
KIRO_CLI_VERSION="2.12.3"
KIRO_CLI_SHA256="0855bab3cbed04963ce595d6105209de8c113d81f4e96d5bff160cf7410ebfb2"
# The `# kiro-cli <version>` trailer is Renovate's version anchor for this
# arch's digest lookup — do not hand-edit or drop it.
# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64
KIRO_CLI_SHA256_ARM64="6ebcc9b4b6540adb74866639ba82d70ff494367fee4c1e23fa56f36e5da5de8f" # kiro-cli 2.12.3

# Tool installs (opt/, npm/, python/, go/) are owned by the in-process
# tools engine, which creates its own subtree; bin/ is created here so
# PATH resolution and the kiro-cli promote below never race it.
mkdir -p "$TOOLS/bin" \
  "$HOME/.local/share/kiro-cli" "$HOME/.ssh" \
  "$KIRO_HOME" "$HOME/.cache/go-build" "$HOME/.docker/cli-plugins" \
  || {
    printf "ERROR: failed to create required directories (is /config mounted and writable?)\n"
    sleep 10
    exit 1
  }

# One-time migration from the legacy KIRO_HOME (/config/kiro). The v3
# engine (KAS) ignores KIRO_HOME and reads $HOME/.kiro, so KIRO_HOME now
# points at /config/home/.kiro. Carry over the two files with user value:
# the kiro-cli settings and the custom-instructions steering doc. The
# generated environment.md is rewritten at startup, and /config/kiro
# sessions are dead v2-engine state (v3 sessions were always written to
# $HOME/.kiro/sessions). Guarded on the legacy dir, so this runs once.
if [ -d /config/kiro ] && [ "$KIRO_HOME" != /config/kiro ]; then
  printf "Migrating legacy kiro state /config/kiro -> %s\n" "$KIRO_HOME"
  mkdir -p "$KIRO_HOME/settings" "$KIRO_HOME/steering" "$KIRO_HOME/agents"
  # Legacy copies are strictly newer than anything already at the
  # destination (writes moved to /config/kiro when it became KIRO_HOME).
  [ -f /config/kiro/settings/cli.json ] \
    && cp -f /config/kiro/settings/cli.json "$KIRO_HOME/settings/cli.json"
  [ -f /config/kiro/steering/custom.md ] \
    && cp -f /config/kiro/steering/custom.md "$KIRO_HOME/steering/custom.md"
  # User-defined agent configs, if any; keep existing destination files.
  if [ -d /config/kiro/agents ]; then
    cp -rn /config/kiro/agents/. "$KIRO_HOME/agents/" 2>/dev/null || true
  fi
  # A stale pre-migration environment.md at the destination would be
  # loaded by kiro-cli before vibekit's first regeneration replaces it;
  # remove it so sessions never see outdated instructions.
  rm -f "$KIRO_HOME/steering/environment.md"
  rm -rf /config/kiro
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
    x86_64) arch="x86_64-linux" ;;
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

  # Verify SHA-256 per arch: KIRO_CLI_SHA256 (x86_64) / KIRO_CLI_SHA256_ARM64
  # (aarch64), both from the install manifest and kept in lockstep with
  # KIRO_CLI_VERSION by Renovate (one grouped PR moves all three literals).
  local actual expected
  actual=$(sha256sum "$zip" | awk '{print $1}')
  printf "kiro-cli zip SHA-256: %s (url=%s)\n" "$actual" "$zip_url"
  case "$arch" in
    x86_64-linux) expected="$KIRO_CLI_SHA256" ;;
    aarch64-linux) expected="$KIRO_CLI_SHA256_ARM64" ;;
  esac
  if [ "$actual" != "$expected" ]; then
    printf "ERROR: kiro-cli SHA-256 mismatch (%s)\n" "$arch" >&2
    printf "  expected: %s\n" "$expected" >&2
    printf "  actual:   %s\n" "$actual" >&2
    printf "  refusing install; bump KIRO_CLI_VERSION and both KIRO_CLI_SHA256* literals together\n" >&2
    rm -rf "$tmpdir"
    return 1
  fi
  printf "kiro-cli SHA-256 verified against pinned %s hash\n" "$arch"

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
  "$tmpdir/kirocli/install.sh" --no-confirm </dev/null >"$install_log" 2>&1
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

# Tool installs and updates are owned by the server's in-process tools
# engine (internal/tools): it enqueues a boot sync job right after
# startup that installs anything missing from tools.json and applies
# updates to unpinned tools, streaming progress to the UI over SSE.
# Nothing blocks here — previously installed tools persist on the
# /config volume and are on PATH immediately; a genuinely new LSP
# becomes active in the next chat once its install job finishes.

# Launch the ACP web UI server (foreground)
# Falls back to sleep if kiro-cli isn't available yet
if command -v kiro-cli >/dev/null 2>&1; then
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
    kiro-cli settings "$flag" true >/dev/null 2>&1 || true
  done
  #   chat.disableInheritingDefaultResources — off, matching kiro-cli's
  #     default. Custom agents inherit default steering/skills/AGENTS.md
  #     unless the user turns this on (Settings -> General). Seeded so the
  #     UI toggle reflects reality instead of the unset->on fallback.
  for flag in chat.enableCheckpoint telemetry.enabled toolSearch.enabled \
    chat.disableInheritingDefaultResources; do
    kiro-cli settings "$flag" false >/dev/null 2>&1 || true
  done
  # Disable in-binary auto-update: KIRO_CLI_VERSION above is the
  # source of truth, kept current by Renovate against the public
  # install manifest. Letting kiro-cli silently replace itself
  # would invalidate the pinned SHA, break image-tag reproducibility,
  # and bypass the version-drift reinstall path. Bumps land via
  # Renovate PR → image rebuild → restart picks them up.
  kiro-cli settings "app.disableAutoupdates" true >/dev/null 2>&1 || true
  # Pin kiro-cli's own conversation/session cleanup OFF (0 = never purge).
  # vibekit owns chat retention end to end: it purges its own archived chats
  # and reaps KAS session state on delete + a periodic orphan sweep. Letting
  # kiro-cli also purge (on its own timer, keyed to the same value) would be
  # two systems fighting — and could delete a session out from under a chat
  # vibekit still keeps. Retention lives in Settings → General (vibekit's
  # config.json chat_retention_days), NOT this key.
  kiro-cli settings cleanup.periodDays 0 >/dev/null 2>&1 || true
  exec /app/vibekit
else
  printf "WARNING: kiro-cli not found, web UI not started\n"
  printf "Check the kiro-cli install log above, then restart the container\n"
  # Stay alive so the container doesn't restart-loop
  exec sleep infinity
fi
