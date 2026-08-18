#!/usr/bin/env bash
# Runs every entrypoint.sh unit test in this directory.
#
# This filename is the contract: cplieger/ci's shell-ci.yaml runs
# `tests/shell/run.sh` when it exists, and skips otherwise, so a repo opts into
# shell unit testing by committing this file. Keep the name.
#
# The hook tests -f and invokes this through `bash`, so the exec bit is not
# load-bearing (it was committed 100644 once, which under an -x check would have
# skipped the whole suite silently and still reported CI green). The bit is set
# anyway, for anyone running it directly.
#
# WHAT THIS REPO'S SUITE COVERS. This file is repo-owned (lib.sh and
# harness_test.sh beside it are synced from cplieger/ci), so the per-repo scope
# rationale lives here.
#
# entrypoint.sh IS vibekit's boot path, and it is now a SHORT one: it declares the
# Renovate-pinned kiro-cli literals and exports them, creates and proves /config,
# prunes the superseded kiro-cli agent-runtime trees, sweeps the legacy $HOME
# residue, and execs the server. The INSTALL is no longer here — the Go server owns
# it (the cplieger/pinstall library, wired in internal/composition/kirocli.go), so
# the download, the digest verification, the version selection and the settings
# reassertion are the library's own tests, and the shell tests that drove
# `install_kiro_cli` and its promotion sequence were deleted with it rather
# than left asserting against code that no longer ships.
#
# What remains here is what the entrypoint still does, and its most consequential
# branches are the ones that REFUSE or that cross the boundary INTO the server: a
# root `rm -rf` aimed at a symlinked or unconfirmable agent-runtime store, a
# /config that cannot be created, the pins-plus-install-root contract the
# server reads (pins_export_test.sh), and what does or does not reach `apt-get
# install` from APT_PACKAGES (apt_packages_test.sh). A healthy image never takes
# the refusals, so tests/image-smoke.sh — which boots the assembled image and
# waits for its HEALTHCHECK — structurally cannot reach them: it can only prove
# the paths a working container walks. And the boundary test covers what a smoke
# test cannot SEE: a dropped export leaves a container that boots, reports
# healthy, and installs nothing.
#
# apt_packages_test.sh is a VERBATIM copy of sister app web-terminal-kiro's,
# because the block it drives is a verbatim copy too: that env var is untrusted
# operator content handed to a root package manager, and every guard in it was
# paid for by a measured failure there (a grammar-valid typo reaching apt's
# regex fallback plans 337 packages). Keeping the test identical is what makes
# the two copies checkable against each other; if the block is ever extracted
# into cplieger/ci, this file goes with it.
#
# The refusals also need asserting DIRECTLY rather than through an exit code,
# for two reasons specific to this file. First, the boot runs without `set -e`
# and without `set -u`, so a guard that stops firing degrades silently instead
# of aborting. Second, hygiene here is warn-never-fatal by invariant (a dev-box
# container must stay repairable from inside), so every one of the prune guards
# returns 0 whether it refused or deleted — the outcome a test can see is the
# on-disk tree and the warning line, never the status.
#
# Each *_test.sh is a separate process, so one test's stubs, traps and shell
# options cannot leak into another's. All of them run even when an early one
# fails: a boot path's tests are cheap, and a maintainer wants the whole picture
# from one CI log rather than one failure at a time.
set -u

cd -- "$(dirname -- "$0")" || exit 1

failed=0
ran=0
for t in ./*_test.sh; do
  # A glob that matches nothing expands to itself; treat that as a harness fault
  # rather than a green run, since an empty suite passing silently is how a
  # test directory quietly stops testing anything.
  if [ ! -f "$t" ]; then
    printf 'harness error: no *_test.sh found in %s\n' "$PWD" >&2
    exit 1
  fi
  printf '=== %s\n' "$(basename "$t")"
  if bash "$t"; then
    ran=$((ran + 1))
  else
    ran=$((ran + 1))
    failed=$((failed + 1))
  fi
  printf '\n'
done

if [ "$failed" -ne 0 ]; then
  printf 'FAILED: %d of %d entrypoint test files failed\n' "$failed" "$ran" >&2
  exit 1
fi
printf 'all %d entrypoint test files passed\n' "$ran"
