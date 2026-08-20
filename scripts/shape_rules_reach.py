"""Reachability rules for shape-audit: who actually calls this?

Three checks the compiler and golangci-lint structurally cannot make, each one
mechanizing a defect that was found by hand in this repo and only after several
rounds of review had missed it.

`unused` cannot make the first one. The fleet's golangci config sets
`tests: true`, so a reference from a `_test.go` file counts as a use — which is
correct for its purpose and blind to a production symbol that only its own test
calls. `Runtime.isHookStatusEnabled` lived that way through four review passes.

Every rule here is deliberately conservative: it suppresses anything named in an
interface (satisfying a contract is a use with no call site), anything a method
value could reach, and the well-known Go entry points. A checker that cries wolf
teaches its reader to skip findings, which is worse than not having it.
"""

import collections
import os
import pathlib
import re
import subprocess

# The repo to audit. Defaults to this script's own repo and is overridable with
# SHAPE_AUDIT_ROOT so the rules can be pointed at a sibling repo — the same fix
# cohesion.py needed. A checker that can only ever see one tree is how a whole
# codebase goes unmeasured while the report reads clean.
ROOT = pathlib.Path(
    os.environ.get("SHAPE_AUDIT_ROOT", pathlib.Path(__file__).resolve().parent.parent)
)

# Names that are called by something other than Go source in this repo: the
# runtime, the test framework, or a generated file.
ENTRY = re.compile(r"^(main|init|Test|Benchmark|Fuzz|Example)")

# A method that satisfies one of these is called through an interface the
# stdlib owns, so there is no call site to find.
STDLIB_CONTRACT = {
    "Error",
    "String",
    "Unwrap",
    "Is",
    "As",
    "MarshalJSON",
    "UnmarshalJSON",
    "ServeHTTP",
    "Read",
    "Write",
    "Close",
    "Len",
    "Less",
    "Swap",
    "Next",
    "Scan",
    "Value",
    "Format",
    "LogValue",
}


def dependency_interface_members():
    """Interface method names declared by this module's FIRST-PARTY dependencies.

    A method satisfying a third-party contract is not named by the repo that
    implements it. Running the Get-prefix rule on subflux renamed eight
    authstore methods before the compiler objected: GetUserByID, GetSessionByHash
    and six others are members of cplieger/auth's UserStore, SessionPersister,
    PasskeyStore and KeyStore. Reading only the audited repo's own interfaces
    cannot see that, so the rule reported eight findings that were not the repo's
    to fix.

    Scoped to github.com/cplieger/* requirements: those are the fleet's own
    libraries, which is where this actually bites. A rule cannot be
    self-consistent across a fleet if it flags a name the fleet's own library
    imposes.
    """
    names = set()
    if not (ROOT / "go.mod").exists():
        return names
    # `go list` rather than parsing go.mod and guessing a cache path: a /vN module
    # lives under a versioned directory, and this fleet rides unpublished majors
    # through local `replace` directives in go.work, so the real directory is
    # often a sibling checkout. Asking the toolchain is the only way to be right
    # about both.
    try:
        out = subprocess.run(
            ["go", "list", "-m", "-f", "{{.Path}} {{.Dir}}", "all"],
            cwd=ROOT,
            capture_output=True,
            text=True,
            timeout=180,
            check=False,
        ).stdout
    except (OSError, subprocess.SubprocessError):
        return names
    for line in out.split("\n"):
        parts = line.split(" ", 1)
        if len(parts) != 2:
            continue
        mod, d = parts[0], parts[1].strip()
        if not mod.startswith("github.com/cplieger/") or not d:
            continue
        if pathlib.Path(d) == ROOT:
            continue
        for f in pathlib.Path(d).rglob("*.go"):
            if f.name.endswith("_test.go") or "node_modules" in str(f):
                continue
            try:
                text = f.read_text(errors="replace")
            except OSError:
                continue
            inside = False
            for line2 in text.split("\n"):
                s = line2.strip()
                if re.match(r"^type \w+ interface \{", s):
                    inside = True
                    continue
                if inside:
                    if s.startswith("}"):
                        inside = False
                        continue
                    m = re.match(r"^(\w+)\(", s)
                    if m:
                        names.add(m.group(1))
    return names


def _go_files(include_tests):
    for p in sorted(ROOT.glob("internal/**/*.go")) + sorted(ROOT.glob("cmd/**/*.go")):
        if "node_modules" in str(p):
            continue
        if p.name.endswith("_test.go") and not include_tests:
            continue
        yield p


def _interface_member_names():
    """Every identifier that appears as a method name inside an interface block.

    A method required by an interface has no direct caller, so this set is the
    suppression list for all three rules below.
    """
    names = set()
    for p in _go_files(include_tests=True):
        inside = False
        for line in p.read_text(errors="replace").split("\n"):
            s = line.strip()
            if re.match(r"^type \w+ interface \{", s):
                inside = True
                continue
            if inside:
                if s.startswith("}"):
                    inside = False
                    continue
                m = re.match(r"^(\w+)\(", s)
                if m:
                    names.add(m.group(1))
    return names


def _declarations():
    """Yield (path, line, kind, name) for every production func and method."""
    for p in _go_files(include_tests=False):
        for i, line in enumerate(p.read_text(errors="replace").split("\n"), 1):
            m = re.match(r"^func \((\w+) \*?(\w+)\) (\w+)\(", line)
            if m:
                yield p, i, "method", m.group(3)
                continue
            m = re.match(r"^func (\w+)\(", line)
            if m:
                yield p, i, "func", m.group(1)


def _reference_counts():
    """Count references to every identifier, split by production vs test file."""
    prod = collections.Counter()
    test = collections.Counter()
    for p in _go_files(include_tests=True):
        text = p.read_text(errors="replace")
        # Strip DECLARATIONS so a symbol never counts as its own use. Two kinds,
        # and missing the second made rule_unreferenced_interface_method vacuous:
        # a func declaration, and a member line inside an interface block. An
        # interface member declaring itself kept every count at one, so the rule
        # could never reach zero and reported nothing on a planted violation.
        keep, inside = [], False
        for ln in text.split("\n"):
            s = ln.strip()
            if re.match(r"^type \w+ interface \{", s):
                inside = True
                continue
            if inside:
                if s.startswith("}"):
                    inside = False
                continue
            if re.match(r"^func (\(\w+ \*?\w+\) )?\w+\(", ln):
                continue
            keep.append(ln)
        body = "\n".join(keep)
        counter = test if p.name.endswith("_test.go") else prod
        for ident in re.findall(r"\b([A-Za-z_]\w*)\b", body):
            counter[ident] += 1
    return prod, test


def rule_test_only_production(report):
    """A production symbol whose only references are in _test.go files.

    Either it is dead and the test is keeping it alive, or the behaviour is real
    and its production caller was removed — both are findings. `unused` cannot
    see this class because the fleet config counts test usage as usage.
    """
    iface = _interface_member_names()
    prod, test = _reference_counts()
    for p, line, _kind, name in _declarations():
        if ENTRY.match(name) or name in iface or name in STDLIB_CONTRACT:
            continue
        if prod[name] == 0 and test[name] > 0:
            rel = p.relative_to(ROOT)
            report(
                "test-only-production",
                f"{rel}:{line} {name} has no production caller ({test[name]} test refs)",
            )


def rule_unreferenced_interface_method(report):
    """An interface method nothing calls.

    The interface is wider than its consumer needs, which is the least-mechanism
    principle applied to a contract: every member is something an implementer
    must supply and a reader must account for.
    """
    prod, test = _reference_counts()
    for p in _go_files(include_tests=False):
        inside = None
        for i, line in enumerate(p.read_text(errors="replace").split("\n"), 1):
            s = line.strip()
            m = re.match(r"^type (\w+) interface \{", s)
            if m:
                inside = m.group(1)
                continue
            if inside:
                if s.startswith("}"):
                    inside = None
                    continue
                mm = re.match(r"^(\w+)\(", s)
                if mm and prod[mm.group(1)] == 0 and test[mm.group(1)] == 0:
                    rel = p.relative_to(ROOT)
                    report(
                        "unreferenced-interface-method",
                        f"{rel}:{i} {inside}.{mm.group(1)} is called nowhere",
                    )


def _method_names_by_type():
    """Map each method name to the set of types declaring it.

    A name declared on more than one type is POLYMORPHIC even without an
    interface: three forge providers each had their own viewPR hitting a
    different CLI. Converting one to a free function and rewriting `p.viewPR(` to
    `viewPR(` across the package silently pointed gitea's and github's calls at
    gitlab's implementation — a behaviour break the compiler accepted, because
    the signatures matched. `unused` is what caught it, by reporting the two
    orphaned methods.
    """
    owners = collections.defaultdict(set)
    for p in _go_files(include_tests=True):
        for line in p.read_text(errors="replace").split("\n"):
            m = re.match(r"^func \(\w+ \*?(\w+)\) (\w+)\(", line)
            if m:
                owners[m.group(2)].add(m.group(1))
    return owners


def rule_method_ignores_receiver(report):
    """A method that never mentions its receiver.

    It is a function with extra ceremony: the receiver implies a dependency on
    the type's state that the body does not have. Interface members are exempt —
    an implementation may legitimately ignore state the contract allows it to.
    """
    iface = _interface_member_names()
    owners = _method_names_by_type()
    for p in _go_files(include_tests=False):
        lines = p.read_text(errors="replace").split("\n")
        for i, line in enumerate(lines):
            m = re.match(r"^func \((\w+) \*?(\w+)\) (\w+)\(", line)
            if not m:
                continue
            recv, typ, name = m.groups()
            if recv == "_" or name in iface or name in STDLIB_CONTRACT:
                continue
            if len(owners.get(name, ())) > 1:
                # Declared on several types: polymorphic in practice, so it is
                # not a free function even when this one body ignores its state.
                continue
            # Two traps in finding the body, both of which produced false
            # findings before they were fixed:
            #
            #   - a one-line body lives on the signature line, and collecting from
            #     i+1 walked into the NEXT function, reporting Runtime.Runs (whose
            #     whole body is `return rt.runs`) as ignoring its receiver.
            #   - splitting on the FIRST `{` lands inside the signature whenever a
            #     parameter is a composite type. Every method taking
            #     `map[string]struct{}` was reported, which is all six of
            #     kirosession.Reaper's sweep methods, and every one of them uses
            #     its receiver.
            #
            # A line ending in `{` opens a multi-line body; anything else carries
            # its body inline after the LAST brace.
            # Strip a trailing line comment first: a signature ending in
            # `{ //nolint:gocritic ...` does not end with the brace, so the
            # inline-body path read the COMMENT as the body and reported
            # activity.Log.startLocked, which uses its receiver eight times.
            code = re.sub(r"//.*$", "", line).rstrip()
            if code.endswith("{"):
                blob = None
            else:
                lb = code.rfind("{")
                blob = code[lb + 1 :].rsplit("}", 1)[0] if lb != -1 else ""
            if blob is None:
                body, j = [], i + 1
                while j < len(lines) and lines[j] != "}":
                    body.append(lines[j])
                    j += 1
                blob = "\n".join(body)
            if not blob.strip():
                continue
            if not re.search(r"(^|[^a-zA-Z0-9_.])" + recv + r"\b", blob):
                report(
                    "method-ignores-receiver",
                    f"{p.relative_to(ROOT)}:{i + 1} {typ}.{name} never uses {recv}",
                )


def rule_stutter(report):
    """An exported name that repeats its own package name.

    Google's naming guidance judges an identifier at the CALL SITE, where the
    package qualifier is already present: `agent.AgentRuntime` says agent twice.
    """
    for p in _go_files(include_tests=False):
        text = p.read_text(errors="replace")
        m = re.search(r"^package (\w+)", text, re.MULTILINE)
        if not m:
            continue
        pkg = m.group(1)
        for i, line in enumerate(text.split("\n"), 1):
            d = re.match(r"^(?:type|func) (\w+)", line)
            if not d:
                continue
            name = d.group(1)
            if not name[0].isupper() or ENTRY.match(name):
                continue
            if not name.lower().startswith(pkg.lower()) or len(name) <= len(pkg):
                continue
            # An AGENT NOUN derived from a verb package is not the stutter Google
            # warns about. Its examples are noun repetition — http.HTTPServer,
            # strings.StringReader — where the prefix carries nothing. resolve.
            # Resolver is the same word in a different part of speech: the type IS
            # the thing the package does, and there is no shorter honest name.
            if name[len(pkg) :].lower() in ("r", "er", "or"):
                continue
            report(
                "stutter",
                f"{p.relative_to(ROOT)}:{i} {pkg}.{name} repeats the package name",
            )


REACH_RULES = [
    rule_test_only_production,
    rule_unreferenced_interface_method,
    rule_method_ignores_receiver,
    rule_stutter,
]
