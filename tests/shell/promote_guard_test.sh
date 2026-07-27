#!/usr/bin/env bash
# is_self_contained_executable(): the predicate that decides whether a candidate
# binary may be promoted out of the install staging tree into $TOOLS/bin.
#
# install_kiro_cli runs the upstream installer under a PRIVATE staging HOME and an
# EXIT trap that deletes that stage on every return path. So a candidate that is a
# SYMLINK into the stage passes both `-f` and `-x` while the stage still exists, gets
# promoted as a link, and dangles the moment the trap fires — after the script has
# already logged a successful install and written the readiness story on top of a
# dead path. The image smoke test cannot see this: the failure needs an installer
# that produces a link rather than a file, which a working upstream install never
# does. This file drives the real predicate against each shape it must refuse.
# Lint directives for this whole file, each against a stated guarantee rather than
# an assumption:
#   SC2015 - the assertion form `[ cond ] && ok "..." || no "..."` cannot mis-fire,
#     because lib.sh's ok/no return 0 unconditionally by design (see their comment).
# shellcheck disable=SC2015
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

load_function is_self_contained_executable

STAGE="$WORK/stage"
mkdir -p "$STAGE"
REAL="$STAGE/kiro-cli"
printf '#!/bin/sh\necho hi\n' >"$REAL"
chmod +x "$REAL"

is_self_contained_executable "$REAL" \
  && ok "a regular executable file is accepted" \
  || no "regular executable" "the promotion candidate was refused"

# The bait is one the UNGUARDED predicate would take, and that is asserted rather
# than assumed: `-f` and `-x` both follow the link, so a `-f && -x` check says yes to
# exactly this shape. Only the -L test refuses it.
LINK="$WORK/link-kiro-cli"
ln -s "$REAL" "$LINK"
if [ -f "$LINK" ] && [ -x "$LINK" ]; then
  ! is_self_contained_executable "$LINK" \
    && ok "a symlink into the staging tree is refused, though -f and -x both accept it" \
    || no "symlink candidate" "it would be promoted as a link and dangle when the stage is cleaned"
else
  no "symlink candidate" "harness fault: the bait is not one -f/-x would accept"
fi

PLAIN="$WORK/plain"
printf 'not a program\n' >"$PLAIN"
chmod 644 "$PLAIN"
! is_self_contained_executable "$PLAIN" \
  && ok "a regular file with no execute bit is refused" \
  || no "non-executable file" "accepted a file that cannot be run"

# A directory answers YES to -x (that is search permission), so without the -f test
# a directory left where the binary should be would be promoted.
DIR="$WORK/adir"
mkdir -p "$DIR"
! is_self_contained_executable "$DIR" \
  && ok "a directory is refused even though -x accepts it" \
  || no "directory candidate" "accepted a directory as the installed binary"

! is_self_contained_executable "$WORK/does-not-exist" \
  && ok "an absent path is refused" \
  || no "absent path" "accepted a path with nothing at it"

report
