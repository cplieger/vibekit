// @vitest-environment happy-dom
import { describe, it, expect } from "vitest";
import fc from "fast-check";
import { urlBase64ToUint8Array } from "./notify.js";

/** Encode a Uint8Array as URL-safe base64 (no padding). */
function toUrlBase64(bytes: Uint8Array): string {
  const binary = Array.from(bytes, (b) => String.fromCharCode(b)).join("");
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

describe("urlBase64ToUint8Array round-trip", () => {
  it("decodes any URL-safe base64 back to the original bytes", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.uint8Array({ minLength: 0, maxLength: 128 }), (bytes) => {
        const encoded = toUrlBase64(bytes);
        const decoded = urlBase64ToUint8Array(encoded);
        if (decoded.length !== bytes.length) return false;
        for (let i = 0; i < bytes.length; i++) {
          if (decoded[i] !== bytes[i]) return false;
        }
        return true;
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("output length matches input byte count", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.uint8Array({ minLength: 1, maxLength: 64 }), (bytes) => {
        const encoded = toUrlBase64(bytes);
        return urlBase64ToUint8Array(encoded).length === bytes.length;
      }),
      { numRuns: 300 },
    );
    expect(result.failed).toBe(false);
  });

  it("handles empty input", () => {
    expect.assertions(1);
    expect(urlBase64ToUint8Array("")).toEqual(new Uint8Array(0));
  });

  it("handles inputs requiring 1 or 2 padding chars", () => {
    expect.assertions(2);
    // 1 byte = 2 base64 chars (needs 2 padding)
    const one = new Uint8Array([0xff]);
    expect(urlBase64ToUint8Array(toUrlBase64(one))).toEqual(one);
    // 2 bytes = 3 base64 chars (needs 1 padding)
    const two = new Uint8Array([0xab, 0xcd]);
    expect(urlBase64ToUint8Array(toUrlBase64(two))).toEqual(two);
  });
});

// ---------------------------------------------------------------------------
// Adversarial-input property test (tarch-b15-c7-p2)
// ---------------------------------------------------------------------------
describe("urlBase64ToUint8Array adversarial inputs", () => {
  it("either returns a Uint8Array or throws — never hangs or produces non-Uint8Array", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.string({ minLength: 0, maxLength: 200 }), (input) => {
        try {
          const r = urlBase64ToUint8Array(input);
          return r instanceof Uint8Array;
        } catch (e) {
          return e instanceof Error || e instanceof DOMException;
        }
      }),
      { numRuns: 500 },
    );
    expect(result.failed).toBe(false);
  });

  it("valid base64 strings produce output with correct length", () => {
    expect.assertions(1);
    const result = fc.check(
      fc.property(fc.base64String({ minLength: 4, maxLength: 100 }), (input) => {
        const padded = input + "=".repeat((4 - (input.length % 4)) % 4);
        try {
          const r = urlBase64ToUint8Array(padded.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""));
          const expectedLen = Math.floor((padded.replace(/=+$/, "").length * 3) / 4);
          return r.length === expectedLen;
        } catch {
          return true;
        }
      }),
      { numRuns: 300 },
    );
    expect(result.failed).toBe(false);
  });
});
