#!/usr/bin/env python3
"""Generate vibekit's 32 --c-term-* ANSI palette values from kitty's defaults.

THIS SCRIPT IS THE SOURCE OF THOSE VALUES. The declarations in
static-src/css/01-tokens.css are its output; `check` fails when the two disagree,
so a hand-edited token is a test failure rather than a silent divergence. 32
values across two themes typed by hand is where an arithmetic error hides, and a
surface change has to be re-DERIVED rather than re-guessed.

Two layers, both web-terminal-engine's, because the first is not legible without
the second.

Layer 1, identity: the 16 slots resolve to kitty's published default palette,
which is what the engine's vt/wire.go basic16RGB does. Those slots are
terminal-DEFINED — no spec assigns them values — so this is a palette CHOICE, and
kitty is the reference because it is the only widely-used terminal whose default
background is pure black, which is web-terminal-ui's default too. Deferring to it
is what makes SGR 34 the same blue in vibekit's transcript, in vibekit's own shell
panel, and in both sibling apps.

Layer 2, legibility: kitty's palette clears WCAG AA 4.5:1 on 13 of 16 slots
against pure black, 10 of 16 against vibekit's dark transcript card and 2 of 16
against the light one, so identity alone ships text nobody can read. The engine
answers that with vt/contrast.go ensureContrast, which blends the foreground
toward white or black — away from the background — until it clears the floor, and
never touches the background. web-terminal-server sets that floor to 4.5. The
functions below are that file transliterated: same 8-bit sRGB blend, same 8-step
binary search, same two-direction fallback, same tie rule.

The engine runs the lift per RUN at render time. CSS cannot: a custom property
holds one value and the server cannot compute it either, because it does not know
which theme the client is in. So the lift is applied here, offline, and baked into
the token values. That is why these are not byte-identical to the engine's table:
each is kitty's value lifted just far enough, which leaves the majority of the
dark ramp at kitty's actual colour.

The fill ramp (--c-term-<name>-bg) has no counterpart in the engine, which needs
none: it lifts the ink against whatever background is really there, an ANSI one
included. CSS cannot pair-match, and a program setting SGR 31;41 would otherwise
get red on red, so the fills are derived by the same machinery with the roles
swapped — the fill moves, away from the ink set, until every ink clears 4.5:1
against it. That is the one generalisation: the engine's predicate is one
background, this one is "every ink that can land here".

One set of four values is AUTHORED rather than lifted: the achromatic slots on
the light theme. kitty's contribution to those four is pure lightness, a light
card can use none of it, and the lift collapsed three of them onto one grey —
see light_achromatic_ramp for why deferring there preserved nothing. Every other
entry, in both themes, is kitty's value or kitty's value lifted.

Usage:
  python3 scripts/css-ansi-palette.py emit [dark|light]   # the CSS block(s)
  python3 scripts/css-ansi-palette.py check               # exit 1 on drift
  python3 scripts/css-ansi-palette.py table               # kitty -> shipped, TSV
"""

from __future__ import annotations

import importlib.util
import math
import sys
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parent

# scripts/css-contrast.py owns the token graph (oklch, var(), color-mix) and the
# WCAG maths this file's port is checked against. Imported rather than restated:
# resolving 01-tokens.css a second time is how the surface these values are lifted
# against would come to differ from the surface the report measures.
_spec = importlib.util.spec_from_file_location(
    "css_contrast", SCRIPTS / "css-contrast.py"
)
assert _spec is not None and _spec.loader is not None
cc = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(cc)


# ------------------------------------------- layer 1: kitty's default palette
#
# kitty/options/definition.py color0-color15, the same table as the engine's
# vt/wire.go basic16RGB. The engine moved off the classic VGA / Linux-console
# values because 0x0000aa blue reads 1.58:1 against black.
KITTY = (
    0x000000, 0xCC0403, 0x19CB00, 0xCECB00,
    0x0D73CC, 0xCB1ED1, 0x0DCDCD, 0xDDDDDD,
    0x767676, 0xF2201F, 0x23FD00, 0xFFFD00,
    0x1A8FFF, 0xFD28FF, 0x14FFFF, 0xFFFFFF,
)  # fmt: skip

NAMES = (
    "black", "red", "green", "yellow", "blue", "magenta", "cyan", "white",
    "bright-black", "bright-red", "bright-green", "bright-yellow",
    "bright-blue", "bright-magenta", "bright-cyan", "bright-white",
)  # fmt: skip

# The surface ANSI actually renders on: `.tool-output pre` paints nothing, and its
# nearest painting ancestor is `.tool-call` at --c-bg-secondary. Agent command
# output moved into the card that spawned it, so there is no second surface; the
# live shell panel is not a consumer of these codes at all, because
# web-terminal-engine paints each run from server-resolved RGB inline.
SURFACE = "--c-bg-secondary"

# The ink a bare `ESC[41m` arrives with: no colour of its own, so the container's
# ink lands on the fill. It joins the 16 in the set every fill must carry.
CONTAINER_INK = "--c-text-secondary"

# WCAG AA for body text, which is what ANSI output in a tool card is. It is also
# what web-terminal-server passes to WithMinimumContrast, what VS Code defaults
# xterm.js's minimumContrastRatio to, and what iTerm2's Minimum Contrast targets.
FLOOR = 4.5


# ------------------------------------ layer 2: web-terminal-engine/vt/contrast.go
#
# Transliterated, not reinterpreted. sRGB throughout — NOT a perceptual space —
# because that is what the engine blends in, and because blending toward pure
# white or pure black moves luminance monotonically there, which is what makes the
# binary search below valid.

# The engine's contrastSearchSteps. Each step halves the interval, so 8 steps
# resolve the blend factor finer than an 8-bit channel can express.
SEARCH_STEPS = 8


def srgb_to_linear(c: int) -> float:
    """One 8-bit sRGB channel to linear light, per the WCAG 2 definition.

    The engine's threshold is 0.03928 and css-contrast.py's is 0.04045 (the sRGB
    spec's own figure, WCAG's erratum). For 8-bit input the two are the same
    function: the band between them, 10.02..10.31, contains no integer.
    verify_against_report() asserts that rather than trusting it.
    """
    v = c / 255
    return v / 12.92 if v <= 0.03928 else ((v + 0.055) / 1.055) ** 2.4


def rel_luminance(c: int) -> float:
    """WCAG 2 relative luminance of a 0xRRGGBB colour."""
    return (
        0.2126 * srgb_to_linear((c >> 16) & 0xFF)
        + 0.7152 * srgb_to_linear((c >> 8) & 0xFF)
        + 0.0722 * srgb_to_linear(c & 0xFF)
    )


def contrast_ratio(a: int, b: int) -> float:
    la, lb = rel_luminance(a), rel_luminance(b)
    if la < lb:
        la, lb = lb, la
    return (la + 0.05) / (lb + 0.05)


def blend(frm: int, to: int, t: float) -> int:
    """Mix frm toward to by t, per channel in sRGB, rounding as Go's math.Round."""

    def mix(shift: int) -> int:
        a = float((frm >> shift) & 0xFF)
        b = float((to >> shift) & 0xFF)
        # math.Round is half-away-from-zero; Python's round() is half-to-even,
        # which would disagree on an exact .5 and put a 1-unit difference between
        # this table and the engine's. Both operands are non-negative here, so
        # floor(x + 0.5) is that rule.
        return math.floor(a + t * (b - a) + 0.5)

    return mix(16) << 16 | mix(8) << 8 | mix(0)


def blend_to_contrast(fg: int, score, target: int) -> tuple[int, bool]:
    """Smallest blend of fg toward target that clears the floor.

    ok is False when target itself falls short, so the caller can try the other
    direction instead of settling for a blend that misses the floor anyway.

    score is the engine's `contrastRatio(c, bg)` widened to a function, which is
    the one thing this port generalises: a fill has to clear every ink that can
    land on it, so its score is the WORST of those ratios rather than one.
    """
    if score(target) < FLOOR:
        return fg, False
    lo, hi = 0.0, 1.0
    best = target  # the full blend is known to satisfy the floor
    for _ in range(SEARCH_STEPS):
        mid = (lo + hi) / 2
        c = blend(fg, target, mid)
        if score(c) >= FLOOR:
            best = c
            hi = mid
        else:
            lo = mid
    return best, True


def ensure_contrast(fg: int, away_from: int, score) -> int:
    """fg lifted just far enough to clear the floor, or fg unchanged.

    away_from is the colour the lift moves away from — the background for an ink,
    the binding ink for a fill. Hue washes out as the lift grows; that is the
    engine's accepted trade for legibility, and a palette chosen for the
    background keeps the lift small.
    """
    if score(fg) >= FLOOR:
        return fg
    # Lighten when the text is already the lighter of the pair (every dark
    # theme), darken otherwise. A tie lightens, because a tie means both sit at
    # the same luminance and the direction is then arbitrary.
    first, second = 0xFFFFFF, 0x000000
    if rel_luminance(away_from) > rel_luminance(fg):
        first, second = second, first
    for target in (first, second):
        c, ok = blend_to_contrast(fg, score, target)
        if ok:
            return c
    # Neither extreme reaches the floor (a mid-luminance background): whichever
    # gets closest, as the engine does. Unreached for both ramps in both themes —
    # verify_against_report() would fail on the under-floor result if it were.
    return max((first, second), key=score)


# ------------------------------------------- the one authored set: light greys
#
# The four slots kitty defines by LIGHTNESS alone, ordered by the lightness each
# one asks for: 0x000000, 0x767676, 0xdddddd, 0xffffff.
ACHROMATIC = ("black", "bright-black", "white", "bright-white")

# The theme whose achromatic four are authored rather than lifted. Named rather
# than derived from the surface so that the override is one grep away, and light
# rather than dark for the reason in light_achromatic_ramp.
AUTHORED_THEME = "light"


def grey(v: int) -> int:
    """The achromatic 0xRRGGBB with every channel at v."""
    return v << 16 | v << 8 | v


def darkest_legible_grey(surface: int) -> int:
    """The lightest achromatic channel value still clearing the floor.

    Walks up from black, which is the only direction that matters here: the ramp
    exists for a surface too light to put light text on, so ratio falls
    monotonically as the grey lightens and the first failure ends the search.
    """
    last = 0
    for v in range(256):
        if contrast_ratio(grey(v), surface) < FLOOR:
            break
        last = v
    return last


def light_achromatic_ramp(surface: int) -> dict[str, int]:
    """The four achromatic inks for a light surface, authored not lifted.

    THIS IS THE ONE PLACE THE ENGINE'S FORMULA IS OVERRIDDEN, and the reason it
    is not a breach of the deferral is that there is no hue here to defer to.
    kitty's contribution to these four slots is purely lightness, chosen for a
    pure-black background where all four separate. A light card has only the dark
    half of the range available for text, so three of the four have to go dark to
    be legible — and the lift takes all three to the SAME place, because it stops
    at the smallest blend clearing the floor, which is a fixed target luminance,
    so every entry starting lighter than that converges on it. ESC[37m, ESC[90m
    and ESC[97m all arrived at #666. Deferring there preserved nothing: #666
    contains no part of 0xdddddd.

    Merging them is an information loss rather than a cosmetic one. ESC[90m is
    grey-for-de-emphasis, one of the most-used codes in real CLI output, and
    ESC[97m is bright-white-for-emphasis; rendering them identically deletes the
    distinction a program was making. The 12 CHROMATIC entries keep the lift
    untouched, because blending toward black scales all three channels and the hue
    angle survives — that is where cross-app identity actually lives.

    DARK needs no override, and the asymmetry is not an oversight. There the lift
    collapses `black` onto `bright-black` at the minimum-legible grey, and both of
    those codes ask for something too dark to read on a dark card, so one answer
    for both is honest; `white` (#ddd) and `bright-white` (#fff) keep kitty's own
    spread. Three distinct levels remain, so de-emphasis is still distinguishable
    from normal text and from emphasis.

    The ramp is even steps in the 8-bit channel kitty's own table is written in,
    anchored at both ends: `color0`, which already clears at 16.6:1 and needs no
    help, and the darkest grey the floor allows. It runs in the order of the
    lightness each code ASKED for, inverted into contrast, because on a light
    background ESC[30m asks for the loudest thing available while ESC[97m asks for
    something invisible — so the quietest legible grey is the honest rendering of
    "bright white", not the loudest.
    """
    quietest = darkest_legible_grey(surface)
    steps = len(ACHROMATIC) - 1
    return {
        name: grey(round(quietest * i / steps)) for i, name in enumerate(ACHROMATIC)
    }


# ------------------------------------------------------------------ derivation


def derive(theme) -> tuple[list[int], list[int]]:
    """The 16 inks and the 16 fills for one theme."""
    surface = int(theme.flat(SURFACE).hex(), 16)

    def vs_surface(c: int) -> float:
        return contrast_ratio(c, surface)

    # Inks: the engine's own call, one background, floor 4.5.
    inks = [ensure_contrast(k, surface, vs_surface) for k in KITTY]

    # ...except the four the formula collapses on a light card, which are authored.
    if theme.name == AUTHORED_THEME:
        ramp = light_achromatic_ramp(surface)
        inks = [ramp.get(n, ink) for n, ink in zip(NAMES, inks, strict=True)]

    # Fills: same machinery, roles swapped. The fill moves until every ink that
    # can land on it clears the floor. The binding member — the ink with the least
    # contrast against kitty's own value — supplies the direction, exactly as the
    # background does for an ink.
    ink_set = [*inks, int(theme.colour(CONTAINER_INK).hex(), 16)]

    def carried_by_worst_ink(c: int) -> float:
        return min(contrast_ratio(ink, c) for ink in ink_set)

    def binding_ink(fill: int) -> int:
        return min(ink_set, key=lambda ink: contrast_ratio(ink, fill))

    fills = [ensure_contrast(k, binding_ink(k), carried_by_worst_ink) for k in KITTY]
    return inks, fills


def as_hex(v: int) -> str:
    """`#rrggbb`, shortened to `#rgb` when every channel is a repeated pair.

    Short form is stylelint-config-standard's `color-hex-length`. Shortening is
    lossless, so kitty's 0xffffff still ships as literally kitty's value.
    """
    full = f"{v:06x}"
    if all(full[i] == full[i + 1] for i in (0, 2, 4)):
        return f"#{full[0]}{full[2]}{full[4]}"
    return f"#{full}"


def origin(theme, role: str, name: str, kitty: int, shipped: int) -> str:
    """Where a shipped value came from: kitty's table, the lift, or this file.

    Three values rather than a lifted yes/no, because the light achromatic four
    are neither kitty's value nor the engine's function applied to it, and a
    boolean would file them under the lift — which is the one claim about them
    that is false.
    """
    if kitty == shipped:
        return "kitty"
    if role == "ink" and theme.name == AUTHORED_THEME and name in ACHROMATIC:
        return "authored"
    return "lift"


def rows(theme) -> list[tuple[str, str, int, int, float, str]]:
    """(role, name, kitty, shipped, ratio-it-must-clear, origin) per token."""
    inks, fills = derive(theme)
    surface = int(theme.flat(SURFACE).hex(), 16)
    ink_set = [*inks, int(theme.colour(CONTAINER_INK).hex(), 16)]
    out = []
    for i, name in enumerate(NAMES):
        ratio = contrast_ratio(inks[i], surface)
        src = origin(theme, "ink", name, KITTY[i], inks[i])
        out.append(("ink", name, KITTY[i], inks[i], ratio, src))
    for i, name in enumerate(NAMES):
        worst = min(contrast_ratio(ink, fills[i]) for ink in ink_set)
        src = origin(theme, "fill", name, KITTY[i], fills[i])
        out.append(("fill", name, KITTY[i], fills[i], worst, src))
    return out


def token(role: str, name: str) -> str:
    return f"--c-term-{name}" if role == "ink" else f"--c-term-{name}-bg"


# ------------------------------------------------------------------- self-check


def verify_against_report(themes) -> None:
    """Refuse to emit values whose ratio this port and css-contrast.py disagree on.

    The port above is a second implementation of the WCAG ratio in this repo, and
    a second implementation is a second thing to be wrong. This is what keeps it
    a PORT: every value it derives is re-measured with css-contrast.py's own
    Colour/contrast and the two must agree to 1e-12. They do, because for 8-bit
    input the two luminance thresholds describe the same function.
    """
    for c in range(256):
        theirs = cc.srgb_decode(c / 255)
        if abs(srgb_to_linear(c) - theirs) > 1e-12:
            raise AssertionError(
                f"channel {c}: engine {srgb_to_linear(c)} vs report {theirs}"
            )
    for th in themes:
        surface = th.flat(SURFACE)
        measured = {}
        for role, name, _kitty, shipped, _r, _o in rows(th):
            if role != "ink":
                continue
            mine = contrast_ratio(shipped, int(surface.hex(), 16))
            theirs = cc.contrast(cc.from_hex(f"{shipped:06x}"), surface)
            if abs(mine - theirs) > 1e-12:
                raise AssertionError(
                    f"{th.name} {token(role, name)}: port {mine} vs report {theirs}"
                )
            if mine < FLOOR:
                raise AssertionError(
                    f"{th.name} {token(role, name)} lifted to {mine}, under floor"
                )
            measured[name] = (shipped, mine)
        if th.name == AUTHORED_THEME:
            verify_achromatic_ramp(th.name, measured)


def verify_achromatic_ramp(
    theme_name: str, measured: dict[str, tuple[int, float]]
) -> None:
    """Refuse to emit an achromatic ramp that is not strictly ordered.

    The collapse this ramp exists to undo was silent: every value cleared the
    floor, so nothing failed, and three codes rendered as one colour. The floor
    alone cannot catch that — only the ORDER can, so it is checked here as well as
    re-measured in css-contrast.py through the stylesheet.
    """
    ramp = [(n, *measured[n]) for n in ACHROMATIC]
    if len({v for _n, v, _r in ramp}) != len(ramp):
        raise AssertionError(
            f"{theme_name} achromatic ramp collapsed: "
            + ", ".join(f"{n}={as_hex(v)}" for n, v, _r in ramp)
        )
    ratios = [r for _n, _v, r in ramp]
    if ratios != sorted(ratios, reverse=True):
        raise AssertionError(
            f"{theme_name} achromatic ramp out of order: "
            + ", ".join(f"{n} {cc.fmt(r)}" for n, _v, r in ramp)
        )


# ----------------------------------------------------------------------- output

DARK_HEADER = """\
    /* ---- SEEDS: the 16-colour ANSI palette --------------------------------
     * GENERATED by scripts/css-ansi-palette.py. Do not hand-edit: the generator
     * is the source of these 32 values and `css-ansi-palette.py check` fails on
     * any divergence. 15-ansi.css reads them; css-contrast.py measures them
     * through that stylesheet, so neither the report nor this block can drift
     * from what ships.
     *
     * ANSI renders on ONE surface: --c-bg-secondary, a tool card, reached by
     * `.tool-output pre` -> `.tool-call`. Agent command output moved into the
     * card that spawned it, so the agent-terminal pane and its --c-term-bg are
     * gone, and the live shell panel never reads these codes at all.
     *
     * DEFERRED to web-terminal-engine, in both of its layers. The slot values
     * are kitty's published defaults (kitty/options/definition.py color0-15),
     * which is the table the engine's vt/wire.go basic16RGB resolves 0-15 to;
     * the 16 slots are terminal-DEFINED, so that is a palette CHOICE, and kitty
     * is the reference because it is the only widely-used terminal whose default
     * background is pure black — web-terminal-ui's default too. Deferring is
     * what makes SGR 34 the same blue here, in the shell panel two panes away,
     * and in both sibling apps.
     *
     * Not byte-identical to that table, and the difference has one cause: the
     * engine's SECOND layer. vt/contrast.go ensureContrast blends a foreground
     * toward white or black, away from the background, until it clears 4.5:1,
     * and never touches the background; web-terminal-server sets that floor.
     * kitty's 16 clear it on only 13 slots against pure black and 10 against the
     * transcript card, so identity without the lift is text nobody can read. The
     * engine lifts per RUN at render time. A custom property holds one value and
     * the server does not know the client's theme, so the lift is applied
     * offline and baked in here — only to the entries that FAIL, and only as far
     * as needed, which is why most of this ramp is kitty's actual colour.
     */"""

LIGHT_HEADER = """\
    /* The ANSI palette against the LIGHT card. The 12 CHROMATIC entries are the
     * same deferral: 11 of them lift here against 6 in dark, because kitty's
     * slots are chosen for a pure-black background and a light surface is the
     * opposite end. They keep their hue and lose their brightness — blending
     * toward black scales all three channels, so the angle survives while yellow
     * arrives as an olive. A light surface has only the dark half of the range
     * available for text; that is the cost of a light theme, not of this table.
     *
     * The four ACHROMATIC entries are AUTHORED here, and they are the only values
     * in either theme that are neither kitty's nor the engine's function applied
     * to kitty's. Deferring cost more than it bought: kitty defines those four by
     * LIGHTNESS alone, a light card can use none of it, and the lift drove white,
     * bright-black and bright-white onto ONE grey, because it stops at the
     * smallest blend clearing the floor and that is a fixed target luminance. So
     * ESC[90m (de-emphasis) and ESC[97m (emphasis) rendered identically, which
     * deletes a distinction rather than washing one out, and #666 preserved no
     * part of 0xdddddd — there was no hue left to defer to.
     *
     * The ramp is even steps in kitty's own 8-bit space, from color0 (which
     * clears at 16.6:1 unaided) to the darkest grey the floor allows, ordered by
     * the lightness each code ASKED for: on a light background ESC[30m asks for
     * the loudest thing available and ESC[97m asks for something invisible, so
     * the quietest legible grey is the honest rendering of "bright white".
     *
     * DARK is left alone deliberately. There the lift collapses `black` onto
     * `bright-black`, and both of those ask for something too dark to read on a
     * dark card, so one answer for both is honest; `white` and `bright-white`
     * keep kitty's spread, so three distinct levels survive. Light had one.
     */"""

FILL_HEADER = """\
    /* The fills. The engine has no second ramp and needs none — it lifts the ink
     * against whatever background is really there, an ANSI one included. CSS
     * cannot pair-match, and one ramp measures 256 of 256 pairs under 4.5:1
     * (`ESC[31;41m` is red on red), so the fills are derived by the same
     * machinery with the roles swapped: the fill moves, away from the ink set,
     * until every ink clears 4.5:1 on it.
     *
     * The consequence is arithmetic, not taste. Carrying the darkest ink pins a
     * fill past the surface's own luminance, so against the surface these read
     * {lo}:1 .. {hi}:1 — a fill WITH text on it is fully legible and a TEXTLESS
     * coloured region is quiet. css-contrast.py records that figure without
     * gating it, because gating it would be 32 standing failures for a floor no
     * palette that keeps its hues can reach. */"""


def fill_header(theme) -> str:
    surface = int(theme.flat(SURFACE).hex(), 16)
    vs = [
        contrast_ratio(v, surface)
        for role, _n, _k, v, _r, _o in rows(theme)
        if role == "fill"
    ]
    return FILL_HEADER.format(lo=cc.fmt(min(vs)), hi=cc.fmt(max(vs)))


def emit(themes) -> None:
    for th in themes:
        r = rows(th)
        print(DARK_HEADER if th.name == "dark" else LIGHT_HEADER)
        for role, name, _k, v, _ratio, _o in r:
            if role == "ink":
                print(f"    --c-term-{name}: {as_hex(v)};")
        print()
        print(fill_header(th))
        for role, name, _k, v, _ratio, _o in r:
            if role == "fill":
                print(f"    --c-term-{name}-bg: {as_hex(v)};")


def table(themes) -> None:
    print("theme\trole\ttoken\tkitty\tshipped\torigin\tratio")
    for th in themes:
        for role, name, kitty, shipped, ratio, src in rows(th):
            print(
                f"{th.name}\t{role}\t{token(role, name)}\t{as_hex(kitty)}\t{as_hex(shipped)}\t"
                f"{src}\t{cc.fmt(ratio)}"
            )


def check(themes) -> int:
    drift = []
    for th in themes:
        for role, name, _kitty, shipped, _ratio, _o in rows(th):
            tok = token(role, name)
            have = th.decls.get(tok)
            want = as_hex(shipped)
            if have is None:
                drift.append(f"{th.name} {tok}: not declared, want {want}")
            elif " ".join(have.split()).lower() != want:
                drift.append(
                    f"{th.name} {tok}: 01-tokens.css has {have}, generator says {want}"
                )
    for line in drift:
        print(line)
    print(f"{len(drift)} drifted")
    return 1 if drift else 0


def main() -> int:
    cmd = sys.argv[1] if len(sys.argv) > 1 else "emit"
    want = sys.argv[2] if len(sys.argv) > 2 else None
    dark, light = cc.parse_themes()
    themes = [dark, light]
    verify_against_report(themes)
    if cmd == "emit":
        emit([th for th in themes if want in (None, th.name)])
        return 0
    if cmd == "table":
        table(themes)
        return 0
    if cmd == "check":
        return check(themes)
    print(__doc__)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
