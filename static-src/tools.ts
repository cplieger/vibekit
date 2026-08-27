// ---------------------------------------------------------------------------
// Tool management (Settings -> Tools) over the v2 tools engine.
//
// The server owns the manifest, install state, and a single-flight job
// queue; this module is a pure projection. Mutations return 202 with a
// job; progress arrives over the tool_job_changed / tool_job_output
// SSE events (the output panel is a live follower that survives
// reloads via GET /api/tools/jobs). The add flow is search-first: the
// catalog (compiled from the mise + aqua registries) is the browse
// surface. There is no manual-command escape hatch: a form asking a reader to
// author an install script is the shell with worse ergonomics, and the shell is
// one click away. A closing note on every result set says so instead.
// ---------------------------------------------------------------------------

import { closeModal, openModal, RollingOutput } from "./modals.js";
import { confirm as confirmDialog } from "./confirm.js";
import { ICON_PIN, ICON_PIN_FILLED, ICON_SPINNER, ICON_TRASH } from "./icons.js";
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
import { join } from "@cplieger/keyenc";
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

// The two toolbelt job kinds a reader launches from a named pill. The other
// four — install, uninstall, disable, reconcile — are launched from a tool row
// or by the server itself, so they are the residual case in `pillOwns` and are
// never named here.
const JOB_UPDATE = "update";
const JOB_CATALOG_REFRESH = "catalog-refresh";

/** Whether a job is still queued or running. One predicate, because every
 *  control's face and the output panel's headline read the same answer. */
function jobIsLive(job: Job): boolean {
  return job.state === "queued" || job.state === "running";
}

/** Whether the pill that launched a job carries its cancel. Two kinds have a
 *  named pill of their own; every other kind is launched from a tool row
 *  (install, uninstall, disable) or by the server (the boot reconcile), so its
 *  only cancel control is the shared Cancel pill. Exhaustive over toolbelt's six
 *  kinds: two named, four residual.
 *
 *  A per-tool Update rides the Update-all pill deliberately. It is the same kind
 *  of work, and splitting on `names.length` would leave that pill sitting idle
 *  through an update while a nameless Cancel appeared beside it — the shape this
 *  whole control replaced. */
function pillOwns(kind: string): boolean {
  return kind === JOB_UPDATE || kind === JOB_CATALOG_REFRESH;
}

interface JobPillSpec {
  /** What a click does while no job of this pill's kind is live. */
  start: () => void;
  /** What a click does while one is. */
  cancel: () => void;
  /** The busy face's accessible name. The visible label is just "Cancel"; a
   *  screen reader gets the whole "Cancel the running update", because out of
   *  the row's context a bare "Cancel" says nothing about what it stops. */
  busyAria: string;
}

/** An action pill that owns one job kind: it launches the work, and while that
 *  work is live it BECOMES the control that stops it — spinner for the glyph,
 *  "Cancel" for the label, one click to cancel.
 *
 *  Same shape as the composer's send button (`prompt-input.ts`: Send while idle,
 *  a stop square mid-turn) and for the same reason — the control that started
 *  the work is where a reader looks to stop it. It replaced a fourth pill that
 *  un-hid beside these three whenever ANY job was live, which cost two things:
 *  the launching pill showed no sign that its own work was running, and a bare
 *  "Cancel" sat next to three neighbours with nothing saying which it belonged
 *  to.
 *
 *  The idle face is CAPTURED from the authored markup rather than rebuilt here,
 *  so index.html stays the one place a pill's glyph and label are written.
 *
 *  Nothing here sets `disabled`. The busy face IS the cancel control, so
 *  disabling it would remove the affordance it exists to offer; the only guard
 *  is `requested`, which stops a second click re-sending a cancel already on the
 *  wire. That also subsumes the `disabled` the catalog-refresh pill used to
 *  carry for the whole life of its job: the engine's queue would accept a
 *  duplicate refresh, and a pill whose click CANCELS cannot enqueue one. */
class JobPill {
  private readonly idleFace: readonly Node[];
  private readonly idleAria: string;
  private busy = false;
  private requested = false;

  constructor(
    private readonly btn: HTMLButtonElement,
    private readonly spec: JobPillSpec,
  ) {
    this.idleFace = [...btn.childNodes].map((n) => n.cloneNode(true));
    this.idleAria = btn.getAttribute("aria-label") ?? "";
    btn.addEventListener("click", () => {
      this.onClick();
    });
  }

  /** Follow the live job. Idempotent: the SSE reports one event per state
   *  transition and several of them describe the same live job. */
  setBusy(busy: boolean): void {
    if (busy === this.busy) {
      return;
    }
    this.busy = busy;
    this.requested = false;
    this.paint();
  }

  private onClick(): void {
    if (!this.busy) {
      this.spec.start();
      return;
    }
    if (this.requested) {
      return;
    }
    this.requested = true;
    this.paint();
    this.spec.cancel();
  }

  private paint(): void {
    if (!this.busy) {
      this.btn.replaceChildren(...this.idleFace.map((n) => n.cloneNode(true)));
      this.btn.setAttribute("aria-label", this.idleAria);
      this.btn.removeAttribute("data-tooltip");
      this.btn.classList.remove("is-busy");
      return;
    }
    const label = this.requested ? "Cancelling…" : "Cancel";
    const full = this.requested ? label : this.spec.busyAria;
    this.btn.replaceChildren(iconEl(ICON_SPINNER), el("span", null, label));
    // The visible label stays one word so the pill does not resize the row it
    // sits in; the full "Cancel the running update" reaches a screen reader as
    // the accessible name and a pointer as the tooltip. Same split the send
    // button uses for its own stop face.
    this.btn.setAttribute("aria-label", full);
    this.btn.setAttribute("data-tooltip", full);
    this.btn.classList.add("is-busy");
  }
}

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
};

class ToolsManager {
  private data: Inventory | null = null;
  private output: RollingOutput | null = null;
  /** Job id the output panel is currently following. */
  private followedJob = "";
  /** The job the SSE last reported queued or running, null when nothing is.
   *  Decides which of the three controls carries its cancel. */
  private live: Job | null = null;
  private updatePill: JobPill | null = null;
  private refreshPill: JobPill | null = null;
  private unsubscribes: (() => void)[] = [];

  /** Public hook for global cleanup: cancels in-flight tool fetch. */
  cancelLoad(): void {
    loadTools.cancel();
  }

  init(): void {
    this.output = new RollingOutput($.toolUpdateOutput, "git-output-modal");
    // Wiring a panel resets what it believes is running: it learns that from the
    // seed in loadToolsList or from the first SSE event, never from last time.
    // One wiring per page load, so this is a no-op in production.
    //
    // The pills below CAPTURE their idle face from the live DOM, so a second
    // init() over a strip that is currently showing a Cancel face would adopt
    // that as the face to restore. Re-mount the markup before re-wiring, or keep
    // init() to once.
    this.live = null;
    this.followedJob = "";

    $.toolAddBtn.addEventListener("click", () => {
      this.openAddModal();
    });
    this.updatePill = new JobPill($.toolUpdateBtn, {
      start: () => {
        void updateTools.dispatch(undefined);
      },
      cancel: () => {
        this.cancelLiveJob();
      },
      busyAria: "Cancel the running update",
    });
    bindLoadingState("tools.update", $.toolUpdateBtn);
    this.refreshPill = new JobPill(f.catalogRefresh, {
      start: () => {
        void refreshCatalog.dispatch(undefined);
      },
      cancel: () => {
        this.cancelLiveJob();
      },
      busyAria: "Cancel the running catalog refresh",
    });
    bindLoadingState("tools.refresh_catalog", f.catalogRefresh);
    // The residual Cancel pill, for a job kind no pill above owns.
    f.cancel.addEventListener("click", () => {
      this.cancelLiveJob();
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
        // The SSE stream is ordered, so its account of what is running is never
        // stale: this is the authoritative writer for the control faces, and the
        // inventory only ever SEEDS them.
        const live = jobIsLive(job);
        this.setLive(live ? job : null);
        this.followJob(job);
        // loadToolsList refetches the catalog meta line too, so a
        // settling catalog-refresh job needs no extra fetch here.
        this.loadToolsList();
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

    // Add-modal wiring: a debounced search over the catalog and apt.
    const runSearch = debounce(() => {
      void this.renderSearch(f.search.value);
    }, 200);
    f.search.addEventListener("input", runSearch);
  }

  dispose(): void {
    for (const un of this.unsubscribes) {
      un();
    }
    this.unsubscribes = [];
  }

  /** Point the output panel at a job: reset on a new id, headline the
   *  terminal state, refresh the follow target. */
  private followJob(job: Job): void {
    if (this.output === null) {
      return;
    }
    if (job.id !== this.followedJob) {
      this.followedJob = job.id;
      this.output.clear();
      this.output.append(jobHeadline(job));
      return;
    }
    if (!jobIsLive(job)) {
      this.output.append(jobHeadline(job));
    }
  }

  /** THE writer for the three controls a live job decides, because the ownership
   *  rule has to be asked the same way by the pill that turns into Cancel and by
   *  the pill that hides. Answering it in two places is how they disagree. */
  private setLive(job: Job | null): void {
    this.live = job;
    const kind = job?.kind ?? "";
    this.updatePill?.setBusy(kind === JOB_UPDATE);
    this.refreshPill?.setBusy(kind === JOB_CATALOG_REFRESH);
    f.cancel.classList.toggle("hidden", job === null || pillOwns(kind));
  }

  /** Give the inventory ONE chance to report a job that started before this
   *  module was wired. The panel is lazily initialized, so a boot reconcile or an
   *  install a feature banner triggered can already be running when the SSE
   *  handlers register, and nothing replays those events.
   *
   *  On the way IN only, and `followedJob` is the test for that: it is set by the
   *  first job this panel hears about from any source, so a non-empty one means
   *  the stream has already spoken and outranks a snapshot. That ordering is the
   *  whole point — `Inventory.job` is a snapshot while the SSE is a stream, and
   *  `loadToolsList` runs once per job event, so a GET issued while a job was
   *  queued can resolve after the event that finished it. Adopting that answer
   *  would strand a pill on its Cancel face with nothing left to cancel. */
  private seedLiveJob(job: Job | undefined): void {
    if (this.followedJob !== "" || job === undefined || !jobIsLive(job)) {
      return;
    }
    this.setLive(job);
  }

  /** Cancel whatever is running. All three controls route here: the engine's
   *  queue is single-flight, so "the live job" is unambiguous. Read from `live`
   *  rather than from the output panel's follow target, which outlives the job it
   *  points at — cancelling a settled job is a request with no subject. */
  private cancelLiveJob(): void {
    const id = this.live?.id;
    if (id === undefined) {
      return;
    }
    void cancelToolJob.dispatch({ id });
  }

  loadToolsList(): void {
    this.loadCatalogMeta();
    void loadTools.dispatch(undefined, {
      onSuccess: (d) => {
        this.data = d;
        this.renderToolsList();
        this.seedLiveJob(d.job);
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

    // THREE groups, and the first two are one list split by ONE fact the
    // engine reports: `essential`. A pre-bundled tool is not an extra layered
    // over the catalog — it IS a catalog entry, installed and updated by the
    // same machinery as any other, which this app declares as necessary for it
    // to work properly. So it sits in its own group rather than in a second
    // mechanism, and the engine refuses to remove it (ErrEssential), which is
    // why its row carries no bin (`toolActions`). Disable stays available: that
    // is the honest escape hatch, and it does not lose the entry.
    //
    // Labels appear only when the split is real. With nothing essential the
    // list is one unlabelled group, exactly as before.
    const flat: ListEntry[] = [];
    const essential = d.tools.filter((t) => t.essential === true);
    const chosen = d.tools.filter((t) => t.essential !== true);
    if (essential.length > 0) {
      flat.push({ kind: "label", label: "pre-bundled, kept current by the catalog" });
      for (const t of essential) {
        flat.push({ kind: "tool", tool: t });
      }
      if (chosen.length > 0) {
        flat.push({ kind: "label", label: "added by you" });
      }
    }
    for (const t of chosen) {
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
      // Every branch builds its key with keyenc `join` so the three key
      // families stay in one namespace no component can cross into. This
      // list was ALREADY injective before the change: a tool name is
      // validated colon-free and unique server-side, so the `tool:<name>:`
      // prefix could not be forged, and the label/system branches carry
      // literals and system names. The adoption is for uniformity with the
      // other composite keys, not a fix. Had a collision been possible, the
      // effect would be a REMOUNT, not a lost row: reconcile walks backwards,
      // so the earlier duplicate is re-mounted on every pass (dropped focus,
      // restarted animation).
      key: (e: ListEntry) => {
        switch (e.kind) {
          case "label":
            return join("label", e.label);
          case "system":
            return join("sys", e.name);
          case "tool":
            // State fields participate in the key so any transition
            // (installing spinner, error, new version) remounts the
            // row — reconcile's update path only patches text.
            return join(
              "tool",
              e.tool.name,
              // `?? ""` where the old array form relied on Array.join
              // coercing an absent optional field to "". Same bytes; the
              // typed signature just makes the coercion explicit.
              e.tool.version ?? "",
              e.tool.latest ?? "",
              String(e.tool.installed),
              String(e.tool.installing),
              String(e.tool.pin ?? false),
              String(e.tool.disabled ?? false),
              // `dependents` is read by the disable/remove pre-flight, which
              // reads the row's captured ToolInfo. Enabling or removing an
              // entry elsewhere changes who depends on this one without
              // touching any other field here, so the set has to be in the
              // key or the pre-flight asks its question from a stale answer.
              (e.tool.dependents ?? []).join(","),
              // Both drive chips rather than text, so a change to either has to
              // remount the row. `installed` covers the usual checksum
              // transition (empty until something is installed), but a reinstall
              // onto a definition that gained a checksum source moves it with
              // every other field unchanged.
              e.tool.checksum ?? "",
              String(e.tool.essential ?? false),
              e.tool.last_error === undefined || e.tool.last_error === "" ? "ok" : "err",
            );
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
    const chips = rowChips(t);
    const row = el(
      "div",
      { className: "list-row" },
      stateDot(t),
      chips.length > 0 ? el("span", { className: "tool-name-wrap" }, name, ...chips) : name,
      el("span", { className: "list-row-meta" }, metaText(t)),
    ) as HTMLDivElement;
    if (t.disabled === true) {
      // Dimming is for a TEMPLATE (a row the user has not opted into),
      // not for "not installed". An enabled tool that is missing or
      // whose install failed is the row that most needs attention, and
      // it already carries a grey or red dot plus an Install/Retry
      // button — dimming it to tertiary text argued the opposite.
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
      const nextDisabled = !toggleInput.checked;
      toggleInput.disabled = true;
      void this.toggleDisabled(t, nextDisabled).then((changed) => {
        if (!changed) {
          // Nothing moved server-side, so every keyed field is unchanged and
          // reconcile reuses this exact node — a refetch cannot put the switch
          // back. Restore it to the state the row was rendered from.
          toggleInput.checked = !disabled;
          toggleInput.disabled = false;
        }
      });
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

    // No bin on a pre-bundled row: the engine refuses the removal
    // (ErrEssential), so offering the control would only produce a 409 the
    // reader cannot act on. The switch above is the escape hatch — it
    // uninstalls the footprint and keeps the entry.
    const trailing: HTMLElement[] = [];
    if (t.essential !== true) {
      const delBtn = el(
        "button",
        { className: "list-row-btn", "data-tooltip": "Remove", "aria-label": `Remove ${t.name}` },
        iconEl(ICON_TRASH),
      );
      delBtn.addEventListener("click", () => {
        void this.runDelete(t);
      });
      trailing.push(delBtn as HTMLElement);
    } else {
      // A GHOST bin, reserving the box the real one would occupy. The
      // alignment rule below depends on the widths to the RIGHT of the switch
      // being fixed, and a pre-bundled row that simply dropped the control
      // would slide its switch 26px right — the same stepping column the pin's
      // position exists to prevent, with the step now falling between the two
      // groups. Reserving the icon's own box rather than a hardcoded width
      // keeps the two in lockstep. `visibility: hidden` and not `display: none`
      // for the space; `aria-hidden` and a non-button element so nothing
      // reaches the accessibility tree or the tab order.
      trailing.push(
        el(
          "span",
          { className: "list-row-btn list-row-btn-ghost", "aria-hidden": "true" },
          iconEl(ICON_TRASH),
        ) as HTMLElement,
      );
    }

    // THE TRAILING ORDER IS ONE STATEMENT BECAUSE IT IS AN ALIGNMENT RULE.
    // `.list-row-actions` is right-aligned (`margin-inline-start: auto`), so an
    // element's x is decided by the widths to its RIGHT — which means only the
    // controls that appear on EVERY row may sit to the right of a control whose
    // column has to read as a column. The pin is conditional (a disabled entry
    // has no version to pin), and it used to sit between the switch and the bin,
    // so the switch landed 28px further right on every disabled row and the
    // column of switches visibly stepped in and out. With the pin to its LEFT,
    // the only thing right of the switch is the bin, which is unconditional, so
    // the switch column is fixed. The Install/Update button inherits the
    // variance instead, which costs nothing: it is on a minority of rows and its
    // three labels are three different widths, so it never formed a column.
    // It also groups better — Update and Pin are both about the version.
    //
    // `trailing` is that unconditional slot: the bin on a row that can be
    // removed, a same-sized ghost on one that cannot.
    if (disabled) {
      actions.append(toggle, ...trailing);
    } else {
      actions.append(pinBtn, toggle, ...trailing);
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

  /** Flip the enabled/disabled state. `dependents` names the enabled entries
   *  that require this one, so when disabling is going to cascade the question
   *  is asked BEFORE the request and the answer rides on it. The engine
   *  re-derives the set under the manifest lock and still answers 409, which
   *  is what makes the field safe to trust: a stale inventory is refused, not
   *  obeyed, and the 409 branch below is that fallback.
   *
   *  Returns whether anything reached the server, so a declined confirm can
   *  put the switch back. */
  private async toggleDisabled(t: ToolInfo, disabled: boolean): Promise<boolean> {
    const known = t.dependents ?? [];
    if (disabled && known.length > 0) {
      const force = await confirmDialog(
        `${t.name} is required by: ${known.join(", ")}. Disable it anyway?`,
        "Disable",
        "destructive",
      );
      if (!force) {
        return false;
      }
      await patchTool.dispatch({ name: t.name, disabled, force: true });
      this.loadToolsList();
      return true;
    }

    const d = await patchTool.dispatch({ name: t.name, disabled });
    if (disabled && d !== null && d.code === "has_dependents" && d.dependents !== undefined) {
      const list = d.dependents.join(", ");
      const force = await confirmDialog(
        `${t.name} is required by: ${list}. Disable it anyway?`,
        "Disable",
        "destructive",
      );
      if (!force) {
        return false;
      }
      await patchTool.dispatch({ name: t.name, disabled, force: true });
    }
    this.loadToolsList();
    return true;
  }

  /** Remove, asking once. A tool with known dependents puts them in the first
   *  dialog and forces in one request; the 409 branch stays as the fallback
   *  for an inventory that went stale between the render and the click. */
  private async runDelete(t: ToolInfo): Promise<void> {
    const known = t.dependents ?? [];
    if (known.length > 0) {
      const ok = await confirmDialog(
        `Remove ${t.name}? It is required by: ${known.join(", ")}. Removing it removes them too.`,
        "Remove all",
        "destructive",
      );
      if (!ok) {
        return;
      }
      await deleteTool.dispatch({ name: t.name, force: true });
      this.loadToolsList();
      return;
    }

    const ok = await confirmDialog(`Remove ${t.name}?`, "Remove", "destructive");
    if (!ok) {
      return;
    }
    const d = await deleteTool.dispatch({ name: t.name });
    if (d !== null && d.code === "has_dependents" && d.dependents !== undefined) {
      const list = d.dependents.join(", ");
      const force = await confirmDialog(
        `${t.name} is required by: ${list}. Remove all of them?`,
        "Remove all",
        "destructive",
      );
      if (!force) {
        return;
      }
      await deleteTool.dispatch({ name: t.name, force: true });
    }
    this.loadToolsList();
  }

  // --- add modal (search-first) ---

  private openAddModal(): void {
    f.search.value = "";
    openModal($.toolModal);
    void this.renderSearch("");
    f.search.focus();
  }

  /** Render the search: catalog entries first, then Debian packages.
   *
   *  TWO blocks rather than one merged list, because the same name can appear
   *  in both at DIFFERENT versions -- the catalog tracks upstream releases and
   *  apt tracks the distro's candidate -- and a merged list would have to pick
   *  one, hiding a real choice. Each row carries its own version and source, so
   *  the two are comparable rather than conflated.
   *
   *  Uninstallable entries are never requested: the engine hides them unless a
   *  caller opts in with `unavailable=1`, and this client deliberately does not.
   *  A row that cannot be installed is noise in an install picker; the shell
   *  note below is the honest answer for anything absent. */
  private async renderSearch(query: string): Promise<void> {
    const q = query.trim();
    const d = await searchTools.dispatch({ q });
    const box = f.results;
    box.replaceChildren();
    if (d === null) {
      box.appendChild(el("div", { className: "list-empty" }, "Catalog unavailable"));
      return;
    }
    const catalog = d.results.filter((h) => h.apt !== true);
    const apt = d.results.filter((h) => h.apt === true);

    if (catalog.length === 0 && apt.length === 0) {
      box.appendChild(
        el(
          "div",
          { className: "list-empty" },
          q === ""
            ? "Everything featured is already installed. Search by name."
            : `Nothing matches "${q}".`,
        ),
      );
      box.appendChild(this.shellNote(d.apt_available));
      return;
    }

    if (catalog.length > 0) {
      box.appendChild(el("div", { className: "tool-block-head" }, "Catalog"));
      for (const hit of catalog) box.appendChild(this.renderSearchHit(hit));
    }
    if (apt.length > 0) {
      box.appendChild(
        el(
          "div",
          { className: "tool-block-head" },
          "Debian packages",
          // Stated on the block rather than per row: an apt package is not
          // version-managed by the engine and dies with the container layer
          // unless the entry is kept, which is a property of the whole group.
          el("span", { className: "tool-block-note" }, "installed with apt, on this container"),
        ),
      );
      for (const hit of apt) box.appendChild(this.renderSearchHit(hit));
    }
    box.appendChild(this.shellNote(d.apt_available));
  }

  /** The closing note. Present on EVERY result set, including a full one:
   *  the catalog is large but finite, and a reader who cannot find their tool
   *  needs to know the shell is the answer rather than concluding the tool is
   *  unavailable. */
  private shellNote(aptAvailable: boolean): HTMLElement {
    const parts: string[] = [
      "Not listed? Install it in the shell — anything you can run there works. ",
      "The engine only manages what it installed.",
    ];
    // Said explicitly rather than left as an absent block. With apt
    // unavailable the engine returns no Debian hits at all, so a reader
    // searching for a package sees nothing and cannot tell "no such package"
    // from "this container cannot install one".
    if (!aptAvailable) {
      parts.push(" Debian packages are not searchable here: apt needs root and this container has none.");
    }
    return el("div", { className: "tool-shell-note" }, ...parts);
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
      // An apt hit is not a catalog entry, so the engine has no source to
      // hydrate from and the request has to carry it. A catalog hit omits it,
      // which is what lets the engine resolve the source it published.
      void this.submitCatalogAdd(
        hit.apt === true ? { name: hit.name, source: hit.source } : { name: hit.name },
      );
    });
    const chips: HTMLElement[] = [sourceChip(hit.source)];
    // The version rides its own chip because it is the reason both blocks
    // exist: the same name can be one release in the catalog and another in
    // the distro, and a reader choosing between them needs both numbers.
    if (hit.version !== undefined && hit.version !== "") {
      chips.push(el("span", { className: "tool-source-chip" }, hit.version));
    }
    if (hit.lsp === true) chips.push(el("span", { className: "tool-source-chip" }, "LSP"));
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
          ...chips,
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
}

// --- pure row helpers ---

/** Build the catalog meta line's content: "702 tools · aqua v4.541.0 +
 *  mise v2026.7.11 · compiled 2 h ago · checked 1 min ago · auto-refresh
 *  on" with an amber error suffix when the last refresh failed (the
 *  current catalog still stands — keep-last-good). */
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
  // Surface the engine's schedule state so "off" deployments can tell
  // why the catalog ages (only the Refresh button updates it). Skipped
  // when no refresh source is configured — neither mode could fetch.
  if (info.url !== undefined && info.url !== "") {
    bits.push(`auto-refresh ${info.scheduled ? "on" : "off"}`);
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

/** The chips a TABLE row carries, at most two: the LSP badge, and one honesty
 *  chip naming a weaker guarantee than the normal case.
 *
 *  Exactly one honesty chip, because the three conditions are mutually
 *  exclusive by construction and each one supersedes the question the next
 *  would ask. An apt package is Debian's, verified by the distro archive and
 *  reinstalled from it at every boot, so its integrity story is the distro's
 *  rather than the engine's. A self-managed entry was installed by hand, so the
 *  engine neither verified it nor updates it — that chip replaces a silence, as
 *  `updateOne` returns early for a manual source without emitting anything, and
 *  such an entry is otherwise frozen forever with no indication.
 *
 *  Everything else reads ONE fact: whether verification actually happened
 *  (`ToolInfo.Checksum`), never the source kind. Of the catalog's aqua entries
 *  402 declare a checksum and 252 do not, `node` and `go` among them, so a
 *  per-source table would be wrong for either group. A package-manager source
 *  (npm, pip, cargo, go) reports no checksum at all and earns no chip: the
 *  package manager owns verification there, and a chip on every one of those
 *  rows would say nothing. */
function rowChips(t: ToolInfo): HTMLElement[] {
  const chips: HTMLElement[] = [];
  if (t.lsp === true) {
    chips.push(el("span", { className: "tool-source-chip" }, "LSP"));
  }
  const kind = (t.source ?? "").split(":", 1)[0] ?? "";
  if (kind === "apt") {
    chips.push(
      el(
        "span",
        {
          className: "tool-source-chip",
          "data-tooltip": "A Debian package. Reinstalled at every boot, at whatever version apt offers then.",
        },
        "apt",
      ),
    );
  } else if (kind === "manual") {
    chips.push(
      el(
        "span",
        {
          className: "tool-source-chip",
          "data-tooltip": "Installed by hand. The engine does not update it.",
        },
        "self-managed",
      ),
    );
  } else if (t.checksum === "unverified") {
    chips.push(
      el(
        "span",
        {
          className: "tool-source-chip",
          "data-tooltip": "The definition declares no checksum, so the download was not verified against one.",
        },
        "no checksum",
      ),
    );
  }
  return chips;
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
