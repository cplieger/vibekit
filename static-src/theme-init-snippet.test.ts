//
// Drift guard for the anti-FOUC theme-init script inlined in static/index.html.
// That inline <head> IIFE must stay byte-identical to @cplieger/ui-primitives'
// themeInitSnippetFromJSON("vibekit.ui-state", "theme") output: the library owns
// the pre-paint theme resolution, and internal/server/security.go hashes the
// exact bytes into the CSP. If the library changes the snippet, this test fails
// and forces a regeneration of the inline block rather than letting it drift.
import { describe, it, expect } from "vitest";
import { themeInitSnippetFromJSON } from "@cplieger/ui-primitives/theme";
import indexHtml from "../static/index.html?raw";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

// The page arrives as a bundled `?raw` import rather than a `node:fs` read, so
// this suite runs in the browser project with the rest of them. It also needs no
// existsSync guard any more: a Vite import is resolved at transform time, so
// there is no sandbox to escape and no skip condition to get wrong.
describe("anti-FOUC theme-init snippet (static/index.html)", () => {
  it("is present as a single data-theme-init inline script", () => {
    const count = indexHtml.split("<script data-theme-init>").length - 1;
    expect(count).toBe(1);
  });

  it("is the verbatim output of themeInitSnippetFromJSON (drift guard)", () => {
    const m = /<script data-theme-init>([\s\S]*?)<\/script>/.exec(indexHtml);
    expect(m).not.toBeNull();
    expect(m?.[1]).toBe(themeInitSnippetFromJSON(LS_UI_STATE_KEY, "theme"));
  });
});
