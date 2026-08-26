// ---------------------------------------------------------------------------
// The upload doors OTHER than the composer's.
//
// preflightUploads was wired to one of four doors, so the browser drop, the
// browser's upload dialog and the picker's "Upload here" each transmitted the
// bytes and then reported the server's bare 413, which names no file. The server
// cap is real either way, so what these cases pin is the DIAGNOSIS: the offending
// file is named before anything is sent, and a batch that is partly legal sends
// the legal part rather than failing whole.
//
// The composer's own door is covered by files-drop.test.ts. The two DIALOG doors
// (the browser toolbar's Upload, and the picker's "Upload here") are deliberately
// absent: each builds its <input type="file"> on demand, never puts it in the
// document, and relies on an OS dialog no test runner can open, so a test has no
// handle on the element and no way to produce a selection. What they screen with
// is screenUploads, which upload-policy.test.ts drives directly, and the call
// itself is one statement above the dispatch in each.
// ---------------------------------------------------------------------------
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MAX_UPLOAD_FILES, MAX_UPLOAD_TOTAL_BYTES } from "./upload-policy.js";

const dispatch = vi.fn();
const attachPathsToActiveChat = vi.fn();
const toastError = vi.fn();

vi.mock("./actions/files.js", () => ({
  upload: {
    get dispatch() {
      return dispatch;
    },
  },
  partialUploadOf: (): string[] => [],
}));
vi.mock("./chat.js", () => ({ attachPathsToActiveChat }));
vi.mock("./toast.js", () => ({ error: toastError, success: vi.fn(), info: vi.fn() }));

/** A File of a given size without allocating the bytes. */
function sized(name: string, size: number): File {
  const f = new File(["x"], name, { type: "text/plain" });
  Object.defineProperty(f, "size", { value: size });
  return f;
}

function fileList(files: File[]): FileList {
  const dt = new DataTransfer();
  for (const f of files) {
    dt.items.add(f);
  }
  return dt.files;
}

interface UploadArgs {
  files: FileList;
  targetDir: string;
}

function lastArgs(): UploadArgs | undefined {
  return dispatch.mock.calls.at(-1)?.[0] as UploadArgs | undefined;
}

function names(files: FileList | undefined): string[] {
  return Array.from(files ?? [], (f) => f.name);
}

// --- Door: drag-drop into the file browser -------------------------------

vi.mock("./dom.js", () => ({
  $: {
    get fbList() {
      let list = document.getElementById("fb-list");
      if (list === null) {
        const wrap = document.createElement("div");
        wrap.id = "fb-list-wrap";
        list = document.createElement("div");
        list.id = "fb-list";
        wrap.appendChild(list);
        document.body.appendChild(wrap);
      }
      return list;
    },
    get fbDropOverlay() {
      let o = document.getElementById("fb-drop-overlay");
      if (o === null) {
        o = document.createElement("div");
        o.id = "fb-drop-overlay";
        document.body.appendChild(o);
      }
      return o;
    },
  },
}));

/** Drop files onto the browser's list wrapper and return the action's args. */
async function dropInBrowser(files: File[], currentPath = "/workspace"): Promise<void> {
  document.body.innerHTML = "";
  const { initBrowserDragDrop } = await import("./files-browser-drop.js");
  const { $ } = await import("./dom.js");
  const wrap = $.fbList.parentElement;
  initBrowserDragDrop({
    getCurrentPath: () => currentPath,
    getEntryMap: () => new Map(),
    reload: vi.fn(),
  });
  const event = new Event("drop", { bubbles: true, cancelable: true }) as DragEvent;
  Object.defineProperty(event, "dataTransfer", { value: { files: fileList(files) } });
  wrap?.dispatchEvent(event);
}

describe("the file browser's drop door", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
  });

  it("names the over-cap file and sends the rest", async () => {
    await dropInBrowser([
      sized("ok.txt", 10),
      sized("big.zip", MAX_UPLOAD_TOTAL_BYTES + 1),
      sized("also-ok.txt", 20),
    ]);
    expect(names(lastArgs()?.files)).toEqual(["ok.txt", "also-ok.txt"]);
    expect(toastError).toHaveBeenCalledWith(expect.stringContaining("big.zip"));
  });

  it("sends nothing when every file is refused", async () => {
    await dropInBrowser([sized("big.zip", MAX_UPLOAD_TOTAL_BYTES + 1)]);
    expect(dispatch).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledOnce();
  });

  it("refuses the file that would take the request over the total", async () => {
    const thirty = 30 * 1024 * 1024;
    await dropInBrowser([sized("a.bin", thirty), sized("b.bin", thirty)]);
    expect(names(lastArgs()?.files)).toEqual(["a.bin"]);
    expect(toastError).toHaveBeenCalledWith(expect.stringContaining("b.bin"));
  });

  it("caps a dropped folder", async () => {
    const many = Array.from({ length: MAX_UPLOAD_FILES + 5 }, (_, i) =>
      sized(`f${String(i)}.txt`, 1),
    );
    await dropInBrowser(many);
    expect(lastArgs()?.files.length).toBe(MAX_UPLOAD_FILES);
    expect(toastError).toHaveBeenCalledOnce();
  });

  // The screening must not disturb what this door is FOR: uploading where the
  // user is looking, which is the composer door's one real difference.
  it("stays silent on the common path and keeps uploading to the current dir", async () => {
    await dropInBrowser([sized("a.txt", 1)], "/workspace/sub");
    expect(toastError).not.toHaveBeenCalled();
    expect(dispatch).toHaveBeenCalledOnce();
    expect(lastArgs()?.targetDir).toBe("/workspace/sub");
    expect(names(lastArgs()?.files)).toEqual(["a.txt"]);
  });
});

// --- The screening step every door now shares ----------------------------
//
// Here rather than in upload-policy.test.ts because that file is deliberately a
// node-environment test over pure policy, and rebuilding a shortened FileList
// needs a DOM. This is also the coverage the two dialog doors rest on.

describe("screenUploads", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.resetModules();
  });

  it("passes a clean batch through UNCHANGED, allocating no copy", async () => {
    const { screenUploads } = await import("./upload-policy.js");
    const input = fileList([sized("a.txt", 1), sized("b.txt", 2)]);
    const out = screenUploads(input);
    expect(out.skipped).toBe("");
    // Identity, not equality: the common path must not build a DataTransfer per
    // drop to arrive at the same list.
    expect(out.files).toBe(input);
  });

  it("returns null files and a message when everything is refused", async () => {
    const { screenUploads } = await import("./upload-policy.js");
    const out = screenUploads(fileList([sized("big.zip", MAX_UPLOAD_TOTAL_BYTES + 1)]));
    expect(out.files).toBeNull();
    expect(out.skipped).toContain("big.zip");
  });

  it("shortens a mixed batch and names the first refusal", async () => {
    const { screenUploads } = await import("./upload-policy.js");
    const out = screenUploads(
      fileList([sized("ok.txt", 5), sized("big.zip", MAX_UPLOAD_TOTAL_BYTES + 1)]),
    );
    expect(names(out.files ?? undefined)).toEqual(["ok.txt"]);
    expect(out.skipped).toContain("big.zip");
  });

  it("preserves input order in a shortened batch", async () => {
    const { screenUploads } = await import("./upload-policy.js");
    const out = screenUploads(
      fileList([
        sized("1.txt", 1),
        sized("big.zip", MAX_UPLOAD_TOTAL_BYTES + 1),
        sized("2.txt", 1),
        sized("3.txt", 1),
      ]),
    );
    expect(names(out.files ?? undefined)).toEqual(["1.txt", "2.txt", "3.txt"]);
  });

  it("reports an empty gesture as nothing to send and nothing to say", async () => {
    const { screenUploads } = await import("./upload-policy.js");
    const out = screenUploads(fileList([]));
    expect(out.files).toBeNull();
    expect(out.skipped).toBe("");
  });
});
