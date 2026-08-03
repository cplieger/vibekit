// ---------------------------------------------------------------------------
// Permission card: the agent is asking to do something, rendered in the
// interaction dock (decision-dock.ts owns the queue, the host and the settle
// -once guard; this file only builds DOM and reports the choice).
//
// TWO shapes, one payload, and the discriminator is `files`:
//
//   - A TOOL permission ("may I run this bash command"): the ask, an input
//     preview so the user can see what they are approving, one button per
//     offered option, and for shell commands an "Always allow" expansion that
//     persists a native Cedar rule.
//   - A TURN APPROVAL ("this turn wrote these files, which do you keep"):
//     KAS's `autopilot: off` gate. Arrives as an ordinary permission request
//     carrying `files[]`, and answers on the ordinary permission reply with a
//     per-ACTION decision map. See vibekit-acp.md "Supervised mode on v3".
//
// The turn-approval half has two constraints that are easy to get wrong and
// expensive when you do:
//
//   1. The decision unit is the ACTION, not the file. A multi-file semantic
//      rename arrives as several entries sharing ONE action_id, and the map is
//      keyed by that id — so `alpha.py` and `beta.py` cannot disagree. Rows are
//      grouped by action_id and toggle together; N checkboxes over N files
//      would offer a choice the wire cannot express.
//   2. An OMITTED id counts as a REJECT, not as unspecified. So a decision is
//      sent for every action offered, always — a partial map silently discards
//      whatever it forgot.
// ---------------------------------------------------------------------------

import type { ApprovalFile, PermissionNeededPayload, PermissionOption } from "./types.js";
import { el } from "@cplieger/reactive";
import { mcpToolInfo, formatMCPToolName } from "./tool-schema.js";
import { editNativeRule } from "./actions/permissions.js";
import { openFileGitDiff } from "./editor-openers.js";
import { get } from "./store.js";
import { ICON_DIFF } from "./icons.js";
import { iconEl } from "./icon-el.js";

const PREVIEW_CHAR_CAP = 500;

/** Answer callback. `fileDecisions` is present only for a turn approval. */
type SelectFn = (optionID: string, fileDecisions?: Record<string, boolean>) => void;

/** Build the dock card for one permission request. */
export function buildPermissionCard(
  chatID: string,
  payload: PermissionNeededPayload,
  onSelect: SelectFn,
): HTMLElement {
  const files = payload.files ?? [];
  if (files.length > 0) {
    return buildTurnApprovalCard(payload, files, onSelect);
  }
  // Resolved HERE, at render time, rather than when the ask was enqueued: a
  // queued permission can be built long after it arrived, and the tool call
  // carrying the input may not have been ingested yet at that point.
  return buildToolPermissionCard(
    payload,
    lookupToolInput(chatID, payload.tool_call_id ?? ""),
    onSelect,
  );
}

/** The agent's own arguments for the tool it is asking to run — the thing the
 *  user is actually approving. Walks back from the newest message because the
 *  ask is about the turn in flight. */
function lookupToolInput(chatID: string, toolCallID: string): unknown {
  if (toolCallID === "") {
    return undefined;
  }
  const s = get(chatID);
  if (s === undefined) {
    return undefined;
  }
  for (let i = s.messages.length - 1; i >= 0; i--) {
    const m = s.messages[i];
    if (m?.tool_calls === undefined) {
      continue;
    }
    for (const tc of m.tool_calls) {
      if (tc.id === toolCallID) {
        return tc.input;
      }
    }
  }
  return undefined;
}

// --- Tool permission -------------------------------------------------------

function buildToolPermissionCard(
  payload: PermissionNeededPayload,
  input: unknown,
  onSelect: SelectFn,
): HTMLElement {
  const title = payload.title ?? "Tool";
  const kind = payload.kind ?? "";
  const isModeSwitch = kind === "switch_mode";

  const body = el("div", { className: "approval-body" });
  const mcp = mcpToolInfo(title);
  const heading = isModeSwitch
    ? "Switch session mode"
    : mcp !== null
      ? formatMCPToolName(mcp.tool)
      : title;
  body.appendChild(el("strong", null, heading));

  if (isModeSwitch) {
    body.appendChild(el("div", { className: "approval-origin" }, title));
  } else if (mcp !== null) {
    body.appendChild(
      el(
        "div",
        { className: "approval-origin" },
        "from ",
        el("strong", null, mcp.server),
        " MCP integration",
      ),
    );
  }

  const toolCallID = payload.tool_call_id ?? "";
  if (toolCallID !== "") {
    body.appendChild(el("div", { className: "approval-id" }, toolCallID));
  }

  const preview = formatInputPreview(input);
  if (preview !== "") {
    body.appendChild(el("pre", { className: "approval-input" }, preview));
  }

  const actions = el("div", { className: "approval-actions" });
  for (const opt of payload.options) {
    const btn = el(
      "button",
      {
        type: "button",
        className: opt.kind.startsWith("allow")
          ? "btn-small confirm-allow"
          : "btn-small confirm-danger",
      },
      opt.name,
    );
    btn.addEventListener("click", () => {
      onSelect(opt.option_id);
    });
    actions.appendChild(btn);
  }

  if (kind === "execute" && !isModeSwitch) {
    const alwaysRow = buildAlwaysAllowRow(title, payload.options, onSelect);
    if (alwaysRow !== null) {
      actions.appendChild(alwaysRow);
    }
  }

  const card = el("div", { className: "dock-card dock-permission" }, body, actions);
  if (isModeSwitch) {
    card.classList.add("mode-switch");
  }
  return card;
}

// --- Turn approval ---------------------------------------------------------

/** Files sharing one action id: the atomic review unit. */
interface ActionGroup {
  actionID: string;
  paths: string[];
}

/** Group by action id, preserving first-seen order so the list is stable
 *  across the re-render a queue change causes. */
function groupByAction(files: readonly ApprovalFile[]): ActionGroup[] {
  const byID = new Map<string, ActionGroup>();
  for (const f of files) {
    const existing = byID.get(f.action_id);
    if (existing === undefined) {
      byID.set(f.action_id, { actionID: f.action_id, paths: [f.path] });
    } else {
      existing.paths.push(f.path);
    }
  }
  return [...byID.values()];
}

function buildTurnApprovalCard(
  payload: PermissionNeededPayload,
  files: readonly ApprovalFile[],
  onSelect: SelectFn,
): HTMLElement {
  const groups = groupByAction(files);
  // Default: keep everything. The turn's writes are ALREADY on disk (KAS holds
  // the snapshots, not the bytes), so "keep" is the state the workspace is in —
  // an unchecked default would misrepresent what unchecking costs.
  const keep = new Map<string, boolean>(groups.map((g) => [g.actionID, true]));

  const body = el(
    "div",
    { className: "approval-body" },
    el("strong", null, "Review this turn's changes"),
    el(
      "p",
      { className: "approval-hint" },
      fileCountLabel(files.length, groups.length) +
        " already written. Unchecked entries are rolled back.",
    ),
  );

  const list = el("ul", { className: "dock-file-list" });
  for (const g of groups) {
    list.appendChild(buildGroupRow(g, keep));
  }

  const acceptOpt = payload.options.find((o) => o.kind.startsWith("allow"));
  const rejectOpt = payload.options.find((o) => !o.kind.startsWith("allow"));

  const actions = el("div", { className: "approval-actions" });

  if (rejectOpt !== undefined) {
    const rejectAll = el(
      "button",
      { type: "button", className: "btn-small confirm-danger" },
      "Roll back all",
    );
    rejectAll.addEventListener("click", () => {
      // The reject OPTION is the whole-turn no. Sending the map as well would
      // be redundant, and KAS restores everything on this path anyway.
      onSelect(rejectOpt.option_id);
    });
    actions.appendChild(rejectAll);
  }

  if (acceptOpt !== undefined) {
    const apply = el(
      "button",
      { type: "button", className: "btn-small confirm-allow" },
      "Keep selected",
    );
    apply.addEventListener("click", () => {
      // Every offered action gets an entry: an omitted id is a reject, so a
      // sparse map would silently roll back whatever it left out.
      const decisions: Record<string, boolean> = {};
      for (const g of groups) {
        decisions[g.actionID] = keep.get(g.actionID) ?? true;
      }
      onSelect(acceptOpt.option_id, decisions);
    });
    actions.appendChild(apply);
  }

  return el("div", { className: "dock-card dock-approval" }, body, list, actions);
}

function fileCountLabel(fileCount: number, groupCount: number): string {
  const files = fileCount === 1 ? "1 file" : `${String(fileCount)} files`;
  // Only mention actions when they differ from files, which happens exactly
  // when a rename bundled several paths under one id.
  return groupCount === fileCount ? files : `${files} in ${String(groupCount)} changes`;
}

function buildGroupRow(g: ActionGroup, keep: Map<string, boolean>): HTMLElement {
  const box = el("input", {
    type: "checkbox",
    className: "dock-file-check",
    "aria-label": `Keep ${g.paths.join(", ")}`,
  }) as HTMLInputElement;
  box.checked = true;

  const label = el("span", { className: "dock-file-paths" });
  for (const p of g.paths) {
    label.appendChild(el("span", { className: "dock-file-path" }, p));
  }
  // A multi-path group is one decision; say so rather than letting it read as
  // a list the user can split.
  if (g.paths.length > 1) {
    label.appendChild(
      el("span", { className: "dock-file-atomic" }, "moved together — one decision"),
    );
  }

  const diffBtn = el(
    "button",
    {
      type: "button",
      className: "turn-action-btn",
      "data-tooltip": "View diff",
      "aria-label": `View diff for ${g.paths[0] ?? ""}`,
    },
    iconEl(ICON_DIFF),
  );
  diffBtn.addEventListener("click", () => {
    const first = g.paths[0];
    if (first !== undefined) {
      // vs HEAD, because the write already landed: the working tree IS the
      // proposed state, so git shows exactly what this turn did.
      openFileGitDiff(first);
    }
  });

  const row = el("li", { className: "dock-file-row" }, box, label, diffBtn);
  box.addEventListener("change", () => {
    keep.set(g.actionID, box.checked);
    row.classList.toggle("dock-file-rejected", !box.checked);
  });
  return row;
}

// --- Shared ----------------------------------------------------------------

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
  options: readonly PermissionOption[],
  onSelect: SelectFn,
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
  // leaves the ask standing — the user can still Allow once. Buttons disable
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
