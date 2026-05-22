// Table-driven tests for file-extensions.ts extToLang and extToIconKey.
import { describe, it, expect } from "vitest";
import { extToLang, extToIconKey, KNOWN_EXTENSIONS, FILE_EXTS } from "./file-extensions.js";

describe("extToLang", () => {
  const langCases = Object.entries(KNOWN_EXTENSIONS)
    .filter(([, meta]) => meta.lang)
    .map(([ext, meta]) => ({ ext, lang: meta.lang! }));

  it.each(langCases)("$ext → $lang", ({ ext, lang }) => {
    expect(extToLang(ext)).toBe(lang);
  });

  it("returns empty string for unknown extension", () => {
    expect(extToLang("zzz_unknown")).toBe("");
  });

  it("returns empty string for empty string", () => {
    expect(extToLang("")).toBe("");
  });
});

describe("extToIconKey", () => {
  const iconCases = Object.entries(KNOWN_EXTENSIONS)
    .filter(([, meta]) => meta.iconKey)
    .map(([ext, meta]) => ({ ext, iconKey: meta.iconKey! }));

  it.each(iconCases)("$ext → $iconKey", ({ ext, iconKey }) => {
    expect(extToIconKey(ext)).toBe(iconKey);
  });

  it("returns empty string for unknown extension", () => {
    expect(extToIconKey("zzz_unknown")).toBe("");
  });

  it("returns empty string for empty string", () => {
    expect(extToIconKey("")).toBe("");
  });
});

describe("FILE_EXTS registry integrity", () => {
  it("FILE_EXTS length matches KNOWN_EXTENSIONS keys", () => {
    expect(FILE_EXTS.length).toBe(Object.keys(KNOWN_EXTENSIONS).length);
  });

  it("every FILE_EXTS entry exists in KNOWN_EXTENSIONS", () => {
    for (const ext of FILE_EXTS) {
      expect(KNOWN_EXTENSIONS[ext]).toBeDefined();
    }
  });
});
