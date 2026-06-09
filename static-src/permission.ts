// ---------------------------------------------------------------------------
// Permission dialog: shown when the agent requests permission for a tool.
//
// Includes an input preview so the user can see what the agent is asking
// to do (e.g. the full bash command, the file path + content) before
// approving or denying.
// ---------------------------------------------------------------------------

import type { PermissionOption } from "./types.js";
import { el } from "@cplieger/reactive";
import { scroll } from "./scroll.js";
import { $ } from "./dom.js";
import { mcpToolInfo, formatMCPToolName } from "./tool-schema.js";
import { getSubagentName } from "./crew-card.js";
import { addWhitelistEntry } from "./permissions-ui.js";

const approvalEl = $.toolApproval;

const PREVIEW_CHAR_CAP = 500;

export function showPermissionDialog(
  title: string,
  toolCallId: string,
  kind: string,
  input: unknown,
  options: PermissionOption[],
  onSelect: (optionId: string) => void,
  subSessionId?: string,
): void {
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const content = approvalEl.querySelector(".approval-body")!;
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const actions = approvalEl.querySelector(".approval-actions")!;

  const isModeSwitch = kind === "switch_mode";
  const isSubagent = subSessionId !== undefined && subSessionId !== "";
  approvalEl.classList.toggle("mode-switch", isModeSwitch);
  approvalEl.classList.toggle("subagent-permission", isSubagent);

  const preview = formatInputPreview(input);

  const mcp = mcpToolInfo(title);
  const heading = isModeSwitch
    ? "Switch session mode"
    : mcp !== null
      ? formatMCPToolName(mcp.tool)
      : title;

  // Build content via DOM construction (no innerHTML).
  content.replaceChildren();

  content.appendChild(el("strong", null, heading));

  if (isModeSwitch) {
    content.appendChild(el("div", { className: "approval-origin" }, title));
  } else if (isSubagent) {
    const agentName = getSubagentName(subSessionId);
    content.appendChild(
      el("div", { className: "approval-origin" }, "for subagent ", el("strong", null, agentName)),
    );
  } else if (mcp !== null) {
    content.appendChild(
      el(
        "div",
        { className: "approval-origin" },
        "from ",
        el("strong", null, mcp.server),
        " MCP integration",
      ),
    );
  }

  content.appendChild(el("div", { className: "approval-id" }, toolCallId));

  if (preview !== "") {
    content.appendChild(el("pre", { className: "approval-input" }, preview));
  }

  actions.replaceChildren();
  for (const opt of options) {
    const btn = el(
      "button",
      {
        className: opt.kind.startsWith("allow")
          ? "btn-small confirm-allow"
          : "btn-small confirm-danger",
      },
      opt.name,
    );
    btn.addEventListener("click", () => {
      onSelect(opt.option_id);
      hidePermission();
    });
    actions.appendChild(btn);
  }

  // "Always allow..." expansion for shell commands.
  if (kind === "execute" && !isModeSwitch && !isSubagent) {
    const alwaysRow = buildAlwaysAllowRow(title, options, onSelect);
    if (alwaysRow !== null) {
      actions.appendChild(alwaysRow);
    }
  }

  approvalEl.showModal();
  scroll();
}

/** Turn a tool input (usually an object, sometimes raw JSON string or
 *  undefined) into a user-readable preview string. Returns "" if there is
 *  nothing meaningful to show — caller skips the preview block entirely. */
function formatInputPreview(input: unknown): string {
  if (input === undefined || input === null) {
    return "";
  }
  let text: string;
  if (typeof input === "string") {
    // kiro-cli sometimes sends rawInput as a pre-serialized JSON string;
    // try to reparse for pretty-print, fall back to the raw string.
    try {
      text = JSON.stringify(JSON.parse(input), null, 2);
    } catch {
      text = input;
    }
  } else if (typeof input === "object") {
    if (Object.keys(input).length === 0) {
      return "";
    }
    text = JSON.stringify(input, null, 2);
  } else {
    // eslint-disable-next-line @typescript-eslint/no-base-to-string
    text = String(input);
  }

  if (text.length > PREVIEW_CHAR_CAP) {
    const remainder = text.length - PREVIEW_CHAR_CAP;
    text = text.slice(0, PREVIEW_CHAR_CAP) + `\n… (${String(remainder)} more characters)`;
  }
  return text;
}

/** Build the "Always allow..." expansion for shell commands. Returns null
 *  if the command can't be decomposed into useful presets. */
function buildAlwaysAllowRow(
  command: string,
  options: PermissionOption[],
  onSelect: (optionId: string) => void,
): HTMLDetailsElement | null {
  const allowOpt = options.find((o) => o.kind.startsWith("allow"));
  if (allowOpt === undefined) {
    return null;
  }

  const parts = command.trim().split(/\s+/);
  if (parts.length === 0) {
    return null;
  }
  const base = parts[0] ?? "";
  if (base === "") {
    return null;
  }

  // Build preset patterns: base command, base + flags, exact, wildcard.
  const presets: string[] = [];
  presets.push(`${base} *`);
  if (parts.length > 1) {
    // "base flags *" if there are flags before the last arg.
    const withFlags = parts.slice(0, -1).join(" ") + " *";
    if (withFlags !== `${base} *`) {
      presets.push(withFlags);
    }
    presets.push(command.trim());
  }

  const body = el("div", { className: "always-allow-body" });

  for (const pattern of presets) {
    const row = el(
      "button",
      { type: "button", className: "always-allow-preset" },
      el("code", null, pattern),
    );
    row.addEventListener("click", () => {
      void addWhitelistEntry(pattern);
      onSelect(allowOpt.option_id);
      hidePermission();
    });
    body.appendChild(row);
  }

  // Custom pattern input.
  const input = el("input", {
    type: "text",
    className: "chip-input",
    placeholder: `${base} *`,
    "aria-label": "Custom command pattern",
  }) as HTMLInputElement;
  const addBtn = el("button", { type: "button", className: "action-pill" }, "Add");
  addBtn.addEventListener("click", () => {
    const val = input.value.trim();
    if (val === "") {
      return;
    }
    void addWhitelistEntry(val);
    onSelect(allowOpt.option_id);
    hidePermission();
  });
  body.appendChild(el("div", { className: "always-allow-custom" }, input, addBtn));

  return el(
    "details",
    { className: "always-allow-details" },
    el("summary", { className: "always-allow-summary" }, "Always allow\u2026"),
    body,
  ) as HTMLDetailsElement;
}

export function hidePermission(): void {
  approvalEl.close();
}
