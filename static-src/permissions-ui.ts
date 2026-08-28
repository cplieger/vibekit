// ---------------------------------------------------------------------------
// "Permissions" section in the Settings panel.
//
// Two controllers live here:
//
//   - PermissionsUIController — vibekit's own complementary controls,
//     each wired to its config.json field and re-rendered on change:
//       * supervised_default  boolean
//       * agent_ignore_files  string[]
//   - NativePolicyController — the native (Cedar) policy VIEW + editor,
//     the real tool-call authorization surface on v3.
//
// Cedar is the sole tool-call authorization surface on v3: there is no
// client-side shell classifier, no trust modes, and no per-command rule
// list — the permission dialog's "Always allow" persists a native rule.
// ---------------------------------------------------------------------------

import { patchSettings } from "./persist.js";
import type { EffectiveSettings } from "./persist.js";
import { maybeEl } from "./dom.js";
import { apiGet } from "./api-client.js";
import { buildChip } from "./chip.js";
import { registerCleanup, bindLoadingState } from "./actions/index.js";
import { editNativeRule, explainPolicy, setSecurityProfile } from "./actions/permissions.js";
import { reconcile } from "./reconcile.js";
import { onSSE } from "./bus.js";
import { confirm } from "./confirm.js";
import type { PolicyView, PolicyRule, SecurityProfile } from "./types.js";
import { el } from "@cplieger/reactive";

// ---------------------------------------------------------------------------
// PermissionsUIController — encapsulates all module-level state.
// ---------------------------------------------------------------------------

class PermissionsUIController {
  private ignoreFiles: string[] = [];

  initPermissions(initial: EffectiveSettings): void {
    // Supervised-mode default for new chats. (Tool-call approval itself is
    // the native Cedar policy, rendered by NativePolicyController below.)
    const supCheckbox = maybeEl<HTMLInputElement>("supervised-default-checkbox");
    if (supCheckbox !== null) {
      supCheckbox.checked = initial.supervised_default;
      supCheckbox.addEventListener("change", () => {
        void patchSettings({ supervised_default: supCheckbox.checked });
      });
    }
    // Whether a SCHEDULED run's tool request is approved or refused when nobody
    // answers it. Its own switch rather than a read of the policy above, because
    // approving while watching is a different consent from approving unattended.
    const schedCheckbox = maybeEl<HTMLInputElement>("scheduled-auto-approve-checkbox");
    if (schedCheckbox !== null) {
      schedCheckbox.checked = initial.scheduled_auto_approve;
      schedCheckbox.addEventListener("change", () => {
        void patchSettings({ scheduled_auto_approve: schedCheckbox.checked });
      });
    }
    this.initAgentIgnoreUI(initial);
  }

  // --- Private: agent ignore files ---

  private initAgentIgnoreUI(initial: EffectiveSettings): void {
    // No `?? []`. That fallback was the live bug this whole change exists for:
    // the server's default is two patterns (.gitignore, .kiroignore) and the
    // filter applies them whenever the key is absent, so on any config.json that
    // existed without the key this row rendered EMPTY while agent reads were
    // being filtered — and because the row is authoritative on write, the first
    // add or remove persisted an explicit list that dropped both of them.
    this.ignoreFiles = [...initial.agent_ignore_files];
    this.renderIgnoreChips();

    const input = maybeEl<HTMLInputElement>("agent-ignore-input");
    const addBtn = maybeEl("agent-ignore-add");
    if (input === null || addBtn === null) {
      return;
    }
    const submit = (): void => {
      const val = input.value.trim();
      if (val === "") {
        return;
      }
      if (this.ignoreFiles.includes(val)) {
        input.value = "";
        return;
      }
      this.ignoreFiles.push(val);
      void patchSettings({ agent_ignore_files: this.ignoreFiles });
      input.value = "";
      // Refocus for repeat entry (adding several files in a row).
      input.focus();
      this.renderIgnoreChips();
    };
    addBtn.addEventListener("click", submit);
    input.addEventListener("keydown", (e: KeyboardEvent) => {
      if (e.key === "Enter") {
        e.preventDefault();
        submit();
      }
    });
  }

  private renderIgnoreChips(): void {
    const container = maybeEl("agent-ignore-chips");
    const hint = maybeEl("agent-ignore-empty-hint");
    if (hint !== null) {
      hint.classList.toggle("hidden", this.ignoreFiles.length > 0);
    }
    if (container === null) {
      return;
    }
    // Ignore entries are immutable strings, so a key-by-entry, mount-only
    // reconcile is sufficient (no update fn needed): add/remove touches only
    // the changed chip and preserves the rest. The empty state is a sibling
    // element (agent-ignore-empty-hint), toggled above.
    reconcile(container, this.ignoreFiles, {
      key: (entry) => entry,
      mount: (entry) =>
        buildChip({
          label: entry,
          code: true,
          chipClass: "chip mono",
          onRemove: () => {
            this.ignoreFiles = this.ignoreFiles.filter((f) => f !== entry);
            void patchSettings({ agent_ignore_files: this.ignoreFiles });
            this.renderIgnoreChips();
          },
        }),
    });
  }
}

// Singleton instance — internal to the module.
const controller = new PermissionsUIController();

// ---------------------------------------------------------------------------
// NativePolicyController — the native (Cedar) policy VIEW + conservative
// file-writing editor.
//
// The VIEW (GET /api/permissions) is the source of truth for what kiro-cli
// ENFORCES: the layered rule set (kiro/administration/user/workspace/agent/
// session) with per-rule capability, 3-valued effect, path match/exclude, and
// source provenance. The EDITOR writes the user/workspace permissions.yaml
// (POST /api/permissions/rules), which KAS hot-reloads — the server never
// mutates the running policy directly. It is conservative: a new rule
// defaults to Ask (server-enforced), and removing a Deny (which widens
// access) is confirmed first. The list is a pure server projection: refetched
// after every edit and on the permissions_changed SSE, so it can't drift from
// what KAS enforces.
// ---------------------------------------------------------------------------

const NATIVE_SCOPE_ORDER = ["kiro", "administration", "user", "workspace", "agent", "session"];
const NATIVE_SCOPE_LABEL: Record<string, string> = {
  kiro: "Kiro built-in (read-only)",
  administration: "Administration (read-only)",
  user: "User — global",
  workspace: "Workspace",
  agent: "Agent — current mode (read-only)",
  session: "Session — runtime (read-only)",
};

function splitGlobs(raw: string): string[] {
  return raw
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

/** The Custom profile's id, and the one id the client must know by name: it is the
 *  state the table becomes editable in, which is a UI fact rather than a policy one.
 *  Every other profile is rendered from whatever the server sent. */
const CUSTOM_PROFILE = "custom";

/** The loosest profile's id, needed only to decide which selection earns the extra
 *  confirm. The ladder's ORDER is the server's, so this is a hint the picker checks
 *  rather than a policy it enforces — the server would grant the same set either
 *  way, and a rename upstream costs the extra confirm rather than correctness. */
const LOOSEST_PROFILE_HINT = "unrestricted";

/** Human labels, keyed by profile id. Prose for a person, so it lives on the client
 *  rather than travelling with the ladder; an id the client has no label for falls
 *  back to the id, which is ugly but true. */
function profileLabel(id: string): string {
  switch (id) {
    case "guarded":
      return "Guarded";
    case "read-only":
      return "Read-only";
    case "trusted":
      return "Trusted";
    case "unrestricted":
      return "Unrestricted";
    case CUSTOM_PROFILE:
      return "Custom";
    default:
      return id;
  }
}

/** What each profile actually grants, in the terms a reader decides on.
 *
 *  Read-only names where it reaches on purpose. It grants reads OUTSIDE the
 *  workspace, so an SSH key or a sibling project is readable without a prompt, and
 *  a description that said only "reads" would be hiding the part that matters.
 *
 *  Every named rung also says whether a WORKFLOW STEP is covered, because that is
 *  the one place the two halves of a profile differ. A profile's presets ride the
 *  session door and reach only the sessions vibekit opens; Kiro creates a workflow
 *  step's session itself, so a preset never arrives there. Only the loosest rung
 *  also writes a durable rule to the user-scope permissions file, which is the half
 *  a step session does read — and that rule outlives a restart and applies to every
 *  Kiro client on this machine, so it is stated rather than implied.
 *
 *  An UNCOVERED step is not a step that asks for everything, and saying so was this
 *  text's own version of the defect it exists to fix. A step session carries the
 *  bundled agent's policy: workspace reads are allowed there (measured — 123
 *  read_file and 46 grep_search allows in the same logs that produced the 145 asks)
 *  plus a small read-only command allowlist. So what an uncovered step actually
 *  asks for is writes and anything outside that allowlist, and that is what the
 *  three restrictive rungs say.
 *
 *  CUSTOM does cover a step session, and its claim is narrowed to USER scope for
 *  the same reason the rest of this text was rewritten. Only user scope was
 *  measured against a step session; the rule-add form's scope select defaults to
 *  WORKSPACE, and while KAS loads both files process-wide and may well evaluate a
 *  workspace rule for a step too, "may well" is an inference. Promising coverage
 *  for the scope a user's rules land in by default, on an inference, would be this
 *  text's own defect pointed at the one rung the user authors by hand.
 *
 *  SUBAGENTS are covered on every rung, so no rung claims them: invoke_sub_agent
 *  creates no session, so a subagent rides its parent's session id and inherits
 *  whatever the parent was seeded with. Only STEP sessions were ever uncovered.
 *
 *  The step-coverage sentence duplicates a SERVER fact the wire does not carry —
 *  which rungs hold policyfile.Profile.FileRules, where vibekit.SecurityProfile
 *  ships id and presets only. The Go side owns the guard:
 *  TestProfiles_OnlyTheLoosestRungWritesFileRules tables all five rungs against a
 *  wantRules boolean, so a rung gaining file rules turns it red, and that field's
 *  doc comment points back at this function. The two move together.
 *
 *  A description promising a posture the code does not deliver is the defect this
 *  text was rewritten to remove; one that under-delivers, or that oversells what a
 *  restrictive rung withholds, is the same defect pointed the other way. */
function profileDescription(id: string): string {
  switch (id) {
    case "guarded":
      return "Reads files in this workspace. Everything else asks. A workflow step is not covered: it reads this workspace and asks before writing a file or running anything but a few read-only commands.";
    case "read-only":
      return "Also reads any file on this machine, including outside the workspace, runs read-only commands, and reaches the web. Every change asks. A workflow step is not covered: it reads this workspace and asks before writing a file or running anything but a few read-only commands.";
    case "trusted":
      return "Also edits files in this workspace and runs everyday development commands. Destructive and irreversible ones still ask, including git push, reset and clean. A workflow step is not covered: it reads this workspace and asks before writing a file or running anything but a few read-only commands.";
    case "unrestricted":
      return "Never asks, including before installing a power, and the only preset profile that also covers workflow steps. It writes a durable allow rule to your user permissions file, so it survives a restart and applies to every Kiro client on this machine until you pick another profile. Kiro still protects its own settings and still asks before writing .git, .kiro/agents and .kiro/hooks.";
    case CUSTOM_PROFILE:
      return "Your own rules, edited in the table below. Nothing is granted that you do not add, and a rule you add at user scope covers workflow steps too.";
    default:
      return "";
  }
}

function shortSource(src: string): string {
  if (src === "") {
    return "";
  }
  const parts = src.split("/");
  return parts.length <= 2 ? src : ".../" + parts.slice(-2).join("/");
}

class NativePolicyController {
  private writable = new Set<string>();
  private ctrl: AbortController | null = null;
  /** The profile ladder and the id in force, both straight from the policy view.
   *  Never local constants: the ladder decides what one click grants, and
   *  policyfile owns it.
   *
   *  activeProfile is the SERVER's answer rather than the picker's selection, so a
   *  selection that failed leaves the picker showing what is actually in force. */
  private profiles: SecurityProfile[] = [];
  private activeProfile = "";
  /** A transient line under the picker: the outcome of a selection, or a note that
   *  Custom is empty. Carried across the refetch a selection ends in, because that
   *  refetch repaints this same line. */
  private profileNote = "";
  private profileNoteIsError = false;
  /** The rules the last completed read-back reported, kept so the picker's own line
   *  can be repainted without spending another request on the bridge. */
  private lastRules: PolicyRule[] = [];

  init(): void {
    const addBtn = maybeEl<HTMLButtonElement>("native-rule-add");
    if (addBtn === null) {
      return; // permissions panel not present in this build
    }
    addBtn.addEventListener("click", () => {
      void this.addRule();
    });
    maybeEl<HTMLButtonElement>("security-profile-customize")?.addEventListener("click", () => {
      void this.customize();
    });
    maybeEl<HTMLButtonElement>("native-explain-run")?.addEventListener("click", () => {
      void this.runExplain();
    });
    registerCleanup(bindLoadingState("permissions.edit_native_rule", addBtn));
    registerCleanup(
      onSSE("permissions_changed", () => {
        void this.load();
      }),
    );
    registerCleanup(
      onSSE("policy_error", (_chatID, payload) => {
        const msgs = (payload.errors ?? []).map((e) => e.message).filter((m) => m !== "");
        this.showStatus(
          msgs.length > 0 ? "Policy error: " + msgs.join("; ") : "Policy error.",
          true,
        );
      }),
    );
    registerCleanup(() => {
      this.cancel();
    });
    // The initial fetch is deliberately NOT fired here (B2): init() runs
    // during boot (restoreAll, before the auth check), and /api/permissions
    // is utility-bridge-backed — an expensive fetch for a panel that isn't
    // visible. refresh() fires on the Permissions tab's first activation
    // instead (settings-tabs loader map, wired in settings.ts); the
    // permissions_changed SSE registered above keeps it fresh afterwards.
  }

  /** Fetch + render the policy view. Public for the lazy tab loader. */
  refresh(): void {
    void this.load();
  }

  cancel(): void {
    this.ctrl?.abort();
    this.ctrl = null;
  }

  private async load(): Promise<void> {
    this.ctrl?.abort();
    this.ctrl = new AbortController();
    const { signal } = this.ctrl;
    const data = await apiGet<PolicyView>("/api/permissions", signal);
    if (signal.aborted || data === null) {
      return;
    }
    // A completed read-back supersedes any note a past selection left. Without
    // this, a failure message outlived the thing it described and got repainted by
    // every later refetch, including ones triggered by another device.
    this.profileNote = "";
    this.profileNoteIsError = false;
    this.writable = new Set(data.writable_scopes);
    this.profiles = data.profiles;
    this.activeProfile = data.profile;
    this.lastRules = data.rules;
    this.populateCapabilities(data.capabilities);
    if (data.available) {
      this.showStatus("", false);
    } else {
      this.showStatus(
        "Live policy unavailable (no active session) — showing your saved user/workspace rules only.",
        false,
      );
    }
    this.render(data.rules);
    this.renderProfiles();
    // AFTER render(), which rebuilds the rows this locks: locking first would
    // disable controls that are about to be replaced by fresh, enabled ones.
    this.renderProfileState(data.rules);
  }

  // --- The security profile --------------------------------------------------
  //
  // The profile is the policy. Selecting one writes that profile's own rules into
  // the user permissions file server-side and takes the previous profile's back
  // out, so the table below describes what is in force rather than competing with
  // it, and outside Custom the table is read-only for that reason: with a profile
  // in charge, a hand-edit would be a second writer of one posture and the first
  // thing to disagree with the picker.
  //
  // It MERGES rather than replaces: a rule the user authored is not the profile
  // mechanism's to delete, so it survives the switch and keeps applying. That is
  // what the leaving-Custom confirm now warns about — a surviving grant, not a
  // deletion.
  //
  // Two doors into Custom and they differ in where the table starts. Customize
  // materialises the profile in force as a baseline to tweak; picking Custom from
  // the list adds nothing and leaves whatever is already there. Both go through one
  // endpoint, distinguished by `seed`.

  /** Render the picker. One radio per profile, its own description under the label,
   *  because what separates two of them is a sentence rather than a word.
   *
   *  Rebuilt on every load rather than reconciled: five rows with no state of their
   *  own beyond `checked`, so a keyed reconcile would be machinery for nothing. */
  private renderProfiles(): void {
    const host = maybeEl("security-profile-list");
    if (host === null) {
      return;
    }
    const rows = this.profiles.map((p, i) => {
      const input = el("input", { type: "radio", className: "" }) as HTMLInputElement;
      input.type = "radio";
      input.name = "security-profile";
      input.value = p.id;
      input.checked = p.id === this.activeProfile;
      input.addEventListener("change", () => {
        if (input.checked) {
          void this.selectProfile(p.id);
        }
      });
      const row = el("label", { className: "perm-mode profile-row" }, input);
      // The loosest rung is last in the ladder, and the ladder's ORDER is the
      // server's. Deriving "loosest" from the position rather than from the id is
      // what keeps this from hardcoding a profile name the server owns.
      if (i === this.profiles.length - 1 - 1) {
        row.classList.add("profile-row-loosest");
      }
      row.append(el("span", {}, profileLabel(p.id)));
      row.append(el("p", { className: "section-hint profile-desc" }, profileDescription(p.id)));
      return row;
    });
    host.replaceChildren(...rows);
  }

  /** Paint the Customize button and the status line, and lock the table outside
   *  Custom.
   *
   *  The button is present only on a named profile, because on Custom you are
   *  already there and a control that does nothing teaches a reader to distrust
   *  every other one. */
  private renderProfileState(rules: PolicyRule[]): void {
    const custom = this.activeProfile === CUSTOM_PROFILE;
    maybeEl("security-profile-customize")?.classList.toggle("hidden", custom);
    this.lockPolicyTable(!custom);
    const status = maybeEl("security-profile-status");
    if (status === null) {
      return;
    }
    let text = this.profileNote;
    if (text === "" && custom && rules.filter((r) => this.writable.has(r.scope)).length === 0) {
      // An empty Custom policy is not a neutral state and the picker has to say so:
      // Custom sends no presets, so with no rules of its own the agent asks before
      // it may even read a file. Saying nothing here would leave that to be
      // discovered one prompt at a time.
      text =
        "Custom, with no rules. Every capability asks, including reading a file. " +
        "Add rules below, or pick a profile to start from one.";
    }
    status.textContent = text;
    status.classList.toggle("hidden", text === "");
    status.classList.toggle("native-policy-status-error", this.profileNoteIsError);
  }

  /** Disable every editing affordance in the Active policy table.
   *
   *  Genuinely disabled rather than only dimmed: a `pointer-events: none` would
   *  leave the controls in the tab order and reachable by keyboard, which is the
   *  version of this that looks locked and is not. */
  private lockPolicyTable(locked: boolean): void {
    maybeEl("native-policy-section")?.classList.toggle("native-policy-locked", locked);
    const scope = maybeEl("native-policy-section");
    if (scope === null) {
      return;
    }
    for (const control of scope.querySelectorAll<
      HTMLInputElement | HTMLButtonElement | HTMLSelectElement
    >(
      "#native-policy-list select, #native-policy-list button, [data-rule-form='add'] input, [data-rule-form='add'] select, [data-rule-form='add'] button",
    )) {
      control.disabled = locked;
    }
  }

  /** Select a profile. The server merges rather than replaces, so the rules the
   *  user authored SURVIVE the switch — and the confirm says that, because a grant
   *  outliving the posture change that was supposed to narrow it is the surprise
   *  this screen can produce.
   *
   *  Reverting the radio on cancel is not enough on its own — the paint comes from
   *  the server either way — but it stops the picker showing a selection that never
   *  happened for the duration of the round trip. */
  private async selectProfile(id: string): Promise<void> {
    if (id === this.activeProfile) {
      return;
    }
    this.profileNote = "";
    this.profileNoteIsError = false;
    const leavingCustom = this.activeProfile === CUSTOM_PROFILE;
    const editable = this.lastRules.filter((r) => this.writable.has(r.scope)).length;
    if (leavingCustom && editable > 0) {
      const ok = await confirm(
        `Switch to ${profileLabel(id)}? Your ${String(editable)} custom ` +
          `${editable === 1 ? "rule STAYS" : "rules STAY"} on disk and ${editable === 1 ? "keeps" : "keep"} ` +
          `applying alongside that profile, so the agent can end up with more than its name suggests. ` +
          `Remove them in the table first if that is not what you want.`,
        "Switch anyway",
        "destructive",
      );
      if (!ok) {
        void this.load();
        return;
      }
    }
    if (id === LOOSEST_PROFILE_HINT && !(await this.confirmLoosest())) {
      void this.load();
      return;
    }
    await this.applyProfile(id, false);
  }

  /** The extra confirm the loosest profile earns. It is the one that grants
   *  `power`, so a power installed afterwards runs its author's code at this
   *  privilege with nothing asking, and it is also the one whose name invites a
   *  click. It states what it cannot do as well, because a profile that says it
   *  allows everything and then prompts reads as broken rather than as bounded. */
  private confirmLoosest(): Promise<boolean> {
    return confirm(
      `Allow every capability without asking? This includes "power", so a power you ` +
        `install runs its author's code at your privilege with no prompt. It does NOT ` +
        `silence every prompt: Kiro still refuses writes to its own settings ` +
        `directories and still asks before writing .git, .kiro/agents, .kiro/hooks ` +
        `and .vscode.`,
      "Allow everything",
      "destructive",
    );
  }

  /** Copy the profile in force into the editable table and switch to Custom. The
   *  starting-point door, as opposed to the blank one. */
  private async customize(): Promise<void> {
    this.profileNote = "";
    this.profileNoteIsError = false;
    await this.applyProfile(CUSTOM_PROFILE, true);
  }

  /** One writer for both doors. The button is disabled for the round trip so a
   *  second selection cannot interleave with the file replacement this one is
   *  performing. */
  private async applyProfile(id: string, seed: boolean): Promise<void> {
    const btn = maybeEl<HTMLButtonElement>("security-profile-customize");
    if (btn !== null) {
      btn.disabled = true;
    }
    try {
      const res = await setSecurityProfile.dispatch({ profile: id, seed });
      // Repaint from the server FIRST, so the picker shows what is actually in
      // force, then write the failure over it. The other order loses the message:
      // load() clears the note by design, so a note set before it never survives.
      await this.load();
      if (res === null || res.error !== undefined) {
        this.profileNote = res?.error ?? "The profile was not changed.";
        this.profileNoteIsError = true;
        this.renderProfileState(this.lastRules);
      }
    } finally {
      if (btn !== null) {
        btn.disabled = false;
      }
    }
  }

  private populateCapabilities(caps: string[]): void {
    for (const id of ["native-rule-capability", "native-explain-capability"]) {
      const sel = maybeEl<HTMLSelectElement>(id);
      // Populate only when empty: idempotent across refetches (so an SSE
      // reload doesn't reset an in-progress selection) and correct per fresh
      // DOM in tests.
      if (sel === null || sel.options.length > 0) {
        continue;
      }
      sel.replaceChildren(
        ...caps.map((c) => {
          const opt = el("option", { value: c }, c) as HTMLOptionElement;
          opt.value = c; // set the property explicitly so the value is usable everywhere
          return opt;
        }),
      );
    }
  }

  private render(rules: PolicyRule[]): void {
    const emptyHint = maybeEl("native-policy-empty-hint");
    if (emptyHint !== null) {
      emptyHint.classList.toggle("hidden", rules.length > 0);
    }
    const list = maybeEl("native-policy-list");
    if (list === null) {
      return;
    }
    const groups = new Map<string, PolicyRule[]>();
    for (const r of rules) {
      const g = groups.get(r.scope) ?? [];
      g.push(r);
      groups.set(r.scope, g);
    }
    const order = [
      ...NATIVE_SCOPE_ORDER,
      ...[...groups.keys()].filter((s) => !NATIVE_SCOPE_ORDER.includes(s)),
    ];
    const frag: HTMLElement[] = [];
    for (const scope of order) {
      const grp = groups.get(scope);
      if (grp === undefined || grp.length === 0) {
        continue;
      }
      frag.push(
        el("div", { className: "native-policy-scope-label" }, NATIVE_SCOPE_LABEL[scope] ?? scope),
      );
      for (const r of grp) {
        frag.push(this.ruleRow(r));
      }
    }
    list.replaceChildren(...frag);
  }

  private ruleRow(r: PolicyRule): HTMLElement {
    const row = el("div", { className: `native-rule native-rule-${r.effect}` });
    if (this.writable.has(r.scope)) {
      row.append(this.effectSelect(r));
    } else {
      row.append(el("span", { className: `native-rule-effect eff-${r.effect}` }, r.effect));
    }
    row.append(el("span", { className: "native-rule-cap mono" }, r.capability));
    const globs = [
      ...(r.match ?? []).map((m) => "+" + m),
      ...(r.exclude ?? []).map((e) => "\u2212" + e),
    ];
    if (globs.length > 0) {
      row.append(el("span", { className: "native-rule-globs mono" }, globs.join("  ")));
    }
    const src = el("span", { className: "native-rule-src" }, shortSource(r.source));
    if (r.source !== "") {
      src.setAttribute("title", r.source);
    }
    row.append(src);
    if (this.writable.has(r.scope)) {
      const rm = el("button", { type: "button", className: "native-rule-remove" }, "\u00d7");
      rm.setAttribute("aria-label", "Remove rule");
      rm.setAttribute("title", "Remove rule");
      rm.addEventListener("click", () => {
        void this.removeRule(r);
      });
      row.append(rm);
    }
    return row;
  }

  /** Build the in-place effect editor for a writable rule: a select styled
   *  as the effect badge. A widening change (deny→ask, deny→allow,
   *  ask→allow) grants the agent more than it had, so it is confirmed
   *  first — same guardrail as removing a deny. The select reverts on
   *  cancel or a failed write; a successful write refetches the view. */
  private effectSelect(r: PolicyRule): HTMLSelectElement {
    const sel = el("select", {
      className: `native-rule-effect eff-${r.effect}`,
    }) as HTMLSelectElement;
    for (const eff of ["allow", "ask", "deny"]) {
      const opt = el("option", { value: eff }, eff) as HTMLOptionElement;
      opt.value = eff;
      opt.selected = eff === r.effect;
      sel.append(opt);
    }
    sel.setAttribute("aria-label", `Effect for the ${r.capability} rule`);
    sel.setAttribute("title", "Change this rule's effect");
    sel.addEventListener("change", () => {
      void this.updateEffect(r, sel);
    });
    return sel;
  }

  private async updateEffect(r: PolicyRule, sel: HTMLSelectElement): Promise<void> {
    const next = sel.value;
    if (next === r.effect) {
      return;
    }
    const RANK: Record<string, number> = { deny: 2, ask: 1, allow: 0 };
    let confirmFlag = false;
    if ((RANK[next] ?? 0) < (RANK[r.effect] ?? 0)) {
      const ok = await confirm(
        `Change this "${r.capability}" rule from ${r.effect} to ${next}? That WIDENS what the agent is allowed to do.`,
        "Widen rule",
      );
      if (!ok) {
        sel.value = r.effect;
        return;
      }
      confirmFlag = true;
    }
    const res = await editNativeRule.dispatch({
      op: "update",
      scope: r.scope as "user" | "workspace",
      capability: r.capability,
      effect: r.effect,
      new_effect: next,
      match: r.match ?? [],
      exclude: r.exclude ?? [],
      confirm: confirmFlag,
    });
    if (res !== null && res.error === undefined) {
      void this.load();
    } else {
      sel.value = r.effect;
    }
  }

  private async addRule(): Promise<void> {
    const scope = (maybeEl<HTMLSelectElement>("native-rule-scope")?.value ?? "workspace") as
      "user" | "workspace";
    const capability = maybeEl<HTMLSelectElement>("native-rule-capability")?.value ?? "";
    const effect = maybeEl<HTMLSelectElement>("native-rule-effect")?.value ?? "ask";
    const match = splitGlobs(maybeEl<HTMLInputElement>("native-rule-match")?.value ?? "");
    const exclude = splitGlobs(maybeEl<HTMLInputElement>("native-rule-exclude")?.value ?? "");
    if (capability === "") {
      return;
    }
    const res = await editNativeRule.dispatch({
      op: "add",
      scope,
      capability,
      effect,
      match,
      exclude,
    });
    if (res !== null && res.error === undefined) {
      const mi = maybeEl<HTMLInputElement>("native-rule-match");
      const xi = maybeEl<HTMLInputElement>("native-rule-exclude");
      if (xi !== null) {
        xi.value = "";
      }
      if (mi !== null) {
        mi.value = "";
        // Refocus for repeat entry (adding several rules in a row).
        mi.focus();
      }
      void this.load();
    }
  }

  private async removeRule(r: PolicyRule): Promise<void> {
    const scope = r.scope as "user" | "workspace";
    let confirmFlag = false;
    if (r.effect === "deny") {
      const ok = await confirm(
        `Remove this deny rule for "${r.capability}"? That WIDENS what the agent is allowed to do.`,
        "Remove deny rule",
      );
      if (!ok) {
        return;
      }
      confirmFlag = true;
    }
    const res = await editNativeRule.dispatch({
      op: "remove",
      scope,
      capability: r.capability,
      effect: r.effect,
      match: r.match ?? [],
      exclude: r.exclude ?? [],
      confirm: confirmFlag,
    });
    if (res !== null && res.error === undefined) {
      void this.load();
    }
  }

  private async runExplain(): Promise<void> {
    const capability = maybeEl<HTMLSelectElement>("native-explain-capability")?.value ?? "";
    const resource = (maybeEl<HTMLInputElement>("native-explain-resource")?.value ?? "").trim();
    const out = maybeEl("native-explain-result");
    if (capability === "" || out === null) {
      return;
    }
    // Shell decisions are always resource-scoped (there is no
    // command-independent shell decision to simulate).
    if (capability === "shell" && resource === "") {
      out.textContent = "Enter a command to test the shell capability.";
      return;
    }
    const res = await explainPolicy.dispatch({ capability, resource });
    if (res === null) {
      out.textContent = "Could not evaluate — check that a chat session is active.";
      return;
    }
    const parts = [`Effect: ${res.effect}`];
    if (res.scope !== undefined && res.scope !== "") {
      parts.push(`scope: ${res.scope}`);
    }
    if (res.source !== undefined && res.source !== "") {
      parts.push(`source: ${shortSource(res.source)}`);
    }
    out.textContent = parts.join(" \u00b7 ");
  }

  private showStatus(text: string, isError: boolean): void {
    const s = maybeEl("native-policy-status");
    if (s === null) {
      return;
    }
    s.textContent = text;
    s.classList.toggle("hidden", text === "");
    s.classList.toggle("native-policy-status-error", isError);
  }
}

const nativePolicy = new NativePolicyController();

// Public delegate functions preserving the existing module API.
export function initPermissionsUI(initial: EffectiveSettings): void {
  controller.initPermissions(initial);
}

/** Initialise the native Cedar policy view + editor: wires the add-rule /
 *  explain controls and the permissions_changed SSE refetch. Does NOT fetch
 *  GET /api/permissions — the initial load is lazy (loadNativePolicy). */
export function initNativePolicyUI(): void {
  nativePolicy.init();
}

/** Load the native policy view. Wired to the Permissions tab's first
 *  activation (settings-tabs loader map) instead of boot, so the
 *  bridge-backed /api/permissions endpoint isn't hit for an invisible
 *  panel. Safe to call repeatedly (in-flight loads are aborted). */
export function loadNativePolicy(): void {
  nativePolicy.refresh();
}
