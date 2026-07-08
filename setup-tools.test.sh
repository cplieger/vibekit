#!/bin/bash
# Integration test for setup-tools.sh helper functions.
#
# setup-tools.sh guards its install loop behind a source detection, so
# we can source it and exercise the pure helpers (expand, entry_enabled,
# entry_auto_update, requires_satisfied, write_shims, clear_tool) against
# a fixture manifest in a temp tree. No network, no real installs.
#
# Run: bash setup-tools.test.sh   (exit 0 = pass)
set -uo pipefail

FAILS=0
pass() { printf "  ok  - %s\n" "$1"; }
fail() {
  printf "  NOT ok - %s\n" "$1"
  FAILS=$((FAILS + 1))
}
assert_eq() {
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 (got '$2', want '$3')"; fi
}

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

export VK_TOOLS_DIR="$WORK/tools"
export VK_MANIFEST="$WORK/tools.json"
mkdir -p "$VK_TOOLS_DIR/bin"

cat >"$VK_MANIFEST" <<'JSON'
{
  "runtimes": {
    "node": { "enabled": true, "auto_update": true, "version": "v20.19.5" },
    "go":   { "enabled": false, "auto_update": false, "version": "1.23.4" }
  },
  "lsp": {
    "gopls": {
      "enabled": true, "version": "v0.17.1", "method": "go",
      "requires": ["runtimes.go"]
    },
    "tsgo": {
      "enabled": true, "version": "7.0.0-dev", "method": "binary",
      "shims": { "typescript-language-server": "/x/tsgo --lsp --stdio" }
    }
  }
}
JSON

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=/dev/null
source "$SCRIPT_DIR/setup-tools.sh"

# --- expand() placeholders ---
# shellcheck disable=SC2016 # literal ${...} is intentional — expand() resolves them
out=$(expand 'v=${VERSION} np=${VERSION_NOPFX} bin=${BIN}' "v1.2.3")
assert_eq "expand VERSION/NOPFX/BIN" "$out" "v=v1.2.3 np=1.2.3 bin=$VK_TOOLS_DIR/bin"

case "$(uname -m)" in
  aarch64 | arm64) want_arch="arm64" ;;
  *) want_arch="x64" ;;
esac
# shellcheck disable=SC2016 # literal ${...} is intentional
out=$(expand 'a=${ARCH_X64_OR_ARM64}' "v1")
assert_eq "expand ARCH_X64_OR_ARM64" "$out" "a=$want_arch"

# --- entry_enabled ---
if entry_enabled '.runtimes["node"]'; then pass "node enabled"; else fail "node enabled"; fi
if entry_enabled '.runtimes["go"]'; then fail "go should be disabled"; else pass "go disabled"; fi
# Missing field defaults to enabled.
if entry_enabled '.lsp["gopls"]'; then pass "gopls enabled"; else fail "gopls enabled"; fi

# --- entry_auto_update ---
if entry_auto_update '.runtimes["node"]'; then pass "node auto_update on"; else fail "node auto_update"; fi
if entry_auto_update '.runtimes["go"]'; then fail "go auto_update should be off"; else pass "go pinned"; fi
# Missing field defaults to true.
if entry_auto_update '.lsp["tsgo"]'; then pass "tsgo auto_update default on"; else fail "tsgo auto_update default"; fi

# --- requires_satisfied: gopls requires runtimes.go which is disabled ---
if requires_satisfied '.lsp["gopls"]' >/dev/null; then
  fail "gopls requires should NOT be satisfied (go disabled)"
else
  pass "gopls requires unsatisfied (go disabled)"
fi
# tsgo has no requires -> satisfied.
if requires_satisfied '.lsp["tsgo"]' >/dev/null; then pass "tsgo requires satisfied"; else fail "tsgo requires"; fi

# --- write_shims creates the wrapper ---
write_shims '.lsp["tsgo"]' >/dev/null
shim="$VK_TOOLS_DIR/bin/typescript-language-server"
if [ -x "$shim" ]; then pass "shim created + executable"; else fail "shim missing"; fi
if grep -q 'exec /x/tsgo --lsp --stdio' "$shim" 2>/dev/null; then
  pass "shim body correct"
else
  fail "shim body wrong"
fi

# --- clear_tool removes binary + shims ---
touch "$VK_TOOLS_DIR/bin/tsgo"
clear_tool lsp tsgo
if [ ! -e "$VK_TOOLS_DIR/bin/tsgo" ]; then pass "clear_tool removed binary"; else fail "binary remains"; fi
if [ ! -e "$shim" ]; then pass "clear_tool removed shim"; else fail "shim remains"; fi

# --- clear_tool with custom uninstall override ---
cat >"$VK_MANIFEST" <<'JSON'
{ "lsp": { "jdtls": {
  "enabled": true, "version": "1.40.0", "method": "binary",
  "uninstall": "rm -rf ${TOOLS}/lib/jdtls ${BIN}/jdtls"
}}}
JSON
mkdir -p "$VK_TOOLS_DIR/lib/jdtls"
touch "$VK_TOOLS_DIR/bin/jdtls"
clear_tool lsp jdtls
if [ ! -e "$VK_TOOLS_DIR/lib/jdtls" ] && [ ! -e "$VK_TOOLS_DIR/bin/jdtls" ]; then
  pass "custom uninstall override ran"
else
  fail "custom uninstall left files"
fi

echo ""
if [ "$FAILS" -eq 0 ]; then
  echo "All setup-tools.sh helper tests passed."
  exit 0
else
  echo "$FAILS test(s) failed."
  exit 1
fi
