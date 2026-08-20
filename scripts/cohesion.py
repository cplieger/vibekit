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
            fields = set(re.findall(r"\b" + recv + r"\.(\w+)", body))
            yield typ, name, fields
            i = j + 1


def components(methods):
    """Group methods into connected components by shared field access.

    Union-find over the field sets: two methods are in the same component when
    they touch a field in common, transitively.
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

    by_field = collections.defaultdict(list)
    for name, fields in methods:
        find(name)
        for fld in fields:
            by_field[fld].append(name)
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


def main():
    target = pathlib.Path(sys.argv[1]) if len(sys.argv) > 1 else ROOT / "internal/agent"

    by_type = collections.defaultdict(list)
    for typ, name, fields in method_bodies(target):
        by_type[typ].append((name, fields))

    print(f"cohesion report for {target.relative_to(ROOT)}\n")
    print(f"{'type':24} {'methods':>7} {'components':>11}  largest / rest")
    for typ, ms in sorted(by_type.items(), key=lambda kv: -len(kv[1])):
        if len(ms) < 8:
            continue
        comps = components(ms)
        rest = sum(len(c) for c in comps[1:])
        note = "one job" if len(comps) == 1 else f"{len(comps)} groups"
        print(
            f"{typ:24} {len(ms):>7} {len(comps):>11}  {len(comps[0])} / {rest}  {note}"
        )

    print("\nstdlib reference, measured live (the figures that refute a ceiling):")
    print(f"{'type':24} {'methods':>7} {'components':>11}")
    for name, n, ncomp, largest in stdlib_reference():
        print(f"{name:24} {n:>7} {ncomp:>11}  largest {largest}")
    print(
        "\nA high method count with ONE component is reflect.Value: one job, done\n"
        "thoroughly. Several components is the question worth asking. Neither is a\n"
        "verdict — components that genuinely interlock belong together, and Google's\n"
        "coupling test is what settles it."
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
