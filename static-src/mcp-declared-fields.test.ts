// The disclosure half of the MCP setup path: what a publisher declared about an
// env var or header has to reach the form row and the registry result, because
// dropping it is what let a server install cleanly and then fail with nothing on
// screen saying it wanted a token.
import { describe, it, expect, vi } from "vitest";

vi.mock("./icons.js", () => ({ ICON_CLOSE: "<svg></svg>" }));

import { appendKeyPair, renderKeyPairList, collectKeyPairs } from "./mcp-pairs.js";
import { SECRET_MASK } from "./mcp-state.js";

function host(): HTMLDivElement {
  return document.createElement("div");
}

describe("appendKeyPair declared-field disclosure", () => {
  it("renders the publisher's description on the row", () => {
    const h = host();
    appendKeyPair(
      h,
      {
        name: "GITHUB_TOKEN",
        value: "",
        declared: { description: "A personal access token with repo scope." },
      },
      "env",
    );
    const meta = h.querySelector(".mcp-pair-meta");
    expect(meta).not.toBeNull();
    expect(meta!.textContent).toContain("A personal access token with repo scope.");
  });

  it("marks a required field and links it to its value input", () => {
    const h = host();
    appendKeyPair(h, { name: "TOKEN", value: "", declared: { required: true } }, "env");
    const meta = h.querySelector<HTMLElement>(".mcp-pair-meta");
    const value = h.querySelector<HTMLInputElement>(".mcp-pair-value");
    expect(meta!.querySelector(".mcp-pair-mark-required")).not.toBeNull();
    expect(value!.getAttribute("aria-required")).toBe("true");
    // The marker is only useful to a screen reader if the input points at it.
    expect(value!.getAttribute("aria-describedby")).toBe(meta!.id);
    expect(meta!.id).not.toBe("");
  });

  it("marks a declared secret and says so in the placeholder", () => {
    const h = host();
    appendKeyPair(h, { name: "TOKEN", value: "", declared: { secret: true } }, "env");
    const meta = h.querySelector<HTMLElement>(".mcp-pair-meta");
    const value = h.querySelector<HTMLInputElement>(".mcp-pair-value");
    expect(meta!.textContent).toContain("Secret");
    expect(value!.placeholder).toContain("masked");
    // Not a password input: the value is empty and the user needs to be able to
    // verify what they paste. The mask round-trip is what earns type=password.
    expect(value!.type).toBe("text");
  });

  it("gives ids that do not collide across two lists in one modal", () => {
    const a = host();
    const b = host();
    appendKeyPair(a, { name: "A", value: "", declared: { required: true } }, "env");
    appendKeyPair(b, { name: "B", value: "", declared: { required: true } }, "header");
    const idA = a.querySelector<HTMLElement>(".mcp-pair-meta")!.id;
    const idB = b.querySelector<HTMLElement>(".mcp-pair-meta")!.id;
    expect(idA).not.toBe(idB);
  });

  it("adds nothing to a hand-typed row", () => {
    const h = host();
    appendKeyPair(h, { name: "PLAIN", value: "x" }, "env");
    expect(h.querySelector(".mcp-pair-meta")).toBeNull();
    expect(
      h.querySelector<HTMLInputElement>(".mcp-pair-value")!.hasAttribute("aria-describedby"),
    ).toBe(false);
  });

  it("adds nothing when the registry declared a field but said nothing about it", () => {
    const h = host();
    appendKeyPair(h, { name: "BARE", value: "", declared: {} }, "env");
    expect(h.querySelector(".mcp-pair-meta")).toBeNull();
  });

  it("still collects the pair, so disclosure does not change what is saved", () => {
    const h = host();
    renderKeyPairList(
      h,
      [
        { name: "TOKEN", value: "", declared: { required: true, secret: true } },
        { name: "REGION", value: "eu", declared: { description: "Where to talk to." } },
      ],
      "env",
    );
    h.querySelectorAll<HTMLInputElement>(".mcp-pair-value")[0]!.value = "ghp_x";
    expect(collectKeyPairs(h)).toEqual([
      { name: "TOKEN", value: "ghp_x" },
      { name: "REGION", value: "eu" },
    ]);
  });

  it("keeps the stored-secret round trip intact for a masked value", () => {
    const h = host();
    appendKeyPair(h, { name: "TOKEN", value: SECRET_MASK, declared: { secret: true } }, "env");
    const value = h.querySelector<HTMLInputElement>(".mcp-pair-value")!;
    expect(value.type).toBe("password");
    expect(collectKeyPairs(h)).toEqual([{ name: "TOKEN", value: SECRET_MASK }]);
  });
});
