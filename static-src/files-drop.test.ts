// Tests for the composer's upload target and pre-flight. What matters here is
// the ARGUMENTS initChatAttach hands the upload action: the target directory
// (which was wrong in production, see the dir="." case below) and the file set
// (which is now filtered before any bytes leave).
import { beforeEach, describe, expect, it, vi } from "vitest";

import { MAX_UPLOAD_FILES, MAX_UPLOAD_TOTAL_BYTES, UPLOADS_DIR } from "./upload-policy.js";

const dispatch = vi.fn();
const attachPathToActiveChat = vi.fn();
const toastError = vi.fn();

vi.mock("./actions/files.js", () => ({
  upload: {
    get dispatch() {
      return dispatch;
    },
  },
  partialUploadOf: (cause: unknown): string[] => {
    if (typeof cause !== "object" || cause === null || !("uploaded" in cause)) {
      return [];
    }
    const { uploaded } = cause;
    return Array.isArray(uploaded) ? (uploaded as string[]) : [];
  },
}));
vi.mock("./chat.js", () => ({ attachPathToActiveChat }));
vi.mock("./toast.js", () => ({ error: toastError, success: vi.fn(), info: vi.fn() }));

/** The minimum composer DOM initChatAttach touches. */
function mountComposer(): HTMLDivElement {
  document.body.innerHTML = `
    <div id="chat-view"></div>
    <textarea id="prompt-input"></textarea>
  `;
  return document.getElementById("chat-view") as HTMLDivElement;
}

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

/** Drop files on the chat view and return the args the action received. */
async function drop(files: File[]): Promise<{ files: FileList; targetDir: string } | undefined> {
  const chatView = mountComposer();
  const { initChatAttach } = await import("./files-drop.js");
  initChatAttach();
  const event = new Event("drop", { bubbles: true, cancelable: true }) as DragEvent;
  Object.defineProperty(event, "dataTransfer", { value: { files: fileList(files) } });
  chatView.dispatchEvent(event);
  return dispatch.mock.calls.at(-1)?.[0] as { files: FileList; targetDir: string } | undefined;
}

describe("the composer's upload target", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  // The regression this packet fixed. The chat view uploaded with "." for both
  // drop and paste; the server cleans "." to "/", and "/" is inside no granted
  // mount, so every chat-view drop and every pasted screenshot answered 403
  // with a red "Upload failed" toast. Nothing covered the target argument, so
  // the shape shipped.
  it("is the uploads folder, never the root-collapsing '.'", async () => {
    const args = await drop([sized("a.txt", 10)]);
    expect(args?.targetDir).toBe(UPLOADS_DIR);
    expect(args?.targetDir).not.toBe(".");
    expect(args?.targetDir).not.toBe("");
    expect(args?.targetDir).not.toBe("/");
  });

  it("attaches every path the upload returned", async () => {
    await drop([sized("a.txt", 10)]);
    const opts = dispatch.mock.calls.at(-1)?.[1] as {
      onSuccess: (paths: string[]) => void;
    };
    opts.onSuccess([`${UPLOADS_DIR}/a.txt`, `${UPLOADS_DIR}/b.txt`]);
    expect(attachPathToActiveChat.mock.calls.flat()).toEqual([
      `${UPLOADS_DIR}/a.txt`,
      `${UPLOADS_DIR}/b.txt`,
    ]);
  });

  // D98: a partially-failed batch is not rolled back, so the files that landed
  // are still attachable. Discarding them would lose good uploads the user
  // would then have to repeat.
  it("attaches the partial batch when the upload failed partway", async () => {
    await drop([sized("a.txt", 10)]);
    const opts = dispatch.mock.calls.at(-1)?.[1] as {
      onError: (err: { message: string; cause?: unknown }) => void;
    };
    opts.onError({
      message: "1 of 2 uploaded, then big.zip failed: upload too large",
      cause: { uploaded: [`${UPLOADS_DIR}/a.txt`] },
    });
    expect(attachPathToActiveChat).toHaveBeenCalledWith(`${UPLOADS_DIR}/a.txt`);
  });

  it("attaches nothing when the failure carries no partial batch", async () => {
    await drop([sized("a.txt", 10)]);
    const opts = dispatch.mock.calls.at(-1)?.[1] as {
      onError: (err: { message: string; cause?: unknown }) => void;
    };
    opts.onError({ message: "Upload failed" });
    expect(attachPathToActiveChat).not.toHaveBeenCalled();
  });
});

describe("the composer's upload pre-flight", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("sends only the files under the limit and says what it skipped", async () => {
    const args = await drop([
      sized("ok.txt", 10),
      sized("big.zip", MAX_UPLOAD_TOTAL_BYTES + 1),
      sized("also-ok.txt", 20),
    ]);
    expect(Array.from(args?.files ?? [], (f) => f.name)).toEqual(["ok.txt", "also-ok.txt"]);
    expect(toastError).toHaveBeenCalledWith(expect.stringContaining("big.zip"));
  });

  it("does not dispatch at all when every file is refused", async () => {
    await drop([sized("big.zip", MAX_UPLOAD_TOTAL_BYTES + 1)]);
    expect(dispatch).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledOnce();
  });

  // The batch that used to pass pre-flight and then 413 as a whole: the server
  // limit is on the multipart request, not on each file inside it.
  it("drops the file that would take the request over the total", async () => {
    const thirty = 30 * 1024 * 1024;
    const args = await drop([sized("a.bin", thirty), sized("b.bin", thirty)]);
    expect(Array.from(args?.files ?? [], (f) => f.name)).toEqual(["a.bin"]);
    expect(toastError).toHaveBeenCalledWith(expect.stringContaining("b.bin"));
  });

  it("stays silent on the common path", async () => {
    await drop([sized("a.txt", 1), sized("b.txt", 2)]);
    expect(toastError).not.toHaveBeenCalled();
    expect(dispatch).toHaveBeenCalledOnce();
  });

  it("caps a dropped folder rather than opening a long opaque upload", async () => {
    const many = Array.from({ length: MAX_UPLOAD_FILES + 5 }, (_, i) =>
      sized(`f${String(i)}.txt`, 1),
    );
    const args = await drop(many);
    expect(args?.files.length).toBe(MAX_UPLOAD_FILES);
    expect(toastError).toHaveBeenCalledOnce();
  });
});

// The upload-limit hint's test moved to chat-options.test.ts with the control it
// annotates: the standalone attach pill is gone, and the explicit attach door is
// now a row in the chat-actions menu. Drop and paste, which this file covers,
// carry the limit on the drop overlay instead.
