// Tests for sse-tag.ts: the endpoint-derived presence tag and its adoption.
//
// The derivation is a CROSS-LANGUAGE contract: internal/push/tag.go computes the same
// value for the same endpoint, and the fixture both halves read is Go's
// testdata/tag_golden.json, so a drift on either side fails a test rather than
// silencing a device.

import { describe, it, expect, beforeEach, vi } from "vitest";
import goldenRaw from "../internal/push/testdata/tag_golden.json?raw";
import { adoptSubscriptionTag, derivedTag, persistedTag, PROFILE_TAG_KEY } from "./sse-tag.js";

const golden = JSON.parse(goldenRaw) as { endpoint: string; tag: string }[];

beforeEach(() => {
  localStorage.removeItem(PROFILE_TAG_KEY);
});

describe("derivedTag", () => {
  it("matches the Go derivation on every golden endpoint", async () => {
    expect(golden.length).toBeGreaterThan(0);
    for (const { endpoint, tag } of golden) {
      expect(await derivedTag(endpoint)).toBe(tag);
      expect(tag).toHaveLength(22);
    }
  });

  it("stays inside the SSE-Client grammar", async () => {
    // Enough endpoints that a '+' or '/' in the raw base64 would have surfaced.
    for (let i = 0; i < 64; i++) {
      const tag = await derivedTag(`https://fcm.googleapis.com/fcm/send/${String(i)}`);
      expect(tag).toMatch(/^[A-Za-z0-9_-]{22}$/);
    }
  });
});

describe("persistedTag", () => {
  it("returns the persisted tag when it is well formed", () => {
    localStorage.setItem(PROFILE_TAG_KEY, "amxAEqwvwjG23476CxNmK6");
    expect(persistedTag()).toBe("amxAEqwvwjG23476CxNmK6");
  });

  it("mints and persists a 22-character random tag when none is held", () => {
    const tag = persistedTag();
    expect(tag).toMatch(/^[A-Za-z0-9_-]{22}$/);
    expect(localStorage.getItem(PROFILE_TAG_KEY)).toBe(tag);
    expect(persistedTag()).toBe(tag);
  });

  it("replaces a persisted value outside the grammar", () => {
    localStorage.setItem(PROFILE_TAG_KEY, "not a tag!");
    const tag = persistedTag();
    expect(tag).toMatch(/^[A-Za-z0-9_-]{22}$/);
    expect(localStorage.getItem(PROFILE_TAG_KEY)).toBe(tag);
  });
});

describe("adoptSubscriptionTag", () => {
  const endpoint = golden[0]?.endpoint ?? "";
  const derived = golden[0]?.tag ?? "";

  it("persists the derived tag and reconnects once when it differs from the presented one", async () => {
    const reconnect = vi.fn();
    const got = await adoptSubscriptionTag({ endpoint }, "randomrandomrandom1234", reconnect);
    expect(got).toBe(derived);
    expect(localStorage.getItem(PROFILE_TAG_KEY)).toBe(derived);
    expect(reconnect).toHaveBeenCalledTimes(1);
    expect(reconnect).toHaveBeenCalledWith(derived);
  });

  it("reconnects nothing when the subscription resolves to the tag already presented", async () => {
    const reconnect = vi.fn();
    const got = await adoptSubscriptionTag({ endpoint }, derived, reconnect);
    expect(got).toBe(derived);
    expect(reconnect).not.toHaveBeenCalled();
    expect(localStorage.getItem(PROFILE_TAG_KEY)).toBeNull();
  });

  it("keeps the presented tag for a profile with no subscription", async () => {
    const reconnect = vi.fn();
    expect(await adoptSubscriptionTag(null, "randomrandomrandom1234", reconnect)).toBe(
      "randomrandomrandom1234",
    );
    expect(reconnect).not.toHaveBeenCalled();
  });
});
