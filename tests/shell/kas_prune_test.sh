#!/usr/bin/env bash
# prune_superseded_kas_runtimes(): reclaim the ~240 MB agent-server runtime each
# kiro-cli version unpacks under <data-dir>/kas/<version>-<hash>/, keeping only
# the pinned one.
#
# This is the entrypoint's only root `rm -rf`, and its target is derived entirely
# from the environment (XDG_DATA_HOME, else $HOME/.local/share), so the REFUSALS
# carry the weight: a symlinked store or data dir, a path realpath cannot confirm
# inside the data dir, an entry the pruner has never seen. It is hygiene and never
# an integrity gate — it warns and returns 0 either way, which is exactly why an
# exit code proves nothing here and every case below asserts the on-disk tree plus
# the specific warning line.
# Lint directives for this whole file, each against a stated guarantee rather than
# an assumption:
#   SC2015 - the assertion form `[ cond ] && ok "..." || no "..."` cannot mis-fire,
#     because lib.sh's ok/no return 0 unconditionally by design (see their comment).
#   SC2034 - the variables set below are the INPUTS to entrypoint.sh code that is
#     extracted and sourced at RUNTIME, so shellcheck cannot see the reads.
#   SC2329 - the two external-command stubs (realpath, rm) are invoked by that same
#     runtime-sourced code, never from this file, so shellcheck sees a definition
#     with no caller. Every other function here is called directly, so the disable
#     cannot hide a dead helper.
# shellcheck disable=SC2015,SC2034,SC2329
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

load_function prune_superseded_kas_runtimes

KIRO_CLI_VERSION="2.14.2"

# Fresh fake volume per scenario. XDG_DATA_HOME drives the resolution, matching how
# kiro-cli locates its own data dir.
setup() {
  ROOT=$(mktemp -d "$WORK/vol.XXXXXX")
  export XDG_DATA_HOME="$ROOT/share"
  KAS="$XDG_DATA_HOME/kiro-cli/kas"
  mkdir -p "$KAS"
}

# --- 1. ordinary prune: superseded versions go, the pinned one stays -------------
setup
mkdir -p "$KAS/2.14.2-abc" "$KAS/2.13.0-def" "$KAS/2.12.1-ghi"
: >"$KAS/2.13.0-def.lock"
prune_superseded_kas_runtimes >/dev/null 2>&1
[ -d "$KAS/2.14.2-abc" ] && ok "pinned runtime kept" || no "pinned runtime kept" "it was deleted"
[ ! -d "$KAS/2.13.0-def" ] && [ ! -d "$KAS/2.12.1-ghi" ] \
  && ok "superseded runtimes pruned" || no "superseded runtimes pruned" "still present"
[ ! -e "$KAS/2.13.0-def.lock" ] && ok "the superseded .lock sibling pruned too" \
  || no ".lock sibling" "still present"

# Both streams go to files rather than /dev/null: the refusals are on stderr and the
# skip/prune narration is on stdout, and several cases below can only distinguish
# the guard that fired by reading one of them.
prune_quietly() {
  prune_superseded_kas_runtimes >"$WORK/out.log" 2>"$WORK/warn.log"
}
guard_said() {
  grep -q "$1" "$WORK/warn.log"
}
narrated() {
  grep -q "$1" "$WORK/out.log"
}

# --- 2. entries kiro-cli owns but this pruner has never seen ---------------------
setup
mkdir -p "$KAS/2.14.2-abc" "$KAS/unpack-scratch" "$KAS/index"
: >"$KAS/store.lock"
prune_quietly
if [ -d "$KAS/unpack-scratch" ] && [ -d "$KAS/index" ] && [ -e "$KAS/store.lock" ]; then
  ok "unrecognized (non version-keyed) entries left alone"
else
  no "unrecognized entries" "the pruner deleted another program's state"
fi

# The version-keyed test is an ANCHORED three-component match, and the anchor is
# what separates "a runtime tree" from "a name that merely contains a version".
# Neither near-miss below is prunable, and the skip is narrated rather than silent
# so a kas/ layout change shows up in the boot log instead of being absorbed.
setup
mkdir -p "$KAS/2.14.2-abc" "$KAS/2.13-def" "$KAS/v2.13.0-def"
prune_quietly
[ -d "$KAS/2.13-def" ] && [ -d "$KAS/v2.13.0-def" ] \
  && narrated 'v2.13.0-def' && narrated '2.13-def' \
  && ok "a two-component and a v-prefixed name are not version-keyed, kept and narrated" \
  || no "near-miss names" "deleted, or the skip was not narrated"

# The keep-pattern is "$KIRO_CLI_VERSION"-*, and the dash is load-bearing: without
# it a LONGER version sharing the pin's digits would read as the pin and survive
# forever, which is the leak this function exists to stop.
setup
mkdir -p "$KAS/2.14.2-abc" "$KAS/2.14.20-xyz"
prune_quietly
[ -d "$KAS/2.14.2-abc" ] && [ ! -d "$KAS/2.14.20-xyz" ] \
  && ok "a version merely sharing the pin's prefix is pruned, not mistaken for the pin" \
  || no "prefix-sharing version" "2.14.20-xyz survived as though it were the pin"

# --- 3. THE SECURITY CASE: a symlinked store must not redirect a root rm -rf ----
#
# The victim tree's contents are DELIBERATELY version-keyed and non-pinned
# ("2.13.0-victim"), i.e. exactly the shape the pruner deletes. A non-version-keyed
# victim is skipped by the loop anyway, so it would pass with every symlink guard
# removed and prove nothing. Bait has to satisfy the EARLIER guards too: the
# function returns at `[ -d "$kas_dir" ]` before it looks at any symlink, so each
# case below plants the version-keyed entries at the exact path the resolved
# $kas_dir names. Verified against THIS entrypoint by neutralising both guards in a
# /tmp copy and watching each bait get deleted.
plant_victim() {
  mkdir -p "$1/2.13.0-victim" && : >"$1/2.13.0-victim/data"
}

# Survival alone cannot say WHICH guard refused, and for this threat the two are
# redundant: realpath resolves a symlink away, so every redirect the -L check
# catches ALSO fails the containment check. No input isolates -L by outcome —
# removing it alone leaves the tree intact. What the guards do not share is what
# they SAY, so each case asserts its own refusal line; drop the -L check and the
# symlink cases report the containment refusal instead, and fail here.
setup
VICTIM="$ROOT/victim"
plant_victim "$VICTIM"
rm -rf "$KAS"
mkdir -p "$XDG_DATA_HOME/kiro-cli"
ln -s "$VICTIM" "$KAS" # kas -> an arbitrary tree
prune_quietly
[ -f "$VICTIM/2.13.0-victim/data" ] && guard_said 'is a symlink' \
  && ok "symlinked kas store refused BY the symlink guard; the victim tree survived" \
  || no "symlinked kas store" "the pruner deleted through the symlink, or a different guard caught it"

setup
VICTIM="$ROOT/victim2"
# The victim's version-keyed entries sit under a real `kas` child, because that is
# what $kas_dir resolves to once `kiro-cli` is the symlink -- without it the `-d`
# check returns first and no guard is exercised at all.
plant_victim "$VICTIM/kas"
rm -rf "$XDG_DATA_HOME/kiro-cli"
ln -s "$VICTIM" "$XDG_DATA_HOME/kiro-cli" # the data dir itself is the symlink
prune_quietly
[ -f "$VICTIM/kas/2.13.0-victim/data" ] && guard_said 'is a symlink' \
  && ok "symlinked data dir refused BY the symlink guard; the victim tree survived" \
  || no "symlinked data dir" "the pruner deleted through the symlink, or a different guard caught it"

# The containment check is a SECOND, independent guard, and this case isolates it:
# NO symlink is involved anywhere, so the -L guard above cannot fire. A
# non-canonical XDG_DATA_HOME (one carrying "..", which an operator env var
# legitimately can) makes realpath disagree with the literal path the case pattern
# is built from, and the pruner refuses rather than deleting against a path it
# cannot confirm.
setup
mkdir -p "$ROOT/real-share"
XDG_DATA_HOME="$ROOT/real-share/../real-share"
KAS="$XDG_DATA_HOME/kiro-cli/kas"
mkdir -p "$KAS"
plant_victim "$KAS"
prune_quietly
[ -f "$KAS/2.13.0-victim/data" ] && guard_said 'does not resolve inside the data dir' \
  && ok "non-canonical data-dir path refused BY the containment guard (no symlink involved)" \
  || no "containment check" "pruned against a path realpath could not confirm, or a different guard caught it"

# The same guard's OTHER input: realpath itself failing. `kas_real=""` must not
# collapse into "resolved fine" -- an empty answer has to refuse, and say so
# ("unknown") rather than printing an empty target nobody can act on. realpath is an
# external command, so a shell function shadows it -- inside a SUBSHELL, so the stub
# cannot outlive the one call and cannot shadow this file's own use of the same
# command name.
setup
plant_victim "$KAS"
(
  realpath() { return 1; }
  prune_superseded_kas_runtimes
) >"$WORK/out.log" 2>"$WORK/warn.log"
[ -f "$KAS/2.13.0-victim/data" ] && guard_said 'does not resolve inside the data dir' \
  && guard_said 'unknown' \
  && ok "an unresolvable kas path refuses and names its target unknown" \
  || no "unresolvable kas path" "pruned anyway, or reported an empty target"

# --- 4. a failed delete warns; it never fails the boot ---------------------------
# Root cannot be denied a directory it owns, so the rm failure this branch exists
# for (an immutable attribute, EPERM on a foreign mount) is provoked by shadowing
# the external rm, again scoped to a subshell. The contract is warn-and-continue:
# hygiene must not brick a container whose /config the operator is free to reshape.
setup
mkdir -p "$KAS/2.13.0-def"
(
  rm() { return 1; }
  prune_superseded_kas_runtimes
) >"$WORK/out.log" 2>"$WORK/warn.log"
rc=$?
[ "$rc" -eq 0 ] && [ -d "$KAS/2.13.0-def" ] \
  && guard_said 'failed to prune superseded kiro-cli agent runtime 2.13.0-def' \
  && ok "a failed delete warns, names the entry, and still returns 0" \
  || no "failed delete" "returned $rc, or the warning lost the entry name: $(cat "$WORK/warn.log")"

# --- 5. degenerate inputs must not fail the boot --------------------------------
setup
prune_superseded_kas_runtimes >/dev/null 2>&1
[ $? -eq 0 ] && ok "empty store returns 0" || no "empty store" "non-zero return"

setup
rm -rf "$XDG_DATA_HOME"
prune_superseded_kas_runtimes >/dev/null 2>&1
[ $? -eq 0 ] && ok "absent data dir returns 0" || no "absent data dir" "non-zero return"

setup
unset XDG_DATA_HOME
HOME_SAVED="${HOME:-}"
unset HOME
# "Without touching anything" needs the EXECUTION to be observable, not just the
# status: with the HOME guard deleted, bash expands the next line to
# data_home=/.local/share and the function still returns 0 because that store does
# not exist -- a false green over an environment-derived root path in the same
# function that runs a root rm -rf. The xtrace log is the observable: the guard's
# return must fire before any data_home assignment is reached.
_trace="$WORK/unset-home-trace"
{
  BASH_XTRACEFD=7
  set -x
  prune_superseded_kas_runtimes >/dev/null 2>&1
  rc=$?
  set +x
  unset BASH_XTRACEFD
} 7>"$_trace"
export HOME="$HOME_SAVED"
# The first assignment (data_home="${XDG_DATA_HOME:-}") legitimately runs and is
# empty; what the guard must prevent is the HOME-fallback DERIVATION, whose trace
# line carries .local/share (from an unset HOME it would derive /.local/share, a
# root-relative path in the same function that runs a root rm -rf).
# The `data_home=` anchor proves the CAPTURE IS LIVE. Without it the assertion is
# purely negative, so a trace that never landed (a lost BASH_XTRACEFD, output going
# to the discarded stderr instead) would satisfy it vacuously -- and would satisfy
# it while the guard was broken too.
grep -q 'data_home=' "$_trace" && [ "$rc" -eq 0 ] && ! grep -q '\.local/share' "$_trace" \
  && ok "neither XDG_DATA_HOME nor HOME set returns 0 before the HOME fallback is derived" \
  || no "unset HOME" "rc=$rc, trace lines=$(wc -l <"$_trace"), derivations: $(grep '\.local/share' "$_trace" | head -2 | tr '\n' ';')"

# --- 6. data-dir resolution must match kiro-cli's own ---------------------------
# Pruning a directory the CLI does not use is a silent no-op, the one failure mode a
# hygiene step must not have. Both halves of the resolution are asserted with a
# prunable tree on BOTH candidate paths, so a swapped precedence deletes the wrong
# one and is visible from either side.
setup
HOME_SAVED="${HOME:-}"
export HOME="$ROOT/home"
HOME_KAS="$HOME/.local/share/kiro-cli/kas"
mkdir -p "$HOME_KAS/2.13.0-home" "$KAS/2.13.0-xdg"
prune_quietly
export HOME="$HOME_SAVED"
[ ! -d "$KAS/2.13.0-xdg" ] && [ -d "$HOME_KAS/2.13.0-home" ] \
  && ok "XDG_DATA_HOME wins over HOME; the HOME tree is not touched" \
  || no "XDG precedence" "pruned the HOME tree, or left the XDG one"

setup
unset XDG_DATA_HOME
HOME_SAVED="${HOME:-}"
export HOME="$ROOT/home2"
HOME_KAS="$HOME/.local/share/kiro-cli/kas"
mkdir -p "$HOME_KAS/2.14.2-keep" "$HOME_KAS/2.13.0-home"
prune_quietly
export HOME="$HOME_SAVED"
[ ! -d "$HOME_KAS/2.13.0-home" ] && [ -d "$HOME_KAS/2.14.2-keep" ] \
  && ok "with XDG_DATA_HOME unset the store resolves under \$HOME/.local/share" \
  || no "HOME fallback" "the fallback path was not the one pruned"

report
