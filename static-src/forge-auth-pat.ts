// ---------------------------------------------------------------------------
// PAT (Personal Access Token) paste form. Works for all forge kinds;
// the backend LoginWithPAT endpoint is kind-agnostic.
// Extracted from forge-auth.ts.
// ---------------------------------------------------------------------------

import type { ForgeKind } from "./wire/types.gen.js";
import { HOST_LOCKED_KINDS, DEFAULT_HOST } from "./forge-types.js";
import { connectPAT } from "./actions/forge.js";
import { bindLoadingState } from "./actions/index.js";
import { el } from "@cplieger/reactive";

type PATHelp = { kind: "link"; url: string; label: string } | { kind: "text"; text: string };

const PAT_HELP: Record<ForgeKind, PATHelp> = {
  github: {
    kind: "link",
    url: "https://github.com/settings/tokens?type=beta",
    label: "Create a GitHub fine-grained token",
  },
  gitlab: {
    kind: "link",
    url: "https://gitlab.com/-/profile/personal_access_tokens?scopes=api,read_repository,write_repository",
    label: "Create a GitLab token",
  },
  codeberg: {
    kind: "link",
    url: "https://codeberg.org/user/settings/applications",
    label: "Create a Codeberg token",
  },
  gitea: {
    kind: "text",
    text: "Create a token at /user/settings/applications on your Gitea or Forgejo host.",
  },
};

export interface PATFormDeps {
  /** Host placeholder for the given kind. */
  hostPlaceholder: (kind: ForgeKind) => string;
  /** Close the add-account slot. */
  closeSlot: (slot: HTMLElement) => void;
  /** Register a patForm unbind for cleanup. */
  addPatFormUnbind: (fn: () => void) => void;
  /** Mark a forge ID for expansion on next paint. */
  expandOnNextPaint: (id: string) => void;
  /** Trigger a full panel re-render. */
  renderForgesPanel: () => void;
}

export function renderPATForm(
  hostEl: HTMLElement,
  kind: ForgeKind,
  slot: HTMLElement,
  deps: PATFormDeps,
): void {
  hostEl.innerHTML = "";

  const help = PAT_HELP[kind];
  const helpEl = el("p", { className: "forge-help" });
  switch (help.kind) {
    case "link":
      helpEl.appendChild(
        el("a", { href: help.url, target: "_blank", rel: "noreferrer" }, help.label),
      );
      break;
    case "text":
      helpEl.textContent = help.text;
      break;
  }
  hostEl.appendChild(helpEl);

  const form = el("form", { className: "forge-pat-form" });

  const hostLocked = HOST_LOCKED_KINDS.includes(kind);
  const hostInput = el("input") as HTMLInputElement;
  if (hostLocked) {
    hostInput.type = "hidden";
  } else {
    hostInput.type = "text";
    hostInput.placeholder = deps.hostPlaceholder(kind);
    hostInput.className = "tool-form-input";
    hostInput.required = true;
  }
  hostInput.value = DEFAULT_HOST[kind];
  form.appendChild(hostInput);

  const tokenInput = el("input", {
    type: "password",
    placeholder: "token",
    className: "tool-form-input",
    required: true,
  }) as HTMLInputElement;
  form.appendChild(tokenInput);

  const status = el("div", { className: "forge-card-status" });
  form.appendChild(status);

  const submit = el(
    "button",
    { type: "submit", className: "btn-small btn-primary" },
    "Connect",
  ) as HTMLButtonElement;
  form.appendChild(submit);

  const cancel = el("button", { type: "button", className: "btn-small" }, "Cancel");

  const unbindLoading = bindLoadingState("forge.connect_pat", submit);
  deps.addPatFormUnbind(unbindLoading);
  cancel.addEventListener("click", () => {
    unbindLoading();
    deps.closeSlot(slot);
  });
  form.appendChild(cancel);

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    const hostVal = hostInput.value.trim();
    void doPATConnect(kind, hostVal, tokenInput.value.trim(), status, () => {
      unbindLoading();
      tokenInput.value = "";
      deps.closeSlot(slot);
      deps.expandOnNextPaint(`${kind}:${hostVal}`);
      deps.renderForgesPanel();
    });
  });

  hostEl.appendChild(form);
}

async function doPATConnect(
  kind: ForgeKind,
  host: string,
  token: string,
  status: HTMLElement,
  onSuccess: () => void,
): Promise<void> {
  if (host === "" || token === "") {
    status.textContent = "Both host and token are required.";
    status.className = "forge-card-status err";
    return;
  }
  status.textContent = "Validating…";
  status.className = "forge-card-status";
  // Typed outcome: the framework's error toast is suppressed (error: false),
  // so this status line is the only failure surface — show the real reason
  // (timeout vs network vs HTTP status) instead of a blanket "Network error.",
  // and stay quiet on a cancelled dispatch.
  const o = await connectPAT.dispatch({ kind, host, token }).outcome;
  if (o.status === "cancelled") {
    status.textContent = "";
    status.className = "forge-card-status";
    return;
  }
  if (o.status === "error") {
    status.textContent = o.error.message;
    status.className = "forge-card-status err";
    return;
  }
  const res = o.value;
  if (res.error !== undefined) {
    status.textContent = res.error;
    status.className = "forge-card-status err";
    return;
  }
  status.textContent = "Connected.";
  status.className = "forge-card-status ok";
  onSuccess();
}
