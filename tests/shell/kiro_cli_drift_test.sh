#!/usr/bin/env bash
# needs_kiro_cli_install() + kiro_cli_version(): the boot's decision about whether
# the kiro-cli already on the persistent /config volume is the one the image pinned.
#
# Why this cannot be left to the image smoke test: the binary is NOT in the image.
# It is downloaded on first boot and then survives every container recreation, so
# the interesting states all live on a volume the image build never sees — a binary
# one version behind the pin, a binary that no longer runs, a binary that hangs on
# --version. A missing-only check would leave every existing container on whatever
# version it installed first, which is the regression this drift check exists to
# prevent; nothing in a healthy boot demonstrates that it still works.
# Lint directives for this whole file, each against a stated guarantee rather than
# an assumption:
#   SC2015 - the assertion form `[ cond ] && ok "..." || no "..."` cannot mis-fire,
#     because lib.sh's ok/no return 0 unconditionally by design (see their comment).
#   SC2034 - the variables set below are the INPUTS to entrypoint.sh code that is
#     extracted and sourced at RUNTIME, so shellcheck cannot see the reads.
#   SC2329 - the `timeout` stub is invoked by that same runtime-sourced code, never
#     from this file. Every other function here is called directly, so the disable
#     cannot hide a dead helper.
# shellcheck disable=SC2015,SC2034,SC2329
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

# needs_kiro_cli_install calls kiro_cli_version, so both come from the shipped file
# rather than one of them being faked out.
load_function kiro_cli_version
load_function needs_kiro_cli_install

KIRO_CLI_VERSION="2.14.2"
TOOLS="$WORK/tools"
BIN="$TOOLS/bin/kiro-cli"
mkdir -p "$TOOLS/bin"

# A real on-disk executable, because that is what the function probes: it runs the
# candidate. Nothing about the CLI itself is stubbed beyond what it prints.
plant_cli() {
  printf '#!/bin/sh\n%s\n' "$1" >"$BIN"
  chmod +x "$BIN"
}
warned() {
  grep -q "$1" "$WORK/warn.log"
}
narrated() {
  grep -q "$1" "$WORK/out.log"
}
decide() {
  needs_kiro_cli_install >"$WORK/out.log" 2>"$WORK/warn.log"
}

# --- 1. the four states of the on-disk binary -----------------------------------
# The absent case asks for the install SILENTLY, and that is asserted rather than
# just the return value: the missing-binary check and the drift check are redundant
# by outcome here (probing a path with nothing at it also yields an empty version,
# which also reads as drift), so a status-only assertion cannot tell them apart.
# What separates them is the log — a first boot must say "installing", not
# "version drift: installed=unknown" about a binary that never existed.
rm -f "$BIN"
decide && [ ! -s "$WORK/out.log" ] && [ ! -s "$WORK/warn.log" ] \
  && ok "an absent binary asks for an install, with no drift reported about it" \
  || no "absent binary" "the boot skipped the install, or reported drift on a binary that never existed"

plant_cli 'echo "kiro-cli 2.14.2"'
decide && no "binary already at the pin" "the boot would reinstall on every start" \
  || ok "a binary already at the pin asks for nothing"

plant_cli 'echo "kiro-cli 2.13.0"'
decide && narrated 'installed=2.13.0' && narrated 'pinned=2.14.2' \
  && ok "a drifted binary asks for an install and names both versions" \
  || no "drifted binary" "no reinstall, or the log did not name installed vs pinned"

# An empty answer must read as a mismatch, not as "matches whatever". This is the
# state a half-broken volume presents: the file is there, running it fails.
plant_cli 'exit 3'
decide && narrated 'installed=unknown' \
  && ok "a binary that cannot report its version asks for an install, named unknown" \
  || no "unrunnable binary" "accepted it, or reported an empty version"

# The presence test is `-f`, not `-x`, so a non-executable file is NOT short-circuited
# as absent: it goes through the version probe and comes back as drift. The narration
# is what proves which path it took.
printf '#!/bin/sh\necho "kiro-cli 2.14.2"\n' >"$BIN"
chmod 644 "$BIN"
decide && narrated 'version drift' \
  && ok "a present but non-executable binary is probed and reported as drift" \
  || no "non-executable binary" "treated as absent, or accepted"

# --- 2. kiro_cli_version's own contract -----------------------------------------
# Only the FIRST line's LAST field. Upstream has printed extra lines before, and a
# version parsed out of line two would silently compare unequal forever.
plant_cli 'printf "kiro-cli 2.14.2\nwarning: something chatty\n"'
chmod +x "$BIN"
got=$(kiro_cli_version "$BIN" 2>/dev/null)
[ "$got" = "2.14.2" ] \
  && ok "the version is the first line's last field, ignoring later output" \
  || no "version parse" "got '$got'"

plant_cli 'exit 3'
got=$(kiro_cli_version "$BIN" 2>"$WORK/warn.log")
rc=$?
[ "$rc" -eq 3 ] && [ -z "$got" ] && warned 'version failed' && warned 'rc=3' \
  && ok "a failing probe propagates the binary's status, prints nothing, and warns" \
  || no "failing probe" "rc=$rc out='$got'"

# The deadline is the reason this helper exists: a wedged binary that traps or
# ignores TERM would otherwise hang the boot forever with nothing in the log. Both
# stages of the deadline (TERM=124, then the --kill-after SIGKILL=137) must be named
# as a TIMEOUT rather than folded into the generic failure line, so an operator can
# tell "it is hung" from "it is broken". `timeout` is an external command, so a shell
# function shadows it inside a subshell, scoped to the one call.
for deadline_rc in 124 137; do
  got=$(
    timeout() { return "$deadline_rc"; }
    kiro_cli_version "$BIN" 2>"$WORK/warn.log"
  )
  rc=$?
  [ "$rc" -eq "$deadline_rc" ] && [ -z "$got" ] \
    && warned 'exceeded its 10s deadline' && ! warned 'version failed' \
    && ok "rc=$deadline_rc is reported as the 10s deadline, not as a generic failure" \
    || no "deadline rc=$deadline_rc" "rc=$rc, or the warning was the generic one"
done

report
