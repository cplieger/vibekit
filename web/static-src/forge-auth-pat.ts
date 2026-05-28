// ---------------------------------------------------------------------------
// PAT (Personal Access Token) paste form. Works for all forge kinds;
// the backend LoginWithPAT endpoint is kind-agnostic.
// Extracted from forge-auth.ts.
// ---------------------------------------------------------------------------

import type { ForgeKind } from "./wire/types.gen.js";
import { HOST_LOCKED_KINDS, DEFAULT_HOST } from "./forge-types.js";
import { connectPAT } from "./actions/forge.js";
import { bindLoadingState } from "./actions/index.js";

const PAT_HELP_LINKS: Record<ForgeKind, { url: string; label: string } | null> = {
  github: {
    url: "https://github.com/settings/tokens?type=beta",
    label: "Create a GitHub fine-grained token",
  },
  gitlab: {
    url: "https://gitlab.com/-/profile/personal_access_tokens?scopes=api,read_repository,write_repository",
    label: "Create a GitLab token",
  },
  codeberg: {
    url: "https://codeberg.org/user/settings/applications",
    label: "Create a Codeberg token",
  },
  gitea: null,
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

  const helpLink = PAT_HELP_LINKS[kind];
  if (helpLink !== null) {
    const help = document.createElement("p");
    help.className = "forge-help";
    const a = document.createElement("a");
    a.href = helpLink.url;
    a.target = "_blank";
    a.rel = "noreferrer";
    a.textContent = helpLink.label;
    help.appendChild(a);
    hostEl.appendChild(help);
  } else if (kind === "gitea") {
    const help = document.createElement("p");
    help.className = "forge-help";
    help.textContent =
      "Create a token at /user/settings/applications on your Gitea or Forgejo host.";
    hostEl.appendChild(help);
  }

  const form = document.createElement("form");
  form.className = "forge-pat-form";

  const hostLocked = HOST_LOCKED_KINDS.includes(kind);
  const hostInput = document.createElement("input");
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

  const tokenInput = document.createElement("input");
  tokenInput.type = "password";
  tokenInput.placeholder = "token";
  tokenInput.className = "tool-form-input";
  tokenInput.required = true;
  form.appendChild(tokenInput);

  const status = document.createElement("div");
  status.className = "forge-card-status";
  form.appendChild(status);

  const submit = document.createElement("button");
  submit.type = "submit";
  submit.className = "btn-small btn-primary";
  submit.textContent = "Connect";
  form.appendChild(submit);

  const cancel = document.createElement("button");
  cancel.type = "button";
  cancel.className = "btn-small";
  cancel.textContent = "Cancel";

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
  const res = await connectPAT.dispatch({ kind, host, token });
  if (res === null) {
    status.textContent = "Network error.";
    status.className = "forge-card-status err";
    return;
  }
  if (res.error !== undefined) {
    status.textContent = res.error;
    status.className = "forge-card-status err";
    return;
  }
  status.textContent = "Connected.";
  status.className = "forge-card-status ok";
  onSuccess();
}
