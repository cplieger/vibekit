#!/usr/bin/env bash
# The APT_PACKAGES install path: what reaches apt-get, and what does not.
#
# Two stages guard it. An anchored grammar rejects anything that is not
# name-shaped, then a known-name gate (apt-cache pkgnames) refuses anything that
# is not a real package -- and that second gate is what keeps a token away from
# apt's regex fallback, where `apt-get install -s -- 'jq.'` plans 337 packages.
#
# The gate is attempted even when `apt-get update` FAILED, because the reachable
# failure is a partial refresh whose surviving index still comes from real
# repository metadata: it can only produce false negatives (a valid package it
# does not list waits for a later boot), never a false positive. Only when that
# oracle is unusable (non-zero exit, deadline kill, empty output) does the install
# fall back to the grammar alone, minus every token containing '.' or '+' -- the
# complete set of apt expansion characters the grammar admits ('.' is
# any-character, '+' is one-or-more, so `jq++`, `libjq+1` and `libj+q1` all
# regex-resolve to real packages). Plain names still install, which is the common
# case and the reason skip-everything was rejected.
#
# Drives the real inline block with apt stubbed, recording exactly what argv
# apt-get install would have received.
# Lint directives for this whole file, each against a stated guarantee rather than
# an assumption:
#   SC2015 - the assertion form `[ cond ] && ok "..." || no "..."` cannot mis-fire,
#     because lib.sh's ok/no return 0 unconditionally by design (see their comment).
#   SC2016 - the grep patterns below must stay single-quoted: they match the
#     LITERAL "${APT_PACKAGES:-}" text in entrypoint.sh, not its expansion.
# shellcheck disable=SC2015,SC2016
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

# The logic under test is INLINE boot code, not a function, so it is taken by
# range. lib.sh's block form, which names this block in its own doc comment.
# Captured as an assignment, never as `. "$(extract_range ...)"`: the harness's
# fatal has to reach THIS process, not just a command substitution's subshell.
extract_range '^if \[ -n "${APT_PACKAGES:-}" \]; then$' '^fi$' "$WORK/block.sh" >/dev/null || exit 1
# Fatal precondition, not decoration: an empty or truncated extraction installs
# nothing and warns about nothing, which is indistinguishable from a correctly
# refusing block -- every negative case below would go green against no code at all.
if [ ! -s "$WORK/block.sh" ] || ! bash -n "$WORK/block.sh" 2>/dev/null; then
  printf 'harness error: the extracted APT_PACKAGES block is empty or does not parse\n' >&2
  exit 1
fi
extract_function logfmt_value "$WORK/logfmt.sh" >/dev/null
extract_function warn_skipped_apt_token "$WORK/warn.sh" >/dev/null

# RUN_CWD lets a case choose the directory the harness runs FROM. Empty means "here":
# only the glob-suppression case below needs a controlled cwd, since that guard's
# bait is a directory whose FILENAMES are what an unsuppressed '*' would expand to.
RUN_CWD=""

# run <update_rc> <pkgnames_mode> <APT_PACKAGES> [audit_out] [audit_rc] [journal]
#   pkgnames_mode: ok | partial | empty | fail
#     ok      - a full index: every name the cases use is known
#     partial - the surviving half of a partial refresh: a SUBSET of the real names,
#               which is the only thing an incomplete index can be (false negatives,
#               never false positives)
#     empty   - the command succeeds but yields no names (unusable oracle)
#     fail    - the command itself fails (unusable oracle)
# -> INSTALLED (the argv apt-get install received), SKIPPED (warn count),
#    INSTALL_CALLS (invocations), DPKG_CONFIGURE_CALLS (interrupted-state recovery
#    invocations), $WORK/warns (the stderr, for warn_for)
run() {
  local urc=$1 mode=$2 pkgs=$3 audit_out=${4:-} audit_rc=${5:-0} journal=${6:-}
  cat >"$WORK/harness.sh" <<HARNESS
set -u
APT_PACKAGES='$pkgs'
_URC=$urc
_MODE=$mode
# dpkg's interrupted-state evidence, per case. Default is a HEALTHY tree: no audit
# output, rc 0, an empty updates journal -- so every pre-existing case asserts the
# recovery does NOT fire, and the two cases that supply evidence assert it does.
_DPKG_AUDIT_OUT='$audit_out'
_DPKG_AUDIT_RC=$audit_rc
_DPKG_JOURNAL='$journal'
_DPKG_CONFIGURE_RC=0
apt-get() {
  case "\$*" in
    *update*) return \$_URC ;;
    *install*)
      # Record the CALL itself before any argv filtering: the empty-list guard is
      # asserted on invocations, and a stub that only records package args cannot
      # see an empty install.
      printf 'install-call\n' >>"$WORK/calls"
      # Then record exactly what apt would have been handed AS PACKAGES: everything
      # after the -- separator, verbatim. apt's own invocation options sit before
      # the separator and are not evidence; a hostile token shaped like an option
      # can only be a package, so it lands after -- and is recorded ('opt:'
      # prefixed) instead of vanishing into a filter.
      _sep=0
      for a in "\$@"; do
        case "\$a" in
          --) _sep=1; continue ;;
        esac
        [ "\$_sep" -eq 1 ] || continue
        case "\$a" in
          -*) printf 'opt:%s\n' "\$a" >>"$WORK/installed" ;;
          *) printf '%s\n' "\$a" >>"$WORK/installed" ;;
        esac
      done
      return 0 ;;
  esac
  return 0
}
apt-cache() {
  # The stub answers the same whatever the update rc was, which is the point: a
  # failed update does not by itself make the index unreadable, and the block must
  # ASK rather than assume. _MODE is what decides usability here.
  case "\$_MODE" in
    ok)      printf '%s\n' jq gcc python3 python3.13 docker.io g++ ;;
    partial) printf '%s\n' jq gcc ;;
    empty)   : ;;
    fail)    return 1 ;;
  esac
}
timeout() { shift 3; "\$@"; }
dpkg() {
  # NOT the runner's real dpkg: the block's recovery path would otherwise read the
  # host's package database and, on a runner with genuinely interrupted state, run
  # \`dpkg --configure -a\` as root -- executing maintainer scripts from a suite whose
  # only subject is argv. Audit evidence is per-case; configure is RECORDED, never run.
  case "\$*" in
    *--audit*)
      [ -n "\$_DPKG_AUDIT_OUT" ] && printf '%s\n' "\$_DPKG_AUDIT_OUT"
      return \$_DPKG_AUDIT_RC
      ;;
    *--configure*)
      printf 'dpkg-configure\n' >>"$WORK/calls"
      return \$_DPKG_CONFIGURE_RC
      ;;
  esac
  return 0
}
ls() {
  # Intercept EXACTLY the updates-journal probe, so the third recovery trigger is
  # case-controlled instead of reading the runner's real /var/lib/dpkg/updates.
  # Every other ls still runs -- the same containment the rm stub below uses.
  case "\$*" in
    *"/var/lib/dpkg/updates"*)
      [ -n "\$_DPKG_JOURNAL" ] && printf '%s\n' "\$_DPKG_JOURNAL"
      return 0
      ;;
  esac
  command ls "\$@"
}
rm() {
  # The extracted block ends with the index reclaim (\`rm -rf /var/lib/apt/lists/*\`).
  # apt-get is stubbed here; rm is NOT, so that line is real: run as root -- the
  # ordinary way inside this container -- the suite wipes the package index and the
  # next apt-get install fails until an update. Intercept exactly that path (and
  # record it, so the reclaim stays observable) while every other rm still runs.
  case "\$*" in
    */var/lib/apt/lists/*)
      printf 'apt-lists-reclaim\n' >>"$WORK/calls"
      return 0
      ;;
  esac
  command rm "\$@"
}
$(cat "$WORK/logfmt.sh")
$(cat "$WORK/warn.sh")
$(cat "$WORK/block.sh")
HARNESS
  : >"$WORK/installed"
  : >"$WORK/calls"
  : >"$WORK/warns"
  # stderr is kept in a file rather than counted through a pipe so a case can assert
  # WHICH refusal fired, not just how many did: the oracle's "no such package" and
  # the fallback's expansion-character refusal are different guards, and an
  # outcome-only count cannot tell them apart.
  (cd "${RUN_CWD:-$PWD}" && bash "$WORK/harness.sh" >/dev/null 2>"$WORK/warns")
  SKIPPED=$(grep -c 'level=warn.*skipping' "$WORK/warns" || true)
  INSTALLED=$(tr '\n' ' ' <"$WORK/installed" | sed 's/ *$//')
  INSTALL_CALLS=$(grep -c 'install-call' "$WORK/calls" 2>/dev/null || true)
  DPKG_CONFIGURE_CALLS=$(grep -c 'dpkg-configure' "$WORK/calls" 2>/dev/null || true)
  RECLAIM_CALLS=$(grep -c 'apt-lists-reclaim' "$WORK/calls" 2>/dev/null || true)
}

# warn_for <token> -> the warn line that named exactly this token (empty if none)
warn_for() {
  grep -F "token=\"$1\"" "$WORK/warns" 2>/dev/null | head -1
}

# --- the gate RUNS: unchanged behaviour, expansion characters verified literally --
run 0 ok 'jq python3.13 docker.io g++ nosuchpkg.'
[ "$INSTALLED" = "jq python3.13 docker.io g++" ] \
  && ok "gate available: real '.'/'+' names install (docker.io, g++), the typo is rejected" \
  || no "gate available" "installed='$INSTALLED', want 'jq python3.13 docker.io g++'"

# --- THE OPTION-C PATH: update failed, the oracle still answers ---------------
# The whole point of the design: a partial refresh leaves a usable index, so names
# are still verified EXACTLY instead of being guessed at by character class. With
# the oracle-retry removed (the gate wrapped in `if apt_update_rc -eq 0`) this case
# reports installed='jq' -- docker.io and g++ carry expansion characters and the
# fallback drops them.
run 100 ok 'jq docker.io g++ nosuchpkg.'
[ "$INSTALLED" = "jq docker.io g++" ] \
  && ok "update failed but the oracle answers: names it proves install, including '.'/'+'" \
  || no "degraded oracle recovery" "installed='$INSTALLED', want 'jq docker.io g++'"
# And the refusal must come from the ORACLE, not the character filter: naming the
# guard is what separates "the index answered and this name is not in it" from
# "nothing could answer, so anything with a metacharacter goes". An outcome-only
# assertion is green either way.
case "$(warn_for 'nosuchpkg.')" in
  *"no such package"*) ok "degraded refusal is the oracle's own (no such package), not the character filter" ;;
  '') no "degraded refusal" "no warn named the token 'nosuchpkg.'" ;;
  *) no "degraded refusal" "wrong guard refused it: $(warn_for 'nosuchpkg.')" ;;
esac

# --- a plain typo, no metacharacter at all, must still be refused ------------
# This is what the oracle buys that no character rule can: 'nosuchpkg' is
# grammar-valid, carries neither '.' nor '+', and is not a package. Before the
# oracle retry a degraded boot installed it (apt then resolves nothing and the
# install fails wholesale, taking the valid packages with it).
run 100 ok 'jq nosuchpkg'
[ "$INSTALLED" = "jq" ] && [ "$SKIPPED" -eq 1 ] \
  && ok "degraded boot: a metacharacter-free typo is refused by the oracle, not installed" \
  || no "degraded plain typo" "installed='$INSTALLED' skipped=$SKIPPED, want 'jq' / 1"

# --- an INCOMPLETE index only ever loses a package, never admits one ---------
# 'docker.io' is real but absent from the surviving half, so it is skipped as
# unknown and installs on a boot whose index is complete. That false negative is
# the entire cost of trusting a partial index.
run 100 partial 'jq docker.io'
case "$(warn_for docker.io)" in
  *"no such package"*)
    [ "$INSTALLED" = "jq" ] \
      && ok "partial index: a name it does not list waits for the next boot (false negative only)" \
      || no "partial index" "installed='$INSTALLED', want 'jq'"
    ;;
  *) no "partial index" "docker.io was not refused as unknown: installed='$INSTALLED' warn='$(warn_for docker.io)'" ;;
esac

# --- THE REGRESSION: no usable oracle, so '+' and '.' must both be dropped ---
# `jq++`, `libjq+1` and `libj+q1` are all grammar-valid and all regex-resolve
# through apt's unanchored fallback ('+' is one-or-more, so the metacharacter is
# live ANYWHERE in the token, not only as a trailing suffix). With the filter still
# dot-only, each of them reaches apt's argv and the blowup is live.
for mode in fail empty; do
  run 100 "$mode" 'jq jq++ libjq+1 libj+q1 python3.13'
  case "$INSTALLED" in
    *+* | *python3.13*) no "unusable oracle ($mode)" "an expansion-character token reached apt: '$INSTALLED'" ;;
    *)
      [ "$INSTALLED" = "jq" ] && [ "$SKIPPED" -eq 4 ] \
        && ok "unusable oracle ($mode): jq++ / libjq+1 / libj+q1 and the dotted token are all dropped" \
        || no "unusable oracle ($mode)" "installed='$INSTALLED' skipped=$SKIPPED, want 'jq' / 4"
      ;;
  esac
done

# Each dropped token must be named by the FALLBACK's refusal, and per token: a
# single count cannot tell "all four were filtered" from "one was filtered and
# three vanished somewhere else", and the operator needs the token and the reason.
run 100 fail 'jq++ libjq+1 libj+q1 python3.13'
_reasons_ok=1
for tok in 'jq++' 'libjq+1' 'libj+q1' 'python3.13'; do
  case "$(warn_for "$tok")" in
    *"expansion character"*) ;;
    *) _reasons_ok=0 ;;
  esac
done
[ "$_reasons_ok" -eq 1 ] \
  && ok "each dropped token is reported by name with the unverifiable-expansion-character reason" \
  || no "fallback reporting" "a token was not named with the expansion-character reason: $(cat "$WORK/warns")"

# --- gate unavailable with a SUCCESSFUL update (unreadable index) ------------
for mode in empty fail; do
  run 0 "$mode" 'jq gcc python3.13 nosuchpkg.'
  [ "$INSTALLED" = "jq gcc" ] \
    && ok "index $mode: plain names install, every dotted token dropped" \
    || no "index $mode" "installed='$INSTALLED', want 'jq gcc'"
done

# --- the common case must not regress: nothing to drop -----------------------
# Formerly asserted with the gate presumed dead ('jq gcc g++' silently installed on
# a failed update because '+' was not filtered). It now asserts the same silence for
# the right reason -- the oracle proves g++ -- and its counterpart below carries the
# g++ coverage for the case where nothing can prove it.
run 100 ok 'jq gcc g++'
[ "$INSTALLED" = "jq gcc g++" ] && [ "$SKIPPED" -eq 0 ] \
  && ok "degraded boot with a usable oracle: g++ installs, silently" \
  || no "undotted degraded" "installed='$INSTALLED' skipped=$SKIPPED"
run 100 fail 'jq gcc g++'
[ "$INSTALLED" = "jq gcc" ] && [ "$SKIPPED" -eq 1 ] \
  && ok "degraded boot with no oracle: g++ waits, the plain names still install" \
  || no "undotted no-oracle" "installed='$INSTALLED' skipped=$SKIPPED, want 'jq gcc' / 1"

# --- an all-dropped list must not invoke apt with an empty argv ---------------
# INSTALL_CALLS is the oracle, not the package list: an empty install records no
# packages either way, so only the invocation count can see this guard.
run 100 fail 'python3.13 g++'
[ "$INSTALL_CALLS" -eq 0 ] && [ -z "$INSTALLED" ] && [ "$SKIPPED" -eq 2 ] \
  && ok "all tokens dropped: apt is not called with an empty package list" \
  || no "all dropped" "install_calls=$INSTALL_CALLS installed='$INSTALLED' skipped=$SKIPPED"

# --- the grammar stage still owns its own rejections ------------------------
# Run with NO usable oracle (failed update AND a failing pkgnames): with the gate
# up, these tokens would be rejected by the known-name check too, and this case
# could go green with the grammar deleted. That is where only the grammar stands
# between a hostile token and apt's argv, so that is where it is asserted. 'jq-' is
# the canary: name-shaped enough to reach apt if the grammar's anchor broke, absent
# from any known-name list, and carrying neither '.' nor '+' (so the fallback's
# expansion-character drop cannot mask its rejection either).
# `etc/passwd` carries no dot either, so the fallback cannot mask it: it is the
# traversal/slash canary the way `jq-` is the anchor canary, and it isolates the
# grammar's slash rejection that `../etc/passwd` alone cannot.
# `../etc/passwd` stays for the dotted-traversal shape.
run 100 fail 'jq ../etc/passwd etc/passwd jq- -0day'
case "$INSTALLED" in
  *jq-* | *passwd* | *opt:*) no "grammar" "a grammar-invalid token reached apt: '$INSTALLED'" ;;
  *)
    [ "$INSTALLED" = "jq" ] \
      && ok "grammar rejections hold with no oracle at all (traversal, remove-suffix, option-like)" \
      || no "grammar" "installed='$INSTALLED', want 'jq'"
    ;;
esac

# --- the glob-suppression guard: a stray '*' must stay LITERAL ----------------
# entrypoint.sh runs with cwd=/workspace, so with pathname expansion live a '*'
# token expands to filenames and any expansion that is grammar-valid AND a real
# package gets apt-installed as root on every boot. The bait is what the UNGUARDED
# code would actually take: a cwd holding a file named like a package the
# known-name stub accepts. With `set -f` present the token stays literal, fails the
# grammar (no '*' in its class) and is warn-skipped; with `set -f` gone this case
# reports installed='jq gcc'.
# The bait filename stays inside the stub's known-name list (`gcc`) so the assertion
# fails at the INSTALL argv rather than being masked by the known-name gate, and
# carries neither '.' nor '+' so the fallback cannot mask it either -- the same
# canary discipline the grammar case documents for `jq-` and `etc/passwd`.
mkdir -p "$WORK/globbait" && : >"$WORK/globbait/gcc"
RUN_CWD="$WORK/globbait"
run 0 ok 'jq *'
RUN_CWD=""
[ "$INSTALLED" = "jq" ] && [ "$SKIPPED" -eq 1 ] \
  && ok "a stray '*' stays literal (set -f) and is warn-skipped" \
  || no "glob suppression" "installed='$INSTALLED' skipped=$SKIPPED -- set -f may be gone"

# --- the interrupted-dpkg recovery: AUDIT OUTPUT is the evidence, not the rc -----
# `dpkg --audit` returns 0 while REPORTING unpacked-but-unconfigured packages
# (measured on a scratch admindir: 464 bytes of stdout, rc=0), and that is the
# ordinary state a killed install leaves behind. A predicate reading only the exit
# status never fires on it, so apt-get keeps failing with rc=100 for the rest of the
# container's life -- the exact outcome the recovery was added to prevent. The
# assertion is the CONFIGURE invocation, because the recovery is warn-only and
# changes no argv: an install-list assertion is green with the recovery deleted.
run 0 ok 'jq' 'The following packages have been unpacked but not yet configured.' 0 ''
[ "$DPKG_CONFIGURE_CALLS" -eq 1 ] && [ "$INSTALLED" = "jq" ] \
  && ok "interrupted dpkg reported with rc=0: recovery runs on the audit OUTPUT, and the install proceeds" \
  || no "dpkg recovery on rc=0 output" "configure_calls=$DPKG_CONFIGURE_CALLS installed='$INSTALLED', want 1 / 'jq'"

# A non-zero audit status and a non-empty updates journal are the other two triggers;
# both predate this fix and are pinned here so a later narrowing cannot drop them.
run 0 ok 'jq' '' 2 ''
[ "$DPKG_CONFIGURE_CALLS" -eq 1 ] \
  && ok "interrupted dpkg: a failing audit still triggers the recovery" \
  || no "dpkg recovery on audit rc" "configure_calls=$DPKG_CONFIGURE_CALLS, want 1"
run 0 ok 'jq' '' 0 'unrelated-journal-entry'
[ "$DPKG_CONFIGURE_CALLS" -eq 1 ] \
  && ok "interrupted dpkg: a non-empty updates journal still triggers the recovery" \
  || no "dpkg recovery on journal" "configure_calls=$DPKG_CONFIGURE_CALLS, want 1"

# The negative half, which is what keeps the predicate from becoming "always": a
# healthy tree prints nothing, exits 0 and has an empty journal, so the recovery must
# NOT run -- otherwise every boot would reconfigure every package before installing.
run 0 ok 'jq'
[ "$DPKG_CONFIGURE_CALLS" -eq 0 ] && [ "$INSTALLED" = "jq" ] \
  && ok "healthy dpkg: no audit output, no journal, no reconfigure" \
  || no "dpkg recovery false positive" "configure_calls=$DPKG_CONFIGURE_CALLS installed='$INSTALLED', want 0 / 'jq'"

# --- the index reclaim keys on ATTEMPTED, not succeeded -----------------------
# The condition is `[ -n "${apt_update_rc:-}" ]`, and its whole point is the
# non-zero case: a PARTIAL refresh writes the index files and returns non-zero, so
# rc=0 kept ~21 MB of Debian indexes in the container layer on exactly the boots
# this file spends most of its cases on. Every `run 100` case above drives that
# branch and the harness has recorded the reclaim since it was written, so the
# assertion costs one line and nothing else can see the branch: reverting the
# condition to `-eq 0` leaves all 152 assertions green.
run 0 ok 'jq'
[ "$RECLAIM_CALLS" -eq 1 ] \
  && ok "successful update: the indexes are reclaimed" \
  || no "reclaim on rc=0" "reclaim_calls=$RECLAIM_CALLS, want 1"
run 100 ok 'jq'
[ "$RECLAIM_CALLS" -eq 1 ] \
  && ok "partial refresh (non-zero rc, index written): the indexes are still reclaimed" \
  || no "reclaim on rc!=0" "reclaim_calls=$RECLAIM_CALLS, want 1"
# The load-bearing negative, and the case the condition's own comment names: with
# every token grammar-invalid the update block never runs, apt_update_rc is UNSET,
# and there is nothing to delete. A condition of `[ -n "${apt_update_rc-x}" ]`, or
# a plain unconditional `rm -rf`, passes both cases above and fails here.
run 0 ok '-0day ../etc/passwd'
[ "$RECLAIM_CALLS" -eq 0 ] && [ -z "$INSTALLED" ] \
  && ok "no valid token: update never ran, so nothing is reclaimed" \
  || no "reclaim with update unrun" "reclaim_calls=$RECLAIM_CALLS installed='$INSTALLED', want 0 / ''"

report
