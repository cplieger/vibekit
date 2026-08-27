#!/bin/bash

CONFIG_DIR="/config"
TOOLS="$CONFIG_DIR/tools"

# kiro-cli is pinned via Renovate against the public install manifest at
# https://desktop-release.q.us-east-1.amazonaws.com/index.json. These three
# literals are the ONLY kiro-cli knowledge left in this script: the server
# installs from them (the cplieger/pinstall library, wired in
# internal/composition/kirocli.go), so bumping one and rebuilding the image
# makes the next boot download, verify and activate that version. The in-binary
# auto-update is disabled so what runs always matches the version baked into the
# image tag and the digest verified at install time. KIRO_CLI_SHA256 (x86_64) and
# KIRO_CLI_SHA256_ARM64 (aarch64) are the per-arch sha256 of the headless zip,
# BOTH enforced at install; the kiro-cli packageRule in cplieger/.github groups
# all three literals into one Renovate PR.
#
# Keep them as bare double-quoted shell literals with their `# renovate:` anchor
# comments intact: the shared custom datasource matches on exactly this shape,
# and tests/shell/pins_export_test.sh asserts it along with the export below.
# renovate: datasource=custom.kiro-cli depName=kiro-cli
KIRO_CLI_VERSION="2.20.1"
KIRO_CLI_SHA256="40c49223ce9844746f9cebadbc17dfd2491fa9e46fc19ccd527be70d44798371"
# The `# kiro-cli <version>` trailer is Renovate's version anchor for this
# arch's digest lookup — do not hand-edit or drop it.
# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64
KIRO_CLI_SHA256_ARM64="742f247943b469f980f64a42c71a068bda100446234f52046e1818b33f851a3e" # kiro-cli 2.20.1

# Export the pins to the server, which owns the install. Without this the server
# sees no pins, resolves kiro-cli by bare name and turns its readiness gate OFF —
# a container that reports healthy while installing nothing. That failure is
# silent, so tests/shell/pins_export_test.sh reads both sides of this boundary.
export KIRO_CLI_VERSION KIRO_CLI_SHA256 KIRO_CLI_SHA256_ARM64

# Tool installs (opt/, npm/, python/, go/) are owned by the in-process
# tools engine, which creates its own subtree; bin/ is created here so
# PATH resolution never races it. $TOOLS/kiro-cli-versions is the version-addressed
# kiro-cli install root the server writes into — created here so the first boot
# does not have to, and so this script's own /config failure branch covers it.
# It is a SIBLING of the engine's opt/, never a child: the engine's per-tool prune
# deletes every version directory under opt/<tool> that is not the one it just
# installed, so a manifest entry named `kiro-cli` (any name is accepted, and
# tools.json is hand-editable) used to be able to delete the active kiro-cli and its
# retained predecessor.
mkdir -p "$TOOLS/bin" "$TOOLS/kiro-cli-versions" \
  "$HOME/.local/share/kiro-cli" "$HOME/.ssh" \
  "$KIRO_HOME" "$HOME/.cache/go-build" "$HOME/.docker/cli-plugins" \
  || {
    printf "ERROR: failed to create required directories (is /config mounted and writable?)\n"
    sleep 10
    exit 1
  }

# Normalize the modes of everything just created, EVERY boot — never trust
# mkdir's result. The volume is host-owned storage: an inheritable ACL on the
# mount (TrueNAS/SMB-managed ZFS datasets set these) or a nonstandard umask
# stamps whatever modes it likes on new directories, mkdir -p silently keeps
# whatever an existing directory already has, and two consumers refuse bad
# modes outright — the tools engine will not execute from a group- or
# other-writable root, and sshd/ssh refuses a lax ~/.ssh. Idempotent chmod is
# cheap; a wrong mode here costs a disabled subsystem.
chmod 755 "$HOME" || true
chmod go-w "$TOOLS" "$TOOLS/bin" "$TOOLS/kiro-cli-versions" \
  "$HOME/.local" "$HOME/.local/share" "$HOME/.local/share/kiro-cli" \
  "$KIRO_HOME" "$HOME/.cache" "$HOME/.cache/go-build" \
  "$HOME/.docker" "$HOME/.docker/cli-plugins" || true
chmod 700 "$HOME/.ssh" || true

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

# Reclaim superseded kiro-cli agent-server runtimes. Each version unpacks its own
# ~240 MB tree under <data-dir>/kas/<version>-<hash>/ (plus a sibling .lock) on
# its FIRST bridge launch -- after this entrypoint has exec'd the server -- and
# nothing ever removes the old ones, so the store gains a full tree per Renovate
# bump and never shrinks (six trees / 1.4 GB found on a borgcube volume, 2026-07).
# The installer only ever cleaned what IT writes, which is why this was missed:
# the tree is written later, by the binary it installed. Distinct from the KAS
# SESSION state vibekit already reaps (per-chat records, governed by the
# cleanup.periodDays=0 the server now seeds) -- same acronym, different object,
# and the session reaper never touches these trees. The toolbelt engine applies
# exactly this keep-current-drop-the-rest rule to its own versioned
# opt/<tool>/<version>/ trees
# (pruneOldVersions in install.go), so this extends it to the one install outside
# the engine's custody: kiro-cli, unmanageable by the engine because licensing
# forbids baking it into the image.
#
# Data-dir resolution mirrors kiro-cli's own (XDG_DATA_HOME, else
# $HOME/.local/share, as internal/kiroauth documents): pruning a directory the CLI
# does not use would be a silent no-op, the one failure mode a hygiene step must
# not have. Warn, never fail the boot -- degraded-not-dead, the same posture as the
# install itself.
#
# This one function stayed in the entrypoint when the installer moved into the
# server: it prunes kiro-cli's DATA dir, not the install, and it has to run BEFORE
# the server can start unpacking a new runtime tree.
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

# Runs on EVERY boot and deliberately BEFORE the server starts: on a bump boot the
# pin already names the NEW version while only the OLD tree is on disk, so the old
# tree goes first and peak usage stays at one tree instead of two. The server
# unpacks the new tree later, on the first bridge launch.
prune_superseded_kas_runtimes

# One-time cleanup of residue EARLIER images left in the real HOME. Installs used to
# run with the real HOME, so those images dropped their dispatchers in
# $HOME/.local/bin -- which is on the image PATH and on the persistent /config volume.
# Nothing removes what is already on an inherited volume, so a bare-name `kiro-cli`
# could keep resolving to an old, unpinned copy for the container's lifetime. It stays
# here rather than moving into the server with the rest of the install because it is
# outside $TOOLS, the only tree the server's manager owns, and HOME is already
# resolved here: warn, never fail.
# `rm -rf` on an unmatched glob is already a silent no-op returning 0, so a non-zero
# status here is a real failure (an immutable attribute, EPERM) worth surfacing.
if ! rm -rf "$HOME/.local/bin"/kiro-cli*; then
  printf "WARNING: failed to remove legacy kiro-cli residue from %s/.local/bin; an unpinned copy may stay resolvable by bare name\n" "$HOME" >&2
fi

# Make a value this script did not author safe inside a quoted logfmt field. The
# untrusted classes reaching these log lines share one implementation so the rules
# cannot drift: names read off the /config bind mount, which this script's threat
# model treats as writable by a foreign host user -- the same premise
# secure_tools_dir and the taint flag exist for -- and dpkg's own audit output.
# So the actor a warning describes chooses the bytes inside the field that
# reports him: a file named
# `x" level=info msg="tools tree clean` would otherwise close the field early and append
# attacker-authored logfmt keys, losing the rest of the real message.
#
# Bound the RAW length first (one bad value must not dominate the line, and truncating
# after the backslash doubling could split a `\\` pair and leave a trailing lone
# backslash that escapes the closing quote), double logfmt's escape character, replace
# non-printables, then neutralize the quote that would close the field. $2 is the INPUT
# bound, defaulting to 200 (at most 2x that emitted).
logfmt_value() {
  local raw=$1 safe
  safe=${raw:0:${2:-200}}
  safe=${safe//\\/\\\\}
  safe=${safe//[![:print:]]/?}
  safe=${safe//\"/\'}
  printf '%s' "$safe"
}

# Repair an interrupted dpkg transaction, unconditionally.
#
# This used to sit INSIDE the APT_PACKAGES block, which is why it moved rather
# than being deleted with it: an interrupted install leaves dpkg wedged for the
# whole container's life, and every later apt operation fails until somebody
# reconfigures. Those operations are now the tools engine's (an `apt:` manifest
# entry), so the repair can no longer be gated on an env var the engine does
# not read -- and it has to run BEFORE the listener binds, because the engine
# cannot repair a system database it does not own.
#
# The AUDIT OUTPUT is the primary evidence, not the exit status: `dpkg --audit`
# returns 0 while REPORTING unpacked-but-unconfigured packages (measured: 464
# bytes on stdout, rc=0), which is the ordinary interrupted state this exists
# for -- gating on rc alone would never fire on it. A healthy tree prints
# nothing at all, so non-empty output cannot false-positive. The updates journal
# stays a third trigger: it is evidence of a transaction killed even earlier,
# before any package reached the unpacked state.
#
# Warn-only, per the failure posture: the state is either absent (a no-op) or
# the only thing standing between the operator and a working apt.
dpkg_audit_rc=0
dpkg_audit_out=$(timeout --signal=TERM --kill-after=30s 300s dpkg --audit 2>/dev/null) || dpkg_audit_rc=$?
# Bounded for the log line: audit output is short in practice, and a truncated
# first line is enough to tell an operator WHICH interrupted state was seen.
dpkg_audit_summary=$(printf '%s' "${dpkg_audit_out:0:400}" | tr '\n' ' ')
if [ "$dpkg_audit_rc" -ne 0 ] || [ -n "$dpkg_audit_out" ] \
  || [ -n "$(ls -A /var/lib/dpkg/updates 2>/dev/null)" ]; then
  printf 'level=warn msg="dpkg is in an interrupted state (an install was killed mid-transaction); reconfiguring" audit_rc=%d audit="%s" component=entrypoint\n' \
    "$dpkg_audit_rc" "$(logfmt_value "$dpkg_audit_summary" 400)" >&2
  dpkg_fix_rc=0
  timeout --signal=TERM --kill-after=30s 300s dpkg --configure -a || dpkg_fix_rc=$?
  if [ "$dpkg_fix_rc" -ne 0 ]; then
    printf 'level=warn msg="dpkg --configure -a failed; apt installs will keep failing until the container is recreated" rc=%d component=entrypoint\n' "$dpkg_fix_rc" >&2
  fi
fi

# Everything else kiro-cli is the server's: it downloads the pinned archive,
# verifies its per-arch sha256, installs into $TOOLS/kiro-cli-versions/<version>/,
# selects the active version, reasserts the settings the pin depends on, prunes
# superseded versions, and purges the layout this script's installer used to
# promote into $TOOLS/bin. The listener binds first, so a first-boot download
# shows up as /api/health reporting unready with a reason -- not as a container
# with no server. Tool installs and updates are likewise the server's, via the
# in-process tools engine; nothing blocks here.
exec /app/vibekit
