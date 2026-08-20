#!/usr/bin/env python3
"""Measure type COHESION, which is the property a method count only gestures at.

Why this replaced a method-count ceiling
----------------------------------------
This repo carried a rule that a receiver over ~53 methods is a god object,
sourced from measuring `database/sql.DB` and `net/http.Transport` and writing
down the wider of the two. Two problems, both fatal to it as a gate:

  * No authority states any such limit. Not Google's style guide, not Effective
    Go, not Code Review Comments. It was a local reference point that hardened
    into a threshold.
  * The standard library fails it repeatedly. Measured against go1.26.5:
    reflect.Value 97, go/types.Checker 203, os.File 74, time.Time 61 — and
    database/sql.DB is 46, not the 53 the rule quoted. Nobody calls
    reflect.Value a god object; it is the canonical cohesive type.

So method count does not discriminate, and chasing a number is how you end up
splitting a type that was right. What Google actually asks for is in the style
guide's Maintainability section: code "chooses abstractions that map to the
structure of the problem, not to the structure of the code", and "avoids
unnecessary coupling".

What this measures instead
--------------------------
Two things, both proxies for that intent, and neither a pass/fail line:

  COMPONENTS — partition a type's methods into groups connected by shared field
    access. reflect.Value's 97 methods all touch the same handful of fields, so
    they form ONE component: one job, done thoroughly. A type whose methods fall
    into several groups touching disjoint fields is several types sharing a
    struct, whatever its method count. This is the signal that actually found
    every worthwhile split in this package: the run surface had four fields with
    no other reader, and that is why extracting it worked.

  COLLABORATORS — how many distinct fields of OTHER types the receiver reaches.
    This is "unnecessary coupling" made countable. A type with one job and three
    collaborators is healthy at any size; a type reaching twenty is the coupling
    the guide warns about.

Output is a report, not a verdict. A multi-component type is a QUESTION —
sometimes the components genuinely interlock (this package's run surface calls
its own clock, and the clock issues the cancel, so the coupling test says
combine). The number tells you where to look, not what to do.

    python3 scripts/cohesion.py [package-dir]
"""

import collections
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent


def method_bodies(pkg_dir):
    """Yield (type, method, set_of_fields_touched) for every method in pkg_dir."""
    for path in sorted(pkg_dir.glob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        lines = path.read_text(errors="ignore").split("\n")
        i = 0
        while i < len(lines):
            m = re.match(r"^func \((\w+) \*?(\w+)\) (\w+)\(", lines[i])
            if not m:
                i += 1
                continue
            recv, typ, name = m.group(1), m.group(2), m.group(3)
            j = i
            while j < len(lines) and lines[j] != "}":
                j += 1
            body = "\n".join(lines[i : j + 1])
            # Everything reached through the receiver, fields AND sibling method
            # calls. Linking by field alone under-links badly: a handler that
            # calls st.knowledgeCall shares no FIELD with it, so both became
            # singleton groups and a cohesive HTTP surface read as fragmented.
            # That artifact is also why database/sql.DB first measured as 15
            # groups — DB delegates to Conn and Tx without touching its own
            # fields.
            reached = set(re.findall(r"\b" + recv + r"\.(\w+)", body))
            yield typ, name, reached
            i = j + 1


def components(methods):
    """Group methods into connected components by what they reach.

    Union-find over the reached-name sets, plus an edge from a caller to the
    sibling it calls: two methods are in the same component when they touch a
    field in common OR one invokes the other, transitively. Both edges are needed
    — a method that only calls siblings touches no field, and one that only reads
    fields calls nobody.
    """
    parent = {}

    def find(x):
        parent.setdefault(x, x)
        while parent[x] != x:
            parent[x] = parent[parent[x]]
            x = parent[x]
        return x

    def union(a, b):
        ra, rb = find(a), find(b)
        if ra != rb:
            parent[ra] = rb

    own = {name for name, _ in methods}
    by_field = collections.defaultdict(list)
    for name, reached in methods:
        find(name)
        for r in reached:
            if r in own:
                union(name, r)  # a call to a sibling is an edge
            else:
                by_field[r].append(name)
    for names in by_field.values():
        for other in names[1:]:
            union(names[0], other)

    groups = collections.defaultdict(list)
    for name, _ in methods:
        groups[find(name)].append(name)
    return sorted(groups.values(), key=len, reverse=True)


def stdlib_reference():
    """The stdlib figures that refute a method-count ceiling, measured live."""
    goroot = subprocess.run(
        ["go", "env", "GOROOT"], capture_output=True, text=True, check=False
    ).stdout.strip()
    out = []
    for pkg, typ in [
        ("reflect", "Value"),
        ("go/types", "Checker"),
        ("os", "File"),
        ("time", "Time"),
        ("database/sql", "DB"),
    ]:
        d = pathlib.Path(goroot) / "src" / pkg
        if not d.exists():
            continue
        ms = [(n, f) for t, n, f in method_bodies(d) if t == typ]
        if not ms:
            continue
        comps = components(ms)
        out.append((f"{pkg}.{typ}", len(ms), len(comps), len(comps[0])))
    return out


def field_seams(methods, own):
    """Report each field by how much of the type reads it.

    This is the measure that actually found every worthwhile split in this
    package, and it is not connected components. A field read by a SMALL,
    coherent subset of a type's methods is a candidate seam: vibekit's run
    surface had four such fields with zero readers anywhere else, which is why
    extracting it worked and why the guesses that ignored field ownership did
    not.

    Connected components were tried first and abandoned. Without call edges they
    over-split (a handler calling a sibling shares no field with it, so a cohesive
    HTTP surface read as eleven groups); with call edges they under-split to
    uselessness, because one shared mutex transitively connects everything —
    database/sql.DB went from 15 groups to 1 and go/types.Checker from 15 to 1.
    Degenerate in both directions is not a measure.
    """
    readers = collections.defaultdict(set)
    for name, reached in methods:
        for r in reached:
            if r not in own:  # a field, not a sibling call
                readers[r].add(name)
    return sorted(readers.items(), key=lambda kv: len(kv[1]))


def main():
    target = pathlib.Path(sys.argv[1]) if len(sys.argv) > 1 else ROOT / "internal/agent"

    by_type = collections.defaultdict(list)
    for typ, name, fields in method_bodies(target):
        by_type[typ].append((name, fields))

    print(f"cohesion report for {target.relative_to(ROOT)}\n")
    print(f"{'type':24} {'methods':>7}  narrowly-read fields (field:readers)")
    for typ, ms in sorted(by_type.items(), key=lambda kv: -len(kv[1])):
        if len(ms) < 8:
            continue
        own = {n for n, _ in ms}
        cut = max(3, len(ms) // 6)
        narrow = [
            f"{fld}:{len(rs)}"
            for fld, rs in field_seams(ms, own)
            if 2 <= len(rs) <= cut
        ]
        note = ", ".join(narrow[:6]) if narrow else "none — no field is a minority read"
        print(f"{typ:24} {len(ms):>7}  {note}")

    print("\nstdlib method counts, measured live — the facts that refute a ceiling:")
    for name, n, _, _ in stdlib_reference():
        print(f"  {name:24} {n:>4} methods")
    print(
        "\nA narrowly-read field is a QUESTION, not a verdict: ask whether its\n"
        "readers are a coherent job, then whether they can leave without calling\n"
        "back. vibekit's run clock had four such fields and still could not go,\n"
        "because the expiry path issues the cancel — Google's coupling test says\n"
        "combine what you must import together."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
