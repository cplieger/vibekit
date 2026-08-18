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
KIRO_CLI_VERSION="2.18.1"
KIRO_CLI_SHA256="d8d9837ce549e97a966d8e8b1a03610d9b11592677eb22ed45b2df61de9a0dd6"
# The `# kiro-cli <version>` trailer is Renovate's version anchor for this
# arch's digest lookup — do not hand-edit or drop it.
# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64
KIRO_CLI_SHA256_ARM64="dc6b3304fed9cc368d5138c8474a6d41ee4fe3d7a4132d210c62d50396dd630e" # kiro-cli 2.18.1

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

# Make a value this script did not author safe inside a quoted logfmt field. Two
# untrusted classes reach these log lines and they share one implementation so the rules
# cannot drift: APT_PACKAGES tokens (env content) and names read off the /config bind
# mount, which this script's threat model treats as writable by a foreign host user --
# the same premise secure_tools_dir and the taint flag exist for. So the actor a warning
# describes chooses the bytes inside the field that reports him: a file named
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

# Warn about a rejected APT_PACKAGES token. The token is untrusted env content, so it
# goes through the shared sanitizer above with a tighter 64 INPUT char bound (at most 128
# emitted). Shared by both rejection paths (grammar and known-name) so the sanitizing
# rules cannot drift between them.
warn_skipped_apt_token() {
  local msg=$1 raw=$2 safe
  safe=$(logfmt_value "$raw" 64)
  printf 'level=warn msg="%s" token="%s" component=entrypoint\n' "$msg" "$safe" >&2
}

# OS packages (APT_PACKAGES env, e.g. "gcc libc6-dev"). apt state lives in the
# ephemeral container layer — never on /config — so it is re-applied on every
# container start: compose-level intent, not volume intent. Everything under
# /config/tools is the tools engine's instead (manifest: /config/tools/tools.json
# v2), converged in the background after the listener binds.
#
# This is the operator's answer to the one class that engine cannot own. It has no
# apt backend BY DESIGN: its installs live on the /config volume and are recorded
# in tools-state.json, while an apt package dies with the container layer, so a
# library behind that state file would read as installed after every recreate and
# be gone. A shared library also ships no binary, and the engine's install
# detection is "execute the recorded bin", so such an entry could never be
# verified. Two live needs it serves here: a runtime the engine installs may link
# an OS library the image does not carry (node's own linux-x64 build started
# linking libatomic.so.1 at v25 — that one is baked into the image, but the next
# would need a release without this hatch), and the agent's own Go work needs a C
# compiler, because `go test -race` build-fails without one while plain `go test`
# does not.
#
# This block is a VERBATIM copy of sister app web-terminal-kiro's, helpers
# included, and must stay one: every guard in it was paid for by a measured
# failure there, and a paraphrase makes the next diff between the two files
# unreadable. Change it in both, or extract it into cplieger/ci for both.
#
# Validation is two-stage, and the stages answer different questions:
# the grammar rejects tokens that are not shaped like a package name (so env
# content cannot smuggle apt options, a `pkg=version` pin, `pkg:arch`, or the
# `pkg-` REMOVE form), while the known-name gate rejects tokens that are not
# ACTUALLY packages. `apt-get update` is REQUIRED before either install or the
# gate because the image deletes the package indexes at build time.
# Warn-not-fail preserves the degraded-boot posture throughout.
if [ -n "${APT_PACKAGES:-}" ]; then
  apt_pkgs=()
  # Word-splitting of $APT_PACKAGES is intentional; glob expansion is not
  # (cwd is /workspace, so a stray "*" token would expand to repo filenames
  # and any name matching package grammar would be apt-installed). set -f
  # keeps such a token literal so the validator below warn-skips it.
  set -f
  for pkg in $APT_PACKAGES; do
    # Also reject a trailing '-': apt-get treats 'pkg-' as a REMOVE request
    # (and a nonexistent 'name.-' as a regex remove), so a grammar-valid
    # token ending in '-' smuggles a removal through this install-only
    # path. No Debian package name ends in '-' (trailing '+' stays: g++).
    if [[ "$pkg" =~ ^[a-z0-9][a-z0-9+.-]*$ && "$pkg" != *- ]]; then
      apt_pkgs+=("$pkg")
    else
      warn_skipped_apt_token 'skipping invalid APT_PACKAGES token' "$pkg"
    fi
  done
  set +f
  if [ "${#apt_pkgs[@]}" -gt 0 ]; then
    # Refresh the indexes on their OWN deadline, before the known-name gate that
    # reads them. Splitting update from install also makes an exhausted deadline
    # attributable: previously one 600s budget covered both, so a timeout could
    # not say which half consumed it. 300s rather than a tight bound for the same
    # reason the kiro-cli download uses stall detection: a deadline sized for a
    # fast link is a bandwidth floor in disguise.
    apt_update_rc=0
    printf 'level=info msg="refreshing apt indexes for APT_PACKAGES" component=entrypoint\n' >&2
    timeout --signal=TERM --kill-after=30s 300s apt-get update -qq || apt_update_rc=$?
    if [ "$apt_update_rc" -eq 124 ] || [ "$apt_update_rc" -eq 137 ]; then
      # 124/137 = the 300s deadline (TERM, then the --kill-after SIGKILL fallback),
      # named distinctly for the same reason every sibling timeout here does: a
      # stalled mirror and an index apt rejected outright call for different
      # operator action, and the generic wording cannot tell them apart.
      printf 'level=warn msg="apt-get update exceeded its 300s deadline and was terminated; APT_PACKAGES install may fail and the known-name check runs against whatever index survived" rc=%d component=entrypoint\n' "$apt_update_rc" >&2
    elif [ "$apt_update_rc" -ne 0 ]; then
      printf 'level=warn msg="apt-get update failed; APT_PACKAGES install may fail and the known-name check runs against whatever index survived" rc=%d component=entrypoint\n' "$apt_update_rc" >&2
    fi

    # Known-name gate. The grammar above is only a PROXY for "is this a real
    # Debian package name", and apt-get has a third interpretation layer the proxy
    # cannot express: a token containing '.', '?' or '*' that matches no literal
    # package is retried as an UNANCHORED REGEX over every package name. Measured
    # on apt 3.0.3 (trixie's major): `apt-get install -s -- 'jq.'` plans 337
    # packages. So one grammar-valid operator typo (python3., libssl.) turns this
    # install-only path into an unbounded root install into the container layer,
    # re-run on every start. '.' cannot leave the grammar (python3.13, docker.io
    # are real names) and apt-get has no flag to disable the fallback, so the fix
    # is to stop handing apt anything that is not already known to be a literal
    # package name: a token that survives this gate cannot reach the regex path.
    #
    # apt-cache pkgnames is the only safe oracle here. `apt-cache show`, `policy`
    # and `showpkg` ALL regex-expand (verified: `showpkg -- 'jq.'` reports
    # libjs-jquery.sparkline) and return rc=0 for names that do not exist, so none
    # of them can answer "does this literal name exist".
    #
    # Known limit: pkgnames omits PURE VIRTUAL names (awk, provided by mawk/gawk
    # but never a real package), so such a token is skipped here. Acceptable, and
    # the warning says so: `apt-get install awk` fails on its own anyway, because a
    # multi-provider virtual has no installation candidate. Naming a concrete
    # provider is the fix in both cases, and the warning is clearer than apt's.
    #
    # The gate is ATTEMPTED even when apt-get update failed, and that ordering is
    # the safety property, not an optimization. The reachable failure is a PARTIAL
    # refresh (some mirrors fine, non-zero exit, index still usable), and every
    # name such an index yields still comes from real repository metadata -- so an
    # incomplete index can only produce false NEGATIVES (a valid package it does
    # not list is skipped and installs on a later boot), never a false positive
    # that admits a token to apt's regex path. The character fallback below is
    # therefore the LAST resort, reached only when this oracle is unusable.
    apt_gate_ran=0
    apt_names=$(mktemp) || apt_names=''
    # Bounded like every other external call in this foreground path: this runs
    # before any listener exists, so an index apt cannot read through (corrupt or
    # partially-written cache, very slow storage) would otherwise stall boot with
    # no deadline and no diagnostic -- the container would sit in starting/
    # unhealthy forever, and restart:unless-stopped never acts because nothing
    # exited. A killed probe leaves apt_gate_ran=0, which the narrowing below
    # already handles exactly as it handles an unreadable index.
    apt_names_rc=0
    if [ -n "$apt_names" ]; then
      timeout --signal=TERM --kill-after=10s 60s apt-cache pkgnames >"$apt_names" 2>/dev/null || apt_names_rc=$?
    else
      apt_names_rc=1
    fi
    if [ "$apt_names_rc" -eq 124 ] || [ "$apt_names_rc" -eq 137 ]; then
      # 124/137 = the 60s deadline (TERM, then the --kill-after SIGKILL fallback),
      # named distinctly from the generic unreadable-index warning for the same
      # reason the sibling timeouts are: a wedged cache and an index apt rejected
      # outright call for different operator action.
      printf 'level=warn msg="apt-cache pkgnames exceeded its 60s deadline and was terminated; falling back to the expansion-character filter for APT_PACKAGES" rc=%d component=entrypoint\n' "$apt_names_rc" >&2
    fi
    # Usable means exactly this: the command succeeded AND produced a non-empty
    # name list. Anything else (non-zero exit, deadline kill, empty output, no
    # temp file to capture into) is unusable and falls through to the filter.
    if [ "$apt_names_rc" -eq 0 ] && [ -s "$apt_names" ]; then
      apt_gate_ran=1
      known_pkgs=()
      for pkg in "${apt_pkgs[@]}"; do
        if grep -qxF -- "$pkg" "$apt_names"; then
          known_pkgs+=("$pkg")
        else
          warn_skipped_apt_token 'skipping unknown APT_PACKAGES token (no such package; a pure virtual package needs a concrete provider)' "$pkg"
        fi
      done
      apt_pkgs=("${known_pkgs[@]}")
    elif [ -z "$apt_names" ]; then
      # The index may be perfectly readable: what failed is creating the file to
      # capture the name list into. Named separately for the same reason the
      # deadline warnings are -- an unwritable container temp dir and an index apt
      # cannot read call for different operator action.
      printf 'level=warn msg="could not create the temp file for the apt known-name check (is the container temp dir writable?); falling back to the expansion-character filter for APT_PACKAGES" component=entrypoint\n' >&2
    elif [ "$apt_names_rc" -ne 124 ] && [ "$apt_names_rc" -ne 137 ]; then
      printf 'level=warn msg="apt package index unreadable; falling back to the expansion-character filter for APT_PACKAGES" component=entrypoint\n' >&2
    fi
    [ -z "$apt_names" ] || rm -f "$apt_names"

    # Whenever the gate could NOT run -- an unusable oracle, whatever cost it (a
    # failed apt-get update that also left no readable index, a corrupt cache, a
    # killed probe) -- skipping it is still the right failure mode: rejecting every
    # token would turn a transient problem into "none of your packages installed"
    # plus a misleading per-token typo warning, and the grammar still holds. But
    # the pre-gate behaviour is exactly what leaves the 337-package regex blowup
    # described above reachable, so it degrades with ONE narrowing.
    #
    # The narrowing drops every token containing '.' or '+', which is the COMPLETE
    # set of apt expansion characters the grammar admits ('?' and '*' are outside
    # its character class, and a trailing '-' -- apt's remove form -- is already
    # rejected by the grammar; an internal '-' has no special interpretation).
    # Both are live: '.' is any-character and '+' is one-or-more, so `jq++`,
    # `libjq+1` and `libj+q1` ALL regex-resolve to real packages (measured on apt
    # 3.0.3, trixie's major) -- a '+' anywhere in the token is enough, this is not
    # only a trailing-suffix concern. Dropping just these removes the blowup while
    # every plain name still installs, which is the common case and the reason the
    # skip-everything option was rejected.
    #
    # Handled here rather than inside the gate's branches so every way of losing
    # the gate lands on the same rule and they cannot drift apart.
    #
    # The cost is narrow and self-healing: a real name carrying one of these
    # characters (docker.io, python3.13, g++) waits for a boot whose index answers,
    # which is a boot where the install was going to be unreliable anyway. Per "a
    # broken state must be able to heal itself", the next boot's gate installs it —
    # and because the gate is now attempted even after a failed update, a partial
    # index is usually enough to install it on THIS boot.
    if [ "$apt_gate_ran" -eq 0 ] && [ "${#apt_pkgs[@]}" -gt 0 ]; then
      ungated_pkgs=()
      for pkg in "${apt_pkgs[@]}"; do
        if [[ "$pkg" == *.* || "$pkg" == *+* ]]; then
          warn_skipped_apt_token 'skipping unverifiable APT_PACKAGES token containing an apt expansion character (. or +) while the known-name check is unavailable; retry once the package index is readable' "$pkg"
        else
          ungated_pkgs+=("$pkg")
        fi
      done
      apt_pkgs=("${ungated_pkgs[@]}")
    fi
  fi
  if [ "${#apt_pkgs[@]}" -gt 0 ]; then
    # A SIGKILL landing inside the install deadline (docker stop during the
    # foreground window; this shell defers SIGTERM until the child returns) leaves
    # dpkg interrupted, and apt state is container-layer state that SURVIVES
    # docker start -- so every later boot would refuse the install with rc=100 and
    # no package would ever be installed again for this container's life. Reconfigure
    # once, bounded, warn-only: the state is either absent (a no-op) or the only
    # thing standing between the operator and their packages.
    #
    # The AUDIT OUTPUT is the primary evidence, not the exit status: `dpkg --audit`
    # returns 0 while REPORTING unpacked-but-unconfigured packages (measured: 464
    # bytes on stdout, rc=0), which is the ordinary interrupted state this recovery
    # exists for -- gating on rc alone would never fire on it. A healthy tree prints
    # nothing at all, so non-empty output cannot false-positive here. The updates
    # journal stays a third trigger: it is evidence of a transaction killed even
    # earlier, before any package reached the unpacked state.
    dpkg_audit_rc=0
    dpkg_audit_out=$(timeout --signal=TERM --kill-after=30s 300s dpkg --audit 2>/dev/null) || dpkg_audit_rc=$?
    # Bounded for the log line: audit output is short in practice, and a truncated
    # first line is enough to tell an operator WHICH interrupted state was seen.
    dpkg_audit_summary=$(printf '%s' "${dpkg_audit_out:0:400}" | tr '\n' ' ')
    if [ "$dpkg_audit_rc" -ne 0 ] || [ -n "$dpkg_audit_out" ] \
      || [ -n "$(ls -A /var/lib/dpkg/updates 2>/dev/null)" ]; then
      printf 'level=warn msg="dpkg is in an interrupted state (an earlier APT_PACKAGES install was killed mid-transaction); reconfiguring before installing" audit_rc=%d audit="%s" component=entrypoint\n' \
        "$dpkg_audit_rc" "$(logfmt_value "$dpkg_audit_summary" 400)" >&2
      dpkg_fix_rc=0
      timeout --signal=TERM --kill-after=30s 300s dpkg --configure -a || dpkg_fix_rc=$?
      if [ "$dpkg_fix_rc" -ne 0 ]; then
        printf 'level=warn msg="dpkg --configure -a failed; APT_PACKAGES will keep failing until the container is recreated" rc=%d component=entrypoint\n' "$dpkg_fix_rc" >&2
      fi
    fi
    printf 'level=info msg="installing OS packages" packages="%s" component=entrypoint\n' "${apt_pkgs[*]}" >&2
    # Called directly rather than through `bash -c`: with update split out there is
    # nothing left to chain, and one less shell between env content and apt is one
    # less layer that could reinterpret a token.
    timeout --signal=TERM --kill-after=30s 600s apt-get install -y -qq --no-install-recommends -- "${apt_pkgs[@]}"
    apt_rc=$?
    if [ "$apt_rc" -ne 0 ]; then
      # 124/137 = the 600s deadline (TERM, then the --kill-after SIGKILL
      # fallback); logged distinctly so Loki shows deadline exhaustion
      # rather than a generic apt failure.
      if [ "$apt_rc" -eq 124 ] || [ "$apt_rc" -eq 137 ]; then
        printf 'level=warn msg="APT_PACKAGES install exceeded its 600s deadline and was terminated; container continues without them" rc=%d component=entrypoint\n' "$apt_rc" >&2
      else
        printf 'level=warn msg="APT_PACKAGES install failed; container continues without them" rc=%d component=entrypoint\n' "$apt_rc" >&2
      fi
    fi
  fi
  # Reclaim the indexes whenever this block refreshed them -- ATTEMPTED, not succeeded --
  # whether or not anything was ultimately installed (every token may have been skipped).
  # A partial refresh returns non-zero with the files written, which is the state the
  # known-name gate above is designed around, so keying on rc=0 kept ~21 MB of Debian
  # indexes (measured on trixie, main only) in the CONTAINER LAYER on exactly those boots:
  # invisible on the /config volume, surviving every docker start, and cleared only by a
  # recreate or a later boot whose update happened to return 0. The image deletes these at
  # build time for the same reason. Retaining them buys nothing -- the gate reads only an
  # index refreshed in THIS boot, never a pre-existing one.
  #
  # An UNSET apt_update_rc is the one case that must not delete: update never ran, because
  # every APT_PACKAGES token failed the grammar.
  if [ -n "${apt_update_rc:-}" ]; then
    rm -rf /var/lib/apt/lists/*
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
