// ---------------------------------------------------------------------------
// Agent terminal tabs: read-only terminal views for agent-spawned commands.
//
// When kiro-cli creates a terminal via the ACP terminal/create handler,
// the server broadcasts terminal_created. We add a tab to the shell panel
// and stream output into a <pre> element. When the process exits, the tab
// gets a status indicator.
//
// These are NOT full xterm.js instances (no input, no ANSI parsing needed
// for most agent commands). Plain <pre> with monospace font and auto-scroll.
// If ANSI rendering is needed later, swap <pre> for xterm.js read-only.
// ---------------------------------------------------------------------------

import { onSSE } from "./bus.js";
import { registerCleanup } from "./actions/index.js";

interface AgentTerm {
  id: string;
  command: string;
  tab: HTMLButtonElement;
  output: HTMLPreElement;
  exited: boolean;
  exitCode: number;
}

const terms = new Map<string, AgentTerm>();
const exitedQueue: string[] = [];
const MAX_TERMINALS = 50;

export function initAgentTerminals(): void {
  const unsub1 = onSSE("terminal_created", (_chatID, p) => {
    createTab(p.terminal_id, p.command, p.args);
  });

  const unsub2 = onSSE("terminal_output", (_chatID, p) => {
    const term = terms.get(p.terminal_id);
    if (term === undefined) {
      return;
    }
    term.output.textContent += p.data;
    // Auto-scroll to bottom.
    const container = term.output.parentElement;
    if (container !== null) {
      container.scrollTop = container.scrollHeight;
    }
  });

  const unsub3 = onSSE("terminal_exited", (_chatID, p) => {
    const term = terms.get(p.terminal_id);
    if (term === undefined) {
      return;
    }
    term.exited = true;
    term.exitCode = p.exit_code;
    term.tab.classList.toggle("term-exited-ok", p.exit_code === 0);
    term.tab.classList.toggle("term-exited-err", p.exit_code !== 0);
    exitedQueue.push(p.terminal_id);
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
      const id = btn.dataset.shellTab ?? "";
      switchTab(id);
    });
  }

  // Wire full-screen toggle.
  const fsBtn = document.getElementById("shell-fullscreen-btn");
  if (fsBtn !== null) {
    fsBtn.addEventListener("click", () => {
      const panel = document.getElementById("shell-panel");
      if (panel !== null) {
        panel.classList.toggle("shell-fullscreen");
      }
    });
  }
}

function createTab(termId: string, command: string, args?: string[]): void {
  const tabBar = document.getElementById("shell-tabs");
  const container = document.getElementById("agent-terminals");
  if (tabBar === null || container === null) {
    return;
  }

  // Build a short label from the command.
  const label = args !== undefined && args.length > 0 ? `${command} ${args[0] ?? ""}` : command;
  const shortLabel = label.length > 20 ? label.slice(0, 18) + "\u2026" : label;

  // Create tab button.
  const tab = document.createElement("button");
  tab.type = "button";
  tab.className = "shell-tab";
  tab.dataset["shellTab"] = termId;
  tab.setAttribute("role", "tab");
  tab.setAttribute("aria-selected", "false");
  tab.textContent = shortLabel;
  tab.title = label;
  tabBar.appendChild(tab);

  // Create output pane.
  const pane = document.createElement("div");
  pane.className = "agent-term-pane hidden";
  pane.dataset["termId"] = termId;
  const pre = document.createElement("pre");
  pre.className = "agent-term-output";
  // role="log" is implicit aria-live=off (navigable but not auto-announced).
  // Terminals stream char-by-char; aria-live="polite" would flood screen
  // readers. Users can navigate to the region; exit status announced
  // separately via announceTermExit.
  pre.setAttribute("role", "log");
  pre.setAttribute("aria-label", `Terminal output: ${label}`);
  pane.appendChild(pre);
  container.appendChild(pane);

  terms.set(termId, {
    id: termId,
    command,
    tab,
    output: pre,
    exited: false,
    exitCode: 0,
  });

  // Evict oldest completed terminal if cap is reached.
  if (terms.size > MAX_TERMINALS) {
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
    if (evictId !== undefined) {
      const t = terms.get(evictId);
      if (t !== undefined) {
        t.tab.remove();
        const oldPane = container.querySelector(`[data-term-id="${CSS.escape(evictId)}"]`);
        if (oldPane !== null) {
          oldPane.remove();
        }
        terms.delete(evictId);
      }
    }
  }

  // Auto-switch to the new tab.
  switchTab(termId);

  // Show the agent terminals container.
  container.classList.remove("hidden");
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
