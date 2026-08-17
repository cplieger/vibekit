#!/usr/bin/env python3
"""Enumerate every rule pair whose winner flips if a stylesheet leaves its @layer.

Removing `@layer components` from 10-shell-app.css changes three classes of
comparison, not one, so a specificity-only audit under-reports:

  1. vs a LATER unlayered file  - today the layer loses always; after, specificity
     decides, so the pair flips when this file's rule is MORE specific.
  2. vs `@layer utilities` / `@layer mobile` (30-utilities.css, 50-mobile.css) -
     today those layers outrank `components` whatever the specificity; after,
     unlayered outranks them, so EVERY colliding pair flips.
  3. vs an EARLIER unlayered file (03-base.css, the ui-primitives skin) - today
     unlayered beats the layer; after, both are unlayered and this file is later
     in concatenation order, so it wins ties too.

Winner model for two normal declarations: higher layer rank, then specificity,
then later position in the concatenated bundle. Overlap is decided structurally:
two subjects can match one element when their positive class/id constraints nest,
they share at least one class or id, neither negates what the other requires,
and element name + pseudo-element agree.

Usage: python3 scripts/css-cascade-audit.py [--file 10-shell-app.css] [--loose]
"""

from __future__ import annotations

import argparse
import re
from pathlib import Path

SRC = Path(__file__).resolve().parent.parent / "static-src"
CSS_DIR = SRC / "css"
WTUI_DIR = SRC / "node_modules" / "@cplieger" / "web-terminal-ui" / "css"

# Declared in 00-header.css: @layer reset, tokens, components, utilities, mobile;
# An unlayered rule sits in the implicit outer layer, which outranks all of them.
LAYER_RANK = {"reset": 1, "tokens": 2, "components": 3, "utilities": 4, "mobile": 5}
UNLAYERED = 99

FUNCTIONAL_MAX = ("is", "not", "has", "matches", "any")
SKIP_AT = (
    "@keyframes",
    "@font-face",
    "@property",
    "@page",
    "@counter-style",
    "@view-transition",
)


def read_manifest(path: Path, base: Path) -> list[tuple[str, Path]]:
    out = []
    for line in path.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith("#"):
            out.append((line, base / line))
    return out


def bundle_order() -> list[tuple[str, Path]]:
    """The concatenation order cmd/bundle/main.go builds style.css from."""
    parts: list[tuple[str, Path]] = []
    touch = WTUI_DIR / "MANIFEST.touch"
    if touch.exists():
        parts += read_manifest(touch, WTUI_DIR)
    parts += read_manifest(CSS_DIR / "MANIFEST", CSS_DIR)
    return [(n, p) for n, p in parts if p.exists()]


def strip_comments(css: str) -> str:
    """Blank comments out WITHOUT losing their newlines.

    Collapsing a multi-line comment to one space silently shifts every reported
    line number below it — 10-shell-app.css is comment-heavy enough that
    `.tab-close` came out 34 lines early, which is the difference between a
    citable coordinate and a wrong one.
    """
    return re.sub(
        r"/\*.*?\*/",
        lambda m: "\n" * m.group(0).count("\n") or " ",
        css,
        flags=re.DOTALL,
    )


def split_top_level(s: str, sep: str = ",") -> list[str]:
    out, buf, depth, quote = [], [], 0, None
    for ch in s:
        if quote:
            buf.append(ch)
            if ch == quote:
                quote = None
            continue
        if ch in "\"'":
            quote = ch
            buf.append(ch)
            continue
        if ch in "([":
            depth += 1
        elif ch in ")]":
            depth -= 1
        if ch == sep and depth == 0:
            out.append("".join(buf))
            buf = []
        else:
            buf.append(ch)
    out.append("".join(buf))
    return [p.strip() for p in out if p.strip()]


def close_at(s: str, start: int, o: str = "(", c: str = ")") -> int:
    depth, j = 0, start
    while j < len(s):
        if s[j] == o:
            depth += 1
        elif s[j] == c:
            depth -= 1
            if depth == 0:
                return j
        j += 1
    return len(s) - 1


def specificity(sel: str) -> tuple[int, int, int]:
    a = b = c = 0
    i, n = 0, len(sel)
    while i < n:
        ch = sel[i]
        if ch == "#":
            mm = re.match(r"#[-\w\\]+", sel[i:])
            a += 1
            i += mm.end() if mm else 1
        elif ch == ".":
            mm = re.match(r"\.[-\w\\]+", sel[i:])
            b += 1
            i += mm.end() if mm else 1
        elif ch == "[":
            b += 1
            i = close_at(sel, i, "[", "]") + 1
        elif ch == ":":
            if sel.startswith("::", i):
                mm = re.match(r"::[-\w]+", sel[i:])
                c += 1
                i += mm.end() if mm else 2
                continue
            mm = re.match(r":([-\w]+)(\()?", sel[i:])
            if not mm:
                i += 1
                continue
            name = mm.group(1).lower()
            if mm.group(2):
                j = close_at(sel, i + mm.end() - 1)
                inner = sel[i + mm.end() : j]
                if name == "where":
                    pass
                elif name in FUNCTIONAL_MAX:
                    best = max(
                        (specificity(p) for p in split_top_level(inner)),
                        default=(0, 0, 0),
                    )
                    a, b, c = a + best[0], b + best[1], c + best[2]
                elif name in ("nth-child", "nth-last-child"):
                    b += 1
                    if " of " in inner:
                        best = max(
                            (
                                specificity(p)
                                for p in split_top_level(inner.split(" of ", 1)[1])
                            ),
                            default=(0, 0, 0),
                        )
                        a, b, c = a + best[0], b + best[1], c + best[2]
                else:
                    b += 1
                i = j + 1
            else:
                if name in ("before", "after", "first-line", "first-letter"):
                    c += 1
                else:
                    b += 1
                i += mm.end()
        elif ch.isalpha() or ch in "*|":
            mm = re.match(r"[-\w]+(\|[-\w]+)?|\*", sel[i:])
            if mm:
                if mm.group(0) != "*":
                    c += 1
                i += mm.end()
            else:
                i += 1
        else:
            i += 1
    return (a, b, c)


def rightmost_compound(sel: str) -> str:
    depth, cut, quote = 0, 0, None
    for i, ch in enumerate(sel):
        if quote:
            if ch == quote:
                quote = None
            continue
        if ch in "\"'":
            quote = ch
            continue
        if ch in "([":
            depth += 1
        elif ch in ")]":
            depth -= 1
        elif depth == 0 and ch in " >+~":
            cut = i + 1
    return sel[cut:]


def keys_of(compound: str) -> tuple[frozenset[str], frozenset[str], str | None, str]:
    pos: set[str] = set()
    neg: set[str] = set()
    elem: str | None = None
    pseudo = ""
    i, n = 0, len(compound)
    while i < n:
        ch = compound[i]
        if ch == ".":
            mm = re.match(r"\.([-\w]+)", compound[i:])
            if mm:
                pos.add("." + mm.group(1))
                i += mm.end()
                continue
            i += 1
        elif ch == "#":
            mm = re.match(r"#([-\w]+)", compound[i:])
            if mm:
                pos.add("#" + mm.group(1))
                i += mm.end()
                continue
            i += 1
        elif ch == "[":
            j = close_at(compound, i, "[", "]")
            body = compound[i + 1 : j].strip()
            mm = re.match(r"id\s*=\s*[\"']?([-\w]+)", body)
            pos.add("#" + mm.group(1) if mm else "[" + body + "]")
            i = j + 1
        elif ch == ":":
            if compound.startswith("::", i):
                mm = re.match(r"::([-\w]+)", compound[i:])
                pseudo = mm.group(1) if mm else ""
                i += mm.end() if mm else 2
                continue
            mm = re.match(r":([-\w]+)(\()?", compound[i:])
            if not mm:
                i += 1
                continue
            name = mm.group(1).lower()
            if name in ("before", "after", "first-line", "first-letter"):
                pseudo = name
            if mm.group(2):
                j = close_at(compound, i + mm.end() - 1)
                inner = compound[i + mm.end() : j]
                if name == "not":
                    for part in split_top_level(inner):
                        p, _, _, _ = keys_of(rightmost_compound(part))
                        neg |= set(p)
                elif name in ("is", "where", "has", "matches", "any"):
                    parts = split_top_level(inner)
                    if len(parts) == 1:
                        p, _, _, _ = keys_of(rightmost_compound(parts[0]))
                        pos |= set(p)
                i = j + 1
            else:
                i += mm.end()
        elif ch.isalpha() or ch == "*":
            mm = re.match(r"[a-zA-Z][-\w]*|\*", compound[i:])
            if mm:
                if mm.group(0) != "*" and elem is None and i == 0:
                    elem = mm.group(0).lower()
                i += mm.end()
            else:
                i += 1
        else:
            i += 1
    return frozenset(pos), frozenset(neg), elem, pseudo


class Rule:
    __slots__ = (
        "at",
        "elem",
        "file",
        "layer",
        "line",
        "neg",
        "order",
        "pos",
        "props",
        "pseudo",
        "selector",
        "spec",
    )

    def __init__(self, file, line, order, selector, props, layer, at):
        self.file = file
        self.line = line
        self.order = order  # position in the concatenated bundle
        self.selector = " ".join(selector.split())
        self.props = props
        self.spec = specificity(selector)
        self.layer = layer
        self.pos, self.neg, self.elem, self.pseudo = keys_of(
            rightmost_compound(selector)
        )
        self.at = at

    @property
    def where(self) -> str:
        return f"{self.file}:{self.line}"

    @property
    def layer_name(self) -> str:
        if self.layer == UNLAYERED:
            return "unlayered"
        for k, v in LAYER_RANK.items():
            if v == self.layer:
                return f"@layer {k}"
        return f"layer{self.layer}"


def parse(
    path: Path, name: str, order: int, force_layer: int | None = None
) -> list[Rule]:
    css = strip_comments(path.read_text())
    rules: list[Rule] = []
    i, n, line = 0, len(css), 1
    # stack entries: (parent selectors, at-context, layer)
    stack: list[tuple[list[str], str, int]] = [([], "", UNLAYERED)]
    buf: list[str] = []

    while i < n:
        ch = css[i]
        if ch == "\n":
            line += 1
            buf.append(ch)
            i += 1
            continue
        if ch == ";":
            buf = []
            i += 1
            continue
        if ch == "}":
            buf = []
            if len(stack) > 1:
                stack.pop()
            i += 1
            continue
        if ch != "{":
            buf.append(ch)
            i += 1
            continue

        prelude = " ".join("".join(buf).split())
        buf = []
        parents, at, layer = stack[-1]

        if prelude.startswith("@"):
            atname = prelude.split()[0].lower()
            if atname in SKIP_AT:
                depth, j = 1, i + 1
                while j < n and depth:
                    if css[j] == "{":
                        depth += 1
                    elif css[j] == "}":
                        depth -= 1
                    elif css[j] == "\n":
                        line += 1
                    j += 1
                i = j
                continue
            if atname == "@layer":
                lname = prelude[len("@layer") :].strip().strip("{").strip()
                stack.append((parents, at, LAYER_RANK.get(lname, layer)))
            else:
                stack.append((parents, (at + " " + prelude).strip(), layer))
            i += 1
            continue

        sels: list[str] = []
        for part in split_top_level(prelude):
            if "&" in part:
                for p in parents or [""]:
                    sels.append(part.replace("&", p))
            elif parents:
                for p in parents:
                    sels.append(f"{p} {part}")
            else:
                sels.append(part)
        sels = [" ".join(s.split()) for s in sels if s.strip()]

        depth, j = 1, i + 1
        dbuf: list[str] = []
        own: list[str] = []
        while j < n and depth:
            cj = css[j]
            if cj == "{":
                depth += 1
                dbuf = []
            elif cj == "}":
                depth -= 1
                dbuf = []
            elif depth == 1:
                if cj == ";":
                    own.append("".join(dbuf))
                    dbuf = []
                else:
                    dbuf.append(cj)
            j += 1
        props = set()
        for d in own:
            d = d.strip()
            if ":" in d and not d.startswith("--"):
                prop = d.split(":", 1)[0].strip().lower()
                if re.fullmatch(r"-?[a-z][-a-z]*", prop):
                    props.add(prop)

        eff = force_layer if force_layer is not None else layer
        for s in sels:
            rules.append(Rule(name, line, order, s, props, eff, at))
        stack.append((sels, at, layer))
        i += 1
    return rules


def can_match_same_element(a: Rule, b: Rule, loose: bool = False) -> bool:
    if a.pseudo != b.pseudo:
        return False
    if a.elem and b.elem and a.elem != b.elem:
        return False
    if a.pos & b.neg or b.pos & a.neg:
        return False
    ca = {bridge(k) for k in a.pos if k[0] in ".#"}
    cb = {bridge(k) for k in b.pos if k[0] in ".#"}
    if not (ca & cb):
        return False
    if loose:
        return True
    return ca <= cb or cb <= ca


def wins(a: Rule, b: Rule, a_layer: int, b_layer: int) -> str:
    """Which of the two rules wins, given each one's effective layer rank.

    Both ranks are arguments rather than read off the rules, because the
    unlayering pass asks the same question twice about the same pair — once with
    the shipped ranks and once with the hypothetical ones — and mutating a Rule
    between the two calls would make the second answer depend on the first.
    """
    if a_layer != b_layer:
        return "a" if a_layer > b_layer else "b"
    if a.spec != b.spec:
        return "a" if a.spec > b.spec else "b"
    return "a" if a.order > b.order else "b"


def bridge(key: str) -> str:
    """Fold `#name` onto `.name`.

    This app names an element's id and its component class identically
    (`<button id="send-btn" class="send-btn">`), so `[id="send-btn"]` in one
    stylesheet and `.send-btn` in another are the SAME element and their pairing
    is decided by the cascade. Keying them apart hides those pairs entirely.
    """
    return "." + key[1:] if key.startswith("#") else key


def collect(loose: bool) -> tuple[list[Rule], dict[str, list[Rule]]]:
    rules: list[Rule] = []
    for k, (name, path) in enumerate(bundle_order()):
        rules.extend(parse(path, name, k))
    index: dict[str, list[Rule]] = {}
    for r in rules:
        for k in r.pos:
            if k[0] in ".#":
                index.setdefault(bridge(k), []).append(r)
    del loose
    return rules, index


def colliding_pairs(rules: list[Rule], index: dict[str, list[Rule]], loose: bool):
    """Yield (a, b, shared) once per unordered colliding pair, a earlier than b."""
    emitted: set[tuple[int, int]] = set()
    for a in rules:
        if not a.props:
            continue
        for k in a.pos:
            if k[0] not in ".#":
                continue
            for b in index.get(bridge(k), ()):
                if a is b or not b.props:
                    continue
                lo, hi = sorted((id(a), id(b)))
                if (lo, hi) in emitted:
                    continue
                shared = a.props & b.props
                if not shared or not can_match_same_element(a, b, loose):
                    continue
                emitted.add((lo, hi))
                yield a, b, sorted(shared)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument(
        "--unlayer",
        default="10-shell-app.css",
        help="comma-separated stylesheets to treat as unlayered in the AFTER state",
    )
    ap.add_argument("--loose", action="store_true", help="report any shared-class pair")
    ap.add_argument(
        "--losers", help="instead: list rules in this file that currently LOSE"
    )
    args = ap.parse_args()

    unlayer = {s.strip() for s in args.unlayer.split(",") if s.strip()}
    rules, index = collect(args.loose)
    print(f"bundle: {len(bundle_order())} stylesheets, {len(rules)} flattened rules")

    if args.losers:
        print(
            f"\nrules in {args.losers} overridden by another stylesheet, as shipped:\n"
        )
        found = 0
        for a, b, shared in colliding_pairs(rules, index, args.loose):
            for x, y in ((a, b), (b, a)):
                if x.file != args.losers:
                    continue
                if wins(x, y, x.layer, y.layer) == "b":  # y wins
                    found += 1
                    print(
                        f"  LOSES  {x.where} spec={x.spec} {x.layer_name}  {x.selector}"
                    )
                    print(
                        f"  to     {y.where} spec={y.spec} {y.layer_name}  {y.selector}"
                    )
                    print(f"    dead properties: {', '.join(shared)}")
                    if x.at or y.at:
                        print(f"    at-context: [{x.at or '-'}] vs [{y.at or '-'}]")
                    print()
        if not found:
            print("  none")
        return 0

    print(f"unlayering: {', '.join(sorted(unlayer))}\n")
    reversals = []
    for a, b, shared in colliding_pairs(rules, index, args.loose):
        la_before, lb_before = a.layer, b.layer
        la_after = UNLAYERED if a.file in unlayer else a.layer
        lb_after = UNLAYERED if b.file in unlayer else b.layer
        if (la_before, lb_before) == (la_after, lb_after):
            continue
        before = wins(a, b, la_before, lb_before)
        after = wins(a, b, la_after, lb_after)
        if before != after:
            reversals.append((a, b, shared, before, after))

    if not reversals:
        print(
            "CASCADE-CHANGE SET: EMPTY (0 rules change outcome).\n\n"
            "No pair of rules that declare the same property and can match the same\n"
            "element has a winner that depends on the layer change. Behaviour-neutral."
        )
        return 0

    print(f"CASCADE-CHANGE SET: {len(reversals)} rule pair(s) flip:\n")
    for a, b, shared, before, after in sorted(
        reversals, key=lambda h: (h[0].order, h[0].line, h[1].order, h[1].line)
    ):
        win_before = a if before == "a" else b
        win_after = a if after == "a" else b
        print(f"  A {a.where} spec={a.spec} {a.layer_name}  {a.selector}")
        print(f"  B {b.where} spec={b.spec} {b.layer_name}  {b.selector}")
        if a.at or b.at:
            print(f"    at-context: A[{a.at or '-'}] B[{b.at or '-'}]")
        print(f"    contested: {', '.join(shared)}")
        print(f"    winner: {win_before.where} -> {win_after.where}\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
