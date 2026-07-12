// Unit tests for mcp-content.ts: extracting insertable text from raw MCP
// prompt/resource results (single-block, array-block, non-text, empty).

import { describe, it, expect } from "vitest";
import { blockText, promptResultToText, resourceResultToText } from "./mcp-content.js";

describe("blockText", () => {
  it("returns empty for undefined content", () => {
    expect(blockText(undefined)).toBe("");
  });

  it("reads a single text block object", () => {
    expect(blockText({ type: "text", text: "hello" })).toBe("hello");
  });

  it("treats a typeless block as text", () => {
    expect(blockText({ text: "no type" })).toBe("no type");
  });

  it("joins an array of text blocks and skips non-text", () => {
    expect(
      blockText([
        { type: "text", text: "a" },
        { type: "image", text: "ignored" },
        { type: "text", text: "b" },
      ]),
    ).toBe("a\nb");
  });
});

describe("promptResultToText", () => {
  it("joins message texts with blank lines", () => {
    const res = {
      messages: [
        { role: "user", content: { type: "text", text: "First." } },
        { role: "assistant", content: { type: "text", text: "Second." } },
      ],
    };
    expect(promptResultToText(res)).toBe("First.\n\nSecond.");
  });

  it("handles the live-probe single-block shape", () => {
    // Shape captured from @modelcontextprotocol/server-everything.
    const res = {
      messages: [{ role: "user", content: { type: "text", text: "What's weather in Paris, TX?" } }],
    };
    expect(promptResultToText(res)).toBe("What's weather in Paris, TX?");
  });

  it("returns empty when there are no messages", () => {
    expect(promptResultToText({})).toBe("");
    expect(promptResultToText({ messages: [] })).toBe("");
  });
});

describe("resourceResultToText", () => {
  it("joins text contents and skips binary blobs", () => {
    const res = {
      contents: [
        { uri: "demo://a", mimeType: "text/markdown", text: "# Doc" },
        { uri: "demo://b", blob: "aGVsbG8=" }, // binary: no text → skipped
        { uri: "demo://c", text: "more" },
      ],
    };
    expect(resourceResultToText(res)).toBe("# Doc\n\nmore");
  });

  it("returns empty when there are no contents", () => {
    expect(resourceResultToText({})).toBe("");
  });
});
