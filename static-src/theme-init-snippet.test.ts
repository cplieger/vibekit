// @vitest-environment node
//
// Drift guard for the anti-FOUC theme-init script inlined in static/index.html.
// That inline <head> IIFE must stay byte-identical to @cplieger/ui-primitives'
// themeInitSnippetFromJSON("vibekit.ui-state", "theme") output: the library owns
// the pre-paint theme resolution, and internal/server/security.go hashes the
// exact bytes into the CSP. If the library changes the snippet, this test fails
// and forces a regeneration of the inline block rather than letting it drift.
import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { describe, it, expect } from "vitest";
import { themeInitSnippetFromJSON } from "@cplieger/ui-primitives/theme";
import { LS_UI_STATE_KEY } from "./ls-keys.js";

const here = dirname(fileURLToPath(import.meta.url));
const indexPath = join(here, "..", "static", "index.html");
// Stryker's sandbox copies static-src only (ignorePatterns excludes
// ../static), so this drift guard is skipped under mutation runs; every
// normal vitest run still enforces it against the real static/index.html.
const inSandbox = !existsSync(indexPath);
const indexHtml = inSandbox ? "" : readFileSync(indexPath, "utf8");

describe.skipIf(inSandbox)("anti-FOUC theme-init snippet (static/index.html)", () => {
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
