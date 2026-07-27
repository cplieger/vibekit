#!/usr/bin/env bash
# The boot's first decision: the `mkdir -p` of every directory the container needs,
# and its fail-CLOSED branch.
#
# This block lives inline in the boot path rather than in a function, so it is taken
# with extract_range. It is the ONE place the entrypoint aborts on /config state, and
# aborting is correct here: with nothing to persist there is nothing to serve, and
# the invariant that everything else warns rather than dying rests on this branch
# still being the exception. A healthy image takes the success path only, so the
# smoke test can never show the abort works — and if it silently stopped aborting,
# the boot would carry on into an install that writes nowhere.
#
# Every directory in the list is load-bearing: $TOOLS/bin is the PATH entry and the
# kiro-cli promotion target, $HOME/.local/share/kiro-cli is the parent of the agent
# runtime store the pruner resolves, $HOME/.ssh and $KIRO_HOME hold the state that
# has to survive container recreation.
# Lint directives for this whole file, each against a stated guarantee rather than
# an assumption:
#   SC2015 - the assertion form `[ cond ] && ok "..." || no "..."` cannot mis-fire,
#     because lib.sh's ok/no return 0 unconditionally by design (see their comment).
#   SC2034 - the variables set below are the INPUTS to entrypoint.sh code that is
#     extracted and sourced at RUNTIME, so shellcheck cannot see the reads.
#   SC2329 - the mkdir/sleep stubs are invoked by that same runtime-sourced code,
#     never from this file. Every other function here is called directly, so the
#     disable cannot hide a dead helper.
#   SC1090 - the sourced path is produced by extract_range at runtime, so there is
#     nothing on disk for shellcheck to follow at lint time.
# shellcheck disable=SC2015,SC2034,SC2329,SC1090
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

# The block, verbatim: from the `mkdir -p` that opens it to the closing brace of its
# `||` handler. Captured as a path rather than sourced through a substitution, so a
# failed extraction stops this file instead of reporting passes against nothing.
BLOCK=$(extract_range '^mkdir -p' '^  }$') || exit 1
# extract_range's end anchor is a two-space `}`, which is NOT unique in
# entrypoint.sh: the next one belongs to install_kiro_cli's mktemp handler ~90 lines
# later, so a shipped edit that removed this block's closing brace would capture the
# legacy-/config/kiro migration and part of the installer along with it. Today that
# over-capture does not parse, so the source fails loudly, but that is luck rather
# than a guarantee. Name the fault instead of relying on it.
if grep -q '/config/kiro' "$BLOCK"; then
  printf 'harness error: extract_range ran past the mkdir block; its closing-brace end anchor is not unique\n' >&2
  exit 1
fi

# --- 1. the success path creates every required directory ------------------------
ROOT="$WORK/ok"
(
  TOOLS="$ROOT/tools"
  HOME="$ROOT/home"
  KIRO_HOME="$ROOT/home/.kiro"
  . "$BLOCK"
) >"$WORK/out.log" 2>&1
rc=$?
missing=""
for d in tools/bin home/.local/share/kiro-cli home/.ssh home/.kiro \
  home/.cache/go-build home/.docker/cli-plugins; do
  [ -d "$ROOT/$d" ] || missing="$missing $d"
done
[ "$rc" -eq 0 ] && [ -z "$missing" ] \
  && ok "the boot creates all six required directories and continues" \
  || no "required directories" "rc=$rc missing:$missing"

# --- 2. a directory it cannot create aborts the boot -----------------------------
# Root cannot be denied a mkdir under a directory it owns, so the failure this branch
# exists for (an unmounted or read-only /config) is provoked by shadowing the
# external mkdir, scoped to the subshell. `sleep` is shadowed too: the shipped delay
# is 10 real seconds, and waiting them out would prove nothing the exit status does
# not already say.
ROOT="$WORK/fail"
(
  TOOLS="$ROOT/tools"
  HOME="$ROOT/home"
  KIRO_HOME="$ROOT/home/.kiro"
  mkdir() { return 1; }
  sleep() { return 0; }
  . "$BLOCK"
) >"$WORK/out.log" 2>&1
rc=$?
[ "$rc" -ne 0 ] && grep -Fq 'failed to create required directories (is /config mounted and writable?)' "$WORK/out.log" \
  && ok "a mkdir it cannot complete aborts the boot and says /config may be unmounted" \
  || no "unwritable /config" "rc=$rc, or the message lost its remediation hint: $(cat "$WORK/out.log")"

# --- 3. an unset KIRO_HOME aborts rather than being skipped ----------------------
# KIRO_HOME comes from the image's ENV, not from this script, so nothing here defaults
# it. An unset value reaches mkdir as an EMPTY operand, which mkdir refuses — so the
# coupling to the Dockerfile ENV fails the boot loudly instead of quietly leaving the
# kiro state directory uncreated. The subshell runs with `set -u` off because the
# shipped entrypoint does (`grep '^set ' entrypoint.sh` finds nothing); asserting this
# under the test file's own `set -u` would measure the harness, not the boot.
ROOT="$WORK/nokirohome"
(
  set +u
  TOOLS="$ROOT/tools"
  HOME="$ROOT/home"
  unset KIRO_HOME
  sleep() { return 0; }
  . "$BLOCK"
) >"$WORK/out.log" 2>&1
rc=$?
[ "$rc" -ne 0 ] && grep -q 'failed to create required directories' "$WORK/out.log" \
  && [ -d "$ROOT/tools/bin" ] \
  && ok "an unset KIRO_HOME aborts the boot, after the operands it could satisfy" \
  || no "unset KIRO_HOME" "rc=$rc: the boot continued with no kiro state directory"

report
