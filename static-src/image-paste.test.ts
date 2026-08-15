import { describe, expect, it, vi } from "vitest";

import {
  clipboardImages,
  downscaleImage,
  pastedImageName,
  prepareForUpload,
} from "./image-paste.js";

describe("pastedImageName", () => {
  it("is unique per second and sorts chronologically", () => {
    const a = pastedImageName("image/png", new Date("2026-08-15T08:42:11.000Z"));
    const b = pastedImageName("image/png", new Date("2026-08-15T08:42:12.000Z"));
    expect(a).toBe("pasted-2026-08-15T08-42-11.png");
    expect(a < b).toBe(true);
  });

  it("carries no colon, which is illegal in a filename on some platforms", () => {
    expect(pastedImageName("image/png")).not.toContain(":");
  });

  it("matches the extension to the encoded type", () => {
    // The server keys its MIME lookup off the extension, so a .png name on JPEG
    // bytes would be inlined with the wrong mimeType.
    expect(pastedImageName("image/jpeg")).toMatch(/\.jpg$/);
    expect(pastedImageName("image/webp")).toMatch(/\.webp$/);
    expect(pastedImageName("image/gif")).toMatch(/\.png$/); // re-encoded
  });
});

describe("clipboardImages", () => {
  const asDT = (files: File[]): DataTransfer => ({ files }) as unknown as DataTransfer;

  it("returns nothing for a text paste, so the event falls through", () => {
    expect(clipboardImages(asDT([]))).toEqual([]);
    expect(clipboardImages(null)).toEqual([]);
  });

  it("picks images and ignores other files on the same clipboard", () => {
    const png = new File(["x"], "a.png", { type: "image/png" });
    const txt = new File(["x"], "a.txt", { type: "text/plain" });
    expect(clipboardImages(asDT([txt, png, txt]))).toEqual([png]);
  });
});

describe("downscaleImage", () => {
  it("returns the input untouched when the browser lacks the APIs", async () => {
    // No OffscreenCanvas in happy-dom, which is also the real fallback path.
    const f = new File(["x"], "a.png", { type: "image/png" });
    await expect(downscaleImage(f)).resolves.toBe(f);
  });

  it("never loses the paste when decoding throws", async () => {
    vi.stubGlobal(
      "createImageBitmap",
      vi.fn(() => Promise.reject(new Error("bad image"))),
    );
    vi.stubGlobal(
      "OffscreenCanvas",
      class {
        constructor(
          public w: number,
          public h: number,
        ) {}
      },
    );
    const f = new File(["x"], "a.png", { type: "image/png" });
    await expect(downscaleImage(f)).resolves.toBe(f);
    vi.unstubAllGlobals();
  });
});

describe("prepareForUpload", () => {
  it("renames even an image it did not resize", async () => {
    // Uploads land in the workspace root and clipboard images are all called
    // "image.png", so two pastes would collide without this.
    const a = new File(["x"], "image.png", { type: "image/png" });
    const b = new File(["y"], "image.png", { type: "image/png" });
    const [pa, pb] = await prepareForUpload([a, b]);
    expect(pa?.name).toMatch(/^pasted-.*\.png$/);
    expect(pb?.name).toMatch(/^pasted-.*\.png$/);
    expect(pa?.type).toBe("image/png");
  });
});
