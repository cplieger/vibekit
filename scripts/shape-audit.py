#!/usr/bin/env python3
"""Mechanical shape audit for this module's Go packages.

Written because three rounds of hand-auditing kept finding gaps the previous
round had missed: each pass checked whatever rules the reader happened to
remember, so "I fixed my findings" was always a weaker claim than it sounded.
A checklist that runs is the only kind that stays complete.

Every rule below cites the authority it enforces. Run it, fix, run it again,
until the exit code is 0.

    python3 scripts/shape-audit.py            # report
    python3 scripts/shape-audit.py --quiet    # exit code only

Sources:
  R2  name repeats receiver      google.github.io/styleguide/go/best-practices
                                 ("do not repeat the name of the method receiver")
  R3  Get prefix                 go.dev/wiki/CodeReviewComments (getters)
  R4  one receiver name per type google's "predictable names" (guide, Maintainability)
  R5  receiver matches its type  same; a receiver naming a former type is stale
  R9  wide consumer interface    google's coupling caution; a composite spanning
                                 several owners forces a god object to satisfy it
  R10 unexplained nolint         .kiro go-rulebook (a waiver states its reason)
  R11 stale vocabulary           a rename that leaves the old name in identifiers
                                 has only moved the problem
"""

import collections
import pathlib
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent

# Concept words a method name must not repeat from its receiver's type name.
# Keyed by type, valued by the substrings that would be repetition.
CONCEPTS = {
    "Runs": ["Run"],
    "Runtime": ["Runtime"],
    "Settings": ["Setting"],
    "BridgeCoordinator": ["Coordinator"],
    "agentTerminals": ["Terminal", "Term"],
    "mcpRegistry": ["MCP", "Registry"],
    "bridgeManager": ["Manager"],
    "utilityLease": ["Lease"],
    "utilitySession": ["Session"],
    "runRoutes": ["Route"],
    "inbound": ["Inbound"],
    "bus": ["Bus"],
    "lifetime": ["Lifetime"],
}

# Receiver abbreviation expected for a type: derived, with explicit exceptions
# for the stdlib-style one-letter cases.
RECEIVER = {
    "Runtime": "rt",
    "Runs": "rs",
    "Settings": "st",
    "bus": "b",
    "lifetime": "lt",
    "mcpRegistry": "reg",
    "runRoutes": "rr",
    "inbound": "in",
    "BridgeCoordinator": "bc",
    "agentTerminals": "at",
    "bridgeManager": "bm",
    "sharedBridge": "sb",
    "utilityLease": "l",
    "utilitySession": "us",
    "utilityAgent": "ua",
    "ShellManager": "sm",
    "mcpRecorder": "r",
}

# Identifiers that named a type or package this module has renamed away from.
STALE = {
    "hub": "internal/hub became internal/agent; Hub became Runtime",
    "Plane": "the plane suffix described the structure of the code, not the problem",
}
STALE_EXEMPT = re.compile(r"sse\.Hub|sseHub|webhttp/sse|GitHub|github")


def go_files(pkg=None):
    for p in sorted(ROOT.glob("internal/**/*.go")):
        if p.name.endswith("_test.go"):
            continue
        if pkg and pkg not in str(p.parent):
            continue
        yield p


def methods():
    """Yield (path, line_no, receiver_name, type_name, method_name)."""
    for p in go_files():
        for i, line in enumerate(p.read_text().split("\n"), 1):
            m = re.match(r"^func \((\w+) \*?(\w+)\) (\w+)\(", line)
            if m:
                yield p, i, m.group(1), m.group(2), m.group(3)


# There is deliberately NO method-count rule here, and the one that was here is
# deleted rather than tuned. It flagged a receiver over 53 methods, a figure this
# fleet derived by measuring two stdlib packages; no authority states any such
# limit, and the standard library fails it in four places (reflect.Value 97,
# go/types.Checker 195, os.File 73, time.Time 60 — while database/sql.DB is 46,
# not the 53 quoted). A gate the stdlib fails is not a rule, and chasing it splits
# types that were right.
#
# Cohesion is the property that actually discriminates, and it does not reduce to
# a pass/fail line, so it lives in scripts/cohesion.py as a report to read rather
# than a check to satisfy.
def camel_words(name):
    """Split a Go identifier into its CamelCase components.

    Whole words, not substrings: a naive `in` test reported Runs.answerUnattended
    as repeating "Run", because "answe(run)attended" contains those three letters.
    A checker that cries wolf teaches its reader to skip findings, which is worse
    than not having it.
    """
    return re.findall(r"[A-Z]+(?![a-z])|[A-Z][a-z0-9]*|^[a-z0-9]+", name)


def rule_name_repeats_receiver(f):
    for p, ln, _, typ, name in methods():
        words = {w.lower() for w in camel_words(name)}
        for concept in CONCEPTS.get(typ, []):
            if concept.lower() in words and name.lower() != concept.lower():
                f("R2", f"{p.name}:{ln} {typ}.{name} repeats {concept}")
                break


def rule_get_prefix(f):
    for p, ln, _, typ, name in methods():
        # GetOrX is a get-or-CREATE, which is an action; Go's rule targets pure
        # accessors ("LookupUser, not GetUser"). Dropping the prefix there would
        # lose the "or create" the caller needs to see.
        if re.match(r"^[Gg]etOr[A-Z]", name):
            continue
        if re.match(r"^[Gg]et[A-Z]", name):
            f("R3", f"{p.name}:{ln} {typ}.{name} uses a Get prefix")


def rule_one_receiver_per_type(f):
    # Keyed by PACKAGE and type. Keying by type name alone reported Buffer and
    # Store as having two receivers each, because several packages declare a type
    # by those names — the same trap that invents a god object out of ten
    # different Handlers, recorded in architecture.md.
    seen = collections.defaultdict(set)
    for p, _, recv, typ, _ in methods():
        seen[(str(p.parent.relative_to(ROOT)), typ)].add(recv)
    for (pkg, typ), recvs in sorted(seen.items()):
        if len(recvs) > 1:
            f(
                "R4",
                f"{pkg}.{typ} has {len(recvs)} receiver names: {', '.join(sorted(recvs))}",
            )


def rule_receiver_matches_type(f):
    for p, ln, recv, typ, _ in methods():
        want = RECEIVER.get(typ)
        if want and recv != want:
            f("R5", f"{p.name}:{ln} {typ} receiver is {recv}, want {want}")


def rule_wide_interfaces(f):
    """A wide interface must say WHY it is wide.

    The first version of this rule flagged anything over five members and
    produced four findings, all of them wrong: promptSlot, acpSessionFacts, prOps
    and ChatStoreContract are each one collaborator's cohesive surface, which is
    the io.ReadWriter shape Google blesses. Member count is not the property that
    matters.

    The property that matters is whether the interface spans several OWNERS —
    that is what forces a god object to satisfy it, and it is what
    translate.StreamingAccess did with eight members across six collaborators.
    Static detection of ownership is not available here, so the rule asks for the
    next best thing: a wide interface carries a doc comment, and that comment is
    where the author states the cohesion. An undocumented wide interface is the
    one worth stopping on.
    """
    for p in go_files():
        text = p.read_text()
        lines = text.split("\n")
        for m in re.finditer(r"type (\w+) interface \{(.*?)\n\}", text, re.DOTALL):
            body = m.group(2)
            n = len(re.findall(r"^\t\w+\(", body, re.MULTILINE)) + len(
                re.findall(r"^\t[A-Z]\w+$", body, re.MULTILINE)
            )
            if n <= 5:
                continue
            ln = text[: m.start()].count("\n") + 1
            if ln >= 2 and lines[ln - 2].lstrip().startswith("//"):
                continue
            f(
                "R9",
                f"{p.name}:{ln} interface {m.group(1)} has {n} members and no doc "
                "comment explaining the width",
            )


def rule_nolint_explained(f):
    for p in go_files():
        for i, line in enumerate(p.read_text().split("\n"), 1):
            # A line that is ENTIRELY a comment is prose about nolint, not a
            # directive; one such line said "no //nolint needed" and was reported.
            if line.lstrip().startswith("//"):
                continue
            if "//nolint" in line and "//" not in line.split("//nolint")[1]:
                f("R10", f"{p.name}:{i} nolint with no reason")


def rule_stale_vocabulary(f):
    for p in go_files(pkg="internal/agent"):
        for i, line in enumerate(p.read_text().split("\n"), 1):
            if STALE_EXEMPT.search(line):
                continue
            for word, why in STALE.items():
                if re.search(
                    r"\b\w*" + word + r"\w*\b", line
                ) and not line.lstrip().startswith("//"):
                    f("R11", f"{p.name}:{i} stale {word} in code ({why})")
                    break


# Deliberately NOT here, because golangci-lint already enforces them with a real
# parser and this file only had regexes: ctx-first (revive context-as-argument),
# exported doc comments (revive exported, enabled org-wide), error-string style
# (staticcheck ST1005). Each of my three reimplementations produced only false
# positives — a func-TYPE parameter containing context.Context, an env var name
# read as a capitalised sentence — which is the argument against writing them at
# all. This file's job is the SHAPE rules no linter knows about.
RULES = [
    rule_name_repeats_receiver,
    rule_get_prefix,
    rule_one_receiver_per_type,
    rule_receiver_matches_type,
    rule_wide_interfaces,
    rule_nolint_explained,
    rule_stale_vocabulary,
]


def main():
    quiet = "--quiet" in sys.argv
    found = collections.defaultdict(list)

    def report(rule, msg):
        found[rule].append(msg)

    for rule in RULES:
        rule(report)

    total = sum(len(v) for v in found.values())
    if not quiet:
        for rule in sorted(found):
            msgs = found[rule]
            print(f"{rule}  {len(msgs)} finding(s)")
            for m in msgs[:12]:
                print("    " + m)
            if len(msgs) > 12:
                print(f"    ... and {len(msgs) - 12} more")
        print(f"\nTOTAL: {total}")
    return 1 if total else 0


if __name__ == "__main__":
    sys.exit(main())
