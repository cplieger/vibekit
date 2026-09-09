// `uploadFiles`'s progress reporting: what the native <progress> is told, and
// what the container is no longer told.
//
// TWO THINGS THIS PINS THAT NOTHING ELSE DOES. The ARIA fix: the container used to
// carry `role="progressbar"` plus every `aria-*`, and per ARIA a progressbar's
// children are presentational — so the Cancel button sitting inside it was
// flattened out of the accessibility tree and the only way to stop an upload was
// unreachable. And the three-state value path: determinate assigns a value,
// indeterminate REMOVES the attribute (the native indeterminate rendering, which
// `progress-bar-css.test.ts` states as its premise), and a later determinate tick
// puts one back.
//
// `upload.ts` is excluded from the coverage config because its shell is an XHR
// against a real server; that is about the NUMBER, not about testability. A fake
// XMLHttpRequest reaches every branch below, and these assertions would go red on
// a revert of either half.

import { vi, describe, it, expect, beforeEach, afterEach } from "vitest";

vi.mock("./actions/index.js", () => ({
  hasErrorString: (x: unknown): boolean =>
    typeof x === "object" && x !== null && typeof (x as { error?: unknown }).error === "string",
}));

// The registry reads the ids the shipped page declares; the test builds that row.
vi.mock("./dom.js", () => ({
  $: {
    get uploadProgress() {
      return document.getElementById("upload-progress");
    },
    get uploadProgressBar() {
      return document.getElementById("upload-progress-bar");
    },
    get uploadProgressLabel() {
      return document.getElementById("upload-progress-label");
    },
    get uploadProgressCancel() {
      return document.getElementById("upload-progress-cancel");
    },
  },
}));

import { uploadFiles } from "./upload.js";

type Listener = (e: Event) => void;

/** The one XHR every case drives. Only the surface `uploadFiles` touches. */
class FakeXHR {
  static last: FakeXHR | null = null;
  status = 200;
  responseText = "{}";
  timeout = 0;
  readonly upload = {
    listeners: new Map<string, Listener[]>(),
    addEventListener(type: string, fn: Listener) {
      const list = this.listeners.get(type) ?? [];
      list.push(fn);
      this.listeners.set(type, list);
    },
  };
  private readonly listeners = new Map<string, Listener[]>();
  sent = false;

  constructor() {
    FakeXHR.last = this;
  }

  open(): void {
    /* nothing to record */
  }

  send(): void {
    this.sent = true;
  }

  abort(): void {
    this.fire("abort");
  }

  addEventListener(type: string, fn: Listener): void {
    const list = this.listeners.get(type) ?? [];
    list.push(fn);
    this.listeners.set(type, list);
  }

  /** Drive one of the xhr's own events (load / error / timeout / abort). */
  fire(type: string): void {
    for (const fn of this.listeners.get(type) ?? []) {
      fn(new Event(type));
    }
  }

  /** Drive one `xhr.upload` progress event. */
  progress(loaded: number, total: number, lengthComputable: boolean): void {
    for (const fn of this.upload.listeners.get("progress") ?? []) {
      fn(new ProgressEvent("progress", { loaded, total, lengthComputable }));
    }
  }
}

/** The upload row exactly as `static/index.html` declares it. */
function mountRow(): { bar: HTMLProgressElement; row: HTMLElement; label: HTMLElement } {
  document.body.replaceChildren();
  const row = document.createElement("div");
  row.id = "upload-progress";
  row.className = "upload-progress upload-closed";
  const bar = document.createElement("progress");
  bar.id = "upload-progress-bar";
  bar.className = "upload-progress-bar";
  bar.max = 100;
  bar.value = 0;
  bar.setAttribute("aria-label", "Upload progress");
  const label = document.createElement("span");
  label.id = "upload-progress-label";
  label.className = "upload-progress-label";
  const cancel = document.createElement("button");
  cancel.type = "button";
  cancel.id = "upload-progress-cancel";
  cancel.className = "upload-progress-cancel hidden";
  row.append(bar, label, cancel);
  document.body.appendChild(row);
  return { bar, row, label };
}

function fileList(...names: string[]): FileList {
  const files = names.map((n) => new File(["x"], n));
  return {
    length: files.length,
    item: (i: number) => files[i] ?? null,
    ...Object.fromEntries(files.map((f, i) => [i, f])),
    [Symbol.iterator]: () => files[Symbol.iterator](),
  } as unknown as FileList;
}

/** Start an upload and hand back the row plus the xhr it created. */
function start(...names: string[]): {
  bar: HTMLProgressElement;
  row: HTMLElement;
  label: HTMLElement;
  xhr: FakeXHR;
} {
  const parts = mountRow();
  uploadFiles({ files: fileList(...names), targetDir: "/workspace/uploads" });
  const xhr = FakeXHR.last;
  expect(xhr, "uploadFiles created an XHR").not.toBeNull();
  if (xhr === null) {
    throw new Error("no xhr");
  }
  return { ...parts, xhr };
}

beforeEach(() => {
  FakeXHR.last = null;
  vi.stubGlobal("XMLHttpRequest", FakeXHR);
});

afterEach(() => {
  document.body.replaceChildren();
});

describe("the container is a plain layout div", () => {
  it("claims no progressbar role, so the Cancel button stays in the a11y tree", () => {
    const { row } = start("a.png");
    // Every one of these was set on the container before the conversion, and
    // `role="progressbar"` is what made its children presentational.
    expect(row.getAttribute("role")).toBeNull();
    expect(row.getAttribute("aria-valuemin")).toBeNull();
    expect(row.getAttribute("aria-valuemax")).toBeNull();
    expect(row.getAttribute("aria-valuenow")).toBeNull();
    expect(row.getAttribute("aria-label")).toBeNull();
    // The button it contains is a real button again, not a presentational span.
    const cancel = row.querySelector("#upload-progress-cancel");
    expect(cancel).not.toBeNull();
    expect(cancel?.classList.contains("hidden")).toBe(false);
  });

  it("names the bar itself with the file count the container's label carried", () => {
    const { bar } = start("a.png", "b.png", "c.png");
    expect(bar.getAttribute("aria-label")).toBe("Uploading 3 file(s)");
  });

  it("opens the row and starts the bar at zero", () => {
    const { bar, row, label } = start("a.png");
    expect(row.classList.contains("upload-closed")).toBe(false);
    expect(bar.value).toBe(0);
    expect(label.textContent).toBe("Uploading 1 file(s)...");
  });
});

describe("the progress path", () => {
  it("assigns the percentage on a determinate tick", () => {
    const { bar, label, xhr } = start("a.png");
    xhr.progress(42, 100, true);
    expect(bar.value).toBe(42);
    expect(bar.position).toBeCloseTo(0.42, 5);
    expect(label.textContent).toBe("Uploading... 42%");
  });

  it("goes indeterminate when the total is unknown, then recovers", () => {
    const { bar, label, xhr } = start("a.png");
    xhr.progress(30, 100, true);
    expect(bar.hasAttribute("value")).toBe(true);

    xhr.progress(60, 0, false);
    // Not "value 0": a valueless <progress> is the native indeterminate
    // rendering, which is what the removed `aria-valuenow` used to mean.
    expect(bar.hasAttribute("value")).toBe(false);
    expect(bar.position).toBe(-1);
    expect(label.textContent).toBe("Uploading...");

    xhr.progress(80, 100, true);
    expect(bar.value).toBe(80);
    expect(bar.position).toBeCloseTo(0.8, 5);
  });

  it("fills the bar on a successful load", () => {
    const { bar, label, xhr } = start("a.png");
    xhr.progress(42, 100, true);
    xhr.status = 200;
    xhr.responseText = '{"uploaded":["a.png"]}';
    xhr.fire("load");
    expect(bar.value).toBe(100);
    expect(label.textContent).toBe("Upload complete");
  });
});

describe("the failure paths write the label and nothing else", () => {
  // They never touched the bar before the conversion either; pinned so a later
  // edit does not "helpfully" zero it, which would report a fresh upload rather
  // than a stopped one.
  const cases = [
    { name: "a transport error", drive: (x: FakeXHR) => x.fire("error"), label: "Upload failed" },
    { name: "a timeout", drive: (x: FakeXHR) => x.fire("timeout"), label: "Upload timed out" },
    { name: "a cancel", drive: (x: FakeXHR) => x.abort(), label: "Upload cancelled" },
  ] as const;

  for (const c of cases) {
    it(`leaves the bar where it was on ${c.name}`, () => {
      const { bar, label, xhr } = start("a.png");
      xhr.progress(42, 100, true);
      c.drive(xhr);
      expect(label.textContent).toBe(c.label);
      expect(bar.value).toBe(42);
    });
  }

  it("reports a server rejection in the label and leaves the bar alone", () => {
    const { bar, label, xhr } = start("a.png");
    xhr.progress(42, 100, true);
    xhr.status = 413;
    xhr.responseText = '{"error":"upload too large"}';
    xhr.fire("load");
    expect(label.textContent).toBe("upload too large");
    expect(bar.value).toBe(42);
  });
});
