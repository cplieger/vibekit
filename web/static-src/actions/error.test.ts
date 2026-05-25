import { describe, it, expect } from "vitest";
import { ActionError, hasErrorString, toActionError, classifyFetchError } from "./error.js";

describe("ActionError", () => {
  it("sets message, status, code, and cause", () => {
    const cause = new Error("root");
    const err = new ActionError("fail", { status: 409, code: "conflict", cause });
    expect(err.message).toBe("fail");
    expect(err.status).toBe(409);
    expect(err.code).toBe("conflict");
    expect(err.cause).toBe(cause);
    expect(err.name).toBe("ActionError");
    expect(err).toBeInstanceOf(Error);
  });

  it("works with no options", () => {
    const err = new ActionError("simple");
    expect(err.message).toBe("simple");
    expect(err.status).toBeUndefined();
    expect(err.code).toBeUndefined();
    expect(err.cause).toBeUndefined();
  });
});

describe("hasErrorString", () => {
  it("returns true for { error: string }", () => {
    expect(hasErrorString({ error: "bad" })).toBe(true);
  });

  it("returns false for null", () => {
    expect(hasErrorString(null)).toBe(false);
  });

  it("returns false for non-object", () => {
    expect(hasErrorString("string")).toBe(false);
    expect(hasErrorString(42)).toBe(false);
    expect(hasErrorString(undefined)).toBe(false);
  });

  it("returns false when error is not a string", () => {
    expect(hasErrorString({ error: 123 })).toBe(false);
    expect(hasErrorString({ error: null })).toBe(false);
    expect(hasErrorString({})).toBe(false);
  });
});

describe("toActionError", () => {
  it("preserves ActionError fields", () => {
    const err = new ActionError("x", { status: 404, code: "not_found" });
    const result = toActionError(err);
    expect(result.message).toBe("x");
    expect(result.status).toBe(404);
    expect(result.code).toBe("not_found");
  });

  it("maps DOMException TimeoutError", () => {
    const dom = new DOMException("timed out", "TimeoutError");
    const result = toActionError(dom);
    expect(result.code).toBe("timeout");
    expect(result.cause).toBe(dom);
  });

  it("maps DOMException AbortError", () => {
    const dom = new DOMException("aborted", "AbortError");
    const result = toActionError(dom);
    expect(result.code).toBe("cancelled");
  });

  it("maps DOMException NetworkError", () => {
    const dom = new DOMException("network", "NetworkError");
    const result = toActionError(dom);
    expect(result.code).toBe("network");
  });

  it("maps unknown DOMException name to lowercase", () => {
    const dom = new DOMException("custom", "CustomError");
    const result = toActionError(dom);
    expect(result.code).toBe("customerror");
  });

  it("handles plain Error", () => {
    const err = new Error("plain");
    const result = toActionError(err);
    expect(result.message).toBe("plain");
    expect(result.cause).toBe(err);
    expect(result.status).toBeUndefined();
  });

  it("preserves numeric status from Error-like objects", () => {
    const err = Object.assign(new Error("http fail"), { status: 503 });
    const result = toActionError(err);
    expect(result.message).toBe("http fail");
    expect(result.status).toBe(503);
    expect(result.cause).toBe(err);
  });

  it("ignores non-numeric status on Error-like objects", () => {
    const err = Object.assign(new Error("bad"), { status: "not a number" });
    const result = toActionError(err);
    expect(result.status).toBeUndefined();
  });

  it("handles non-Error values (string)", () => {
    const result = toActionError("oops");
    expect(result.message).toBe("oops");
    expect(result.cause).toBe("oops");
  });

  it("handles non-Error values (number)", () => {
    const result = toActionError(42);
    expect(result.message).toBe("42");
  });
});

describe("classifyFetchError", () => {
  it("returns cancelled when signal is aborted", () => {
    const ac = new AbortController();
    ac.abort();
    const err = classifyFetchError(new Error("fetch failed"), ac.signal);
    expect(err).toBeInstanceOf(ActionError);
    expect(err.code).toBe("cancelled");
    expect(err.cause).toBeInstanceOf(Error);
  });

  it("returns timeout for DOMException TimeoutError", () => {
    const ac = new AbortController();
    const dom = new DOMException("timed out", "TimeoutError");
    const err = classifyFetchError(dom, ac.signal);
    expect(err.code).toBe("timeout");
    expect(err.message).toBe("Request timed out");
  });

  it("returns timeout for DOMException AbortError when signal is NOT aborted", () => {
    const ac = new AbortController();
    const dom = new DOMException("aborted", "AbortError");
    const err = classifyFetchError(dom, ac.signal);
    expect(err.code).toBe("timeout");
  });

  it("returns network for non-DOMException errors", () => {
    const ac = new AbortController();
    const err = classifyFetchError(new TypeError("Failed to fetch"), ac.signal);
    expect(err.code).toBe("network");
    expect(err.message).toBe("Failed to fetch");
  });

  it("returns network with generic message for non-Error values", () => {
    const ac = new AbortController();
    const err = classifyFetchError(42, ac.signal);
    expect(err.code).toBe("network");
    expect(err.message).toBe("network error");
  });
});
