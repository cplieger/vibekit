// ---------------------------------------------------------------------------
// Tool management (Settings -> Tools) over the v2 tools engine.
//
// The server owns the manifest, install state, and a single-flight job
// queue; this module is a pure projection. Mutations return 202 with a
// job; progress arrives over the tool_job_changed / tool_job_output
// SSE events (the output panel is a live follower that survives
// reloads via GET /api/tools/jobs). The add flow is search-first: the
// catalog (compiled from the mise + aqua registries) is the browse
// surface, with a manual-command escape hatch.
// ---------------------------------------------------------------------------

import { closeModal, openModal, RollingOutput } from "./modals.js";
import { confirm as confirmDialog } from "./confirm.js";
import { ICON_PIN, ICON_PIN_FILLED, ICON_TRASH } from "./icons.js";
import { iconEl } from "./icon-el.js";
import {
  loadTools,
  createTool,
  installTool,
  updateTools,
  patchTool,
  deleteTool,
  searchTools,
  getToolsJobs,
  getCatalogInfo,
  refreshCatalog,
  ensureTool,
  cancelToolJob,
} from "./actions/tools.js";
import type { CreateToolRequest } from "./actions/tools.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";
import { onSSE } from "./bus.js";
import { $, byId } from "./dom.js";
import { el } from "@cplieger/reactive";
import { createDisclosure, type DisclosureController } from "@cplieger/ui-primitives/disclosure";
import { reconcile } from "./reconcile.js";
import type { CatalogInfo, Inventory, Job, SearchHit, ToolInfo } from "./types.js";

/** Trailing-edge debounce for the catalog search input. */
function debounce(fn: () => void, ms: number): () => void {
  let timer: ReturnType<typeof setTimeout> | null = null;
  return () => {
    if (timer !== null) {
      clearTimeout(timer);
    }
    timer = setTimeout(() => {
      timer = null;
      fn();
    }, ms);
  };
}

type ListEntry =
  | { kind: "label"; label: string }
  | { kind: "tool"; tool: ToolInfo }
  | { kind: "system"; name: string; installed: boolean };

// Modal form fields owned by this module (feature-local ids).
const f = {
  get cancel(): HTMLButtonElement {
    return byId("tool-cancel-btn");
  },
  get catalogRefresh(): HTMLButtonElement {
    return byId("tool-catalog-refresh-btn");
  },
  get catalogMeta(): HTMLParagraphElement {
    return byId("tool-catalog-meta");
  },
  get search(): HTMLInputElement {
    return byId("tool-search");
  },
  get results(): HTMLDivElement {
    return byId("tool-search-results");
  },
  get manualToggle(): HTMLButtonElement {
    return byId("tool-manual-toggle");
  },
  get manualForm(): HTMLElement {
    return byId("tool-manual-form");
  },
  get manualName(): HTMLInputElement {
    return byId("tool-manual-name");
  },
  get manualVersion(): HTMLInputElement {
    return byId("tool-manual-version");
  },
  get manualInstall(): HTMLInputElement {
    return byId("tool-manual-install");
  },
  get manualUninstall(): HTMLInputElement {
    return byId("tool-manual-uninstall");
  },
  get manualProbe(): HTMLInputElement {
    return byId("tool-manual-probe");
  },
  get manualAdd(): HTMLButtonElement {
    return byId("tool-manual-add");
  },
};

class ToolsManager {
  private data: Inventory | null = null;
  private output: RollingOutput | null = null;
  /** Job id the output panel is currently following. */
  private followedJob = "";
  private unsubscribes: (() => void)[] = [];
  private manualFormCtl: DisclosureController | null = null;

  /** Public hook for global cleanup: cancels in-flight tool fetch. */
  cancelLoad(): void {
    loadTools.cancel();
  }

  init(): void {
    this.output = new RollingOutput($.toolUpdateOutput, "git-output-modal");

    $.toolAddBtn.addEventListener("click", () => {
      this.openAddModal();
    });
    $.toolUpdateBtn.addEventListener("click", () => {
      void updateTools.dispatch(undefined);
    });
    bindLoadingState("tools.update", $.toolUpdateBtn);
    f.catalogRefresh.addEventListener("click", () => {
      void refreshCatalog.dispatch(undefined);
    });
    bindLoadingState("tools.refresh_catalog", f.catalogRefresh);
    f.cancel.addEventListener("click", () => {
      if (this.followedJob !== "") {
        void cancelToolJob.dispatch({ id: this.followedJob });
      }
    });

    // Live job following: state transitions re-render the list (rows
    // flip installing/installed/error); output lines stream into the
    // rolling panel. Both survive any number of Settings tab
    // open/close cycles — subscriptions are module-lifetime.
    this.unsubscribes.push(
      onSSE("tool_job_changed", (_chat, payload) => {
        const job = payload.job;
        if (job === undefined) {
          return;
        }
        this.followJob(job);
        // loadToolsList refetches the catalog meta line too, so a
        // settling catalog-refresh job needs no extra fetch here.
        this.loadToolsList();
        if (job.kind === "catalog-refresh") {
          // One catalog refresh at a time: the queue would happily
          // accept a duplicate, so disable the button while one is
          // queued or running.
          f.catalogRefresh.disabled = job.state === "queued" || job.state === "running";
        }
      }),
      onSSE("tool_job_output", (_chat, payload) => {
        if (payload.job_id !== this.followedJob || this.output === null) {
          return;
        }
        for (const line of payload.lines) {
          this.output.append(line);
        }
      }),
    );

    // Add-modal wiring: debounced catalog search + manual escape hatch.
    const runSearch = debounce(() => {
      void this.renderSearch(f.search.value);
    }, 200);
    f.search.addEventListener("input", runSearch);
    // The manual-command escape hatch is a standard trigger disclosure: the
    // primitive owns aria-expanded/aria-controls, activation, and the
    // animated aria-hidden/inert collapse (the old code toggled the hidden
    // class + ARIA by hand). Normalize away the authored hidden class first.
    f.manualForm.classList.remove("hidden");
    this.manualFormCtl = createDisclosure(f.manualToggle, f.manualForm, { open: false });
    f.manualAdd.addEventListener("click", () => {
      void this.submitManual();
    });
    bindLoadingState("tools.create", f.manualAdd);
  }

  dispose(): void {
    for (const un of this.unsubscribes) {
      un();
    }
    this.unsubscribes = [];
  }

  /** Point the output panel at a job: reset on a new id, headline the
   *  terminal state, refresh the follow target, and toggle the Cancel
   *  affordance with the job's liveness. */
  private followJob(job: Job): void {
    if (this.output === null) {
      return;
    }
    const live = job.state === "queued" || job.state === "running";
    f.cancel.classList.toggle("hidden", !live);
    if (job.id !== this.followedJob) {
      this.followedJob = job.id;
      this.output.clear();
      this.output.append(jobHeadline(job));
      return;
    }
    if (!live) {
      this.output.append(jobHeadline(job));
    }
  }

  loadToolsList(): void {
    this.loadCatalogMeta();
    void loadTools.dispatch(undefined, {
      onSuccess: (d) => {
        this.data = d;
        this.renderToolsList();
        // A job already running when the panel opens (boot sync, or a
        // reload mid-install): seed the output panel with its tail.
        if (d.job !== undefined && d.job.id !== this.followedJob) {
          void this.resumeJobOutput(d.job);
        }
      },
      onError: () => {
        $.toolsList.replaceChildren();
        $.toolsList.appendChild(el("div", { className: "list-empty" }, "Failed to load tools"));
      },
    });
  }

  private async resumeJobOutput(job: Job): Promise<void> {
    this.followedJob = job.id;
    const jobs = await getToolsJobs.dispatch(undefined);
    if (jobs === null || this.output === null) {
      return;
    }
    const active = jobs.active;
    if (active?.id !== job.id) {
      return;
    }
    this.output.clear();
    this.output.append(jobHeadline(active));
    for (const line of active.output_tail ?? []) {
      this.output.append(line);
    }
  }

  /** Render the catalog freshness line: entry count, registry refs,
   *  where the live catalog came from, and the last refresh error. */
  private loadCatalogMeta(): void {
    void getCatalogInfo.dispatch(undefined, {
      onSuccess: (info) => {
        f.catalogMeta.replaceChildren(...catalogMetaParts(info));
        f.catalogMeta.classList.remove("hidden");
      },
      onError: () => {
        f.catalogMeta.classList.add("hidden");
      },
    });
  }

  // --- list rendering ---

  private renderToolsList(): void {
    const container = $.toolsList;
    const d = this.data;
    if (d === null) {
      return;
    }

    const flat: ListEntry[] = [];
    for (const t of d.tools) {
      flat.push({ kind: "tool", tool: t });
    }
    const system = d.system.filter((s) => s.installed);
    if (system.length > 0) {
      flat.push({ kind: "label", label: "built into the image" });
      for (const s of system) {
        flat.push({ kind: "system", name: s.name, installed: s.installed });
      }
    }

    // Drop any non-keyed empty-state placeholder before reconciling.
    for (const child of [...container.children]) {
      if ((child as HTMLElement).getAttribute("data-reconcile-key") === null) {
        child.remove();
      }
    }

    if (d.tools.length === 0) {
      container.replaceChildren();
      container.appendChild(
        el(
          "div",
          { className: "list-empty" },
          "No tools installed yet — Add tool searches a catalog of ~700 runtimes, language servers, and CLIs.",
        ),
      );
      return;
    }

    reconcile(container, flat, {
      key: (e: ListEntry) => {
        switch (e.kind) {
          case "label":
            return `label:${e.label}`;
          case "system":
            return `sys:${e.name}`;
          case "tool":
            // State fields participate in the key so any transition
            // (installing spinner, error, new version) remounts the
            // row — reconcile's update path only patches text.
            return [
              "tool",
              e.tool.name,
              e.tool.version,
              e.tool.latest ?? "",
              String(e.tool.installed),
              String(e.tool.installing),
              String(e.tool.pin ?? false),
              String(e.tool.disabled ?? false),
              e.tool.last_error === undefined || e.tool.last_error === "" ? "ok" : "err",
            ].join(":");
        }
      },
      mount: (e: ListEntry) => {
        switch (e.kind) {
          case "label":
            return el("div", { className: "list-group-label" }, e.label);
          case "system":
            return this.renderSystemRow(e.name);
          case "tool":
            return this.renderToolRow(e.tool);
        }
      },
      update: () => {
        // All volatile state is in the key; same-key rows are static.
      },
    });
  }

  private renderSystemRow(name: string): HTMLDivElement {
    return el(
      "div",
      { className: "list-row list-row-system" },
      el("span", { className: "tool-state-dot tool-state-ok", "aria-hidden": "true" }),
      el("span", { className: "list-row-name" }, name),
      el("span", { className: "list-row-meta" }, "system"),
    ) as HTMLDivElement;
  }

  private renderToolRow(t: ToolInfo): HTMLDivElement {
    const name = el("span", { className: "list-row-name", title: t.description ?? "" }, t.name);
    const row = el(
      "div",
      { className: "list-row" },
      stateDot(t),
      t.lsp === true
        ? el(
            "span",
            { className: "tool-name-wrap" },
            name,
            el("span", { className: "tool-source-chip" }, "LSP"),
          )
        : name,
      el("span", { className: "list-row-meta" }, metaText(t)),
    ) as HTMLDivElement;
    if (!t.installed && !t.installing) {
      row.classList.add("list-row-disabled");
    }
    row.appendChild(this.toolActions(t));
    return row;
  }

  /** Action cluster: the enabled/disabled toggle (every row), retry
   *  for failed installs, update when a newer version is known, pin
   *  toggle, delete. */
  private toolActions(t: ToolInfo): HTMLDivElement {
    const actions = el("div", { className: "list-row-actions" }) as HTMLDivElement;

    if (t.installing) {
      actions.append(el("span", { className: "list-row-meta tool-installing" }, "installing…"));
      return actions;
    }

    const disabled = t.disabled === true;
    if (!t.installed && !disabled) {
      const installBtn = el(
        "button",
        { className: "btn-small list-row-enable", "aria-label": `Install ${t.name}` },
        t.last_error !== undefined && t.last_error !== "" ? "Retry" : "Install",
      ) as HTMLButtonElement;
      installBtn.addEventListener("click", () => {
        void this.runInstall(t.name);
      });
      actions.append(installBtn);
    } else if (t.latest !== undefined && t.latest !== "") {
      const updateBtn = el(
        "button",
        {
          className: "btn-small",
          "data-tooltip": `Update to ${t.latest}`,
          "aria-label": `Update ${t.name} to ${t.latest}`,
        },
        "Update",
      ) as HTMLButtonElement;
      updateBtn.addEventListener("click", () => {
        void updateTools.dispatch({ names: [t.name] });
      });
      actions.append(updateBtn);
    }

    // The enable/disable switch: on = installed and reconciled, off =
    // template kept, footprint uninstalled.
    const toggleInput = el("input", {
      type: "checkbox",
      "aria-label": disabled
        ? `Enable ${t.name} (installs it)`
        : `Disable ${t.name} (uninstalls, keeps the entry)`,
    }) as HTMLInputElement;
    toggleInput.checked = !disabled;
    toggleInput.addEventListener("change", () => {
      toggleInput.disabled = true;
      void this.toggleDisabled(t.name, !toggleInput.checked);
    });
    const toggle = el(
      "label",
      {
        className: "toggle toggle-inline tool-toggle",
        "data-tooltip": disabled
          ? "Disabled template — switch on to install"
          : "Enabled — switch off to uninstall (keeps the entry)",
      },
      toggleInput,
      el("span", { className: "toggle-slider" }),
    );
    actions.append(toggle);

    const pinned = t.pin ?? false;
    const pinBtn = el(
      "button",
      {
        className: "list-row-btn list-row-pin",
        "aria-label": pinned ? `Unpin ${t.name}` : `Pin ${t.name} version`,
        "data-tooltip": pinned
          ? "Pinned — won't auto-update. Click to resume auto-updates."
          : "Auto-updating. Click to pin this version.",
      },
      iconEl(pinned ? ICON_PIN_FILLED : ICON_PIN),
    );
    if (pinned) {
      pinBtn.classList.add("list-row-pin-active");
    }
    pinBtn.addEventListener("click", () => {
      void this.togglePin(t.name, !pinned);
    });

    const delBtn = el(
      "button",
      { className: "list-row-btn", "data-tooltip": "Remove", "aria-label": `Remove ${t.name}` },
      iconEl(ICON_TRASH),
    );
    delBtn.addEventListener("click", () => {
      void this.runDelete(t.name);
    });

    if (disabled) {
      actions.append(delBtn);
    } else {
      actions.append(pinBtn, delBtn);
    }
    return actions;
  }

  private async runInstall(name: string): Promise<void> {
    await installTool.dispatch({ name });
    this.loadToolsList();
  }

  private async togglePin(name: string, pin: boolean): Promise<void> {
    await patchTool.dispatch({ name, pin });
    this.loadToolsList();
  }

  /** Flip the enabled/disabled state. Disabling a tool that enabled
   *  entries require answers 409 with the dependents — confirm the
   *  forced cascade like delete does. */
  private async toggleDisabled(name: string, disabled: boolean): Promise<void> {
    const d = await patchTool.dispatch({ name, disabled });
    if (disabled && d !== null && d.code === "has_dependents" && d.dependents !== undefined) {
      const list = d.dependents.join(", ");
      const force = await confirmDialog(
        `${name} is required by: ${list}. Disable it anyway?`,
        "Disable",
        "destructive",
      );
      if (force) {
        await patchTool.dispatch({ name, disabled, force: true });
      }
    }
    this.loadToolsList();
  }

  /** Delete with cascade-aware confirm (409 lists dependents). */
  private async runDelete(name: string): Promise<void> {
    const ok = await confirmDialog(`Remove ${name}?`, "Remove", "destructive");
    if (!ok) {
      return;
    }
    const d = await deleteTool.dispatch({ name });
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
      await deleteTool.dispatch({ name, force: true });
    }
    this.loadToolsList();
  }

  // --- add modal (search-first) ---

  private openAddModal(): void {
    f.search.value = "";
    this.manualFormCtl?.close();
    f.manualName.value = "";
    f.manualVersion.value = "";
    f.manualInstall.value = "";
    f.manualUninstall.value = "";
    f.manualProbe.value = "";
    openModal($.toolModal);
    void this.renderSearch("");
    f.search.focus();
  }

  /** Render catalog hits (empty query = the featured starter set). */
  private async renderSearch(query: string): Promise<void> {
    const d = await searchTools.dispatch({ q: query.trim() });
    const box = f.results;
    box.replaceChildren();
    if (d === null) {
      box.appendChild(el("div", { className: "list-empty" }, "Catalog unavailable"));
      return;
    }
    if (d.results.length === 0) {
      box.appendChild(
        el(
          "div",
          { className: "list-empty" },
          query.trim() === ""
            ? "Everything featured is already installed. Search the catalog by name."
            : `No catalog match for "${query.trim()}" — use “Custom install command” below.`,
        ),
      );
      return;
    }
    for (const hit of d.results) {
      box.appendChild(this.renderSearchHit(hit));
    }
  }

  private renderSearchHit(hit: SearchHit): HTMLElement {
    const addBtn = el(
      "button",
      { className: "btn-small list-row-enable", "aria-label": `Install ${hit.name}` },
      "Install",
    ) as HTMLButtonElement;
    addBtn.addEventListener("click", () => {
      addBtn.disabled = true;
      addBtn.textContent = "Queued…";
      void this.submitCatalogAdd({ name: hit.name });
    });
    return el(
      "div",
      { className: "list-row tool-hit" },
      el(
        "div",
        { className: "tool-hit-text" },
        el(
          "div",
          { className: "tool-hit-title" },
          el("span", { className: "list-row-name" }, hit.name),
          sourceChip(hit.source),
          ...(hit.lsp === true ? [el("span", { className: "tool-source-chip" }, "LSP")] : []),
        ),
        el("span", { className: "tool-hit-desc" }, hit.description ?? ""),
      ),
      addBtn,
    );
  }

  private async submitCatalogAdd(req: CreateToolRequest): Promise<void> {
    const d = await createTool.dispatch(req);
    if (d !== null) {
      closeModal($.toolModal);
      this.loadToolsList();
    } else {
      void this.renderSearch(f.search.value);
    }
  }

  private async submitManual(): Promise<void> {
    const name = f.manualName.value.trim();
    const version = f.manualVersion.value.trim();
    const install = f.manualInstall.value.trim();
    if (name === "" || version === "" || install === "") {
      return;
    }
    const req: CreateToolRequest = { name, source: "manual", version, install };
    const uninstall = f.manualUninstall.value.trim();
    if (uninstall !== "") {
      req.uninstall = uninstall;
    }
    const probe = f.manualProbe.value.trim();
    if (probe !== "") {
      req.probe = probe;
    }
    await this.submitCatalogAdd(req);
  }
}

// --- pure row helpers ---

/** Build the catalog meta line's content: "702 tools · mise v2026.7.11 +
 *  aqua v4.541.0 · refreshed 2 h ago" with an amber error suffix when the
 *  last refresh failed (the current catalog still stands — keep-last-good). */
function catalogMetaParts(info: CatalogInfo): (string | HTMLElement)[] {
  const bits: string[] = [`${String(info.entries)} tools`];
  const refs = Object.entries(info.refs ?? {})
    .map(([name, ref]) => `${name} ${ref}`)
    .sort()
    .join(" + ");
  if (refs !== "") {
    bits.push(refs);
  }
  // The catalog's own age (its compile stamp) is the freshness that
  // matters; the fetch time only says when we last checked. A stale
  // artifact fetched a minute ago is still stale.
  const generatedMs = info.generated !== undefined ? Date.parse(info.generated) : Number.NaN;
  if (!Number.isNaN(generatedMs)) {
    bits.push(`compiled ${relativeTime(generatedMs)}`);
  }
  if (info.fetched_at !== undefined && info.fetched_at > 0) {
    bits.push(`checked ${relativeTime(info.fetched_at)}`);
  } else if (info.source === "baked") {
    bits.push("from the image (not refreshed yet)");
  } else if (info.source === "cached") {
    bits.push("from the last refresh (previous run)");
  }
  const parts: (string | HTMLElement)[] = [`Catalog: ${bits.join(" · ")}`];
  if (info.last_error !== undefined && info.last_error !== "") {
    parts.push(
      el(
        "span",
        { className: "catalog-meta-error" },
        ` · last refresh failed (kept current catalog)`,
      ),
    );
  }
  return parts;
}

/** Coarse relative-time formatter for the catalog meta line. */
function relativeTime(unixMs: number): string {
  const mins = Math.max(0, Math.round((Date.now() - unixMs) / 60000));
  if (mins < 1) {
    return "just now";
  }
  if (mins < 60) {
    return `${String(mins)} min ago`;
  }
  const hours = Math.round(mins / 60);
  if (hours < 48) {
    return `${String(hours)} h ago`;
  }
  return `${String(Math.round(hours / 24))} d ago`;
}

function stateDot(t: ToolInfo): HTMLElement {
  let cls = "tool-state-missing";
  let label = "not installed";
  if (t.installing) {
    cls = "tool-state-busy";
    label = "installing";
  } else if (t.disabled === true) {
    cls = "tool-state-off";
    label = "disabled template — switch on to install";
  } else if (t.last_error !== undefined && t.last_error !== "") {
    cls = "tool-state-error";
    label = `failed: ${t.last_error}`;
  } else if (t.installed) {
    cls = "tool-state-ok";
    label = "installed";
  }
  return el("span", {
    className: `tool-state-dot ${cls}`,
    "data-tooltip": label,
    role: "img",
    "aria-label": label,
  });
}

function metaText(t: ToolInfo): string {
  if (t.disabled === true) {
    return "template";
  }
  const version =
    t.installed && t.installed_version !== undefined && t.installed_version !== ""
      ? (t.installed_version ?? "")
      : (t.version ?? "");
  if (t.latest !== undefined && t.latest !== "") {
    return `${version} → ${t.latest}`;
  }
  if (!t.installed && t.last_error !== undefined && t.last_error !== "") {
    return t.last_error;
  }
  return version;
}

/** Short source chip text: "aqua:cli/cli" -> "github", "npm:x" -> "npm". */
function sourceChip(source: string): HTMLElement {
  const kind = source.split(":", 1)[0] ?? source;
  const label = kind === "aqua" ? "binary" : kind;
  return el("span", { className: "tool-source-chip" }, label);
}

function jobHeadline(job: Job): string {
  const names = (job.names ?? []).join(", ");
  const what = names === "" ? job.kind : `${job.kind} ${names}`;
  switch (job.state) {
    case "queued":
      return `queued: ${what}`;
    case "running":
      return `running: ${what}`;
    case "done":
      return `✓ ${what} finished`;
    case "cancelled":
      return `${what} cancelled`;
    default:
      return `✗ ${what} failed${job.error !== undefined && job.error !== "" ? `: ${job.error}` : ""}`;
  }
}

/** Install a tool by name (creating it from the catalog if needed) and
 *  resolve once its job reaches a terminal state. Output lines stream
 *  into onLine. Shared by the forge-CLI and MCP-node install banners.
 *
 *  The SSE listeners register BEFORE the mutation is dispatched (a fast
 *  job could otherwise finish between the 202 response and listener
 *  registration, stranding the promise), buffering events until the
 *  job id is known; a post-dispatch poll of the jobs endpoint covers
 *  the remaining case of a terminal event lost to an SSE reconnect. */
export async function installToolAndWait(
  name: string,
  onLine: (line: string) => void,
): Promise<{ ok: boolean; error?: string }> {
  const jobRef: { id: string | undefined } = { id: undefined };
  let settled = false;
  let settle: (r: { ok: boolean; error?: string }) => void = () => {
    /* replaced below */
  };
  const result = new Promise<{ ok: boolean; error?: string }>((resolve) => {
    settle = (r) => {
      if (!settled) {
        settled = true;
        unOut();
        unChanged();
        resolve(r);
      }
    };
  });
  const buffered: { id: string; lines: string[] }[] = [];
  const terminal = new Map<string, { state: string; error?: string }>();
  const settleFromState = (state: string, error?: string): void => {
    if (state === "done") {
      settle({ ok: true });
    } else if (state === "failed" || state === "cancelled") {
      settle({ ok: false, ...(error !== undefined && error !== "" ? { error } : {}) });
    }
  };
  const unOut = onSSE("tool_job_output", (_c, p) => {
    if (jobRef.id === undefined) {
      buffered.push({ id: p.job_id, lines: [...p.lines] });
      return;
    }
    if (p.job_id !== jobRef.id) {
      return;
    }
    for (const line of p.lines) {
      onLine(line);
    }
  });
  const unChanged = onSSE("tool_job_changed", (_c, p) => {
    const job = p.job;
    if (job === undefined) {
      return;
    }
    if (jobRef.id === undefined) {
      terminal.set(job.id, {
        state: job.state,
        ...(job.error !== undefined ? { error: job.error } : {}),
      });
      return;
    }
    if (job.id !== jobRef.id) {
      return;
    }
    settleFromState(job.state, job.error);
  });

  const d = await ensureTool.dispatch({ name });
  const id = d?.job?.id;
  if (id === undefined) {
    settle({ ok: false, error: "install request failed" });
    return result;
  }
  jobRef.id = id;
  // Drain anything that arrived before the id was known.
  for (const b of buffered) {
    if (b.id === id) {
      for (const line of b.lines) {
        onLine(line);
      }
    }
  }
  const t = terminal.get(id);
  if (t !== undefined) {
    settleFromState(t.state, t.error);
  }
  return result;
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
