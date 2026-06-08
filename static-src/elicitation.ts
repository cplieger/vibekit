// ---------------------------------------------------------------------------
// Elicitation dialog: shown when an MCP server requests structured input
// mid-tool-execution (MCP elicitation, forwarded by kiro-cli over ACP).
//
// Renders a form from the request's JSON-schema-shaped `requested_schema`
// (form mode) or an "open link" affordance (url mode), collects the
// answer, and reports {action, content} back to the caller. Mirrors the
// permission dialog's request/response shape; styling reuses the
// approval-dialog vocabulary.
// ---------------------------------------------------------------------------

import type { ElicitationNeededPayload, ElicitationPropertySchema } from "./types.js";
import { $ } from "./dom.js";
import { trapFocus } from "./focus-trap.js";

type ElicitAction = "accept" | "decline" | "cancel";
type SubmitFn = (action: ElicitAction, content?: Record<string, unknown>) => void;

// Resolved lazily: the dialog element only exists in the real app DOM,
// and accessing $.elicitationDialog throws if missing. Keeping the
// lookup out of module scope lets test files import this module (e.g.
// via handlers/turn.ts) without a DOM stub.
let cachedDialog: HTMLDialogElement | null = null;
function dlg(): HTMLDialogElement {
  cachedDialog ??= $.elicitationDialog;
  return cachedDialog;
}
// The request currently shown, so the SSE elicitation_complete handler
// can dismiss only the matching dialog (a stale completion for an
// already-answered request is a no-op).
let activeRequestID: number | null = null;
let activeSubmit: SubmitFn | null = null;
let answered = false;
let releaseFocus: (() => void) | null = null;

// Reading inputs back: each rendered field registers a getter that
// returns its current value, or `undefined` when left empty (so optional
// fields are omitted from the content object rather than sent as "").
type FieldReader = () => { name: string; value: unknown; filled: boolean };

export function showElicitationDialog(payload: ElicitationNeededPayload, onSubmit: SubmitFn): void {
  // Settle any dialog already open: it's being superseded, so cancel it.
  if (activeRequestID !== null && !answered) {
    finish("cancel");
  }

  activeRequestID = payload.request_id;
  activeSubmit = onSubmit;
  answered = false;

  const dialogEl = dlg();
  const form = dialogEl.querySelector(".elicitation-form") as HTMLFormElement;
  const body = dialogEl.querySelector(".elicitation-body") as HTMLDivElement;
  const fieldsEl = dialogEl.querySelector(".elicitation-fields") as HTMLDivElement;
  const actions = dialogEl.querySelector(".elicitation-actions") as HTMLDivElement;
  body.replaceChildren();
  fieldsEl.replaceChildren();
  actions.replaceChildren();

  const heading = document.createElement("strong");
  heading.textContent = payload.message !== undefined && payload.message !== "" ? payload.message : "Input requested";
  body.appendChild(heading);

  const isURL = payload.mode === "url" && payload.url !== undefined && payload.url !== "";
  const readers: FieldReader[] = [];

  if (isURL) {
    const link = document.createElement("a");
    link.className = "elicitation-url btn-small confirm-allow";
    link.href = payload.url ?? "";
    link.target = "_blank";
    link.rel = "noopener noreferrer";
    link.textContent = "Open link\u2026";
    body.appendChild(link);
  } else {
    const schema = payload.requested_schema;
    const required = new Set(schema?.required ?? []);
    const props = schema?.properties ?? {};
    for (const name of Object.keys(props)) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      const reader = renderField(fieldsEl, name, props[name]!, required.has(name));
      readers.push(reader);
    }
  }

  // --- action buttons ---
  const submitBtn = document.createElement("button");
  submitBtn.type = "button";
  submitBtn.className = "btn-small confirm-allow";
  submitBtn.textContent = isURL ? "Done" : "Submit";
  submitBtn.addEventListener("click", () => {
    const content = isURL ? undefined : collect(readers, fieldsEl);
    if (!isURL && content === null) {
      return; // a required field is empty; renderField marked it.
    }
    finish("accept", content ?? undefined);
  });

  const declineBtn = document.createElement("button");
  declineBtn.type = "button";
  declineBtn.className = "btn-small confirm-danger";
  declineBtn.textContent = "Decline";
  declineBtn.addEventListener("click", () => finish("decline"));

  actions.append(submitBtn, declineBtn);

  // Native <dialog> "cancel" (Escape) and "close" both settle as cancel
  // unless the user already submitted/declined.
  form.onsubmit = (e): void => e.preventDefault();
  dialogEl.oncancel = (e): void => {
    e.preventDefault();
    finish("cancel");
  };

  dialogEl.showModal();
  releaseFocus = trapFocus(dialogEl);
}

/** Dismiss the dialog for requestID without sending a response (upstream
 *  already cancelled). No-op if a different request is showing. */
export function dismissElicitation(requestID: number): void {
  if (activeRequestID !== requestID) {
    return;
  }
  answered = true; // suppress the cancel-on-close path; agent isn't waiting.
  closeDialog();
}

function finish(action: ElicitAction, content?: Record<string, unknown>): void {
  if (answered) {
    return;
  }
  answered = true;
  const submit = activeSubmit;
  closeDialog();
  submit?.(action, content);
}

function closeDialog(): void {
  releaseFocus?.();
  releaseFocus = null;
  activeSubmit = null;
  activeRequestID = null;
  const dialogEl = dlg();
  if (dialogEl.open) {
    dialogEl.close();
  }
}

/** Render one form field and return a reader for its value. */
function renderField(
  container: HTMLElement,
  name: string,
  schema: ElicitationPropertySchema,
  required: boolean,
): FieldReader {
  const wrap = document.createElement("label");
  wrap.className = "elicitation-field";

  const labelText = document.createElement("span");
  labelText.className = "elicitation-label";
  labelText.textContent = (schema.title !== undefined && schema.title !== "" ? schema.title : name) + (required ? " *" : "");
  wrap.appendChild(labelText);

  if (schema.description !== undefined && schema.description !== "") {
    const hint = document.createElement("span");
    hint.className = "elicitation-hint";
    hint.textContent = schema.description;
    wrap.appendChild(hint);
  }

  const control = buildControl(name, schema);
  wrap.appendChild(control.el);
  container.appendChild(wrap);

  return () => {
    const { value, filled } = control.read();
    return { name, value, filled };
  };
}

interface Control {
  el: HTMLElement;
  read: () => { value: unknown; filled: boolean };
}

function buildControl(name: string, schema: ElicitationPropertySchema): Control {
  // Enum → <select>.
  if (schema.enum !== undefined && schema.enum.length > 0) {
    const sel = document.createElement("select");
    sel.className = "elicitation-input";
    sel.name = name;
    const blank = document.createElement("option");
    blank.value = "";
    blank.textContent = "\u2014";
    sel.appendChild(blank);
    for (const opt of schema.enum) {
      const o = document.createElement("option");
      o.value = opt;
      o.textContent = opt;
      sel.appendChild(o);
    }
    if (typeof schema.default === "string") {
      sel.value = schema.default;
    }
    return { el: sel, read: () => ({ value: sel.value, filled: sel.value !== "" }) };
  }

  switch (schema.type) {
    case "boolean": {
      const box = document.createElement("input");
      box.type = "checkbox";
      box.className = "elicitation-checkbox";
      box.name = name;
      if (schema.default === true) {
        box.checked = true;
      }
      // A checkbox is always "filled" (false is a valid answer).
      return { el: box, read: () => ({ value: box.checked, filled: true }) };
    }
    case "number":
    case "integer": {
      const inp = document.createElement("input");
      inp.type = "number";
      inp.className = "elicitation-input";
      inp.name = name;
      if (schema.type === "integer") {
        inp.step = "1";
      }
      if (typeof schema.default === "number") {
        inp.value = String(schema.default);
      }
      return {
        el: inp,
        read: () => {
          if (inp.value === "") {
            return { value: undefined, filled: false };
          }
          const n = schema.type === "integer" ? parseInt(inp.value, 10) : parseFloat(inp.value);
          return { value: Number.isNaN(n) ? undefined : n, filled: !Number.isNaN(n) };
        },
      };
    }
    case "array": {
      // No structured items in the wire schema; accept comma-separated
      // values and emit a string[]. Empty → omitted.
      const inp = document.createElement("input");
      inp.type = "text";
      inp.className = "elicitation-input";
      inp.name = name;
      inp.placeholder = "comma,separated,values";
      return {
        el: inp,
        read: () => {
          const parts = inp.value
            .split(",")
            .map((s) => s.trim())
            .filter((s) => s !== "");
          return { value: parts, filled: parts.length > 0 };
        },
      };
    }
    default: {
      const inp = document.createElement("input");
      inp.type = schema.format === "email" ? "email" : schema.format === "uri" ? "url" : "text";
      inp.className = "elicitation-input";
      inp.name = name;
      if (schema.pattern !== undefined && schema.pattern !== "") {
        inp.pattern = schema.pattern;
      }
      if (typeof schema.minLength === "number") {
        inp.minLength = schema.minLength;
      }
      if (typeof schema.maxLength === "number") {
        inp.maxLength = schema.maxLength;
      }
      if (typeof schema.default === "string") {
        inp.value = schema.default;
      }
      return { el: inp, read: () => ({ value: inp.value, filled: inp.value.trim() !== "" }) };
    }
  }
}

/** Collect all field values. Returns null (and marks the offending field)
 *  if a required field is empty; otherwise an object of filled values. */
function collect(readers: FieldReader[], container: HTMLElement): Record<string, unknown> | null {
  const required = new Set<string>();
  for (const labelEl of container.querySelectorAll<HTMLElement>(".elicitation-label")) {
    if (labelEl.textContent?.endsWith(" *") === true) {
      // strip the trailing " *" and recover the field name via its input
      const input = labelEl.parentElement?.querySelector<HTMLElement>("[name]");
      const n = input?.getAttribute("name");
      if (n !== null && n !== undefined) {
        required.add(n);
      }
    }
  }

  const out: Record<string, unknown> = {};
  let missing: HTMLElement | null = null;
  for (const read of readers) {
    const { name, value, filled } = read();
    if (!filled) {
      if (required.has(name) && missing === null) {
        missing = container.querySelector<HTMLElement>(`[name="${CSS.escape(name)}"]`);
      }
      continue;
    }
    out[name] = value;
  }

  if (missing !== null) {
    missing.classList.add("elicitation-invalid");
    missing.focus();
    return null;
  }
  return out;
}
