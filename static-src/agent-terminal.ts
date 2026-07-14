// ---------------------------------------------------------------------------
// Agent terminal tabs: read-only terminal views for agent-spawned commands.
//
// When kiro-cli creates a terminal via the ACP terminal/create handler, the
// server broadcasts terminal_created. We add a tab to the shell panel and
// stream output into a <pre>. When the process exits, the tab gets a status
// class plus an "exited (code N)" / "killed (SIGNAL)" footer on the card.
//
// Each terminal renders ANSI colour through its OWN stateful converter
// (createAnsiConverter) so open SGR state never bleeds between concurrent
// terminals. Rendered output is capped (tail kept, front dropped) so a chatty
// long-running command can't grow the DOM without bound, mirroring the
// server's ring-buffer tail semantics. Creation and exit are announced to
// assistive tech via the shared polite live region.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { announce } from "@cplieger/ui-primitives/announce";
import { onSSE } from "./bus.js";
import { createAnsiConverter, type AnsiConverter } from "./ansi.js";
import { registerCleanup } from "./actions/index.js";

interface AgentTerm {
  id: string;
  label: string;
  tab: HTMLButtonElement;
  pane: HTMLDivElement;
  output: HTMLPreElement;
  // Per-terminal ANSI→HTML converter with isolated SGR state, so colours and
  // bold from one terminal never leak into another concurrent terminal.
  conv: AnsiConverter;
  // Rendered characters currently held in the <pre>, tracked for output cap.
  outputLen: number;
  exited: boolean;
}

const terms = new Map<string, AgentTerm>();
const exitedQueue: string[] = [];
const MAX_TERMINALS = 50;

// Cap the rendered client-side scrollback per terminal. The server rings agent
// output at 64 KB, but the SSE push stream delivers every chunk, so the client
// keeps a larger bounded tail and drops from the front on overflow — matching
// the server ring's tail semantics without unbounded DOM growth.
export const MAX_TERMINAL_OUTPUT_CHARS = 256 * 1024;

export function initAgentTerminals(): void {
  const unsub1 = onSSE("terminal_created", (_chatID, p) => {
    createTab(p.terminal_id, p.command, p.args);
  });

  const unsub2 = onSSE("terminal_output", (_chatID, p) => {
    const term = terms.get(p.terminal_id);
    if (term === undefined) {
      return;
    }
    appendOutput(term, p.data);
  });

  const unsub3 = onSSE("terminal_exited", (_chatID, p) => {
    const term = terms.get(p.terminal_id);
    if (term === undefined || term.exited) {
      return;
    }
    markExited(term, p.exit_code, p.signal);
  });

  registerCleanup(() => {
    unsub1();
    unsub2();
    unsub3();
    terms.clear();
    exitedQueue.length = 0;
  });

  // Wire tab clicks via event delegation on the tab bar.
  const tabBar = document.getElementById("shell-tabs");
  if (tabBar !== null) {
    tabBar.addEventListener("click", (e: MouseEvent) => {
      const btn = (e.target as HTMLElement).closest<HTMLElement>("[data-shell-tab]");
      if (btn === null) {
        return;
      }
      const id = btn.dataset["shellTab"] ?? "";
      switchTab(id);
    });
  }

  // Wire full-screen toggle. aria-pressed mirrors the panel class so the
  // button reads as a toggle; shell.ts resets both when the panel closes.
  const fsBtn = document.getElementById("shell-fullscreen-btn");
  if (fsBtn !== null) {
    fsBtn.setAttribute("aria-pressed", "false");
    fsBtn.addEventListener("click", () => {
      const panel = document.getElementById("shell-panel");
      if (panel !== null) {
        const on = panel.classList.toggle("shell-fullscreen");
        fsBtn.setAttribute("aria-pressed", on ? "true" : "false");
      }
    });
  }
}

/** Human-readable label for a spawned command, e.g. "npm test". */
function labelFor(command: string, args?: string[]): string {
  return args !== undefined && args.length > 0 ? `${command} ${args[0] ?? ""}` : command;
}

function createTab(termId: string, command: string, args?: string[]): void {
  const tabBar = document.getElementById("shell-tabs");
  const container = document.getElementById("agent-terminals");
  if (tabBar === null || container === null) {
    return;
  }

  const label = labelFor(command, args);
  const shortLabel = label.length > 20 ? label.slice(0, 18) + "\u2026" : label;

  // Create tab button.
  const tab = el(
    "button",
    {
      type: "button",
      className: "shell-tab",
      "data-shell-tab": termId,
      role: "tab",
      "aria-selected": "false",
      title: label,
    },
    shortLabel,
  ) as HTMLButtonElement;
  tabBar.appendChild(tab);

  // Create output pane.
  // role="log" is implicit aria-live=off (navigable but not auto-announced).
  // Terminals stream char-by-char; aria-live="polite" would flood screen
  // readers. Creation and exit are announced separately via the shared polite
  // live region (announce), not this region.
  const pre = el("pre", {
    className: "agent-term-output",
    role: "log",
    "aria-label": `Terminal output: ${label}`,
  }) as HTMLPreElement;
  // The card wraps the output; label it so assistive tech can identify which
  // agent terminal it is.
  const pane = el(
    "div",
    {
      className: "agent-term-pane hidden",
      "data-term-id": termId,
      role: "group",
      "aria-label": `Agent terminal: ${label}`,
    },
    pre,
  ) as HTMLDivElement;
  container.appendChild(pane);

  terms.set(termId, {
    id: termId,
    label,
    tab,
    pane,
    output: pre,
    conv: createAnsiConverter(),
    outputLen: 0,
    exited: false,
  });

  evictIfOverCap();

  // Auto-switch to the new tab.
  switchTab(termId);

  // Show the agent terminals container.
  container.classList.remove("hidden");

  announce(`Terminal started: ${label}`);
}

/** Evict a terminal when the cap is exceeded: prefer an exited one, else the
 *  oldest (which may still be running). */
function evictIfOverCap(): void {
  if (terms.size <= MAX_TERMINALS) {
    return;
  }
  let evictId: string | undefined;
  // Find the first exited entry that still exists in terms.
  while (exitedQueue.length > 0) {
    const candidate = exitedQueue.shift()!; // eslint-disable-line @typescript-eslint/no-non-null-assertion
    if (terms.has(candidate)) {
      evictId = candidate;
      break;
    }
  }
  // Fallback: if no exited terminals, evict the oldest running terminal.
  evictId ??= terms.keys().next().value;
  if (evictId === undefined) {
    return;
  }
  const t = terms.get(evictId);
  if (t !== undefined) {
    const wasActive = t.tab.classList.contains("active");
    t.tab.remove();
    t.pane.remove();
    terms.delete(evictId);
    // Evicting the selected terminal would leave no visible pane (the user
    // terminal stays hidden); fall back to the human PTY tab.
    if (wasActive) {
      switchTab("user");
    }
  }
}

/** Render a chunk of ANSI output through the terminal's own converter, append
 *  it, cap the scrollback to the tail, and auto-scroll to the bottom. */
function appendOutput(term: AgentTerm, data: string): void {
  const pre = term.output;
  // Convert with the terminal's OWN converter (isolated SGR state), then
  // measure the rendered (ANSI-stripped) length via a detached template before
  // appending so the cap counter tracks visible characters, not raw bytes.
  const tpl = document.createElement("template");
  tpl.innerHTML = term.conv.toHtml(data);
  // DocumentFragment.textContent is always a string (never null).
  term.outputLen += tpl.content.textContent.length;
  pre.appendChild(tpl.content);

  // Cap: drop leading nodes (oldest output) until under the budget, keeping the
  // tail — matching the server ring-buffer's tail semantics.
  while (term.outputLen > MAX_TERMINAL_OUTPUT_CHARS) {
    const first = pre.firstChild;
    if (first === null) {
      break;
    }
    term.outputLen -= first.textContent?.length ?? 0;
    pre.removeChild(first);
  }

  // Auto-scroll the pane to the bottom.
  const container = pre.parentElement;
  if (container !== null) {
    container.scrollTop = container.scrollHeight;
  }
}

/** Surface a terminal's exit: tab status class, an "exited (code N)" /
 *  "killed (SIGNAL)" footer on the card, and a screen-reader announcement. */
function markExited(
  term: AgentTerm,
  exitCode: number | undefined,
  signal: string | undefined,
): void {
  term.exited = true;
  exitedQueue.push(term.id);

  const sig = signal ?? "";
  const clean = sig === "" && exitCode === 0;
  term.tab.classList.toggle("term-exited-ok", clean);
  term.tab.classList.toggle("term-exited-err", !clean);

  const statusText = exitStatusText(exitCode, sig);
  term.tab.title = `${term.label} \u2014 ${statusText}`;

  // Footer line on the card, below the output. It is a plain styled line, not
  // a live region — the announce() below owns the assistive-tech notification,
  // so nesting a role="status" here would double-announce.
  const status = el(
    "div",
    { className: `agent-term-status ${clean ? "is-ok" : "is-err"}` },
    `\u25B8 ${statusText}`,
  );
  term.pane.appendChild(status);

  announce(`Terminal ${term.label} ${statusText}`);
}

/** "killed (SIGTERM)" for a signal death, else "exited (code N)". */
function exitStatusText(exitCode: number | undefined, signal: string): string {
  if (signal !== "") {
    return `killed (${signal})`;
  }
  if (exitCode !== undefined) {
    return `exited (code ${exitCode})`;
  }
  return "exited";
}

function switchTab(id: string): void {
  const tabBar = document.getElementById("shell-tabs");
  const userTerminal = document.getElementById("shell-terminal");
  const agentContainer = document.getElementById("agent-terminals");
  if (tabBar === null || userTerminal === null || agentContainer === null) {
    return;
  }

  // Update tab active states.
  for (const btn of tabBar.querySelectorAll<HTMLButtonElement>("[data-shell-tab]")) {
    const isActive = btn.dataset["shellTab"] === id;
    btn.classList.toggle("active", isActive);
    btn.setAttribute("aria-selected", isActive ? "true" : "false");
  }

  if (id === "user") {
    // Show user terminal, hide agent panes.
    userTerminal.classList.remove("hidden");
    for (const pane of agentContainer.querySelectorAll<HTMLDivElement>(".agent-term-pane")) {
      pane.classList.add("hidden");
    }
  } else {
    // Hide user terminal, show the selected agent pane.
    userTerminal.classList.add("hidden");
    for (const pane of agentContainer.querySelectorAll<HTMLDivElement>(".agent-term-pane")) {
      pane.classList.toggle("hidden", pane.dataset["termId"] !== id);
    }
  }
}
