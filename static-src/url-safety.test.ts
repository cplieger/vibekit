import { describe, it, expect } from "vitest";
import { isSafeURL } from "./url-safety.js";

describe("isSafeURL", () => {
  it("accepts http and https", () => {
    expect(isSafeURL("https://auth.example.com/oauth?code=1")).toBe(true);
    expect(isSafeURL("http://localhost:8080/callback")).toBe(true);
    // URL parsing lowercases the scheme.
    expect(isSafeURL("HTTPS://Example.com")).toBe(true);
  });

  it("rejects non-http(s) schemes and unparseable input", () => {
    for (const u of [
      "file:///etc/passwd",
      "javascript:alert(1)",
      "data:text/html,<script>",
      "ftp://example.com",
      "vscode://open",
      "",
      "not a url",
      "//example.com",
    ]) {
      expect(isSafeURL(u), u).toBe(false);
    }
  });
});
