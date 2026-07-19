// ---------------------------------------------------------------------------
// Permission dialog: shown when the agent requests permission for a tool.
//
// Includes an input preview so the user can see what the agent is asking
// to do (e.g. the full bash command, the file path + content) before
// approving or denying.
// ---------------------------------------------------------------------------

import type { PermissionOption } from "./types.js";
import { el } from "@cplieger/reactive";
import { openDialog } from "@cplieger/ui-primitives/dialog";
import { scroll } from "./scroll.js";
import { $ } from "./dom.js";
import { mcpToolInfo, formatMCPToolName } from "./tool-schema.js";
import { editNativeRule } from "./actions/permissions.js";

const approvalEl = $.toolApproval;

const PREVIEW_CHAR_CAP = 500;

export function showPermissionDialog(
  title: string,
  toolCallId: string,
  kind: string,
  input: unknown,
  options: PermissionOption[],
  onSelect: (optionId: string) => void,
): void {
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const content = approvalEl.querySelector(".approval-body")!;
  // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
  const actions = approvalEl.querySelector(".approval-actions")!;

  const isModeSwitch = kind === "switch_mode";
  approvalEl.classList.toggle("mode-switch", isModeSwitch);

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
  if (kind === "execute" && !isModeSwitch) {
    const alwaysRow = buildAlwaysAllowRow(title, options, onSelect);
    if (alwaysRow !== null) {
      actions.appendChild(alwaysRow);
    }
  }

  openDialog(approvalEl);
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

/** Characters that make a command's full-string pattern meaningless to the
 *  native policy engine: it evaluates tree-sitter-split SUBCOMMANDS, so a
 *  pattern spanning shell operators can never match; `?` is a glob there
 *  (broader than intended) and `\` is rewritten to `/`. */
const UNREPRESENTABLE_RE = /[;&|`$><\\"'?\n\r]/;

/** Build the "Always allow..." expansion for shell commands: each preset
 *  persists a workspace-scope native allow rule (the same permissions.yaml
 *  the Settings → Permissions editor writes; KAS hot-reloads it), then
 *  approves the pending request. Mirrors the IDE's trust patterns — base
 *  command, base + flags, exact — skipping a leading `sudo`. Returns null
 *  when nothing useful can be offered (no allow option, or the command
 *  contains shell structure the engine evaluates per-subcommand, where a
 *  full-string pattern could never match). */
function buildAlwaysAllowRow(
  command: string,
  options: PermissionOption[],
  onSelect: (optionId: string) => void,
): HTMLDetailsElement | null {
  const allowOpt = options.find((o) => o.kind.startsWith("allow"));
  if (allowOpt === undefined) {
    return null;
  }

  const trimmed = command.trim();
  if (UNREPRESENTABLE_RE.test(trimmed)) {
    return null;
  }
  const parts = trimmed.split(/\s+/);
  // Mirror the IDE: derive patterns from the real command, not the sudo
  // wrapper (a `sudo *` allow would be far broader than intended).
  const baseIdx = parts[0] === "sudo" && parts.length > 1 ? 1 : 0;
  const base = parts[baseIdx] ?? "";
  if (base === "") {
    return null;
  }
  const effective = parts.slice(baseIdx);

  // Preset patterns: base, base + flags, exact.
  const presets: string[] = [`${base} *`];
  if (effective.length > 1) {
    const withFlags = effective.slice(0, -1).join(" ") + " *";
    if (withFlags !== `${base} *`) {
      presets.push(withFlags);
    }
    presets.push(effective.join(" "));
  }

  const body = el("div", { className: "always-allow-body" });

  // Persist the allow rule, then approve. The approval WAITS for the rule
  // write: guard_resource makes the server refuse when an explicit ask rule
  // covers this command (the allow would be shadowed), and a failed write
  // leaves the dialog open — the user can still Allow once. Buttons disable
  // while the write is in flight so a double-click can't double-fire.
  const persistThenApprove = async (pattern: string): Promise<void> => {
    const buttons = body.querySelectorAll("button");
    for (const b of buttons) {
      b.disabled = true;
    }
    const res = await editNativeRule.dispatch({
      op: "add",
      scope: "workspace",
      capability: "shell",
      effect: "allow",
      match: [pattern],
      guard_resource: trimmed,
    });
    if (res === null || res.error !== undefined) {
      // Write failed (or was refused): the action's toast explains why.
      // Leave the permission pending; re-enable for another choice.
      for (const b of buttons) {
        b.disabled = false;
      }
      return;
    }
    onSelect(allowOpt.option_id);
    hidePermission();
  };

  for (const pattern of presets) {
    const row = el(
      "button",
      { type: "button", className: "always-allow-preset" },
      el("code", null, pattern),
    );
    row.addEventListener("click", () => {
      void persistThenApprove(pattern);
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
    void persistThenApprove(val);
  });
  body.appendChild(el("div", { className: "always-allow-custom" }, input, addBtn));

  return el(
    "details",
    { className: "always-allow-details" },
    el("summary", { className: "always-allow-summary" }, "Always allow\u2026"),
    body,
  ) as HTMLDetailsElement;
}

function hidePermission(): void {
  approvalEl.close();
}
