// ---------------------------------------------------------------------------
// Template-based SVG icon cloning. Avoids innerHTML in the render path
// by parsing each SVG string once and cloning on subsequent uses.
// ---------------------------------------------------------------------------

const iconTemplate = document.createElement("template");
const iconCache = new Map<string, Element>();

/** Parse an SVG string once via <template>, then clone on each call.
 *  Avoids innerHTML in the render path. */
export function iconEl(svg: string): Element {
  let cached = iconCache.get(svg);
  if (cached === undefined) {
    iconTemplate.innerHTML = svg;
    cached = iconTemplate.content.firstElementChild!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    iconCache.set(svg, cached);
  }
  return cached.cloneNode(true) as Element;
}
