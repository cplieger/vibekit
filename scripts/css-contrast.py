#!/usr/bin/env python3
"""Measure WCAG contrast between vibekit's design tokens, per theme.

Resolves the token graph in static-src/css/01-tokens.css for both themes
(`:root` = dark, `:root[data-theme="light"]` = light), including `var()`
indirection, `oklch()` with alpha, and `color-mix()` in oklch and srgb, then
reports contrast ratios.

Colour path: OKLCH -> OKLab -> linear sRGB -> clip -> sRGB encode -> 8-bit ->
WCAG relative luminance. Clipping is a naive per-channel clamp where a browser
would gamut-map, so a heavily out-of-gamut colour is approximate; every token
measured here is in gamut (reported when it is not).

Usage:
  python3 scripts/css-contrast.py ramp          # the surface ramp, both themes
  python3 scripts/css-contrast.py pairs         # the interaction/selection pairs
  python3 scripts/css-contrast.py text          # text-on-surface AA checks
  python3 scripts/css-contrast.py selected      # the inks that sit ON a selected fill
  python3 scripts/css-contrast.py shadow        # elevation layers vs the surface below
  python3 scripts/css-contrast.py ansi          # the ansi_up palette vs the terminal base
  python3 scripts/css-contrast.py show TOKEN..  # resolve named tokens
  python3 scripts/css-contrast.py all
"""

from __future__ import annotations

import math
import re
import sys
from pathlib import Path

TOKENS = Path(__file__).resolve().parent.parent / "static-src" / "css" / "01-tokens.css"


# ---------------------------------------------------------------- colour maths


class Colour:
    """A colour in gamma-encoded sRGB (0..1 per channel) plus alpha."""

    __slots__ = ("r", "g", "b", "a", "clipped")

    def __init__(self, r: float, g: float, b: float, a: float = 1.0, clipped: bool = False):
        self.r, self.g, self.b, self.a, self.clipped = r, g, b, a, clipped

    def __repr__(self) -> str:
        return f"#{self.hex()}" + ("" if self.a >= 1 else f" a={self.a:.3f}")

    def hex(self) -> str:
        return "".join(f"{round(max(0.0, min(1.0, c)) * 255):02x}" for c in (self.r, self.g, self.b))

    def over(self, bg: "Colour") -> "Colour":
        """Composite self over an opaque backdrop (CSS composites in sRGB)."""
        if self.a >= 1:
            return Colour(self.r, self.g, self.b, 1.0, self.clipped)
        t = self.a
        return Colour(
            self.r * t + bg.r * (1 - t),
            self.g * t + bg.g * (1 - t),
            self.b * t + bg.b * (1 - t),
            1.0,
            self.clipped or bg.clipped,
        )

    def luminance(self) -> float:
        """WCAG 2.x relative luminance, from the 8-bit value a display shows."""

        def lin(c: float) -> float:
            v = round(max(0.0, min(1.0, c)) * 255) / 255
            return v / 12.92 if v <= 0.04045 else ((v + 0.055) / 1.055) ** 2.4

        return 0.2126 * lin(self.r) + 0.7152 * lin(self.g) + 0.0722 * lin(self.b)


def srgb_encode(x: float) -> float:
    return 12.92 * x if x <= 0.0031308 else 1.055 * (x ** (1 / 2.4)) - 0.055


def srgb_decode(x: float) -> float:
    return x / 12.92 if x <= 0.04045 else ((x + 0.055) / 1.055) ** 2.4


def oklch_to_colour(L: float, C: float, H: float, a: float) -> Colour:
    h = math.radians(H)
    A, B = C * math.cos(h), C * math.sin(h)
    l_ = L + 0.3963377774 * A + 0.2158037573 * B
    m_ = L - 0.1055613458 * A - 0.0638541728 * B
    s_ = L - 0.0894841775 * A - 1.2914855480 * B
    l, m, s = l_**3, m_**3, s_**3
    r = 4.0767416621 * l - 3.3077115913 * m + 0.2309699292 * s
    g = -1.2684380046 * l + 2.6097574011 * m - 0.3413193965 * s
    b = -0.0041960863 * l - 0.7034186147 * m + 1.7076147010 * s
    clipped = any(v < -1e-4 or v > 1 + 1e-4 for v in (r, g, b))
    return Colour(*(srgb_encode(max(0.0, min(1.0, v))) for v in (r, g, b)), a, clipped)


def colour_to_oklab(c: Colour) -> tuple[float, float, float]:
    r, g, b = (srgb_decode(v) for v in (c.r, c.g, c.b))
    l = 0.4122214708 * r + 0.5363325363 * g + 0.0514459929 * b
    m = 0.2119034982 * r + 0.6806995451 * g + 0.1073969566 * b
    s = 0.0883024619 * r + 0.2817188376 * g + 0.6299787005 * b
    l_, m_, s_ = (v ** (1 / 3) if v >= 0 else -((-v) ** (1 / 3)) for v in (l, m, s))
    return (
        0.2104542553 * l_ + 0.7936177850 * m_ - 0.0040720468 * s_,
        1.9779984951 * l_ - 2.4285922050 * m_ + 0.4505937099 * s_,
        0.0259040371 * l_ + 0.7827717662 * m_ - 0.8086757660 * s_,
    )


def colour_to_oklch(c: Colour) -> tuple[float, float, float]:
    L, A, B = colour_to_oklab(c)
    C = math.hypot(A, B)
    H = math.degrees(math.atan2(B, A)) % 360
    return L, C, H


def mix_oklch(c1: Colour, p1: float, c2: Colour) -> Colour:
    """color-mix(in oklch, c1 p1, c2 (1-p1)) with premultiplied alpha."""
    L1, C1, H1 = colour_to_oklch(c1)
    L2, C2, H2 = colour_to_oklch(c2)
    p2 = 1 - p1
    # Powerless hue when chroma is zero (transparent black, greys).
    if C1 < 1e-6:
        H1 = H2
    if C2 < 1e-6:
        H2 = H1
    d = ((H2 - H1 + 540) % 360) - 180  # shorter arc, CSS default
    H = (H1 + d * p2) % 360
    a = c1.a * p1 + c2.a * p2
    if a <= 1e-9:
        return Colour(0, 0, 0, 0)
    # Premultiplied interpolation: mixing with `transparent` must not darken.
    L = (L1 * c1.a * p1 + L2 * c2.a * p2) / a
    C = (C1 * c1.a * p1 + C2 * c2.a * p2) / a
    out = oklch_to_colour(L, C, H, a)
    return out


def mix_srgb(c1: Colour, p1: float, c2: Colour) -> Colour:
    p2 = 1 - p1
    a = c1.a * p1 + c2.a * p2
    if a <= 1e-9:
        return Colour(0, 0, 0, 0)
    ch = []
    for x, y in ((c1.r, c2.r), (c1.g, c2.g), (c1.b, c2.b)):
        ch.append((x * c1.a * p1 + y * c2.a * p2) / a)
    return Colour(ch[0], ch[1], ch[2], a, c1.clipped or c2.clipped)


def contrast(fg: Colour, bg: Colour) -> float:
    a, b = fg.luminance(), bg.luminance()
    lo, hi = min(a, b), max(a, b)
    return (hi + 0.05) / (lo + 0.05)


# ------------------------------------------------------------- token resolving

NAMED = {
    "transparent": Colour(0, 0, 0, 0),
    "white": Colour(1, 1, 1),
    "black": Colour(0, 0, 0),
    "currentcolor": None,
}


def split_args(s: str) -> list[str]:
    out, buf, depth = [], [], 0
    for ch in s:
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth -= 1
        if ch == "," and depth == 0:
            out.append("".join(buf).strip())
            buf = []
        else:
            buf.append(ch)
    if "".join(buf).strip():
        out.append("".join(buf).strip())
    return out


def func_body(s: str, name: str) -> str | None:
    i = s.find(name + "(")
    if i < 0:
        return None
    depth, j = 0, i + len(name)
    while j < len(s):
        if s[j] == "(":
            depth += 1
        elif s[j] == ")":
            depth -= 1
            if depth == 0:
                return s[i + len(name) + 1 : j]
        j += 1
    return None


class Theme:
    def __init__(self, decls: dict[str, str], name: str):
        self.decls = decls
        self.name = name
        self._cache: dict[str, Colour] = {}

    def resolve(self, expr: str, _depth: int = 0) -> Colour:
        if _depth > 24:
            raise RecursionError(expr)
        e = " ".join(expr.split())
        low = e.lower()

        if low in NAMED and NAMED[low] is not None:
            return NAMED[low]

        if low.startswith("var("):
            inner = func_body(e, "var")
            assert inner is not None
            parts = split_args(inner)
            name = parts[0]
            if name in self.decls:
                return self.resolve(self.decls[name], _depth + 1)
            if len(parts) > 1:
                return self.resolve(parts[1], _depth + 1)
            raise KeyError(f"{name} is READ but never DECLARED in the {self.name} theme")

        if low.startswith("over("):
            # Not CSS. `over(A, B)` is alpha compositing — what a browser
            # actually paints when a translucent A sits on an opaque B — which
            # no CSS function expresses, and which color-mix() is NOT: a 50/50
            # mix of a 15% wash and a surface is a different colour from that
            # wash composited onto it.
            inner = func_body(e, "over")
            assert inner is not None
            fg, bg = (self.resolve(p, _depth + 1) for p in split_args(inner))
            return fg.over(bg)

        if low.startswith("color-mix("):
            inner = func_body(e, "color-mix")
            assert inner is not None
            args = split_args(inner)
            space = args[0].strip().lower()
            if not space.startswith("in "):
                raise ValueError(f"color-mix needs a space: {e}")
            space = space[3:].strip().split()[0]
            c_specs = args[1:]
            colours, pcts = [], []
            for spec in c_specs:
                m = re.search(r"(-?[\d.]+)%\s*$", spec)
                if m:
                    pcts.append(float(m.group(1)) / 100)
                    colours.append(self.resolve(spec[: m.start()].strip(), _depth + 1))
                else:
                    pcts.append(None)
                    colours.append(self.resolve(spec, _depth + 1))
            if len(colours) != 2:
                raise ValueError(f"expected 2 colours: {e}")
            p0, p1 = pcts
            if p0 is None and p1 is None:
                p0 = p1 = 0.5
            elif p0 is None:
                p0 = 1 - p1
            elif p1 is None:
                p1 = 1 - p0
            total = p0 + p1
            if total <= 0:
                raise ValueError(f"zero-weight mix: {e}")
            w0 = p0 / total
            if space == "oklch":
                return mix_oklch(colours[0], w0, colours[1])
            if space in ("srgb", "srgb-linear"):
                return mix_srgb(colours[0], w0, colours[1])
            raise ValueError(f"unsupported mix space {space}: {e}")

        if low.startswith("oklch("):
            inner = func_body(e, "oklch")
            assert inner is not None
            alpha = 1.0
            if "/" in inner:
                inner, av = inner.rsplit("/", 1)
                av = av.strip()
                alpha = float(av[:-1]) / 100 if av.endswith("%") else float(av)
            nums = inner.replace("deg", " ").split()
            L = float(nums[0][:-1]) / 100 if nums[0].endswith("%") else float(nums[0])
            C = float(nums[1])
            # `none` is a MISSING hue, and that is the only spelling that makes
            # the hue powerless in a browser's color-mix() — an explicit `0deg`
            # is a real hue and gets interpolated, which rotated every status
            # colour mixed against the achromatic inks. Zero is the right value
            # to carry here because `mix_oklch` re-derives a powerless hue from
            # the other operand whenever chroma is zero, and a chroma-zero
            # colour's hue cannot affect its own sRGB value either way.
            H = 0.0
            if len(nums) > 2 and nums[2] != "none":
                H = float(nums[2])
            return oklch_to_colour(L, C, H, alpha)

        if low.startswith("rgb(") or low.startswith("rgba("):
            inner = func_body(e, "rgba" if low.startswith("rgba(") else "rgb")
            assert inner is not None
            alpha = 1.0
            if "/" in inner:
                inner, av = inner.rsplit("/", 1)
                av = av.strip()
                alpha = float(av[:-1]) / 100 if av.endswith("%") else float(av)
            parts = [p for p in re.split(r"[,\s]+", inner.strip()) if p]
            if len(parts) == 4:  # legacy rgba(r, g, b, a)
                av = parts.pop()
                alpha = float(av[:-1]) / 100 if av.endswith("%") else float(av)
            ch = [float(p[:-1]) / 100 if p.endswith("%") else float(p) / 255 for p in parts]
            return Colour(ch[0], ch[1], ch[2], alpha)

        m = re.fullmatch(r"#([0-9a-fA-F]{3,8})", e)
        if m:
            h = m.group(1)
            if len(h) == 3:
                h = "".join(c * 2 for c in h)
            vals = [int(h[i : i + 2], 16) / 255 for i in range(0, len(h), 2)]
            a = vals[3] if len(vals) > 3 else 1.0
            return Colour(vals[0], vals[1], vals[2], a)

        raise ValueError(f"cannot resolve {e!r}")

    def colour(self, token: str) -> Colour:
        if token not in self._cache:
            if token not in self.decls:
                raise KeyError(f"{token} is not declared in the {self.name} theme")
            self._cache[token] = self.resolve(self.decls[token])
        return self._cache[token]

    def flat(self, token: str, backdrop: str | None = None) -> Colour:
        """A token composited over a backdrop token if it is translucent."""
        c = self.colour(token) if token.startswith("--") else self.resolve(token)
        if c.a < 1 and backdrop:
            c = c.over(self.flat(backdrop))
        return c


def block_body(src: str, start: int) -> str:
    i = src.index("{", start)
    depth = 0
    for j in range(i, len(src)):
        if src[j] == "{":
            depth += 1
        elif src[j] == "}":
            depth -= 1
            if depth == 0:
                return src[i + 1 : j]
    raise ValueError("unbalanced")


def parse_themes() -> tuple[Theme, Theme]:
    src = re.sub(r"/\*.*?\*/", " ", TOKENS.read_text(), flags=re.S)

    def decls_of(body: str) -> dict[str, str]:
        # strip nested at-rule blocks so a nested @media does not leak in
        out, depth, buf = {}, 0, []
        i = 0
        while i < len(body):
            ch = body[i]
            if ch == "{":
                depth += 1
            elif ch == "}":
                depth -= 1
            elif depth == 0:
                buf.append(ch)
            i += 1
        for d in "".join(buf).split(";"):
            d = d.strip()
            if d.startswith("--") and ":" in d:
                k, v = d.split(":", 1)
                out[k.strip()] = v.strip()
        return out

    root = re.search(r":root\s*\{", src)
    assert root
    dark = decls_of(block_body(src, root.start()))

    light = dict(dark)
    m = re.search(r':root\[data-theme="light"\]\s*\{', src)
    if m:
        light.update(decls_of(block_body(src, m.start())))
    return Theme(dark, "dark"), Theme(light, "light")


# ------------------------------------------------------------------- reporting


def fmt(v: float) -> str:
    return f"{v:.3f}"


def show_ramp(themes: list[Theme], rungs: list[str]) -> None:
    for th in themes:
        present = [r for r in rungs if r in th.decls]
        print(f"  {th.name}:")
        print(f"    {'token':<22} {'hex':<9} {'Y':<8}  step vs previous")
        prev = None
        for r in present:
            c = th.flat(r)
            step = "" if prev is None else f"{fmt(contrast(c, prev))}:1"
            flag = "  OUT OF GAMUT" if c.clipped else ""
            print(f"    {r:<22} {c.hex():<9} {fmt(c.luminance()):<8}  {step}{flag}")
            prev = c
        if len(present) >= 2:
            e2e = contrast(th.flat(present[0]), th.flat(present[-1]))
            print(f"    end to end ({present[0]} -> {present[-1]}): {fmt(e2e)}:1")
        print()


def show_pairs(themes: list[Theme], pairs: list[tuple[str, str, str, float | None]]) -> None:
    """Contrast each pair, compositing a translucent first colour ONTO the second.

    A wash measured as if it were opaque is not a measurement of anything: a 16%
    white border read 19.7:1 against the base before this composited it, when
    what a reader sees is 1.5:1. `b` is the backdrop, so `a` over `b` is the
    physical arrangement for every pair here (a border on a surface, an inset on
    a surface, ink on a fill).
    """
    for th in themes:
        print(f"  {th.name}:")
        for label, a, b, floor in pairs:
            try:
                cb = th.flat(b)
                ca = th.flat(a)
                if ca.a < 1:
                    ca = ca.over(cb)
            except (KeyError, ValueError) as exc:
                print(f"    {label:<44} n/a  ({exc})")
                continue
            r = contrast(ca, cb)
            verdict = ""
            if floor is not None:
                verdict = "  PASS" if r >= floor else f"  FAIL (want {floor}:1)"
            print(f"    {label:<44} {fmt(r)}:1  [{ca.hex()} vs {cb.hex()}]{verdict}")
        print()


# The inks that sit ON a selected fill. `--c-selected-fg` reaches only the
# descendants that INHERIT it, and a row's metadata and status glyphs declare
# their own colour, so each needs an on-selected variant. Two constructions:
# muted metadata mixes toward the FILL (no hue to protect, stays on the row's
# own axis), a status hue mixes toward the row's INK (lightens on dark, darkens
# on light, and `color-mix(in oklch)` leaves H alone so the hue survives).
SELECTED_INKS: list[tuple[str, str, str]] = [
    # (token, the colour it is mixed with, the direction label)
    ("--c-selected-muted-fg", "var(--c-selected-bg)", "toward fill"),
    ("--c-selected-green-fg", "var(--c-green)", "toward ink"),
    ("--c-selected-red-fg", "var(--c-red)", "toward ink"),
    ("--c-selected-yellow-fg", "var(--c-yellow)", "toward ink"),
    ("--c-selected-blue-fg", "var(--c-blue)", "toward ink"),
    ("--c-selected-accent-fg", "var(--c-accent)", "toward ink"),
]

# The fill a selected row can be in. The floor is resting AND hover: hover is
# the state a pointer RESTS in. Press is transient and reported, not held.
SELECTED_FILLS = ["--c-selected-bg", "--c-selected-bg-hover", "--c-selected-bg-press"]
HELD_FILLS = SELECTED_FILLS[:2]


def selected_expr(other: str, pct: int) -> str:
    return f"color-mix(in oklch, var(--c-selected-fg) {pct}%, {other})"


def show_selected(themes: list[Theme]) -> None:
    print("ON-SELECTED INK (WCAG 1.4.3: 4.5:1 for the small text every one of")
    print("these is; the floor is the resting AND hovered fill, not the pressed one)")
    print()
    for th in themes:
        print(f"  {th.name}:")
        print(f"    {'ink':<26} {'resting':<12} {'hover':<12} {'press':<12} verdict")
        for tok, _other, _dir in SELECTED_INKS:
            if tok not in th.decls:
                print(f"    {tok:<26} not declared")
                continue
            ink = th.colour(tok)
            cells, held = [], []
            for fill in SELECTED_FILLS:
                r = contrast(ink, th.flat(fill))
                cells.append(f"{fmt(r)}:1")
                if fill in HELD_FILLS:
                    held.append(r)
            verdict = "PASS" if min(held) >= 4.5 else f"FAIL (held min {fmt(min(held))}:1)"
            print(f"    {tok:<26} {cells[0]:<12} {cells[1]:<12} {cells[2]:<12} {verdict}")
        # The row's own ink, for the hierarchy check below.
        own = th.colour("--c-selected-fg")
        print(
            f"    {'--c-selected-fg (the row)':<26} "
            + " ".join(f"{fmt(contrast(own, th.flat(f))):<11}" for f in SELECTED_FILLS)
        )
        print()

    print("FRACTION SWEEP: the smallest 1%-step fraction clearing 4.5:1 on the")
    print("resting AND hovered fill in BOTH themes. This is what sizes the tokens.")
    print()
    print(f"  {'construction':<44} {'min %':<7} {'dark':<10} {'light':<10} held min")
    for tok, other, direction in SELECTED_INKS:
        best = None
        for pct in range(1, 101):
            expr = selected_expr(other, pct)
            worst = min(
                contrast(th.resolve(expr), th.flat(fill)) for th in themes for fill in HELD_FILLS
            )
            if worst >= 4.5:
                best = (pct, worst)
                break
        label = f"{tok.replace('--c-selected-', '')} ({direction})"
        if best is None:
            print(f"  {label:<44} none    -          -          cannot reach 4.5:1")
            continue
        pct, worst = best
        per = [
            fmt(min(contrast(th.resolve(selected_expr(other, pct)), th.flat(f)) for f in HELD_FILLS))
            for th in themes
        ]
        print(f"  {label:<44} {pct:<7} {per[0]:<10} {per[1]:<10} {fmt(worst)}:1")
    print()

    print("HIERARCHY: muted metadata must stay visibly quieter than the row's own")
    print("ink, or the distinction it exists to express is gone.")
    for th in themes:
        if "--c-selected-muted-fg" not in th.decls:
            continue
        sep = contrast(th.colour("--c-selected-muted-fg"), th.colour("--c-selected-fg"))
        print(f"  {th.name:<6} muted vs the row's ink: {fmt(sep)}:1")
    print()

    print("HUE PRESERVATION: a status ink that no longer reads as its status is")
    print("worse than a dim one, so the mix must move L and leave H alone.")
    for th in themes:
        print(f"  {th.name}:")
        for tok, other, direction in SELECTED_INKS:
            if direction != "toward ink" or tok not in th.decls:
                continue
            seed = other[4:-1] if other.startswith("var(") else other
            h_before = colour_to_oklch(th.colour(seed))[2]
            h_after = colour_to_oklch(th.colour(tok))[2]
            drift = ((h_after - h_before + 540) % 360) - 180
            print(
                f"    {seed:<16} {h_before:6.1f}deg -> {h_after:6.1f}deg  "
                f"drift {drift:+.1f}deg"
            )
        print()


# Elevation. A floating surface separates from the one below it by a shadow, and
# a BLACK shadow can only darken — against a near-black base there is nothing
# left to darken, so the layer measures ~1.0:1 and the surface reads flat. An
# ambient layer derived off the INK inverts per theme (a rim on dark, a hairline
# on light) and is what carries the separation there.
SHADOW_SITES: list[tuple[str, str]] = [
    # (label, the shadow colour as authored)
    ("uip-toast", "color-mix(in srgb, #000 25%, transparent)"),
    ("uip-tooltip", "oklch(0% 0 0deg / 40%)"),
    ("uip-ask (modal)", "oklch(0% 0 0deg / 50%)"),
    ("tab-drag-ghost", "oklch(0% 0 0deg / 25%)"),
    ("popup", "oklch(0% 0 0deg / 50%)"),
    ("tab-context-menu", "oklch(0% 0 0deg / 25%)"),
    ("scroll-to-bottom", "oklch(0% 0 0deg / 30%)"),
    ("pill-expand-content", "oklch(0% 0 0deg / 12%)"),
    ("git-ci-panel", "rgb(0 0 0 / 22%)"),
    ("chip-menu", "oklch(0% 0 0deg / 40%)"),
    ("git-branch-popover", "oklch(0% 0 0deg / 30%)"),
    ("chat-find (inner)", "rgb(0 0 0 / 20%)"),
    ("chat-find (outer)", "rgb(0 0 0 / 28%)"),
    ("commands-popover", "rgb(0 0 0 / 20%)"),
]

# Candidates for the ambient layer, so the choice is made on numbers. The border
# wash is the incumbent: it already inverts per theme and needs no new palette.
AMBIENT_CANDIDATES = [
    "--c-border",
    "--c-hover",
    "--c-elevation-ambient",
]


# ---------------------------------------------------------------------------
# The tab activity dot (12-tabs.css / 70-selection.css).
#
# A status dot is a graphical object conveying information, so its floor is
# WCAG 1.4.11's 3:1 rather than 4.5:1 — and it has to clear that on FIVE fills,
# because a tab row is either unselected (the sidebar, or the sidebar under the
# hover wash) or selected (three rungs of its own fill). The selected column is
# why the on-selected ink family exists: the dot declares its own colour, so
# `--c-selected-fg` never reaches it.
# ---------------------------------------------------------------------------

# The fills a tab row can present. Held = the states a pointer RESTS in; press
# is transient and reported rather than held, the same rule show_selected uses.
DOT_FILLS: list[tuple[str, str]] = [
    ("resting", "--c-bg-secondary"),
    ("hover", "over(var(--c-hover), var(--c-bg-secondary))"),
]
DOT_SELECTED_FILLS: list[tuple[str, str]] = [
    ("selected", "--c-selected-bg"),
    ("sel+hover", "--c-selected-bg-hover"),
    ("sel+press", "--c-selected-bg-press"),
]
DOT_HELD = ["resting", "hover", "selected", "sel+hover"]

# (state, unselected ink, on-selected ink, channels). The unselected ink is the
# status seed; the selected one is that seed's member of the on-selected family,
# which is what the `.tab.active` rule in 70-selection.css switches --dot-color
# to. `channels` is the state's NON-COLOUR identity, transcribed from the CSS:
#
#   fill     hollow | solid | donut   (donut only under prefers-reduced-motion)
#   surround none | ring               (a static hard 2px ring)
#   motion   still | animated          (the glow beat + travelling wave)
#   shape    circle | diamond
#
# Transcribed rather than parsed, so it is a claim this script CHECKS rather
# than derives: the pairwise test below fails if any two states that can appear
# in the same strip are distinguishable by hue alone (WCAG 1.4.1), and it runs
# twice — once with motion available and once with it removed, because
# 40-a11y.css zeroes every animation under prefers-reduced-motion and the
# vocabulary has to survive that.
DOT_STATES: list[tuple[str, str, str, dict[str, str]]] = [
    ("idle", "var(--c-dot-idle)", "--c-selected-muted-fg", {"fill": "hollow", "surround": "none", "motion": "still", "shape": "circle"}),
    ("working", "--c-accent", "--c-selected-accent-fg", {"fill": "solid", "surround": "none", "motion": "animated", "shape": "circle"}),
    ("waiting", "--c-yellow", "--c-selected-yellow-fg", {"fill": "solid", "surround": "ring", "motion": "still", "shape": "circle"}),
    ("input", "--c-yellow", "--c-selected-yellow-fg", {"fill": "solid", "surround": "ring", "motion": "still", "shape": "circle"}),
    ("failed", "--c-red", "--c-selected-red-fg", {"fill": "solid", "surround": "none", "motion": "still", "shape": "diamond"}),
    ("done", "--c-green", "--c-selected-green-fg", {"fill": "solid", "surround": "none", "motion": "still", "shape": "circle"}),
    # Editor tabs only. It can never share a strip position with a chat state,
    # so the pairwise check excludes it rather than demanding a channel for it.
    ("dirty", "--c-accent", "--c-selected-accent-fg", {"fill": "solid", "surround": "none", "motion": "still", "shape": "circle"}),
]

# The states that share ONE visual on purpose. A 9px dot has four non-colour
# channels and six chat states; `waiting` (the agent declared waiting_on_user,
# turn over) and `input` (a decision is open, turn blocked) are the pair with no
# channel left, so they take the same treatment and differ in the ANNOUNCED name
# instead. Declared here so the pairwise check treats their identity as intended
# rather than reporting it as the failure it would otherwise be.
DOT_ALIASES = [("waiting", "input")]

# `dirty` is an editor-tab state; every other member is a chat state.
DOT_CHAT_ONLY = "dirty"

DOT_FLOOR = 3.0

# Under prefers-reduced-motion an animated state loses its motion and gains a
# hole plus a static ring (the source vocabulary's own degradation), so its
# channels are rewritten before the second pairwise pass.
REDUCED_MOTION_SUBSTITUTION = {"fill": "donut", "surround": "ring", "motion": "still"}


def as_expr(ink: str) -> str:
    """A bare token name becomes a var() reference; an expression passes through."""
    return f"var({ink})" if ink.startswith("--") else ink


def show_dot(themes: list[Theme]) -> None:
    print("TAB ACTIVITY DOT (WCAG 1.4.11: 3:1 for a graphical object; the floor is")
    print("every fill a tab row can present EXCEPT press, which is transient)")
    print()
    for th in themes:
        print(f"  {th.name}:")
        cols = [c for c, _ in DOT_FILLS + DOT_SELECTED_FILLS]
        print(f"    {'state':<10} {'ink':<26} " + " ".join(f"{c:<11}" for c in cols) + " verdict")
        for state, unsel, sel, _ch in DOT_STATES:
            row, held = [], []
            for cols_, ink in ((DOT_FILLS, unsel), (DOT_SELECTED_FILLS, sel)):
                for col, fill in cols_:
                    bg = th.flat(fill)
                    r = contrast(th.resolve(as_expr(ink)).over(bg), bg)
                    row.append(f"{fmt(r)}:1")
                    if col in DOT_HELD:
                        held.append((col, r))
            worst_col, worst = min(held, key=lambda kv: kv[1])
            verdict = (
                "PASS"
                if worst >= DOT_FLOOR
                else f"FAIL ({worst_col} {fmt(worst)}:1, want {DOT_FLOOR}:1)"
            )
            ink = f"{unsel} / {sel.replace('--c-selected-', '')}"
            print(f"    {state:<10} {ink:<26} " + " ".join(f"{c:<11}" for c in row) + f" {verdict}")
        print()

    print("IDLE-RING SWEEP: the smallest 1%-step ink fraction clearing 3:1 on the")
    print("UNSELECTED fills in BOTH themes. The selected fills take")
    print("--c-selected-muted-fg instead, which is already sized at 4.5:1.")
    print()
    print(f"  {'construction':<52} {'min %':<7} held min")
    for base in ("var(--c-text-primary)", "var(--c-text-secondary)"):
        best = None
        for pct in range(1, 101):
            expr = f"color-mix(in oklch, {base} {pct}%, transparent)"
            worst = min(
                contrast(th.resolve(expr).over(th.flat(fill)), th.flat(fill))
                for th in themes
                for col, fill in DOT_FILLS
                if col in DOT_HELD
            )
            if worst >= DOT_FLOOR:
                best = (pct, worst)
                break
        label = f"color-mix(in oklch, {base} N%, transparent)"
        if best is None:
            print(f"  {label:<52} none    cannot reach {DOT_FLOOR}:1")
            continue
        print(f"  {label:<52} {best[0]:<7} {fmt(best[1])}:1")
    print()

    print("NON-COLOUR CHANNELS (WCAG 1.4.1: colour may not be the only means of")
    print("conveying a state). Every pair of chat states must differ on at least one")
    print("of fill / surround / motion / shape, with motion available AND removed.")
    print()
    print(f"  {'state':<10} {'fill':<8} {'surround':<9} {'motion':<10} shape")
    for state, _u, _s, ch in DOT_STATES:
        tag = "  (editor tab)" if state == DOT_CHAT_ONLY else ""
        print(
            f"  {state:<10} {ch['fill']:<8} {ch['surround']:<9} {ch['motion']:<10} {ch['shape']}{tag}"
        )
    print()

    aliases = {frozenset(p) for p in DOT_ALIASES}
    chat = [(s, ch) for s, _u, _sel, ch in DOT_STATES if s != DOT_CHAT_ONLY]
    for pass_name, reduce_motion in (("motion available", False), ("prefers-reduced-motion", True)):
        resolved = []
        for state, ch in chat:
            eff = dict(ch)
            if reduce_motion and ch["motion"] == "animated":
                eff.update(REDUCED_MOTION_SUBSTITUTION)
            resolved.append((state, eff))
        collisions = []
        for i, (a, ca) in enumerate(resolved):
            for b, cb in resolved[i + 1 :]:
                if frozenset((a, b)) in aliases:
                    continue
                if ca == cb:
                    collisions.append((a, b))
        verdict = "PASS  every pair differs on a non-colour channel"
        if collisions:
            pairs = ", ".join(f"{a}/{b}" for a, b in collisions)
            verdict = f"FAIL  hue is the only separator for: {pairs}"
        print(f"  {pass_name:<24} {verdict}")
    for a, b in DOT_ALIASES:
        print(f"  {'aliased on purpose':<24} {a}/{b} share one visual; they differ in the announced name")
    print()

    print("GREYSCALE ORDERING, for reference: what a reader with no hue perception")
    print("sees. It is NOT the guard — the channel matrix above is — but a pair that")
    print("also separates here separates twice.")
    for th in themes:
        rows = []
        for state, unsel, _sel, _ch in DOT_STATES:
            if state == DOT_CHAT_ONLY:
                continue
            c = th.resolve(as_expr(unsel)).over(th.flat("--c-bg-secondary"))
            rows.append((state, c.luminance(), c.hex()))
        rows.sort(key=lambda r: -r[1])
        print(f"  {th.name}:")
        for state, y, hexv in rows:
            print(f"    {state:<10} Y={fmt(y):<8} {hexv}")
    print()


def show_shadow(themes: list[Theme]) -> None:
    print("ELEVATION: each authored shadow layer composited over the surface it")
    print("falls on. A floating surface can sit over any rung, so the figure that")
    print("matters is the WORST rung. Below ~1.05:1 the layer is not a shadow, it")
    print("is a no-op: a black shadow cannot darken a base that is already black.")
    print()
    for th in themes:
        rungs = [r for r in ("--c-bg-primary", "--c-bg-secondary", "--c-bg-tertiary") if r in th.decls]
        print(f"  {th.name}:")
        print(f"    {'site':<24} {'worst rung':<14} {'on':<18} verdict")
        for label, colour in SHADOW_SITES:
            worst, where = None, ""
            for rung in rungs:
                bg = th.flat(rung)
                r = contrast(th.resolve(colour).over(bg), bg)
                if worst is None or r < worst:
                    worst, where = r, rung
            assert worst is not None
            verdict = "ok" if worst >= 1.05 else "FLAT"
            print(f"    {label:<24} {fmt(worst) + ':1':<14} {where:<18} {verdict}")
        print()

    print("AMBIENT LAYER CANDIDATES: a `0 0 0 1px <colour>` ring composited over")
    print("each rung. Derived off the INK, so it lifts on dark and sinks on light")
    print("from one declaration — which is the property a black shadow lacks.")
    print()
    for th in themes:
        print(f"  {th.name}:")
        for tok in AMBIENT_CANDIDATES:
            if tok not in th.decls:
                print(f"    {tok:<26} not declared")
                continue
            c = th.colour(tok)
            cells = []
            for rung in ("--c-bg-primary", "--c-bg-secondary", "--c-bg-tertiary"):
                if rung not in th.decls:
                    continue
                bg = th.flat(rung)
                cells.append(f"{rung.replace('--c-bg-', '')}={fmt(contrast(c.over(bg), bg))}:1")
            print(f"    {tok:<26} {'  '.join(cells)}")
        print()


# The ansi_up class palette (css/15-ansi.css) is 32 hardcoded literals plus the
# agent-terminal status pair. Foreground entries are text on the transcript
# surface; the one background entry that can vanish is black-on-near-black.
ANSI_FG = {
    "black": "#555",
    "red": "#f55",
    "green": "#5f5",
    "yellow": "#ff5",
    "blue": "#55f",
    "magenta": "#f5f",
    "cyan": "#5ff",
    "white": "#ccc",
    "bright-black": "#888",
    "bright-red": "#f88",
    "bright-green": "#8f8",
    "bright-yellow": "#ff8",
    "bright-blue": "#88f",
    "bright-magenta": "#f8f",
    "bright-cyan": "#8ff",
    "bright-white": "#fff",
}
ANSI_BG = {
    "black": "#000",
    "red": "#a00",
    "green": "#0a0",
    "yellow": "#a50",
    "blue": "#00a",
    "magenta": "#a0a",
    "cyan": "#0aa",
    "white": "#aaa",
    "bright-black": "#555",
    "bright-red": "#f55",
    "bright-green": "#5f5",
    "bright-yellow": "#ff5",
    "bright-blue": "#55f",
    "bright-magenta": "#f5f",
    "bright-cyan": "#5ff",
    "bright-white": "#fff",
}


def show_ansi(themes: list[Theme]) -> None:
    print("ANSI PALETTE vs THE SURFACE IT RENDERS ON. ansi_up output appears in the")
    print("transcript (tool-card output), so the backdrop is the app surface, not")
    print("the terminal's. A foreground wants 4.5:1; a BACKGROUND span wants 3:1")
    print("against the surface behind it or the region it marks is invisible.")
    print()
    for surface in ("--c-bg-primary", "--c-code-bg"):
        for th in themes:
            bg = th.flat(surface, "--c-bg-primary")
            print(f"  {th.name} on {surface} ({bg.hex()}):")
            fails = []
            for name, lit in ANSI_FG.items():
                r = contrast(th.resolve(lit), bg)
                if r < 4.5:
                    fails.append(f"      {name + '-fg':<20} {lit:<6} {fmt(r)}:1  FAIL")
            for name, lit in ANSI_BG.items():
                r = contrast(th.resolve(lit), bg)
                if r < 3.0:
                    fails.append(f"      {name + '-bg':<20} {lit:<6} {fmt(r)}:1  FAIL (want 3:1)")
            print("\n".join(fails) if fails else "      all entries clear their floor")
        print()
    print("  The theme token an ANSI black background should take instead:")
    for th in themes:
        if "--c-term-black" not in th.decls:
            continue
        base = th.flat("--c-bg-primary")
        code = th.flat("--c-code-bg", "--c-bg-primary")
        print(
            f"    {th.name:<6} --c-term-black {th.colour('--c-term-black')!r:<10} "
            f"vs base {fmt(contrast(th.colour('--c-term-black'), base))}:1, "
            f"vs code-bg {fmt(contrast(th.colour('--c-term-black'), code))}:1"
        )
    print()


def main() -> int:
    argv = sys.argv[1:] or ["all"]
    cmd = argv[0]
    dark, light = parse_themes()
    themes = [dark, light]

    ramp = ["--c-bg-primary", "--c-bg-secondary", "--c-bg-tertiary", "--c-bg-elevated", "--c-bg-hover"]
    surfaces = [r for r in ramp if r in dark.decls]

    if cmd in ("ramp", "all"):
        print("SURFACE RAMP")
        show_ramp(themes, ramp)

    if cmd in ("pairs", "all"):
        print("EDGE / SELECTION / INTERACTION SEPARATION")
        pairs: list[tuple[str, str, str, float | None]] = [
            ("border vs bg-primary", "--c-border", "--c-bg-primary", None),
            ("border vs bg-secondary", "--c-border", "--c-bg-secondary", None),
            ("border vs bg-tertiary", "--c-border", "--c-bg-tertiary", None),
            ("border vs bg-elevated", "--c-border", "--c-bg-elevated", None),
            ("border vs bg-hover (legacy)", "--c-border", "--c-bg-hover", None),
            ("accent-subtle vs bg-hover (legacy)", "--c-accent-subtle", "--c-bg-hover", None),
            ("selected-bg vs bg-secondary", "--c-selected-bg", "--c-bg-secondary", 1.25),
            ("selected-border vs selected-bg", "--c-selected-border", "--c-selected-bg", 1.25),
            ("selected-fg on selected-bg", "--c-selected-fg", "--c-selected-bg", 4.5),
            ("selected-bg-hover vs selected-bg", "--c-selected-bg-hover", "--c-selected-bg", None),
            ("selected-bg-press vs selected-bg-hover", "--c-selected-bg-press", "--c-selected-bg-hover", None),
            ("tab-active-bg vs bg-secondary (legacy)", "--c-tab-active-bg", "--c-bg-secondary", None),
            ("tab-active-border vs tab-active-bg (legacy)", "--c-tab-active-border", "--c-tab-active-bg", None),
        ]
        show_pairs(themes, pairs)

        print("WASHES vs EVERY SURFACE RUNG (a wash has no position on the ramp,")
        print("so it must clear each rung — this is what the old opaque border could not do)")
        wash_pairs: list[tuple[str, str, str, float | None]] = []
        for tok, floor in (("--c-border", 1.3), ("--c-code-bg", 1.15)):
            for s in surfaces:
                wash_pairs.append((f"{tok.replace('--c-', '')} on {s.replace('--c-bg-', '')}", tok, s, floor))
        show_pairs(themes, wash_pairs)

        print("TINTED HAIRLINES: the color-mix(status, --c-border) sites, over their surface")
        tinted: list[tuple[str, str, str, float | None]] = []
        for hue in ("--c-teal", "--c-danger", "--c-accent"):
            expr = f"color-mix(in srgb, var({hue}) 45%, var(--c-border))"
            tinted.append((f"{hue.replace('--c-', '')} 45% + border", expr, "--c-bg-secondary", 1.3))
        show_pairs(themes, tinted)

        print("HOVER / PRESS WASH, composited over each surface")
        for th in themes:
            print(f"  {th.name}:")
            for base in ("--c-bg-primary", "--c-bg-secondary", "--c-bg-tertiary"):
                if base not in th.decls:
                    continue
                bg = th.flat(base)
                row = [f"    over {base:<18}"]
                for tok in ("--c-hover", "--c-press"):
                    if tok not in th.decls:
                        continue
                    w = th.colour(tok).over(bg)
                    row.append(f"{tok}={fmt(contrast(w, bg))}:1 ({w.hex()})")
                if len(row) > 1:
                    print("  ".join(row))
            print()

    if cmd in ("text", "all"):
        print("TEXT ON SURFACE (WCAG 1.4.3: 4.5:1 body, 3:1 large; 1.4.11: 3:1 UI)")
        text_pairs: list[tuple[str, str, str, float | None]] = []
        for t in ("--c-text-primary", "--c-text-secondary", "--c-text-tertiary", "--c-text-control"):
            for s in surfaces:
                text_pairs.append((f"{t.replace('--c-text-', '')} on {s.replace('--c-bg-', '')}", t, s, 4.5))
        # A control's label sits on its own HOVER wash more often than on the top
        # ramp rung, now that hover is a wash rather than a jump to that rung.
        for t in ("--c-text-control", "--c-text-primary"):
            for s in ("--c-bg-primary", "--c-bg-secondary"):
                expr = f"over(var(--c-hover), var({s}))"
                text_pairs.append((f"{t.replace('--c-text-', '')} on hover({s.replace('--c-bg-', '')})", t, expr, 4.5))
        text_pairs += [
            ("accent on bg-primary", "--c-accent", "--c-bg-primary", 4.5),
            ("on-accent on accent", "--c-on-accent", "--c-accent", 4.5),
            ("link on bg-primary", "--c-link", "--c-bg-primary", 4.5),
            ("green on bg-primary", "--c-green", "--c-bg-primary", 4.5),
            ("red on bg-primary", "--c-red", "--c-bg-primary", 4.5),
            ("yellow on bg-primary", "--c-yellow", "--c-bg-primary", 4.5),
            ("danger on bg-primary", "--c-danger", "--c-bg-primary", 4.5),
        ]
        show_pairs(themes, text_pairs)

    if cmd in ("selected", "all"):
        show_selected(themes)

    if cmd in ("dot", "all"):
        show_dot(themes)

    if cmd in ("shadow", "all"):
        show_shadow(themes)

    if cmd in ("ansi", "all"):
        show_ansi(themes)

    if cmd == "show":
        for tok in argv[1:]:
            print(f"{tok}:")
            for th in themes:
                try:
                    c = th.flat(tok, "--c-bg-primary")
                    print(f"  {th.name:<6} {c!r:<22} Y={fmt(c.luminance())}" + ("  OUT OF GAMUT" if c.clipped else ""))
                except (KeyError, ValueError) as exc:
                    print(f"  {th.name:<6} {exc}")

    if cmd == "pair":
        return show_pair(themes, argv[1:])

    return 0


def show_pair(themes: list[Theme], args: list[str]) -> int:
    """`pair <fg> <bg> [floor]` — one arbitrary pair, both themes, as TSV.

    Every other subcommand answers a question this file already knows to ask.
    This one takes the question from the caller, which is what a new pair needs
    before it has a home here — and what a test needs, so a contrast floor can be
    asserted against the SAME implementation the comments were measured with
    instead of a second copy of the colour maths in another language.

    Output is `theme<TAB>fg_hex<TAB>bg_hex<TAB>ratio` per theme, so it parses
    without a JSON dependency. With a floor, the exit status is 1 when either
    theme misses it.
    """
    if len(args) < 2:
        usage = "usage: css-contrast.py pair <fg-expr> <bg-expr> [floor]"
        print(usage, file=sys.stderr)
        return 2
    fg_expr, bg_expr = args[0], args[1]
    floor = float(args[2]) if len(args) > 2 else None
    failed = False
    for th in themes:
        try:
            fg = th.flat(fg_expr, "--c-bg-primary")
            bg = th.flat(bg_expr, "--c-bg-primary")
        except (KeyError, ValueError) as exc:
            print(f"{th.name}\tERROR\t{exc}", file=sys.stderr)
            return 2
        ratio = contrast(fg, bg)
        print(f"{th.name}\t{fg.hex()}\t{bg.hex()}\t{fmt(ratio)}")
        if floor is not None and ratio < floor:
            failed = True
    return 1 if failed else 0


if __name__ == "__main__":
    raise SystemExit(main())
