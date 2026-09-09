// ---------------------------------------------------------------------------
// Elicitation card: an MCP server is requesting structured input mid-tool-call
// (MCP elicitation, forwarded by kiro-cli over ACP). Rendered in the
// interaction dock, which owns the queue and the settle-once guard.
//
// Renders a form from the request's JSON-schema-shaped `requested_schema`
// (form mode) or an "open link" affordance (url mode), collects the answer,
// and reports {action, content} back to the dock.
//
// It was a centered <dialog> with a backdrop and a focus trap. Both are gone:
// a form asking about a tool call belongs beside the transcript that explains
// why it is being asked, and trapping focus in a non-modal region prevents the
// user from going to read that transcript.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import type { ElicitationNeededPayload, ElicitationPropertySchema } from "./types.js";

type ElicitAction = "accept" | "decline" | "cancel";
type SubmitFn = (action: ElicitAction, content?: Record<string, unknown>) => void;

// Reading inputs back: each rendered field registers a getter that
// returns its current value, or `undefined` when left empty (so optional
// fields are omitted from the content object rather than sent as "").
type FieldReader = () => { name: string; value: unknown; filled: boolean };

/** Build the dock card for one elicitation request. */
export function buildElicitationCard(
  payload: ElicitationNeededPayload,
  onSubmit: SubmitFn,
): HTMLElement {
  const body = el("div", { className: "elicitation-body" });
  const fieldsEl = el("div", { className: "elicitation-fields" });
  const actions = el("div", { className: "elicitation-actions" });

  body.appendChild(
    el(
      "strong",
      null,
      payload.message !== undefined && payload.message !== "" ? payload.message : "Input requested",
    ),
  );

  const isURL = payload.mode === "url" && payload.url !== undefined && payload.url !== "";
  const readers: FieldReader[] = [];

  if (isURL) {
    body.appendChild(
      el(
        "a",
        {
          className: "elicitation-url btn-small confirm-allow",
          href: payload.url ?? "",
          target: "_blank",
          rel: "noopener noreferrer",
        },
        "Open link\u2026",
      ),
    );
  } else {
    const schema = payload.requested_schema;
    const required = new Set(schema?.required ?? []);
    const props = schema?.properties ?? {};
    for (const name of Object.keys(props)) {
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      readers.push(renderField(fieldsEl, name, props[name]!, required.has(name)));
    }
  }

  const submitBtn = el(
    "button",
    { type: "button", className: "btn-small confirm-allow" },
    isURL ? "Done" : "Submit",
  );
  submitBtn.addEventListener("click", () => {
    const content = isURL ? undefined : collect(readers, fieldsEl);
    if (!isURL && content === null) {
      return; // a required field is empty; collect marked it.
    }
    onSubmit("accept", content ?? undefined);
  });

  const declineBtn = el(
    "button",
    { type: "button", className: "btn-small confirm-danger" },
    "Decline",
  );
  declineBtn.addEventListener("click", () => {
    onSubmit("decline");
  });

  actions.append(submitBtn, declineBtn);

  return el("div", { className: "dock-card dock-elicitation" }, body, fieldsEl, actions);
}

/** Render one form field and return a reader for its value. */
function renderField(
  container: HTMLElement,
  name: string,
  schema: ElicitationPropertySchema,
  required: boolean,
): FieldReader {
  const wrap = el("label", { className: "elicitation-field" });

  const labelText = el(
    "span",
    { className: "elicitation-label" },
    (schema.title !== undefined && schema.title !== "" ? schema.title : name) +
      (required ? " *" : ""),
  );
  wrap.appendChild(labelText);

  if (schema.description !== undefined && schema.description !== "") {
    const hint = el("span", { className: "elicitation-hint" }, schema.description);
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

/** Standard JSON-Schema `format` values that have a native input type, so the
 *  browser supplies the picker, the keyboard and the validation instead of the
 *  field being a bare text box. Note `date-time` is the schema spelling and
 *  `datetime-local` the HTML one.
 *
 *  `constrained` records whether the HTML type honours `pattern`, `minLength`
 *  and `maxLength`: per HTML those three apply to `text`, `search`, `url`,
 *  `tel`, `email` and `password` only, so a date picker SILENTLY IGNORES a
 *  constraint the schema stated. Marked per entry rather than tested by format
 *  string at the use site, so a new entry has to answer for itself.
 *
 *  A Map rather than an object literal because `format` is arbitrary text off
 *  the MCP wire: a record's prototype answers for `constructor` and friends, so
 *  `table[fmt] ?? "text"` would set a stringified function as the input type. */
const FORMAT_INPUT_TYPES = new Map<string, { type: string; constrained: boolean }>([
  ["email", { type: "email", constrained: true }],
  ["uri", { type: "url", constrained: true }],
  ["date", { type: "date", constrained: false }],
  ["date-time", { type: "datetime-local", constrained: false }],
]);

/** What a `datetime-local` control's value looks like: `YYYY-MM-DDTHH:mm`, with
 *  seconds (and a fraction of one) only where `step` asks for them. */
const LOCAL_DATE_TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}(:\d{2}(?:\.\d+)?)?$/;

/** One `datetime-local` value as an RFC 3339 `date-time`.
 *
 *  JSON Schema's `date-time` format IS RFC 3339 `date-time`, which requires
 *  seconds and an offset; the control's value carries neither, so answering with
 *  it verbatim answers a schema with a value invalid against it. `datetime-local`
 *  means local time by definition, so the local offset is the reading rather than
 *  an assumption — taken from the ENTERED instant, because a date the far side of
 *  a DST boundary has a different offset from today's.
 *
 *  `format: "date"` needs none of this: a date input's `YYYY-MM-DD` is already
 *  RFC 3339 `full-date`. */
function toRFC3339(value: string): string {
  const shape = LOCAL_DATE_TIME.exec(value);
  const at = new Date(value);
  // Anything this cannot read is answered with as typed, and the EMPTY control is
  // the case that reaches here: "" must stay "" rather than become this moment's
  // timestamp. The two conditions catch different inputs — "" is the wrong shape,
  // and an out-of-range field (`2026-13-01T10:00`, `2026-09-08T25:00`) passes the
  // regex's `\d{2}` and names no instant — and inventing one for either is worse
  // than passing the value on.
  if (shape === null || Number.isNaN(at.getTime())) {
    return value;
  }
  const withSeconds = shape[1] === undefined ? `${value}:00` : value;
  // getTimezoneOffset() is the minutes to ADD to local time to reach UTC, so it
  // is NEGATIVE east of UTC: UTC+02:00 reports -120 and renders "+02:00".
  const offset = at.getTimezoneOffset();
  const magnitude = Math.abs(offset);
  const hh = String(Math.floor(magnitude / 60)).padStart(2, "0");
  const mm = String(magnitude % 60).padStart(2, "0");
  return `${withSeconds}${offset <= 0 ? "+" : "-"}${hh}:${mm}`;
}

function buildControl(name: string, schema: ElicitationPropertySchema): Control {
  // Enum → <select>.
  if (schema.enum !== undefined && schema.enum.length > 0) {
    const sel = el(
      "select",
      { className: "elicitation-input", name },
      el("option", { value: "" }, "\u2014"),
    ) as HTMLSelectElement;
    for (const opt of schema.enum) {
      sel.appendChild(el("option", { value: opt }, opt));
    }
    if (typeof schema.default === "string") {
      sel.value = schema.default;
    }
    return { el: sel, read: () => ({ value: sel.value, filled: sel.value !== "" }) };
  }

  switch (schema.type) {
    case "boolean": {
      const box = el("input", {
        type: "checkbox",
        className: "elicitation-checkbox",
        name,
      }) as HTMLInputElement;
      if (schema.default === true) {
        box.checked = true;
      }
      // A checkbox is always "filled" (false is a valid answer).
      return { el: box, read: () => ({ value: box.checked, filled: true }) };
    }
    case "number":
    case "integer": {
      const inp = el("input", {
        type: "number",
        className: "elicitation-input",
        name,
      }) as HTMLInputElement;
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
      const inp = el("input", {
        type: "text",
        className: "elicitation-input",
        name,
        placeholder: "comma,separated,values",
      }) as HTMLInputElement;
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
      // `minLength: 0` is excluded: every string satisfies it, so it is not a
      // constraint to lose. `maxLength: 0` is, forbidding any input at all.
      const stated =
        (schema.pattern !== undefined && schema.pattern !== "") ||
        (typeof schema.minLength === "number" && schema.minLength > 0) ||
        typeof schema.maxLength === "number";
      const mapped = FORMAT_INPUT_TYPES.get(schema.format ?? "");
      // A stated constraint outranks the picker: on a type that ignores the
      // three text constraints they are dropped without a word, so a schema
      // pairing `format: "date"` with a `pattern` falls back to a text box and
      // keeps enforcing it. `email` and `uri` honour all three, so they keep
      // their native type in every case.
      const inp = el("input", {
        type: mapped === undefined || (stated && !mapped.constrained) ? "text" : mapped.type,
        className: "elicitation-input",
        name,
      }) as HTMLInputElement;
      if (schema.pattern !== undefined && schema.pattern !== "") {
        inp.pattern = schema.pattern;
      }
      if (typeof schema.minLength === "number") {
        inp.minLength = schema.minLength;
      }
      if (typeof schema.maxLength === "number") {
        inp.maxLength = schema.maxLength;
      }
      // HTML's value sanitization for `datetime-local` accepts no offset, so a
      // schema-valid RFC 3339 default (`2026-09-08T14:30:00Z`) is emptied by the
      // control and renders an empty picker. Same schema-value-versus-control-
      // value mismatch `toRFC3339` closes on the read side; open on this one.
      if (typeof schema.default === "string") {
        inp.value = schema.default;
      }
      return {
        el: inp,
        read: () => {
          const raw = inp.value;
          // The TYPE is read off the control rather than the mapping: a UA that
          // does not implement the picker reports `text`, and there the user
          // typed the whole string themselves.
          const value = inp.type === "datetime-local" ? toRFC3339(raw) : raw;
          return { value, filled: raw.trim() !== "" };
        },
      };
    }
  }
}

/** Collect all field values. Returns null (and marks the offending field)
 *  if a required field is empty; otherwise an object of filled values. */
function collect(readers: FieldReader[], container: HTMLElement): Record<string, unknown> | null {
  const required = new Set<string>();
  for (const labelEl of container.querySelectorAll<HTMLElement>(".elicitation-label")) {
    if (labelEl.textContent.endsWith(" *")) {
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
