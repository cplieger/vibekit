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
  python3 scripts/css-contrast.py ansi          # the 16-colour ANSI palette vs its surface
  python3 scripts/css-contrast.py show TOKEN..  # resolve named tokens
  python3 scripts/css-contrast.py all
"""

from __future__ import annotations

import math
import re
import sys
from pathlib import Path

CSS_DIR = Path(__file__).resolve().parent.parent / "static-src" / "css"
TOKENS = CSS_DIR / "01-tokens.css"
ANSI_SHEET = CSS_DIR / "15-ansi.css"


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
    ("working", "--c-blue", "--c-selected-blue-fg", {"fill": "solid", "surround": "none", "motion": "animated", "shape": "circle"}),
    ("waiting", "--c-yellow", "--c-selected-yellow-fg", {"fill": "hollow", "surround": "ring", "motion": "still", "shape": "circle"}),
    ("input", "--c-orange", "--c-selected-orange-fg", {"fill": "solid", "surround": "ring", "motion": "still", "shape": "circle"}),
    ("failed", "--c-red", "--c-selected-red-fg", {"fill": "solid", "surround": "none", "motion": "still", "shape": "diamond"}),
    ("done", "--c-green", "--c-selected-green-fg", {"fill": "solid", "surround": "none", "motion": "still", "shape": "circle"}),
    # Editor tabs only. It can never share a strip position with a chat state,
    # so the pairwise check excludes it rather than demanding a channel for it.
    ("dirty", "--c-accent", "--c-selected-accent-fg", {"fill": "solid", "surround": "none", "motion": "still", "shape": "circle"}),
]

# NO ALIASES, and the empty list is the finding. `waiting` and `input` used to be
# declared here as one intended visual, on the reasoning that six chat states
# against four non-colour channels left the pair nothing. What actually left them
# nothing was sharing ONE hue: with `input` moved to web-terminal-ui's orange, the
# fill channel had a slot free (hollow + ring) and the pair separates on fill as
# well as hue, so the exemption is not needed. Keep it empty — an entry here is a
# state the check has been told not to look at.
DOT_ALIASES: list[tuple[str, str]] = []

# `dirty` is an editor-tab state; every other member is a chat state.
DOT_CHAT_ONLY = "dirty"

DOT_FLOOR = 3.0

# Under prefers-reduced-motion an animated state loses its motion and gains a
# hole, so its channels are rewritten before the second pairwise pass. It does
# NOT gain a ring: the ring is the wants-you marker, and `working` borrowing it
# would put a false signal on the one state that needs nothing from the reader.
REDUCED_MOTION_SUBSTITUTION = {"fill": "donut", "motion": "still"}

# The hue whose two vibekit tokens carry it, and the source token they answer to.
# Printed with the orange sweep so the alignment claim is checkable rather than
# asserted in a comment.
DOT_SOURCE_HUES = [
    ("working", "--c-blue", "--status-working", "#52a9fe"),
    ("input", "--c-orange", "--status-input", "#fb923c"),
    ("failed", "--c-red", "--status-failed", "#dc2626"),
    ("done", "--c-green", "--status-done", "#22c55e"),
]

# The status seeds whose L/C range defines "inside this theme's own palette" for
# the orange sweep below. Read rather than asserted, so a retuned theme moves the
# admissible band with it.
DOT_SEED_BAND = ["--c-green", "--c-red", "--c-yellow", "--c-blue", "--c-teal", "--c-warning"]

# The base favicon's own artwork under the attention badge, and why the badge's
# ink is a separate question from the tab dot's. static/favicon.svg is an opaque
# 48-unit rounded rect filled `linearGradient(#A468FF -> #7E2FF0)` on the (0,0)
# -> (1,1) diagonal; the badge centre sits at (35.25, 12.75) of 48, where that
# gradient's offset is (x + y) / 2 = 0.500 — its exact midpoint. So the badge is
# never seen against a theme surface, which is what decides which theme's value
# it must carry.
FAVICON_BADGE_BACKDROP = ("#A468FF", "#7E2FF0")
FAVICON_CUES = [("input", "--c-orange"), ("done", "--c-green"), ("alert", "--c-red")]


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

    show_dot_hues(themes)
    show_orange_sweep(themes)

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

    show_favicon_badge(themes)


def from_hex(value: str) -> Colour:
    text = value.lstrip("#")
    return Colour(
        int(text[0:2], 16) / 255.0, int(text[2:4], 16) / 255.0, int(text[4:6], 16) / 255.0
    )


def oklab_distance(th: Theme, expr_a: str, expr_b: str) -> float:
    """Perceptual distance between two inks as they land on an unselected row."""
    bg = th.flat("--c-bg-secondary")
    a = colour_to_oklab(th.resolve(expr_a).over(bg))
    b = colour_to_oklab(th.resolve(expr_b).over(bg))
    return math.dist(a, b)


def show_dot_hues(themes: list[Theme]) -> None:
    print("SOURCE HUES: which colour FAMILY carries each meaning, against")
    print("@cplieger/web-terminal-ui's --status-* tokens. The claim is the family,")
    print("not the value: each app draws from its own palette, so a few degrees of")
    print("drift is expected and 24deg is too where a palette's red is a pinkish")
    print("one. What would be a finding is a state whose FAMILY differs — a violet")
    print("`working` against a blue one, which is what this replaced.")
    print()
    print(f"  {'state':<10} {'source token':<18} {'source':<10} {'hue':<8} {'vibekit token':<12} dark hue   light hue")
    for state, token, src_token, src_hex in DOT_SOURCE_HUES:
        _, _, src_hue = colour_to_oklch(from_hex(src_hex))
        cells = []
        for th in themes:
            _, _, hue = colour_to_oklch(th.colour(token))
            cells.append(f"{fmt(hue)}deg ({fmt(abs(hue - src_hue))} off)")
        print(
            f"  {state:<10} {src_token:<18} {src_hex:<10} {fmt(src_hue):<8} {token:<12} "
            + "  ".join(cells)
        )
    print()


def show_orange_sweep(themes: list[Theme]) -> None:
    print("ORANGE SWEEP, which sizes --c-orange. The HUE is fixed by the source")
    print("token (--status-input #fb923c). L and C are swept in 1% / 0.005 steps")
    print("over the L and C range this theme's OWN status seeds occupy, keeping")
    print("only values that clear the floor on all four held fills AND land inside")
    print("sRGB, and the winner MAXIMISES the smaller of two oklab distances: to")
    print("--c-yellow and to --c-red. Those are the orange's neighbours in the dot")
    print("vocabulary, so being confusable with either is the only way the token")
    print("can fail.")
    print()
    print("The in-gamut constraint is not tidiness. Every other seed is in gamut,")
    print("and outside it a browser reduces chroma while the favicon generator")
    print("clamps per channel — so an out-of-gamut orange would paint the tab dot")
    print("and the tab ICON two different colours, which is the one thing the")
    print("attention badge exists to keep in step.")
    print()
    # Rounded to the precision a token would carry, so the declared value is one
    # of the candidates the sweep ranks rather than a near miss it cannot match.
    _, _, exact = colour_to_oklch(from_hex("#fb923c"))
    hue = f"{round(exact, 1):g}"
    for th in themes:
        lightnesses, chromas = [], []
        for token in DOT_SEED_BAND:
            lightness, chroma, _ = colour_to_oklch(th.colour(token))
            lightnesses.append(round(lightness * 100))
            chromas.append(chroma)
        rows = []
        for pct in range(min(lightnesses), max(lightnesses) + 1):
            steps = round((max(chromas) - min(chromas)) / 0.005)
            for i in range(steps + 1):
                chroma = round(min(chromas) + i * 0.005, 3)
                expr = f"oklch({pct}% {chroma:g} {hue}deg)"
                if th.resolve(expr).clipped:
                    continue
                held = []
                for cols, is_sel in ((DOT_FILLS, False), (DOT_SELECTED_FILLS, True)):
                    ink = (
                        f"color-mix(in oklch, var(--c-selected-fg) 56%, {expr})" if is_sel else expr
                    )
                    for col, fill in cols:
                        if col not in DOT_HELD:
                            continue
                        bg = th.flat(fill)
                        held.append((col, contrast(th.resolve(ink).over(bg), bg)))
                worst = min(held, key=lambda kv: kv[1])
                if worst[1] < DOT_FLOOR:
                    continue
                separation = min(
                    oklab_distance(th, expr, "var(--c-yellow)"),
                    oklab_distance(th, expr, "var(--c-red)"),
                )
                rows.append((separation, expr, worst, th.resolve(expr).hex()))
        rows.sort(key=lambda r: -r[0])
        band = (
            f"L {min(lightnesses)}..{max(lightnesses)}  "
            f"C {fmt(min(chromas))}..{fmt(max(chromas))}"
        )
        print(f"  {th.name}: {len(rows)} admissible in the seed band ({band})")
        print(f"    {'':<4} {'construction':<28} {'hex':<9} {'separation':<11} worst held")
        declared = th.decls.get("--c-orange", "")
        for rank, (separation, expr, worst, hexv) in enumerate(rows[:5]):
            mark = "<--" if expr.replace(" ", "") == declared.replace(" ", "") else f"#{rank + 1}"
            print(
                f"    {mark:<4} {expr:<28} {hexv:<9} {fmt(separation):<11} "
                f"{worst[0]} {fmt(worst[1])}:1"
            )
        if declared and all(r[1].replace(" ", "") != declared.replace(" ", "") for r in rows[:5]):
            print(f"    NOTE --c-orange is declared {declared!r}, outside the top 5 above")
        print()


def show_favicon_badge(themes: list[Theme]) -> None:
    print("ATTENTION FAVICON BADGE: the same three cue inks, measured where they")
    print("are actually seen. The badge is composited onto static/favicon.svg,")
    print("whose own artwork is an opaque violet gradient, so its backdrop is")
    print("NEITHER theme's surface — which is what decides that ONE icon serving")
    print("both themes is correct, and which value it has to carry.")
    print()
    start, end = (from_hex(h) for h in FAVICON_BADGE_BACKDROP)
    mid = Colour((start.r + end.r) / 2, (start.g + end.g) / 2, (start.b + end.b) / 2)
    print(f"  badge sits at the gradient midpoint: {mid.hex()}")
    print()
    print(f"  {'cue':<8} {'token':<12} " + " ".join(f"{th.name + ' ink':<18}" for th in themes) + " gamut")
    for cue, token in FAVICON_CUES:
        cells, clipped = [], []
        for th in themes:
            c = th.colour(token)
            cells.append(f"{c.hex()} {fmt(contrast(c, mid))}:1")
            if c.clipped:
                clipped.append(th.name)
        # An out-of-gamut ink is a real defect HERE and nowhere else in this
        # script: a browser reduces chroma to reach sRGB while the generator
        # clamps per channel, so the tab dot and the tab icon would carry two
        # different colours from one declaration.
        verdict = "in sRGB" if not clipped else f"FAIL clipped in {', '.join(clipped)}"
        print(f"  {cue:<8} {token:<12} " + " ".join(f"{c:<18}" for c in cells) + f" {verdict}")
    print()
    print("  The dark inks are pastels and the light ones are deep, so on a")
    print("  saturated violet only the dark family separates at all. The generator")
    print("  therefore reads the default theme's :root and the light overrides are")
    print("  out of scope by MEASUREMENT rather than by omission.")
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


# ------------------------------------------------------------- the ANSI palette
#
# ONE surface, established by tracing the call sites rather than assumed:
# tool-card.ts and messages-tools.ts both write into `.tool-output pre`, which
# paints nothing; the nearest painting ancestor is `.tool-call` (14-tools.css) at
# --c-bg-secondary. It is OPAQUE, so it needs no compositing.
#
# There is no second surface. `.agent-term-pane` at --c-term-bg was one until
# agent command output moved into the card that spawned it and agent-terminal.ts
# was deleted; the live shell panel is not a replacement, because
# web-terminal-engine paints each ANSI run inline from server-resolved RGB and
# reads none of these tokens. Measuring --c-term-bg anyway was a stricter check
# than the app needs, which is a different thing from a correct one: it asserts a
# floor against a surface no ANSI class can land on, so a future palette could be
# blocked by a constraint nothing enforces. --c-bg-primary and --c-code-bg were
# measured here even earlier and were wrong in the other direction — the page base
# is two ramp rungs below where ANSI renders, and --c-code-bg is scoped inside the
# assistant prose bubble, which a tool card is a SIBLING of.
ANSI_SURFACES = ("--c-bg-secondary",)

# The ink that container sets. A bare `ESC[41m` arrives with a fill and no colour
# of its own, so the container's ink is what lands on it. --c-term-fg sat here as
# the live terminal's default ink and left with --c-term-bg for the same reason;
# it was never binding either way (it is lighter than every legal dark fill and
# darker than every legal light one), so the derived fills are unchanged.
ANSI_INKS = {"--c-text-secondary": "--c-bg-secondary"}

ANSI_FG_FLOOR = 4.5
ANSI_PAIR_FLOOR = 4.5

# The 16 ANSI codes, spelled out rather than matched by shape. A `.ansi-*-fg` /
# `-bg` pattern already admitted a 17th entry once: `.ansi-inverse-fg` / `-bg` are
# fallbacks for the DEFAULT colour, not a code a program can select, and they
# arrived here as a palette entry measuring --c-bg-secondary against itself at
# 1.000:1. Renaming them to -ink/-fill fixed that instance; an alternation is what
# makes the next one impossible. The count assertions in ansi-palette.test.ts keep
# this list honest in the other direction.
ANSI_CODES = (
    "black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
    "bright-black", "bright-red", "bright-green", "bright-yellow",
    "bright-blue", "bright-magenta", "bright-cyan", "bright-white",
)  # fmt: skip


def parse_ansi_sheet() -> tuple[dict[str, str], dict[str, str]]:
    """Read css/15-ansi.css and return its declared expressions.

    PARSED, not restated. This used to be two hardcoded dicts mirroring the
    stylesheet's 32 literals, and it had already drifted: `black` still read
    `#000` here long after `.ansi-black-bg` moved to a token, so the section
    reported a colour the app had stopped painting. A duplicated palette is the
    defect this whole section exists to measure, so the report may not keep its
    own copy. Only the class NAMES are enumerated (ANSI_CODES), which is a
    different kind of claim from a colour.
    """
    src = re.sub(r"/\*.*?\*/", " ", ANSI_SHEET.read_text(), flags=re.S)
    fg: dict[str, str] = {}
    bg: dict[str, str] = {}
    codes = "|".join(re.escape(c) for c in ANSI_CODES)
    for m in re.finditer(rf"\.ansi-({codes})-(fg|bg)\s*\{{([^}}]*)\}}", src):
        name, role, body = m.group(1), m.group(2), m.group(3)
        prop = "color" if role == "fg" else "background-color"
        v = re.search(rf"(?<![-\w]){prop}\s*:\s*([^;]+)", body)
        if v:
            (fg if role == "fg" else bg)[name] = v.group(1).strip()
    return fg, bg


def ansi_rows(themes: list[Theme]) -> list[tuple[str, str, str, str, Colour, Colour, float, float]]:
    """Every ANSI measurement as (kind, theme, name, context, fg, bg, ratio, floor).

    A floor of 0 means "recorded, not gated" — see the fill-vs-surface trade.
    """
    fgs, bgs = parse_ansi_sheet()
    rows = []
    for th in themes:
        # A foreground is text on whichever container it landed in.
        for name, expr in fgs.items():
            for s in ANSI_SURFACES:
                surf = th.flat(s)
                rows.append(
                    ("fg-surface", th.name, f"{name}-fg", s, th.resolve(expr), surf,
                     contrast(th.resolve(expr), surf), ANSI_FG_FLOOR)
                )
        # A foreground ON a fill: what a program setting both actually renders.
        for name, expr in fgs.items():
            for bname, bexpr in bgs.items():
                f, b = th.resolve(expr), th.resolve(bexpr)
                rows.append(
                    ("fg-fill", th.name, f"{name}-fg", f"{bname}-bg", f, b,
                     contrast(f, b), ANSI_PAIR_FLOOR)
                )
        # A fill with no ANSI foreground: the container's own ink lands on it.
        for ink in ANSI_INKS:
            for bname, bexpr in bgs.items():
                f, b = th.colour(ink), th.resolve(bexpr)
                rows.append(
                    ("ink-fill", th.name, ink, f"{bname}-bg", f, b,
                     contrast(f, b), ANSI_PAIR_FLOOR)
                )
        # A fill against the surface it marks. RECORDED, NOT GATED, and the
        # reason is arithmetic: carrying the darkest ink at 4.5:1 pins a fill
        # PAST --c-bg-secondary's own luminance, so the fills land between
        # 1.00:1 and 1.41:1 against the surface. 3:1 here and 4.5:1 above cannot
        # both hold unless the inks flatten toward the surface's own extreme,
        # and the conflict survives even at a 3:1 pairing floor. Gating it would
        # put 32 permanent failures in this report for a floor no palette that
        # keeps its hues can reach.
        for bname, bexpr in bgs.items():
            for s in ANSI_SURFACES:
                b, surf = th.resolve(bexpr), th.flat(s)
                rows.append(
                    ("fill-surface", th.name, f"{bname}-bg", s, b, surf,
                     contrast(b, surf), 0.0)
                )
    return rows


def show_ansi(themes: list[Theme]) -> None:
    fgs, bgs = parse_ansi_sheet()
    print("ANSI PALETTE (css/15-ansi.css, read from the stylesheet itself).")
    print()
    print("The 32 --c-term-* values behind these classes are GENERATED — kitty's")
    print("default palette lifted by web-terminal-engine's own contrast rule. Run")
    print("`css-ansi-palette.py table` for the kitty-vs-shipped audit; this section")
    print("only measures the floors, and does so through the stylesheet, so it")
    print("cannot agree with the generator by construction.")
    print()
    print("Two ramps, because a colour that reads AS text and a colour that CARRIES")
    print("text cannot be the same colour. Three checks are GATED: an ink on the one")
    print("surface it renders on (4.5:1), an ink on every fill (4.5:1 — what")
    print("ESC[34;40m renders), and the container's own ink on every fill (4.5:1 —")
    print("the bare ESC[41m case). A fill against the surface it marks is RECORDED,")
    print("NOT GATED: see the trade at the end.")
    print()
    literals = [n for n, e in {**fgs, **bgs}.items() if "var(" not in e]
    if literals:
        print(f"  !! {len(literals)} entries still carry a literal: {', '.join(sorted(literals))}")
        print()

    rows = ansi_rows(themes)
    for kind, title in (
        ("fg-surface", "INK vs THE SURFACE IT RENDERS ON (floor 4.5:1)"),
        ("fg-fill", "INK ON FILL — every pair a program can select (floor 4.5:1)"),
        ("ink-fill", "CONTAINER INK ON FILL — a fill with no ANSI ink (floor 4.5:1)"),
    ):
        group = [r for r in rows if r[0] == kind]
        print(f"  {title}")
        for th in themes:
            mine = [r for r in group if r[1] == th.name]
            fails = [r for r in mine if r[6] < r[7]]
            worst = min(mine, key=lambda r: r[6])
            print(
                f"    {th.name:<6} {len(mine):>3} checks, worst "
                f"{worst[2]} on {worst[3]} = {fmt(worst[6])}:1"
                + (f"   {len(fails)} FAIL" if fails else "   all clear")
            )
            for r in fails:
                print(f"        {r[2]:<26} on {r[3]:<20} {fmt(r[6])}:1  FAIL (want {r[7]})")
        print()

    print("  FILL vs THE SURFACE IT MARKS — recorded, not gated.")
    for th in themes:
        mine = [r for r in rows if r[0] == "fill-surface" and r[1] == th.name]
        lo, hi = min(mine, key=lambda r: r[6]), max(mine, key=lambda r: r[6])
        print(
            f"    {th.name:<6} {fmt(lo[6])}:1 ({lo[2]} on {lo[3]}) .. "
            f"{fmt(hi[6])}:1 ({hi[2]} on {hi[3]})"
        )
    print("    A region marker would want 3:1 and cannot have it: a fill that carries")
    print("    the darkest ink at 4.5:1 is pinned past the surface's own luminance. So")
    print("    a fill WITH text on it is fully legible and a TEXTLESS coloured region")
    print("    is quiet. That is .ansi-black-bg's old trade, now general — and measured")
    print("    against --c-bg-secondary, which is where this palette actually renders.")
    print()


def ansi_check(themes: list[Theme], tsv: bool) -> int:
    """`ansi-check` — every gated ANSI measurement as TSV, exit 1 on any miss.

    Exists so a test can assert these floors against THIS implementation instead
    of a second copy of the colour maths in TypeScript, and so 256 pairs cost one
    process rather than 256.
    """
    rows = ansi_rows(themes)
    failed = 0
    for kind, theme, name, ctx, fg, bg, ratio, floor in rows:
        gated = floor > 0
        miss = gated and ratio < floor
        failed += 1 if miss else 0
        if tsv:
            print(
                f"{kind}\t{theme}\t{name}\t{ctx}\t{fg.hex()}\t{bg.hex()}\t"
                f"{fmt(ratio)}\t{fmt(floor) if gated else '-'}\t"
                f"{'FAIL' if miss else ('ok' if gated else 'recorded')}"
            )
    if not tsv:
        print(f"{len(rows)} measurements, {failed} FAIL")
    return 1 if failed else 0


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
                # The hint ink is sized against the PAGE and is not a
                # general-purpose ink: it clears 4.5:1 on --c-bg-primary and on
                # nothing above it. So pairing it with a raised fill is a rule
                # violation rather than a contrast result, and reporting it here
                # as FAIL puts eight permanent failures in the output for
                # combinations the app does not contain — which teaches a reader
                # to skip the whole section. The rule is asserted where it can
                # actually be checked, over the stylesheets:
                # css-tokens.test.ts, "keeps the hint ink off a raised fill".
                floor = None if t == "--c-text-tertiary" and s != "--c-bg-primary" else 4.5
                text_pairs.append(
                    (f"{t.replace('--c-text-', '')} on {s.replace('--c-bg-', '')}", t, s, floor)
                )
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
        # A status hue is INK as often as it is a fill — a red row label, an
        # accent link inside a card, a yellow badge — and until this block
        # existed each one was only ever measured against the PAGE, which is the
        # one surface they all pass on. 3:1 rather than 4.5:1 because most of
        # these land on a glyph or a badge; a hue used for small body text needs
        # the stricter floor and its own row above.
        for hue in ("--c-green", "--c-red", "--c-yellow", "--c-blue", "--c-danger", "--c-warning"):
            for s in ("--c-bg-secondary", "--c-bg-tertiary", "--c-bg-elevated"):
                # Same reasoning as the hint ink above for the TOP rung: red,
                # danger and warning land between 2.58 and 2.98 there, and the
                # only element in the app with an --c-bg-elevated fill is
                # .pill:active, which carries the primary ink. The structural
                # guard covers it; a floor here would be three standing failures.
                floor = None if s == "--c-bg-elevated" else 3.0
                text_pairs.append(
                    (
                        f"{hue.replace('--c-', '')} ink on {s.replace('--c-bg-', '')}",
                        hue,
                        s,
                        floor,
                    )
                )
        # The focus ring, against every surface it can be drawn over. 35 outlines
        # in the app and every one is the accent, so one token answers for all of
        # them — but it is a GRAPHIC under 1.4.11, so it has a floor and nothing
        # was checking it.
        for s in surfaces:
            text_pairs.append(
                (f"focus ring on {s.replace('--c-bg-', '')}", "--c-accent", s, 3.0)
            )
        show_pairs(themes, text_pairs)

    if cmd in ("selected", "all"):
        show_selected(themes)

    if cmd in ("dot", "all"):
        show_dot(themes)

    if cmd in ("shadow", "all"):
        show_shadow(themes)

    if cmd in ("ansi", "all"):
        show_ansi(themes)

    if cmd == "ansi-check":
        return ansi_check(themes, tsv="--tsv" in argv[1:])

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
