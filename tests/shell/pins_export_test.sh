#!/usr/bin/env bash
# The shell/Go boundary: entrypoint.sh declares the kiro-cli pins and the Go
# server installs from them, so the two halves agree only by name. Nothing else
# checks that agreement, and every way of breaking it is SILENT:
#
#   - drop an `export` and the server sees no pins, resolves kiro-cli by bare name
#     and turns its readiness gate OFF -- a container that reports healthy while
#     installing nothing;
#   - rename a literal and Renovate's custom datasource stops matching, so the pin
#     quietly stops being bumped;
#   - rename an env var on either side and the same fallback applies;
#   - change where the install root lives on one side only and the server writes
#     into a tree this script never created.
#
# So each assertion reads BOTH sides at run time: the shipped entrypoint and the
# Go file that consumes it.
#
# Lint directives, each against a stated guarantee:
#   SC2015 - the assertion form `[ cond ] && ok "..." || no "..."` cannot mis-fire,
#     because lib.sh's ok/no return 0 unconditionally by design (see their comment).
#   SC2016 - the grep patterns below must stay single-quoted: they match LITERAL
#     text in the shipped files, not an expansion.
# shellcheck disable=SC2015,SC2016
set -u

# shellcheck source-path=SCRIPTDIR
. "$(dirname -- "$0")/lib.sh"
new_workdir >/dev/null

REPO=$(cd -- "$(dirname -- "$0")/../.." && pwd)
# Overridable for the same reason lib.sh makes $ENTRYPOINT overridable: the
# red-check a maintainer runs when adding a case here mutates a /tmp COPY of the
# file and confirms the new assertion actually fails against it. Half of these
# assertions read the Go side, so without this seam that half could not be
# red-checked at all -- and an assertion nobody has seen fail is not evidence.
CONFIG_GO="${VIBEKIT_CONFIG_GO:-$REPO/internal/composition/config.go}"
KIROCLI_GO="${VIBEKIT_KIROCLI_GO:-$REPO/internal/composition/kirocli.go}"
GO_SOURCE_ROOT="${VIBEKIT_GO_SOURCE_ROOT:-$REPO}"
README="${VIBEKIT_README:-$REPO/README.md}"
# A precondition, not decoration: an unreadable config.go makes every cross-file
# assertion below fail for the same reason a genuine drift would, so it has to be
# fatal for the section rather than reported as a drift.
if [ ! -r "$CONFIG_GO" ]; then
  printf 'harness error: internal/composition/config.go is not readable at %s\n' "$CONFIG_GO" >&2
  exit 1
fi
if [ ! -r "$KIROCLI_GO" ]; then
  printf 'harness error: internal/composition/kirocli.go is not readable at %s\n' "$KIROCLI_GO" >&2
  exit 1
fi
if [ ! -r "$README" ] || [ ! -d "$GO_SOURCE_ROOT" ]; then
  printf 'harness error: README (%s) or Go source root (%s) is unreadable\n' "$README" "$GO_SOURCE_ROOT" >&2
  exit 1
fi

# --- the Renovate literals still look the way the datasource expects ----------
# cplieger/.github's customDatasources match on these exact comment anchors; the
# version + amd64 sha are rewritten as a pair, and the arm64 sha's
# `# kiro-cli <version>` trailer is its own version anchor.
grep -q '^# renovate: datasource=custom.kiro-cli depName=kiro-cli$' "$ENTRYPOINT" \
  && ok "the amd64 Renovate anchor comment is intact" \
  || no "amd64 Renovate anchor" "the '# renovate: datasource=custom.kiro-cli depName=kiro-cli' line is gone; the version + sha pair will stop being bumped"

grep -q '^# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64$' "$ENTRYPOINT" \
  && ok "the arm64 Renovate anchor comment is intact" \
  || no "arm64 Renovate anchor" "the '# renovate: datasource=custom.kiro-cli-arm64 depName=kiro-cli-arm64' line is gone"

PIN_VERSION=$(sed -n 's/^KIRO_CLI_VERSION="\([^"]*\)"$/\1/p' "$ENTRYPOINT")
[ -n "$PIN_VERSION" ] \
  && ok "KIRO_CLI_VERSION is a bare double-quoted literal ($PIN_VERSION)" \
  || no "KIRO_CLI_VERSION literal" "no 'KIRO_CLI_VERSION=\"...\"' line; the datasource's rewrite target is gone"

grep -qE '^KIRO_CLI_SHA256="[0-9a-f]{64}"$' "$ENTRYPOINT" \
  && ok "KIRO_CLI_SHA256 is a bare 64-hex literal" \
  || no "KIRO_CLI_SHA256 literal" "not a bare 64-hex double-quoted literal"

# The trailer is the arm64 digest's version anchor, so it must name the SAME
# version the pin declares -- a stale trailer sends the datasource looking up the
# wrong release's digest.
grep -qE "^KIRO_CLI_SHA256_ARM64=\"[0-9a-f]{64}\" # kiro-cli ${PIN_VERSION}\$" "$ENTRYPOINT" \
  && ok "KIRO_CLI_SHA256_ARM64 carries a 64-hex literal and a trailer naming the pinned version" \
  || no "KIRO_CLI_SHA256_ARM64 literal" "missing the 64-hex value or the '# kiro-cli $PIN_VERSION' trailer"

# --- every pin the server needs is exported, and read under the same name -----
# Sourcing the file is not an option (it creates /config and execs the server), so
# the export is read from the text -- but paired with the Go read, so a rename on
# either side fails here.
for var in KIRO_CLI_VERSION KIRO_CLI_SHA256 KIRO_CLI_SHA256_ARM64; do
  if grep -qE "^export ([A-Z0-9_]+ )*${var}( |$)" "$ENTRYPOINT"; then
    ok "$var is exported to the server"
  else
    no "$var export" "entrypoint.sh never exports it, so the server falls back to bare-name kiro-cli with its readiness gate off"
  fi
  if grep -q "\"$var\"" "$CONFIG_GO"; then
    ok "$var is read by config.go under that exact name"
  else
    no "$var read" "internal/composition/config.go does not mention \"$var\"; the exported pin reaches nothing"
  fi
done

# --- KIRO_CLI_PATH is GONE, on both surfaces --------------------------------
# It was an operator env var whose whole effect was to stand the install manager
# down: vibekit ran that binary verbatim and /api/health stopped reporting
# kiro-cli readiness. Deleting the variable deleted that mode, so the manager is
# now the only source of the binary path. Re-adding the read is the way the mode
# comes back, and it comes back SILENTLY (vibekit still resolves *a* kiro-cli), so
# its absence is asserted on both surfaces an operator or a developer would reach
# for: the Go sources that could read it, and the README table that would
# advertise it.
#
# Dot-directories are excluded because they are not source: .git, .github, and any
# scratch tree a tool left behind (a stale COPY of config.go there would fail this
# assertion while the shipped code is clean).
if grep -rq --include='*.go' --exclude-dir='.*' 'KIRO_CLI_PATH' "$GO_SOURCE_ROOT"; then
  no "KIRO_CLI_PATH in Go sources" "a Go source still mentions KIRO_CLI_PATH; reading it stands the install manager down and takes kiro-cli readiness off /api/health"
else
  ok "no Go source mentions KIRO_CLI_PATH: the install manager is the only source of the binary path"
fi

grep -q 'KIRO_CLI_PATH' "$README" \
  && no "KIRO_CLI_PATH in the README" "the README still documents KIRO_CLI_PATH; an operator setting a variable vibekit ignores gets no error and no managed install either" \
  || ok "the README config table does not offer KIRO_CLI_PATH"

# --- the tools tree both halves compute independently -------------------------
# The manager installs into <tools dir>/kiro-cli-versions/<version>/, and the tools dir
# is NOT exported: the entrypoint derives it from CONFIG_DIR and the server derives
# it from KIRO_CONFIG_DIR. Deliberately one contract with two derivations rather
# than a second env var for a path vibekit already has a knob for
# (VIBEKIT_TOOLS_DIR) -- but that makes the two derivations the thing to pin.
grep -q '^TOOLS="\$CONFIG_DIR/tools"$' "$ENTRYPOINT" \
  && ok "the entrypoint derives \$TOOLS as \$CONFIG_DIR/tools" \
  || no "entrypoint tools dir" "TOOLS is no longer \$CONFIG_DIR/tools, so it may not be the tree the server installs into"

# Matches the cmp.Or shape envx/v2 requires: String() no longer takes a fallback
# parameter, so the default is composed at the call site. The property being
# pinned is unchanged -- both halves must derive <configDir>/tools -- and this
# grep is deliberately loose about the wrapper so it pins the DERIVATION rather
# than one spelling of it.
grep -qF 'envx.String("VIBEKIT_TOOLS_DIR")' "$CONFIG_GO" \
  && grep -qF 'filepath.Join(configDir, "tools")' "$CONFIG_GO" \
  && ok "the server derives the same tools dir from its config dir" \
  || no "server tools dir" "config.go no longer defaults ToolsDir to <configDir>/tools; the server may install outside the tree this script created"

# --- the install root is created here, before the server writes into it -------
# It is a SIBLING of the toolbelt engine's opt/ tree, not a child of it. The
# engine's per-tool prune removes every version directory under opt/<tool> that is
# not the one it just installed, and it accepts any tool name from a hand-editable
# manifest -- so an entry named `kiro-cli` under opt/ would delete the active
# kiro-cli and its retained predecessor.
grep -qF 'mkdir -p "$TOOLS/bin" "$TOOLS/kiro-cli-versions"' "$ENTRYPOINT" \
  && ok "the boot mkdir creates \$TOOLS/kiro-cli-versions, the version-install root" \
  || no "install root mkdir" "the install root is not created by the boot mkdir, so a fresh volume's first install has to create it inside the server, outside this script's /config failure branch"

grep -qE 'mkdir -p [^&|]*"\$TOOLS/(opt|npm|python|bin)/' "$ENTRYPOINT" \
  && no "install root under a toolbelt tree" "the boot creates a directory INSIDE one of the toolbelt engine's own trees (opt/npm/python/bin); the engine's per-tool prune and bin republish own everything under those" \
  || ok "the boot creates nothing inside a toolbelt-engine tree, so the engine's prune cannot reach the kiro-cli install"

# --- the shell installer is GONE, not merely unused --------------------------
# Two installers on one volume is the state this change exists to end: the shell
# one promoting into $TOOLS/bin while the server publishes version directories and
# then PURGES $TOOLS/bin/kiro-cli*. Leaving a dormant function behind is how that
# comes back, so its absence is asserted rather than assumed.
for gone in install_kiro_cli needs_kiro_cli_install kiro_cli_version \
  is_self_contained_executable kiro_setting; do
  grep -q "^${gone}()" "$ENTRYPOINT" \
    && no "$gone removed" "entrypoint.sh still defines $gone(); the server owns the install now, and two installers write the same tree" \
    || ok "$gone() is gone from the entrypoint"
done

# The one kiro-cli function that STAYED, and why: it prunes kiro-cli's DATA dir
# (the ~240 MB agent-runtime trees), not the install, and it has to run before the
# server can unpack a new one.
grep -q '^prune_superseded_kas_runtimes()' "$ENTRYPOINT" \
  && ok "prune_superseded_kas_runtimes() stayed: it prunes the data dir, not the install" \
  || no "kas pruner" "the agent-runtime pruner is gone; nothing reclaims the ~240 MB tree each superseded version leaves on the volume"

# --- the taint flag: absent on BOTH sides, deliberately ----------------------
# web-terminal-kiro exports KIRO_CLI_TOOLS_TAINTED=1 when its hardening pass found
# the tools tree writable by others, and its manager then refuses to activate any
# pre-existing version directory (a `.complete` sentinel is forgeable, unlike a
# digest). vibekit has no hardening pass to make that observation, so it exports
# nothing and reads nothing -- and this assertion pins that AGREEMENT, because
# either half alone is worse than neither: an export nothing reads is a no-op that
# looks like a guard, and a read with no producer reports every boot as clean.
# Grow the hardening and this is the line that tells you to wire both ends.
if grep -q 'KIRO_CLI_TOOLS_TAINTED' "$ENTRYPOINT" || grep -q 'KIRO_CLI_TOOLS_TAINTED' "$CONFIG_GO"; then
  grep -q 'KIRO_CLI_TOOLS_TAINTED' "$ENTRYPOINT" && grep -q 'KIRO_CLI_TOOLS_TAINTED' "$CONFIG_GO" \
    && ok "the taint flag is exported by the entrypoint AND read by config.go" \
    || no "taint flag half-wired" "only one side mentions KIRO_CLI_TOOLS_TAINTED: an export nothing reads is a no-op that looks like a guard, and a read with no producer reports every boot as clean"
else
  ok "neither side claims a taint observation vibekit cannot make (no hardening pass here)"
fi

# --- the LIBRARY half of the same non-claim: pinstall.Untrusted --------------
# The block above reads the env var; this reads the pinstall.Config field the env
# var would feed. pinstall renamed it Tainted -> Untrusted at v2, so a grep for
# the old spelling passes vacuously and this one has to name the current field.
# Untrusted restricts activation to versions THIS process installed, on the
# strength of having found the install root writable by others -- an observation
# only a hardening pass produces, and vibekit has none. Setting it would be a
# guard with no producer, which reports every boot as clean while looking like a
# check. kirocli.go states that non-claim in a comment and CITES this assertion;
# without it the citation names nothing, and the field can be set by anyone who
# reads pinstall's docs and not that comment.
if grep -q 'Untrusted:' "$KIROCLI_GO"; then
  no "Untrusted claimed" "internal/composition/kirocli.go now sets pinstall.Untrusted, but vibekit has no hardening pass to make that observation: the field then reports every boot as clean while looking like a check"
else
  ok "the Go side sets no pinstall.Untrusted (the observation has no producer here)"
fi

report
