// ---------------------------------------------------------------------------
// Tool management: list, add, edit, delete, update installed tools.
//
// State is encapsulated in the ToolsManager class. The module exports
// delegate functions (initTools, loadToolsList) that forward to the
// singleton instance, preserving the existing public API.
// ---------------------------------------------------------------------------

import { closeModal, openModal, RollingOutput } from "./modals.js";
import { confirm as confirmDialog } from "./confirm.js";
import { patchSettings } from "./persist.js";
import { ICON_EDIT, ICON_TRASH, ICON_PIN, ICON_PIN_FILLED, iconEl } from "./icons.js";
import {
  installTools,
  saveTools,
  seedMcp,
  loadTools as loadToolsAction,
  enableTool,
  deleteTool,
  patchTool,
  execSlash,
} from "./actions/tools.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { getActiveId } from "./store.js";
import { $, el } from "./dom.js";
import { reconcile } from "./lib/reactive/reconcile.js";

type ToolEntry =
  | { kind: "label"; sec: string; isBuiltin: boolean }
  | {
      kind: "row";
      sec: string;
      name: string;
      entry: Record<string, unknown> | undefined;
      isBuiltin: boolean;
    };

interface MethodSchema {
  fields: { version: boolean; pkg: boolean; install: boolean; binaries: boolean };
  hints: { cat: string; version: string; pkg: string; install: string };
}

const INSTALL_METHODS = {
  go: {
    fields: { version: true, pkg: true, install: false, binaries: true },
    hints: {
      cat: "Installed via go install. Provide the full module path.",
      version: "Go module version tag, e.g. v1.2.0",
      pkg: "Full import path with version, e.g. golang.org/x/tools/cmd/goimports@${VERSION}",
      install: "",
    },
  },
  npm: {
    fields: { version: true, pkg: false, install: false, binaries: false },
    hints: {
      cat: "Installed globally via npm. Just name and version.",
      version: "npm package version, e.g. 17.6.0",
      pkg: "",
      install: "",
    },
  },
  pip: {
    fields: { version: true, pkg: false, install: false, binaries: false },
    hints: {
      cat: "Installed via pip. Just name and version.",
      version: "PyPI package version, e.g. 1.38.0",
      pkg: "",
      install: "",
    },
  },
  cargo: {
    fields: { version: true, pkg: false, install: false, binaries: false },
    hints: {
      cat: "Installed via cargo install. Just name and version.",
      version: "Crate version, e.g. v0.13.1",
      pkg: "",
      install: "",
    },
  },
  apt: {
    fields: { version: false, pkg: false, install: false, binaries: false },
    hints: {
      cat: "Installed via apt-get. No version pinning; uses distro package.",
      version: "",
      pkg: "",
      install: "",
    },
  },
  binary: {
    fields: { version: true, pkg: false, install: true, binaries: false },
    hints: {
      cat: "Downloaded as a prebuilt binary. Provide a curl/tar install command.",
      version: "Release tag, e.g. v2.14.0. Use ${VERSION} in the install command.",
      pkg: "",
      install:
        "Shell command using ${VERSION}, ${BIN}, ${TOOLS}. e.g. curl -fsSL ... | tar -xz -C ${BIN}",
    },
  },
  runtimes: {
    fields: { version: true, pkg: false, install: true, binaries: false },
    hints: {
      cat: "A language runtime (Go, Node, etc). Provide a custom install command.",
      version: "Runtime version, e.g. 1.26.2",
      pkg: "",
      install: "Shell command using ${VERSION}, ${RUNTIMES}. Installs to ${RUNTIMES}/<name>/.",
    },
  },
  custom: {
    fields: { version: true, pkg: false, install: true, binaries: false },
    hints: {
      cat: "Fully custom install script. You control everything.",
      version: "Version label for tracking. Use ${VERSION} in the install command.",
      pkg: "",
      install: "Shell command using ${VERSION}, ${BIN}, ${TOOLS}.",
    },
  },
  mcp: {
    fields: { version: true, pkg: false, install: true, binaries: false },
    hints: {
      cat: "Install an MCP server that needs a package manager or binary (pip install ha-mcp, cargo install, curl). An unconfigured entry is added to the MCP section; fill in credentials + stdio command there.",
      version: "Version label, e.g. 1.0.0",
      pkg: "",
      install: "Install command, e.g. pip install --user ha-mcp",
    },
  },
  lsp: {
    fields: { version: true, pkg: false, install: false, binaries: false },
    hints: {
      cat: "A language server. Pre-populated entries install via their bundled method (npm/binary/go/gem); edit only adjusts the pinned version.",
      version: "LSP version, e.g. 1.1.391",
      pkg: "",
      install: "",
    },
  },
} as const satisfies Readonly<Record<string, MethodSchema>>;

type MethodKind = keyof typeof INSTALL_METHODS;

function isMethodKind(v: string): v is MethodKind {
  return v in INSTALL_METHODS;
}

// Modal form fields that only this module touches. Queried lazily via
// getters so the module can import before DOMContentLoaded.
const f = {
  get title(): HTMLElement {
    return el("tool-modal-title");
  },
  get cat(): HTMLSelectElement {
    return el("tool-cat");
  },
  get name(): HTMLInputElement {
    return el("tool-name");
  },
  get version(): HTMLInputElement {
    return el("tool-version");
  },
  get install(): HTMLInputElement {
    return el("tool-install");
  },
  get binaries(): HTMLInputElement {
    return el("tool-binaries");
  },
  get pkg(): HTMLInputElement {
    return el("tool-package");
  },
  get save(): HTMLButtonElement {
    return el("tool-modal-save");
  },
};

class ToolsManager {
  private toolsData: Record<string, Record<string, Record<string, unknown>>> = {};
  private editingTool: { cat: string; name: string } | null = null;
  /** Public hook for global cleanup: cancels in-flight tool fetch. */
  cancelLoad(): void {
    loadToolsAction.cancel();
  }

  init(): void {
    $.toolAddBtn.addEventListener("click", () => {
      this.openToolModal(null, null);
    });

    const toolOutput = new RollingOutput($.toolUpdateOutput, "git-output-modal");
    $.toolUpdateBtn.addEventListener("click", () => void this.runToolsInstall(toolOutput));
    bindLoadingState("tools.install", $.toolUpdateBtn);

    $.autoUpdateToggle.addEventListener("change", () => {
      void patchSettings({ auto_update: $.autoUpdateToggle.checked }, $.autoUpdateToggle);
    });

    f.save.addEventListener("click", () => {
      this.saveToolFromModal();
    });
    const unbindSave = bindLoadingState("tools.save", f.save);
    const unbindSeed = bindLoadingState("tools.seed_mcp", f.save, { preserveDisabled: true });
    registerCleanup(unbindSave);
    registerCleanup(unbindSeed);
  }

  loadToolsList(): void {
    void loadToolsAction.dispatch(undefined, {
      onSuccess: (d) => {
        this.toolsData = d;
        this.renderToolsList();
      },
      onError: () => {
        $.toolsList.replaceChildren();
        const errDiv = document.createElement("div");
        errDiv.className = "list-empty";
        errDiv.textContent = "Failed to load tools";
        $.toolsList.appendChild(errDiv);
      },
    });
  }

  private async runToolsInstall(toolOutput: RollingOutput): Promise<void> {
    toolOutput.clear();
    toolOutput.append("Running setup-tools.sh...");
    const d = await installTools.dispatch(undefined);
    toolOutput.clear();
    if (d === null) {
      toolOutput.append("Install request failed");
    } else {
      toolOutput.append(d.output ?? "");
      if (d.error !== undefined) {
        toolOutput.append(`Error: ${d.error}`);
      }
    }
    this.loadToolsList();
  }

  private renderToolsList(): void {
    const container = $.toolsList;
    const sections = [
      "runtimes",
      "binary",
      "go",
      "npm",
      "pip",
      "cargo",
      "lsp",
      "apt",
      "custom",
      "builtin",
    ];

    // Flatten groups + rows into a single keyed sequence so reconcile
    // can patch in place across reloads (no flash of empty list).
    const flat: ToolEntry[] = [];
    for (const sec of sections) {
      const entries = this.toolsData[sec];
      if (entries === undefined || Object.keys(entries).length === 0) {
        continue;
      }
      const isBuiltin = sec === "builtin";
      flat.push({ kind: "label", sec, isBuiltin });
      for (const name of Object.keys(entries).sort()) {
        flat.push({ kind: "row", sec, name, entry: entries[name], isBuiltin });
      }
    }

    // Drop any non-keyed empty-state placeholder before reconciling.
    for (const child of [...container.children]) {
      if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }

    if (flat.length === 0) {
      container.replaceChildren();
      const emptyDiv = document.createElement("div");
      emptyDiv.className = "list-empty";
      emptyDiv.textContent = "No tools configured";
      container.appendChild(emptyDiv);
      return;
    }

    reconcile(container, flat, {
      key: (e: ToolEntry) => (e.kind === "label" ? `label:${e.sec}` : `row:${e.sec}:${e.name}`),
      mount: (e: ToolEntry) => {
        if (e.kind === "label") {
          const label = document.createElement("div");
          label.className = "list-group-label";
          label.textContent = e.isBuiltin ? "base os" : e.sec;
          return label;
        }
        return this.renderToolRow(e.sec, e.name, e.entry, e.isBuiltin);
      },
      update: (row, e: ToolEntry) => {
        if (e.kind !== "row") {
          return;
        }
        // Keep version/description text in sync in case the entry changed.
        const meta = row.querySelector(".list-row-meta");
        if (meta !== null) {
          meta.textContent = e.isBuiltin
            ? ((e.entry?.["description"] as string | undefined) ?? "")
            : ((e.entry?.["version"] as string | undefined) ?? "");
        }
      },
    });
  }

  private renderToolRow(
    sec: string,
    name: string,
    entry: Record<string, unknown> | undefined,
    isBuiltin: boolean,
  ): HTMLDivElement {
    const row = document.createElement("div");
    row.className = "list-row";

    // enabled defaults to true when the field is absent (user-authored
    // entries). Pre-populated entries ship enabled:false.
    const enabled = (entry?.["enabled"] as boolean | undefined) ?? true;
    if (!isBuiltin && !enabled) {
      row.classList.add("list-row-disabled");
    }

    const nameEl = document.createElement("span");
    nameEl.className = "list-row-name";
    nameEl.textContent = name;

    const verEl = document.createElement("span");
    verEl.className = "list-row-meta";
    verEl.textContent = isBuiltin
      ? ((entry?.["description"] as string | undefined) ?? "")
      : this.metaText(entry, enabled);

    row.append(nameEl, verEl);
    if (!isBuiltin) {
      row.appendChild(this.toolActions(sec, name, entry, enabled));
    }
    return row;
  }

  /** Meta line: version + a short description hint when disabled. */
  private metaText(entry: Record<string, unknown> | undefined, enabled: boolean): string {
    const version = (entry?.["version"] as string | undefined) ?? "";
    if (enabled) {
      return version;
    }
    const desc = (entry?.["description"] as string | undefined) ?? "";
    // For disabled entries the description is the useful bit; the
    // version is implied. Truncated by CSS ellipsis.
    return desc !== "" ? desc : version;
  }

  /**
   * Action cluster for a tool row. Disabled entries show a single
   * Enable button. Enabled entries show pin (auto_update toggle),
   * edit, and delete. Builtin (base os) rows get no actions.
   */
  private toolActions(
    sec: string,
    name: string,
    entry: Record<string, unknown> | undefined,
    enabled: boolean,
  ): HTMLDivElement {
    const actions = document.createElement("div");
    actions.className = "list-row-actions";

    if (!enabled) {
      const enableBtn = document.createElement("button");
      enableBtn.className = "btn-small list-row-enable";
      enableBtn.textContent = "Enable";
      enableBtn.setAttribute("aria-label", `Enable ${name}`);
      enableBtn.addEventListener("click", () => {
        // Immediate feedback: large runtimes/LSPs (Go, JRE, clangd) can
        // take minutes, and the install output is buffered server-side,
        // so without this the row looks inert. Disable + relabel so the
        // user knows it's working; loadToolsList() re-renders on finish.
        enableBtn.disabled = true;
        enableBtn.textContent = "Installing…";
        void this.runEnable(sec, name);
      });
      actions.append(enableBtn);
      return actions;
    }

    // Pin toggle: filled = auto_update off (pinned), outline = on.
    const autoUpdate = (entry?.["auto_update"] as boolean | undefined) ?? true;
    const pinBtn = document.createElement("button");
    pinBtn.className = "list-row-btn list-row-pin";
    pinBtn.setAttribute("aria-label", autoUpdate ? `Pin ${name} version` : `Unpin ${name}`);
    pinBtn.setAttribute(
      "data-tooltip",
      autoUpdate
        ? "Auto-updating on container start. Click to pin this version."
        : "Pinned — won't auto-update. Click to resume auto-updates.",
    );
    pinBtn.replaceChildren(autoUpdateIcon(autoUpdate));
    if (!autoUpdate) {
      pinBtn.classList.add("list-row-pin-active");
    }
    pinBtn.addEventListener("click", () => {
      void this.togglePin(sec, name, !autoUpdate);
    });

    const editBtn = document.createElement("button");
    editBtn.className = "list-row-btn";
    editBtn.setAttribute("data-tooltip", "Edit");
    editBtn.setAttribute("aria-label", `Edit ${name}`);
    editBtn.replaceChildren(iconEl(ICON_EDIT));
    editBtn.addEventListener("click", () => {
      this.openToolModal(sec, name);
    });

    const delBtn = document.createElement("button");
    delBtn.className = "list-row-btn";
    delBtn.setAttribute("data-tooltip", "Delete");
    delBtn.setAttribute("aria-label", `Delete ${name}`);
    delBtn.replaceChildren(iconEl(ICON_TRASH));
    delBtn.addEventListener("click", () => {
      void this.runDelete(sec, name);
    });

    actions.append(pinBtn, editBtn, delBtn);
    return actions;
  }

  /** Enable a pre-populated entry: install it + its deps with output. */
  private async runEnable(sec: string, name: string): Promise<void> {
    const out = new RollingOutput($.toolUpdateOutput, "git-output-modal");
    out.append(`Enabling ${name}…`);
    const d = await enableTool.dispatch({ section: sec, name });
    if (d === null) {
      out.append("Enable failed");
    } else {
      if (d.enabled_chain !== undefined && d.enabled_chain.length > 1) {
        out.append(`Installing: ${d.enabled_chain.join(", ")}`);
      }
      out.append(d.output ?? "");
      if (d.error !== undefined) {
        out.append(`Error: ${d.error}`);
      } else if (sec === "lsp") {
        // kiro-cli scans PATH for language servers at code-intelligence
        // init time. A server enabled mid-session isn't picked up by the
        // running bridge until it re-initializes. Best-effort fire
        // `/code init -f` into the active chat so the current session
        // adopts the new LSP immediately; new chats auto-init regardless.
        const chatID = getActiveId();
        if (chatID !== "") {
          await execSlash.dispatch({ chatID, command: "/code init -f" });
          out.append("Reinitialized code intelligence in the active chat.");
        } else {
          out.append("Installed. Active in your next chat.");
        }
      }
    }
    this.loadToolsList();
  }

  /** Delete with cascade-aware confirm. */
  private async runDelete(sec: string, name: string): Promise<void> {
    const ok = await confirmDialog(`Remove ${name}?`, "Remove", "destructive");
    if (!ok) {
      return;
    }
    const d = await deleteTool.dispatch({ section: sec, name });
    // 409 cascade: backend lists dependents that also need disabling.
    if (d !== null && d.code === "has_dependents" && d.dependents !== undefined) {
      const list = d.dependents.join(", ");
      const force = await confirmDialog(
        `${name} is required by: ${list}. Remove all of them?`,
        "Remove all",
        "destructive",
      );
      if (!force) {
        return;
      }
      await deleteTool.dispatch({ section: sec, name, force: true });
    }
    this.loadToolsList();
  }

  /** Flip auto_update for an entry. */
  private async togglePin(sec: string, name: string, autoUpdate: boolean): Promise<void> {
    await patchTool.dispatch({ section: sec, name, auto_update: autoUpdate });
    this.loadToolsList();
  }

  private updateToolFormVisibility(): void {
    const catVal = f.cat.value;
    const cat: MethodKind = isMethodKind(catVal) ? catVal : "custom";
    const schema = INSTALL_METHODS[cat];
    toggleLabel("tool-version-label", schema.fields.version);
    toggleLabel("tool-package-label", schema.fields.pkg);
    toggleLabel("tool-install-label", schema.fields.install);
    toggleLabel("tool-binaries-label", schema.fields.binaries);
    setHintText("tool-cat-hint", schema.hints.cat);
    setHintText("tool-version-hint", schema.hints.version);
    setHintText("tool-package-hint", schema.hints.pkg);
    setHintText("tool-install-hint", schema.hints.install);
  }

  private openToolModal(cat: string | null, name: string | null): void {
    f.cat.onchange = () => {
      this.updateToolFormVisibility();
    };
    if (cat !== null && name !== null) {
      f.title.textContent = `Edit: ${name}`;
      this.editingTool = { cat, name };
      const entry = this.toolsData[cat]?.[name];
      f.cat.value = cat;
      f.cat.disabled = true;
      f.name.value = name;
      f.name.disabled = true;
      f.version.value = (entry?.["version"] as string | undefined) ?? "";
      f.install.value = (entry?.["install"] as string | undefined) ?? "";
      f.binaries.value = ((entry?.["binaries"] as string[] | undefined) ?? []).join(", ");
      f.pkg.value = (entry?.["package"] as string | undefined) ?? "";
    } else {
      f.title.textContent = "Add tool";
      this.editingTool = null;
      f.cat.value = "go";
      f.cat.disabled = false;
      f.name.value = "";
      f.name.disabled = false;
      f.version.value = "";
      f.install.value = "";
      f.binaries.value = "";
      f.pkg.value = "";
    }
    this.updateToolFormVisibility();
    openModal($.toolModal);
  }

  private saveToolFromModal(): void {
    const uiCatVal = f.cat.value;
    const uiCat: MethodKind = isMethodKind(uiCatVal) ? uiCatVal : "custom";
    const cat = uiCat === "mcp" ? "custom" : uiCat;
    const name = f.name.value.trim();
    if (name === "") {
      return;
    }
    const { fields } = INSTALL_METHODS[uiCat];
    const version = f.version.value.trim();
    if (fields.version && version === "") {
      return;
    }

    // eslint-disable-next-line @typescript-eslint/prefer-nullish-coalescing
    if (this.toolsData[cat] === undefined) {
      this.toolsData[cat] = {};
    }
    const entry: Record<string, unknown> = {};
    if (fields.version) {
      entry["version"] = version;
    }
    const install = f.install.value.trim();
    if (fields.install && install !== "") {
      entry["install"] = install;
    }
    const binaries = f.binaries.value.trim();
    if (fields.binaries && binaries !== "") {
      entry["binaries"] = binaries
        .split(",")
        .map((s) => s.trim())
        .filter((s) => s !== "");
    }
    const pkg = f.pkg.value.trim();
    if (fields.pkg && pkg !== "") {
      entry["package"] = pkg;
    }
    if (uiCat === "mcp") {
      entry["kind"] = "mcp";
    }

    if (
      this.editingTool !== null &&
      (this.editingTool.cat !== cat || this.editingTool.name !== name)
    ) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion, @typescript-eslint/no-dynamic-delete
      delete this.toolsData[this.editingTool.cat]![this.editingTool.name];
    }
    this.toolsData[cat][name] = entry;
    this.saveToolsData();
    this.renderToolsList();

    if (uiCat === "mcp" && this.editingTool === null) {
      void seedUnconfiguredMCP(name, install);
    }
    closeModal($.toolModal);
  }

  private saveToolsData(): void {
    void saveTools.dispatch(this.toolsData);
  }
}

/** Seed an MCP config entry the user can fill in later. */
async function seedUnconfiguredMCP(name: string, install: string): Promise<void> {
  await seedMcp.dispatch({ name, install });
}

/**
 * Pin icon reflecting auto_update state. Filled pin = pinned
 * (auto_update off); outline pin = tracking upstream (auto_update on).
 */
function autoUpdateIcon(autoUpdate: boolean): HTMLElement {
  return iconEl(autoUpdate ? ICON_PIN : ICON_PIN_FILLED);
}

function toggleLabel(id: string, show: boolean): void {
  el(id).style.display = show ? "" : "none";
}

function setHintText(id: string, text: string): void {
  el(id).textContent = text;
}

// Singleton instance — internal to the module.
const manager = new ToolsManager();
registerCleanup(() => {
  manager.cancelLoad();
});

// Public delegate functions preserving the existing module API.
export function initTools(): void {
  manager.init();
}
export function loadToolsList(): void {
  manager.loadToolsList();
}
