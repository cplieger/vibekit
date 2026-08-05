import { describe, it, expect } from "vitest";
import { rewriteWorkspaceImageSrc } from "./utils-url.js";
describe("rewriteWorkspaceImageSrc", () => {
  // The defect it fixes: the agent drives the Chromium sidecar, writes
  // ![shot](/workspace/out/shot.png), and the browser asked the SPA for that
  // path — whose fallback returns index.html. A broken image, every time.
  it("points a workspace image at the byte-serving route", () => {
    expect(rewriteWorkspaceImageSrc("/workspace/out/shot.png")).toBe(
      "/api/file/download?path=%2Fworkspace%2Fout%2Fshot.png",
    );
  });

  it("covers the image extensions an agent actually writes", () => {
    for (const ext of ["png", "jpg", "jpeg", "gif", "webp", "svg", "avif", "PNG"]) {
      const got = rewriteWorkspaceImageSrc(`/workspace/a.${ext}`);
      expect(got.startsWith("/api/file/download?path=")).toBe(true);
    }
  });

  // The extension list is closed on purpose: without it, any `![](…)` the model
  // writes would become a file read of an arbitrary workspace path.
  it("leaves a non-image workspace path alone", () => {
    for (const p of ["/workspace/.env", "/workspace/id_rsa", "/workspace/notes.md"]) {
      expect(rewriteWorkspaceImageSrc(p)).toBe(p);
    }
  });

  it("leaves remote and relative sources untouched", () => {
    for (const p of [
      "https://example.com/a.png",
      "./local.png",
      "/config/secret.png",
      "workspace/a.png",
    ]) {
      expect(rewriteWorkspaceImageSrc(p)).toBe(p);
    }
  });
});
