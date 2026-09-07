import { describe, expect, it, vi } from "vitest";
import type { PermissionNeededPayload } from "./types.js";

vi.mock("./settings-highlight.js", () => ({ openSetting: vi.fn() }));
vi.mock("./actions/permissions.js", () => ({
  editNativeRule: { dispatch: vi.fn() },
}));
vi.mock("./navigate.js", () => ({ openChange: vi.fn() }));

import { buildPermissionCard } from "./permission.js";

function ask(over: Partial<PermissionNeededPayload>): PermissionNeededPayload {
  return {
    request_id: 1,
    title: "shell command",
    kind: "execute",
    options: [{ option_id: "a", name: "Allow", kind: "allow_once" }],
    ...over,
  } as PermissionNeededPayload;
}

describe("permission MCP attribution", () => {
  it("does not infer MCP identity from a shell command title", () => {
    const card = buildPermissionCard(
      "chat-1",
      ask({ title: "mcp__issues__create_issue" }),
      vi.fn(),
    );

    expect(card.querySelector(".approval-body strong")?.textContent).toBe(
      "mcp__issues__create_issue",
    );
    expect(card.querySelector(".approval-origin")).toBeNull();
  });

  it("renders MCP identity carried by the verified field", () => {
    const card = buildPermissionCard(
      "chat-1",
      ask({
        title: "model-authored title",
        mcp_tool: { server_name: "issues", tool_name: "create_issue" },
      }),
      vi.fn(),
    );

    expect(card.querySelector(".approval-body strong")?.textContent).toBe("create issue");
    expect(card.querySelector(".approval-origin")?.textContent).toBe("from issues MCP integration");
  });
});
