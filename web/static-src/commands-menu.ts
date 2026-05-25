// ---------------------------------------------------------------------------
// Slash-command menu for the prompt input.
//
// Two stages:
//   1. Command picker: "/" at the start → popover of commands + prompts.
//   2. Arg completion: "/<name> <partial>" → debounced fetch of options
//      from kiro.dev/commands/options via /api/slash/options.
//
// The catalog is session-scoped: each active chat has its own
// available_commands + available_prompts lists populated by the
// commands_updated SSE event.
// ---------------------------------------------------------------------------

import { $ } from "./dom.js";
import { getActive, getActiveId } from "./store.js";
import type { AvailableCommand } from "./types.js";
import { apiAction, debouncedDispatch, registerCleanup, subscribeToActions } from "./actions/index.js";

interface OptionEntry {
  label: string;
  description?: string;
}

interface PopoverItem extends AvailableCommand {
  isPrompt?: boolean;
}

interface PopoverRowOpts {
  name: string;
  desc: string;
  badge?: string;
  onClick: () => void;
  index: number;
}

// ---------------------------------------------------------------------------
// Stage-2 fetch action: debounced option lookup for arg completion.
// ---------------------------------------------------------------------------

interface FetchOptionsArgs {
  chatID: string;
  command: string;
  partial: string;
}

interface FetchOptionsResult {
  options: OptionEntry[];
}

const fetchOptionsAction = apiAction<FetchOptionsArgs, FetchOptionsResult>({
  name: "commands.fetchOptions",
  request: ({ chatID, command, partial }) => ({
    method: "GET",
    path: `/api/slash/options?chat_id=${encodeURIComponent(chatID)}`
      + `&command=${encodeURIComponent(command)}`
      + `&partial=${encodeURIComponent(partial)}`,
  }),
  error: false,
  dedupe: (args) => args.chatID + ":" + args.command + ":" + args.partial,
});

const debouncedFetch = debouncedDispatch(fetchOptionsAction, { wait: 150 });

// ---------------------------------------------------------------------------
// CommandsMenuController — encapsulates the popover lifecycle state.
// ---------------------------------------------------------------------------

class CommandsMenuController {
  private popover: HTMLDivElement | null = null;
  private isOpen = false;
  private selectedIndex = 0;
  private cachedItems: PopoverItem[] = [];
  private blurTimer: ReturnType<typeof setTimeout> | undefined;
  /** Tracks whether the popover is showing stage-2 (arg completion). */
  private stage2Command: string | null = null;
  private initialized = false;

  init(): void {
    if (this.popover === null || !document.body.contains(this.popover)) {
      this.popover = this.buildPopover();
    }

    if (this.initialized) return;
    this.initialized = true;

    const input = $.promptInput;

    input.addEventListener("input", () => {
      this.clearBlurTimer();
      const val = input.value;
      if (!val.startsWith("/")) {
        this.closePopover();
        debouncedFetch.cancel();
        return;
      }
      const spaceIdx = val.indexOf(" ");
      if (spaceIdx === -1) {
        // Stage 1: command picker.
        debouncedFetch.cancel();
        this.openPopover(val);
      } else {
        // Stage 2: arg completion.
        this.closePopover();
        const cmd = val.slice(0, spaceIdx);
        const partial = val.slice(spaceIdx + 1);
        this.scheduleOptions(cmd, partial);
      }
    });

    input.addEventListener("keydown", (e: KeyboardEvent) => {
      // Flush pending debounce on Enter even if popover hasn't opened yet.
      if (e.key === "Enter" && !this.isOpen && debouncedFetch.isPending()) {
        debouncedFetch.flush();
      }
      if (!this.isOpen) return;
      const items = this.cachedItems;
      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          e.stopImmediatePropagation();
          if (items.length === 0) return;
          this.selectedIndex = (this.selectedIndex + 1) % items.length;
          this.renderSelection();
          break;
        case "ArrowUp":
          e.preventDefault();
          e.stopImmediatePropagation();
          if (items.length === 0) return;
          this.selectedIndex = (this.selectedIndex - 1 + items.length) % items.length;
          this.renderSelection();
          break;
        case "Tab":
        case "Enter":
          if (items.length === 0) return;
          e.preventDefault();
          e.stopImmediatePropagation();
          if (this.stage2Command !== null) {
            this.acceptOption(this.stage2Command, items[this.selectedIndex]!.name);
          } else {
            this.acceptItem(items[this.selectedIndex] as PopoverItem);
          }
          break;
        case "Escape":
          e.preventDefault();
          e.stopPropagation();
          e.stopImmediatePropagation();
          this.closePopover();
          debouncedFetch.cancel();
          break;
      }
    });

    input.addEventListener("focus", () => { this.clearBlurTimer(); });

    input.addEventListener("blur", () => {
      this.blurTimer = setTimeout(() => { this.closePopover(); debouncedFetch.cancel(); }, 120);
    });
  }

  private clearBlurTimer(): void {
    if (this.blurTimer !== undefined) {
      clearTimeout(this.blurTimer);
      this.blurTimer = undefined;
    }
  }

  // --- Stage 2: arg completion ---

  private scheduleOptions(command: string, partial: string): void {
    const chatID = getActiveId();
    if (chatID === "") return;
    debouncedFetch({ chatID, command, partial });
  }

  /** Public hook for global cleanup. */
  cancelLoad(): void { debouncedFetch.cancel(); fetchOptionsAction.cancel(); }

  /**
   * Called by the action's onSuccess callback to render options.
   * Kept as a method so the controller instance is accessible.
   */
  showOptions(command: string, options: OptionEntry[]): void {
    const chatID = getActiveId();
    if (chatID === "") return;
    if (options.length === 0) {
      this.closePopover();
      return;
    }
    this.openOptionsPopover(command, options);
  }

  private openOptionsPopover(command: string, options: OptionEntry[]): void {
    if (this.popover === null) return;
    this.isOpen = true;
    this.selectedIndex = 0;
    this.stage2Command = command;
    // Cache option items as PopoverItems for keydown handler.
    this.cachedItems = options.map((opt) => ({
      name: opt.label,
      description: opt.description ?? "",
    }));
    this.popover.classList.remove("hidden");
    this.popover.setAttribute("aria-label", `Options for ${command}`);
    this.popover.innerHTML = "";
    options.forEach((opt, i) => {
      const row = buildPopoverRow({
        name: opt.label,
        desc: opt.description ?? "",
        onClick: () => this.acceptOption(command, opt.label),
        index: i,
      });
      row.id = `cmd-opt-${i}`;
      this.popover?.appendChild(row);
    });
    this.renderSelection();
    this.positionPopover();
  }

  private acceptOption(command: string, label: string): void {
    const input = $.promptInput;
    input.value = `${command} ${label}`;
    input.focus();
    this.closePopover();
  }

  // --- Stage 1: command picker ---

  private buildPopover(): HTMLDivElement {
    const el = document.createElement("div");
    el.className = "commands-popover hidden";
    el.setAttribute("role", "listbox");
    el.setAttribute("aria-label", "Slash commands");
    document.body.appendChild(el);
    return el;
  }

  private openPopover(filter: string): void {
    if (this.popover === null) return;
    const items = this.filterAll(filter);
    if (items.length === 0) {
      this.closePopover();
      return;
    }
    this.isOpen = true;
    this.selectedIndex = 0;
    this.stage2Command = null;
    this.cachedItems = items;
    this.popover.classList.remove("hidden");
    this.popover.innerHTML = "";
    items.forEach((cmd, i) => {
      const opts: PopoverRowOpts = {
        name: cmd.name,
        desc: cmd.description ?? "",
        onClick: () => this.acceptItem(cmd),
        index: i,
      };
      if (cmd.isPrompt === true) opts.badge = "prompt";
      const row = buildPopoverRow(opts);
      this.popover?.appendChild(row);
    });
    this.renderSelection();
    this.positionPopover();
  }

  private positionPopover(): void {
    if (this.popover === null) return;
    const rect = $.promptInput.getBoundingClientRect();
    this.popover.style.left = `${rect.left}px`;
    this.popover.style.bottom = `${window.innerHeight - rect.top + 4}px`;
    this.popover.style.width = `${rect.width}px`;
  }

  private renderSelection(): void {
    if (this.popover === null) return;
    const rows = this.popover.querySelectorAll<HTMLButtonElement>(".commands-popover-row");
    rows.forEach((r, i) => r.classList.toggle("selected", i === this.selectedIndex));
  }

  private closePopover(): void {
    if (this.popover === null) return;
    this.popover.classList.add("hidden");
    this.isOpen = false;
    this.cachedItems = [];
    this.stage2Command = null;
  }

  private filterAll(filter: string): PopoverItem[] {
    const session = getActive();
    if (session === undefined) return [];
    const prefix = filter.startsWith("/") ? filter.toLowerCase() : "";
    const cmds: PopoverItem[] = session.available_commands
      .filter((c) => prefix === "" || c.name.toLowerCase().startsWith(prefix));
    const prompts: PopoverItem[] = (session.available_prompts ?? [])
      .filter((p) => {
        const name = p.name.startsWith("/") ? p.name : `/${p.name}`;
        return prefix === "" || name.toLowerCase().startsWith(prefix);
      })
      .map((p) => ({
        name: p.name.startsWith("/") ? p.name : `/${p.name}`,
        description: p.description ?? "",
        isPrompt: true,
      } as PopoverItem));
    return [...cmds, ...prompts];
  }

  private acceptItem(cmd: PopoverItem): void {
    const input = $.promptInput;
    input.value = cmd.name + " ";
    input.focus();
    input.dispatchEvent(new Event("input"));
    this.closePopover();
  }
}

// ---------------------------------------------------------------------------
// Shared popover-row builder — used by both command picker and arg completion.
// ---------------------------------------------------------------------------

function buildPopoverRow(opts: PopoverRowOpts): HTMLButtonElement {
  const row = document.createElement("button");
  row.className = "commands-popover-row";
  row.type = "button";
  row.setAttribute("role", "option");
  row.dataset["index"] = String(opts.index);
  const name = document.createElement("span");
  name.className = "commands-popover-name";
  name.textContent = opts.name;
  if (opts.badge !== undefined) {
    const badge = document.createElement("span");
    badge.className = "commands-popover-badge";
    badge.textContent = opts.badge;
    name.appendChild(badge);
  }
  const desc = document.createElement("span");
  desc.className = "commands-popover-desc";
  desc.textContent = opts.desc;
  row.append(name, desc);
  row.addEventListener("mousedown", (e) => {
    e.preventDefault();
    opts.onClick();
  });
  return row;
}

// ---------------------------------------------------------------------------
// Module export — backward-compatible init function for app.ts.
// ---------------------------------------------------------------------------

const controller = new CommandsMenuController();
registerCleanup(() => controller.cancelLoad());

subscribeToActions((instance) => {
  if (instance.name !== "commands.fetchOptions" || instance.status !== "success") return;
  const args = instance.args as FetchOptionsArgs;
  const result = instance.result as FetchOptionsResult | undefined;
  if (args.chatID !== getActiveId()) return;
  const options = result?.options;
  if (!Array.isArray(options) || options.length === 0) return;
  controller.showOptions(args.command, options);
});

export function initCommandsMenu(): void {
  controller.init();
}
