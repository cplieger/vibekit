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
KIRO_CLI_VERSION="2.15.0"
KIRO_CLI_SHA256="1b3fe0d70b0fb371d243378f64e0c39c0a26102942a6d291d2d19a4886f06164"
# The `# kiro-cli <version>` trailer is Renovate's version anchor for this
# arch's digest lookup — do not hand-edit or drop it.
# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64
KIRO_CLI_SHA256_ARM64="5b071cb12e2a3eab9f6ee48ea912bf8cab569ed9aa0c15c46abf572b57cdf8b2" # kiro-cli 2.15.0

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
# 0 when the path is a REGULAR, non-symlink, executable file that survives on its
# own. `-f`/`-x` both FOLLOW symlinks, so a link into the install staging tree would
# pass either test, be promoted as a link, and then dangle the moment the staging
# cleanup runs -- after this script logged a successful install.
is_self_contained_executable() {
  [ -f "$1" ] && [ ! -L "$1" ] && [ -x "$1" ] && [ ! -L "$1" ]
}

# Print the version a kiro-cli binary reports, under a hard deadline. Without the
# timeout a wedged binary (one that traps or ignores TERM) hangs the boot forever
# with no diagnostic; --kill-after gives it a second-stage SIGKILL. Callers treat an
# empty answer as a mismatch.
kiro_cli_version() {
  local out rc
  out=$(timeout --signal=TERM --kill-after=5s 10s "$1" --version 2>/dev/null)
  rc=$?
  if [ "$rc" -ne 0 ]; then
    # 124/137 = the 10s deadline (TERM, then the --kill-after SIGKILL fallback).
    if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then
      printf "WARNING: kiro-cli --version exceeded its 10s deadline and was terminated (%s, rc=%d)\n" "$1" "$rc" >&2
    else
      printf "WARNING: kiro-cli --version failed (%s, rc=%d)\n" "$1" "$rc" >&2
    fi
    return "$rc"
  fi
  printf '%s\n' "$out" | awk 'NR==1{print $NF; exit}'
}

# Note the `(` rather than `{`: the body runs in a subshell so the EXIT trap below is
# the single cleanup owner for every temp resource, on every return path.
install_kiro_cli() (
  printf "Installing kiro-cli %s\n" "$KIRO_CLI_VERSION"
  printf "  kiro-cli is proprietary AWS Content; by installing you accept\n"
  printf "  the AWS Customer Agreement. License: https://kiro.dev/license/\n"

  # Direct download of the versioned zip per the docs:
  # https://kiro.dev/docs/cli/installation/ ("With a zip file"). We pin
  # the version (not /latest/) so a given image tag is reproducible and
  # verify the sha256 before running install.sh.
  local arch zip_url tmpdir='' zip install_log='' stage=''
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
  # Private staging HOME for the upstream installer. install.sh writes its
  # dispatchers into $HOME/.local/bin, which is BOTH on the image PATH (see the
  # Dockerfile's ENV PATH) and on the persistent /config volume -- so installing
  # with the real HOME exposed an UNVERIFIED candidate to bare-name resolution
  # (`docker exec ... kiro-cli`) for the whole download-and-validate window, and left
  # it reachable for the container lifetime whenever a later step failed. Staging
  # under $TOOLS keeps the candidate off PATH entirely and on the same filesystem as
  # $TOOLS/bin, so promotion stays a rename rather than a copy.
  stage=$(mktemp -d "$TOOLS/.kiro-cli-stage.XXXXXX") || {
    rm -rf "$tmpdir"
    return 1
  }
  trap 'rm -rf "$tmpdir" "$stage"; [ -z "$install_log" ] || rm -f "$install_log"' EXIT
  zip="$tmpdir/kirocli.zip"

  # The zip is ~528 MB, so a flat --max-time would be a BANDWIDTH FLOOR rather than a
  # hang guard. Stall detection expresses the real condition: abort only when
  # throughput stays under --speed-limit for --speed-time consecutive seconds, with
  # --max-time as an absolute per-attempt backstop and --retry-max-time capping the
  # retry envelope. Before this the download carried NO timeouts at all, so a hung
  # connection blocked boot indefinitely with nothing in the log. --proto-redir pins
  # redirects to https too; --proto alone only constrains the initial request.
  if ! curl --proto '=https' --proto-redir '=https' --tlsv1.2 -fsSL \
    --connect-timeout 20 --speed-limit 4096 --speed-time 60 \
    --max-time 3600 --retry 3 --retry-delay 5 --retry-max-time 5400 \
    "$zip_url" -o "$zip"; then
    printf "ERROR: failed to download kiro-cli zip from %s\n" "$zip_url" >&2
    return 1
  fi
  if [ ! -s "$zip" ]; then
    printf "ERROR: kiro-cli zip is empty (partial download?)\n" >&2
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
    return 1
  fi
  printf "kiro-cli SHA-256 verified against pinned %s hash\n" "$arch"

  if ! unzip -q "$zip" -d "$tmpdir"; then
    printf "ERROR: failed to extract kiro-cli zip\n" >&2
    return 1
  fi

  # Run upstream install.sh against the PRIVATE staging HOME. Don't gate on its
  # exit code — the kiro-cli installer touches shell profiles and other side
  # surfaces that legitimately fail in our minimal root container; what matters is
  # whether the binary it drops at $stage/.local/bin/kiro-cli reports the version we
  # pinned. Capture install.sh output to a tempfile so we can surface it on failure.
  local install_rc staged="$stage/.local/bin/kiro-cli"
  install_log=$(mktemp) || return 1
  HOME="$stage" "$tmpdir/kirocli/install.sh" --no-confirm </dev/null >"$install_log" 2>&1
  install_rc=$?

  if ! is_self_contained_executable "$staged"; then
    printf "ERROR: install.sh did not produce a self-contained executable kiro-cli binary at %s (absent, not executable, or a symlink whose target dies with the staging cleanup) (rc=%d)\n" \
      "$staged" "$install_rc" >&2
    printf "install.sh output:\n" >&2
    cat "$install_log" >&2
    return 1
  fi
  local installed
  installed=$(kiro_cli_version "$staged")
  if [ "$installed" != "$KIRO_CLI_VERSION" ]; then
    printf "ERROR: installed binary reports version %s, wanted %s (install.sh rc=%d)\n" \
      "${installed:-unknown}" "$KIRO_CLI_VERSION" "$install_rc" >&2
    printf "install.sh output:\n" >&2
    cat "$install_log" >&2
    return 1
  fi

  # Enforce the pin BEFORE promotion, through the STAGED binary but against the real
  # persisted HOME (no HOME override here). app.disableAutoupdates is the one setting
  # the integrity story depends on: with auto-update live the binary can replace
  # itself, invalidating the sha verified above and the image-tag reproducibility the
  # pin exists to provide. It was previously seeded after promotion, best-effort, so a
  # failure left a self-replacing binary in place with only a warning. Refuse to
  # promote a candidate whose self-replacement could not be turned off.
  if ! timeout --signal=TERM --kill-after=5s 10s "$staged" settings app.disableAutoupdates true >/dev/null 2>&1; then
    printf "ERROR: failed to disable kiro-cli auto-update; refusing to promote a binary that may replace itself and invalidate the pinned digest (%s)\n" "$staged" >&2
    return 1
  fi

  # Promote to the canonical $TOOLS/bin/ location so PATH ordering (which puts
  # /config/tools/bin first) and any in-process absolute-path references resolve to
  # the freshly installed binary. $TOOLS/bin/kiro-cli is the COMMIT POINT, so it goes
  # last: a failure part-way leaves the previous install intact and the next boot's
  # drift check retries, rather than half-swapping the dispatcher set.
  #
  # The sidecars are what `kiro-cli chat` dispatches to. vibekit launches
  # `kiro-cli acp`, which the main binary serves directly, so a missing sidecar does
  # NOT break vibekit -- hence warn rather than fail. But it must not be SILENT
  # either: the previous `2>/dev/null || true` discarded both the diagnostic and the
  # status, so an upstream rename of the dispatcher set would have gone unnoticed.
  local sidecar src
  for sidecar in kiro-cli-chat kiro-cli-term; do
    src="$stage/.local/bin/$sidecar"
    if [ ! -e "$src" ]; then
      printf "WARNING: install.sh produced no %s sidecar dispatcher (upstream dispatcher set changed?)\n" "$sidecar" >&2
    elif ! is_self_contained_executable "$src"; then
      printf "WARNING: install.sh produced an invalid %s sidecar dispatcher; skipping promotion\n" "$sidecar" >&2
    elif ! mv -f "$src" "$TOOLS/bin/$sidecar"; then
      printf "WARNING: failed to promote the %s sidecar dispatcher to %s\n" "$sidecar" "$TOOLS/bin/$sidecar" >&2
    fi
  done
  if ! mv -f "$staged" "$TOOLS/bin/kiro-cli"; then
    printf "ERROR: failed to promote kiro-cli binary to %s\n" "$TOOLS/bin/kiro-cli" >&2
    return 1
  fi
  printf "kiro-cli %s installed and promoted to %s\n" "$KIRO_CLI_VERSION" "$TOOLS/bin/kiro-cli"
)

# Reclaim superseded kiro-cli agent-server runtimes. Each version unpacks its own
# ~240 MB tree under <data-dir>/kas/<version>-<hash>/ (plus a sibling .lock) on
# its FIRST bridge launch -- after this entrypoint has exec'd the server -- and
# nothing ever removes the old ones, so the store gains a full tree per Renovate
# bump and never shrinks (six trees / 1.4 GB found on a borgcube volume, 2026-07).
# install_kiro_cli above cleans only what IT writes, which is why this was missed:
# the tree is written later, by the binary it promoted. Distinct from the KAS
# SESSION state vibekit already reaps (per-chat records, see the cleanup.periodDays
# note below) -- same acronym, different object, and the session reaper never
# touches these trees. The toolbelt engine applies exactly this
# keep-current-drop-the-rest rule to its own versioned opt/<tool>/<version>/ trees
# (pruneOldVersions in install.go), so this extends it to the one install outside
# the engine's custody: kiro-cli, unmanageable by the engine because licensing
# forbids baking it into the image.
#
# Data-dir resolution mirrors kiro-cli's own (XDG_DATA_HOME, else
# $HOME/.local/share, as internal/kiroauth documents): pruning a directory the CLI
# does not use would be a silent no-op, the one failure mode a hygiene step must
# not have. Warn, never fail the boot -- degraded-not-dead, the same posture as the
# install itself.
prune_superseded_kas_runtimes() {
  local data_home kas_dir kas_real entry name
  data_home="${XDG_DATA_HOME:-}"
  if [ -z "$data_home" ]; then
    [ -n "${HOME:-}" ] || return 0
    data_home="$HOME/.local/share"
  fi
  kas_dir="$data_home/kiro-cli/kas"
  [ -d "$kas_dir" ] || return 0
  # `-d` FOLLOWS symlinks and the rm below runs as root, so a `kiro-cli` or `kas`
  # symlink planted on a volume that once permitted foreign writes would redirect
  # this sweep at an arbitrary tree and delete every entry that does not match the
  # pin. Prove the store is a real directory resolving where it is named, or skip
  # the prune (warn, never fail: disk hygiene must not brick boot).
  if [ -L "$data_home/kiro-cli" ] || [ -L "$kas_dir" ]; then
    printf "WARNING: kiro-cli data dir or its kas store is a symlink; refusing to prune through it (%s)\n" "$kas_dir" >&2
    return 0
  fi
  kas_real=$(realpath "$kas_dir" 2>/dev/null) || kas_real=""
  case "$kas_real" in
    "$data_home"/kiro-cli/kas) ;;
    *)
      printf "WARNING: kiro-cli kas store does not resolve inside the data dir; refusing to prune (%s -> %s)\n" \
        "$kas_dir" "${kas_real:-unknown}" >&2
      return 0
      ;;
  esac
  for entry in "$kas_dir"/*; do
    # An empty store leaves the glob unexpanded.
    [ -e "$entry" ] || continue
    name="${entry##*/}"
    # One pattern covers the tree and its sibling .lock (both carry the
    # <version>-<hash> stem); quoting keeps a version string from being read as a
    # glob. Anything not on the pin is a superseded version.
    case "$name" in
      "$KIRO_CLI_VERSION"-*) continue ;;
    esac
    # Only VERSION-KEYED entries are superseded runtimes. kas/ is kiro-cli's
    # directory, not ours, so an entry with no leading numeric version component is
    # something this pruner has never seen (a store-wide lock, an unpack scratch
    # dir, an index) -- and deleting another program's unrecognized state on every
    # boot is a worse failure than leaving a few MB behind. Both layouts observed
    # today (the <version>-<hash> tree and its .lock sibling) match, so this is a
    # no-op against the current CLI; log the skip so a layout change is visible
    # instead of silent.
    if [[ ! "$name" =~ ^[0-9]+\.[0-9]+\.[0-9]+- ]]; then
      printf "Leaving unrecognized (non version-keyed) entry in the kiro-cli agent runtime store: %s\n" "$name"
      continue
    fi
    if rm -rf "$entry"; then
      printf "Pruned superseded kiro-cli agent runtime: %s (pin %s)\n" "$name" "$KIRO_CLI_VERSION"
    else
      printf "WARNING: failed to prune superseded kiro-cli agent runtime %s\n" "$name" >&2
    fi
  done
  return 0
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
  # Bounded probe: an unbounded `--version` here hangs the boot on a wedged binary,
  # and this call runs on EVERY boot, not just install boots.
  current=$(kiro_cli_version "$TOOLS/bin/kiro-cli")
  if [ "$current" != "$KIRO_CLI_VERSION" ]; then
    printf "kiro-cli version drift: installed=%s pinned=%s; reinstalling\n" \
      "${current:-unknown}" "$KIRO_CLI_VERSION"
    return 0
  fi
  return 1
}

# Runs on EVERY boot and deliberately BEFORE the install: on a bump boot the pin
# already names the NEW version while only the OLD tree is on disk, so the old tree
# goes first and peak usage stays at one tree instead of two. A boot that installs
# nothing prunes nothing.
prune_superseded_kas_runtimes

if needs_kiro_cli_install; then
  if ! install_kiro_cli; then
    printf "WARNING: kiro-cli install failed; the web UI starts anyway but chats cannot run until kiro-cli is present (degraded start)\n"
    printf "Check the install log above, then restart the container to retry the install\n"
  fi
fi

# One-time cleanup of the residue the staging change leaves behind. Installs used to
# run with the real HOME, so EARLIER images dropped their dispatchers in
# $HOME/.local/bin -- which is on the image PATH and on the persistent /config volume.
# The installer no longer writes there, but nothing removes what is already on an
# inherited volume, so a bare-name `kiro-cli` could keep resolving to an old, unpinned
# copy for the container's lifetime. $TOOLS/bin leads PATH, so this is hygiene rather
# than an active shadow whenever the canonical binary is present: warn, never fail.
# `rm -rf` on an unmatched glob is already a silent no-op returning 0, so a non-zero
# status here is a real failure (an immutable attribute, EPERM) worth surfacing.
if ! rm -rf "$HOME/.local/bin"/kiro-cli*; then
  printf "WARNING: failed to remove legacy kiro-cli residue from %s/.local/bin; an unpinned copy may stay resolvable by bare name\n" "$HOME" >&2
fi
# Same argument for the installer's own scratch: an EXIT trap cleans the stage on
# every ordinary path, so anything left here is SIGKILL residue occupying /config.
if ! rm -rf "$TOOLS"/.kiro-cli-stage.*; then
  printf "WARNING: failed to remove orphaned kiro-cli staging directories from %s\n" "$TOOLS" >&2
fi

# Tool installs and updates are owned by the server's in-process tools
# engine (internal/tools): it enqueues a boot sync job right after
# startup that installs anything missing from tools.json and applies
# updates to unpinned tools, streaming progress to the UI over SSE.
# Nothing blocks here — previously installed tools persist on the
# /config volume and are on PATH immediately; a genuinely new LSP
# becomes active in the next chat once its install job finishes.

# Apply one kiro-cli settings call under a hard deadline. Every one of these spawns
# the CLI, so an unbounded call hangs the boot forever on a binary that traps or
# ignores TERM -- and the very first call in the block below would do it, with no
# diagnostic. Warn and carry on: these are seeded preferences, not correctness gates
# (the one that IS load-bearing, app.disableAutoupdates, is additionally asserted
# before promotion in install_kiro_cli, which refuses to promote without it).
kiro_setting() {
  local rc
  timeout --signal=TERM --kill-after=5s 10s kiro-cli settings "$1" "$2" >/dev/null 2>&1
  rc=$?
  if [ "$rc" -ne 0 ]; then
    # 124/137 = the 10s deadline (TERM, then the --kill-after SIGKILL fallback).
    printf "WARNING: failed to seed kiro-cli setting %s=%s (rc=%d)\n" "$1" "$2" "$rc" >&2
  fi
  return "$rc"
}

# Seed kiro-cli settings when the binary is present, then launch the
# web UI server (foreground) UNCONDITIONALLY — degraded-not-dead, the
# same posture as web-terminal-kiro. A first-boot install failure used
# to dead-end in `exec sleep infinity`: a live container with no
# server, a forever-failing HEALTHCHECK, and no diagnostics page. Now
# the server always comes up; /api/health reports "kiro-cli
# unavailable" (503) and the UI shows a degraded banner until a
# container restart retries the install.
if command -v kiro-cli >/dev/null 2>&1; then
  # Enable the experimental kiro-cli features we surface in the UI.
  # Defaults: on for features vibekit renders natively, off for the
  # duplicate-of-vibekit and telemetry toggles:
  #
  #   chat.enableCheckpoint       — off. vibekit has its own
  #     content-addressed checkpoint store (internal/checkpoint/ —
  #     JSONL event log + blob store, no git) wired into the Restore
  #     buttons, diff endpoint, and per-file Undo. Leaving kiro-cli's
  #     parallel system on doubled the checkpoint disk cost with no
  #     user-visible benefit.
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
  # Seed failures are logged (not fatal): a silently failed write leaves
  # the Settings UI toggle showing a state the CLI doesn't have until
  # the next boot, which is exactly the drift the seeding exists to
  # prevent.
  for flag in chat.enableTodoList chat.enableKnowledge \
    chat.enableSubagent chat.enablePromptHints \
    hooks.showStatus; do
    kiro_setting "$flag" true || true
  done
  #   chat.disableInheritingDefaultResources — off, matching kiro-cli's
  #     default. Custom agents inherit default steering/skills/AGENTS.md
  #     unless the user turns this on (Settings -> General). Seeded so the
  #     UI toggle reflects reality instead of the unset->on fallback.
  for flag in chat.enableCheckpoint telemetry.enabled toolSearch.enabled \
    chat.disableInheritingDefaultResources; do
    kiro_setting "$flag" false || true
  done
  # Disable in-binary auto-update: KIRO_CLI_VERSION above is the
  # source of truth, kept current by Renovate against the public
  # install manifest. Letting kiro-cli silently replace itself
  # would invalidate the pinned SHA, break image-tag reproducibility,
  # and bypass the version-drift reinstall path. Bumps land via
  # Renovate PR → image rebuild → restart picks them up.
  kiro_setting app.disableAutoupdates true || true
  # Pin kiro-cli's own conversation/session cleanup OFF (0 = never purge).
  # vibekit owns chat retention end to end: it purges its own archived chats
  # and reaps KAS session state on delete + a periodic orphan sweep. Letting
  # kiro-cli also purge (on its own timer, keyed to the same value) would be
  # two systems fighting — and could delete a session out from under a chat
  # vibekit still keeps. Retention lives in Settings → General (vibekit's
  # config.json chat_retention_days), NOT this key.
  kiro_setting cleanup.periodDays 0 || true
else
  printf "WARNING: kiro-cli not found — starting the web UI in degraded mode\n"
  printf "Chats cannot run until kiro-cli installs; check the install log above, then restart the container\n"
  # Settings seeding is skipped this boot; the restart that recovers
  # kiro-cli re-runs this block, so no drift persists.
fi
exec /app/vibekit
