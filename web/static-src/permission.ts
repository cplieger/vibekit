// ---------------------------------------------------------------------------
// Permission dialog: shown when the agent requests permission for a tool.
//
// Includes an input preview so the user can see what the agent is asking
// to do (e.g. the full bash command, the file path + content) before
// approving or denying.
// ---------------------------------------------------------------------------

import type { PermissionOption } from "./types.js";
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

  const headingEl = document.createElement("strong");
  headingEl.textContent = heading;
  content.appendChild(headingEl);

  if (isModeSwitch) {
    const originDiv = document.createElement("div");
    originDiv.className = "approval-origin";
    originDiv.textContent = title;
    content.appendChild(originDiv);
  } else if (isSubagent) {
    const agentName = getSubagentName(subSessionId);
    const originDiv = document.createElement("div");
    originDiv.className = "approval-origin";
    originDiv.append("for subagent ");
    const strong = document.createElement("strong");
    strong.textContent = agentName;
    originDiv.appendChild(strong);
    content.appendChild(originDiv);
  } else if (mcp !== null) {
    const originDiv = document.createElement("div");
    originDiv.className = "approval-origin";
    originDiv.append("from ");
    const strong = document.createElement("strong");
    strong.textContent = mcp.server;
    originDiv.appendChild(strong);
    originDiv.append(" MCP integration");
    content.appendChild(originDiv);
  }

  const idDiv = document.createElement("div");
  idDiv.className = "approval-id";
  idDiv.textContent = toolCallId;
  content.appendChild(idDiv);

  if (preview !== "") {
    const preEl = document.createElement("pre");
    preEl.className = "approval-input";
    preEl.textContent = preview;
    content.appendChild(preEl);
  }

  actions.replaceChildren();
  for (const opt of options) {
    const btn = document.createElement("button");
    btn.textContent = opt.name;
    btn.className = opt.kind.startsWith("allow")
      ? "btn-small confirm-allow"
      : "btn-small confirm-danger";
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

  const details = document.createElement("details");
  details.className = "always-allow-details";
  const summary = document.createElement("summary");
  summary.className = "always-allow-summary";
  summary.textContent = "Always allow\u2026";
  details.appendChild(summary);

  const body = document.createElement("div");
  body.className = "always-allow-body";

  for (const pattern of presets) {
    const row = document.createElement("button");
    row.type = "button";
    row.className = "always-allow-preset";
    const code = document.createElement("code");
    code.textContent = pattern;
    row.appendChild(code);
    row.addEventListener("click", () => {
      void addWhitelistEntry(pattern);
      onSelect(allowOpt.option_id);
      hidePermission();
    });
    body.appendChild(row);
  }

  // Custom pattern input.
  const custom = document.createElement("div");
  custom.className = "always-allow-custom";
  const input = document.createElement("input");
  input.type = "text";
  input.className = "chip-input";
  input.placeholder = `${base} *`;
  input.setAttribute("aria-label", "Custom command pattern");
  const addBtn = document.createElement("button");
  addBtn.type = "button";
  addBtn.className = "action-pill";
  addBtn.textContent = "Add";
  addBtn.addEventListener("click", () => {
    const val = input.value.trim();
    if (val === "") {
      return;
    }
    void addWhitelistEntry(val);
    onSelect(allowOpt.option_id);
    hidePermission();
  });
  custom.appendChild(input);
  custom.appendChild(addBtn);
  body.appendChild(custom);

  details.appendChild(body);
  return details;
}

export function hidePermission(): void {
  approvalEl.close();
}
