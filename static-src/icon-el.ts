// ---------------------------------------------------------------------------
// Canonical icon -> DOM helper for the whole client. Every module that turns
// an ICON_* / FILE_ICONS SVG string into a node goes through this one
// function; icons.ts and tabs.ts no longer carry their own copies.
//
// Each unique SVG string is parsed once into a detached <template> and the
// resulting element is cached; callers get a fresh clone. Parsing via the
// HTML template (rather than DOMParser with "image/svg+xml") is what places
// the <svg> in the SVG namespace even though our icon strings carry no
// explicit xmlns -- an XML parse would leave it in the null namespace and the
// glyph would never paint. innerHTML is safe here: the inputs are static
// compile-time constants, never user input.
// ---------------------------------------------------------------------------

const SVG_NS = "http://www.w3.org/2000/svg";

// Lazily created on first call so importing this module never touches the
// DOM (keeps it safe to import from node-environment unit tests that never
// render an icon).
let iconTemplate: HTMLTemplateElement | null = null;
const iconCache = new Map<string, Element>();

/** Parse an SVG string once via <template>, cache it, and return a fresh
 *  clone on each call. Falls back to an empty <svg> when the string has no
 *  element root so callers never have to guard. */
export function iconEl(svg: string): Element {
  let cached = iconCache.get(svg);
  if (cached === undefined) {
    iconTemplate ??= document.createElement("template");
    iconTemplate.innerHTML = svg;
    cached = iconTemplate.content.firstElementChild ?? document.createElementNS(SVG_NS, "svg");
    iconCache.set(svg, cached);
  }
  return cached.cloneNode(true) as Element;
}
