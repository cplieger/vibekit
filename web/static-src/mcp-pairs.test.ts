// @vitest-environment happy-dom
// Unit tests for mcp-pairs.ts — collectKeyPairs and renderKeyPairList.
import { describe, it, expect, vi } from "vitest";

vi.mock("./icons.js", () => ({ ICON_CLOSE: "<svg></svg>" }));

import { collectKeyPairs, renderKeyPairList } from "./mcp-pairs.js";
import { SECRET_MASK } from "./mcp-state.js";

describe("collectKeyPairs", () => {
  function makeRow(name: string, value: string, secret = false): HTMLDivElement {
    const row = document.createElement("div");
    row.className = "mcp-pair-row";
    const nameIn = document.createElement("input");
    nameIn.className = "mcp-pair-name";
    nameIn.value = name;
    const valIn = document.createElement("input");
    valIn.className = "mcp-pair-value";
    valIn.value = value;
    if (secret) {valIn.dataset["secret"] = "true";}
    row.append(nameIn, valIn);
    return row;
  }

  it("skips empty names", () => {
    const host = document.createElement("div");
    host.appendChild(makeRow("", "some-value"));
    host.appendChild(makeRow("VALID", "val"));
    const result = collectKeyPairs(host);
    expect(result).toEqual([{ name: "VALID", value: "val" }]);
  });

  it("preserves SECRET_MASK for untouched secrets", () => {
    const host = document.createElement("div");
    host.appendChild(makeRow("TOKEN", SECRET_MASK, true));
    const result = collectKeyPairs(host);
    expect(result).toEqual([{ name: "TOKEN", value: SECRET_MASK }]);
  });

  it("reads plain value for non-secret", () => {
    const host = document.createElement("div");
    host.appendChild(makeRow("KEY", "plain-text"));
    const result = collectKeyPairs(host);
    expect(result).toEqual([{ name: "KEY", value: "plain-text" }]);
  });
});

describe("renderKeyPairList", () => {
  it("with empty array creates one blank row", () => {
    const host = document.createElement("div");
    renderKeyPairList(host, [], "env");
    const rows = host.querySelectorAll(".mcp-pair-row");
    expect(rows.length).toBe(1);
    const nameIn = rows[0]!.querySelector<HTMLInputElement>(".mcp-pair-name");
    const valIn = rows[0]!.querySelector<HTMLInputElement>(".mcp-pair-value");
    expect(nameIn!.value).toBe("");
    expect(valIn!.value).toBe("");
  });
});
