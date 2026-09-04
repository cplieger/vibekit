// The shell paints in the first frame: structural guard over static/index.html
// and static/manifest.json.
//
// Both halves of the reported "5-10 s blank, unresponsive window" were shape
// rather than compute. An opaque `#app-loading` at `z-index: 400` sat over an
// `#app` that was `visibility: hidden`, and the ONLY thing that released either
// was a call at the end of a five-`await` boot chain — measured p50 754 ms, p90
// 1,947 ms, outliers to 21,026 ms, and three boots in 68 where it was never
// released at all because whoami timed out and the branch returned early. From
// the user's seat that is indistinguishable from a blocked main thread. And
// `launch_handler.client_mode: "navigate-existing"` made a Windows taskbar
// restore RE-NAVIGATE the window, so the live page and its SSE stream were thrown
// away: 81 document loads in 48 h, 33 of them to bare `/`.
//
// Asserted against the shipped files rather than a built DOM, because what has to
// hold is a property of the ARTIFACT: nothing may reintroduce the overlay, and no
// script has to run for the shell to be visible.

import { describe, it, expect } from "vitest";
import indexHtml from "../static/index.html?raw";
import manifestRaw from "../static/manifest.json?raw";
import a11yCss from "./css/40-a11y.css?raw";

/** Parse a slice of the shell rather than the whole document: a full-document
 *  parse makes the runner chase the <link rel=stylesheet> over the network. */
function slice(html: string, from: string, to: string): HTMLElement {
  const start = html.indexOf(from);
  const end = html.indexOf(to, start + 1);
  expect(start, `marker not found: ${from}`).toBeGreaterThan(-1);
  expect(end, `marker not found: ${to}`).toBeGreaterThan(start);
  const host = document.createElement("div");
  host.innerHTML = html.slice(start, end);
  return host;
}

describe("the shell has nothing covering it", () => {
  it("ships no splash overlay and no hidden app root", () => {
    expect(indexHtml).not.toContain("app-loading");
    expect(indexHtml).not.toContain("app-hidden");
    // The rules that made either of them opaque are gone with them, so a
    // reintroduced element cannot inherit a working overlay.
    expect(a11yCss).not.toContain("app-loading");
    expect(a11yCss).not.toContain("app-hidden");
  });

  it("opens #app with no class at all", () => {
    const m = /<div id="app"([^>]*)>/.exec(indexHtml);
    expect(m, "#app must be declared in index.html").not.toBeNull();
    expect(m?.[1]?.trim()).toBe("");
  });
});

describe("every region whose content is pending says so", () => {
  it("gives the tab strip authored skeleton rows", () => {
    const strip = slice(indexHtml, '<div id="tab-list"', '<div class="sidebar-footer">');
    const skeleton = strip.querySelector("#tab-strip-skeleton");
    expect(skeleton, "#tab-strip-skeleton is the strip's pending state").not.toBeNull();
    // Announced to nobody: it stands in for content, so a screen reader must not
    // read three empty rows.
    expect(skeleton?.getAttribute("aria-hidden")).toBe("true");
    // Every row carries `.skeleton`, which is what supplies the shimmer.
    const rows = skeleton?.querySelectorAll(".skeleton.skeleton-tab-row") ?? [];
    expect(rows.length).toBeGreaterThan(0);
    // No `data-tab-id` on any of them: tabs.ts reconciles the strip's children by
    // that attribute, so the placeholder and the real rows cannot contend.
    for (const row of rows) {
      expect((row as HTMLElement).dataset["tabId"]).toBeUndefined();
    }
  });

  it("gives the sidebar identity row a pending shimmer", () => {
    const footer = slice(indexHtml, '<a id="user-email"', "</a>");
    const pending = footer.querySelector(".skeleton.sidebar-email-skeleton");
    expect(pending, "the identity row is pending until /api/whoami answers").not.toBeNull();
    expect(pending?.getAttribute("aria-hidden")).toBe("true");
  });
});

describe("a relaunch focuses the running app", () => {
  it("declares focus-existing, so a taskbar restore is not a navigation", () => {
    const manifest: unknown = JSON.parse(manifestRaw);
    expect(manifest).toMatchObject({ launch_handler: { client_mode: "focus-existing" } });
  });
});
