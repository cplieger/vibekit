import { describe, expect, it } from "vitest";

import {
  MAX_UPLOAD_BYTES,
  MAX_UPLOAD_FILES,
  MAX_UPLOAD_TOTAL_BYTES,
  UPLOADS_DIR,
  preflightMessage,
  preflightUploads,
  uploadLimitHint,
} from "./upload-policy.js";

/** A File of a given size without allocating the bytes. */
function sized(name: string, size: number): File {
  const f = new File(["x"], name, { type: "application/octet-stream" });
  Object.defineProperty(f, "size", { value: size });
  return f;
}

describe("UPLOADS_DIR", () => {
  it("is container-absolute, which is what both consumers need", () => {
    // As an upload target the server resolves it against its granted mounts, so
    // a bare "uploads" would clean to /uploads and be refused. As an attachment
    // path prefix the prompt builder joins a relative path onto the workspace,
    // so the absolute spelling satisfies both without a second transformation.
    expect(UPLOADS_DIR.startsWith("/")).toBe(true);
    expect(UPLOADS_DIR).toBe("/workspace/uploads");
  });
});

describe("the client budget against the server ceiling", () => {
  // The server applies maxUploadSize to the whole multipart BODY, and framing
  // is part of that body, so a client budget equal to it can never be met.
  // Under-promising is the only honest direction for a limit a user reads
  // before choosing a file.
  it("is strictly below the server's whole-request ceiling", () => {
    expect(MAX_UPLOAD_BYTES).toBe(50 * 1024 * 1024);
    expect(MAX_UPLOAD_TOTAL_BYTES).toBeLessThan(MAX_UPLOAD_BYTES);
    expect(MAX_UPLOAD_TOTAL_BYTES).toBeGreaterThan(0);
  });

  // The reserve exists for framing, not to shrink the feature: a batch of the
  // maximum file count at the longest legal filename costs on the order of
  // tens of kilobytes.
  it("holds back enough for framing without eating the allowance", () => {
    const reserve = MAX_UPLOAD_BYTES - MAX_UPLOAD_TOTAL_BYTES;
    expect(reserve).toBeGreaterThanOrEqual(64 * 1024);
    expect(reserve).toBeLessThan(MAX_UPLOAD_BYTES / 10);
  });
});

describe("uploadLimitHint", () => {
  // The hint states the TOTAL, and states it exactly. "Up to 50 MB per file"
  // described no request the server accepts.
  it("names the enforced total in round megabytes", () => {
    expect(uploadLimitHint()).toBe("Up to 49 MB per upload, all files together");
    expect(MAX_UPLOAD_TOTAL_BYTES).toBe(49 * 1024 * 1024);
  });

  // A hint that promised more than the pre-flight allows is the defect this
  // replaced, so the number in the words has to be reachable.
  it("promises no more than the pre-flight accepts", () => {
    const stated = Number(/Up to (\d+) MB/.exec(uploadLimitHint())?.[1] ?? "0");
    expect(stated).toBeGreaterThan(0);
    const atTheStatedLimit = preflightUploads([sized("edge.bin", stated * 1024 * 1024)]);
    expect(atTheStatedLimit.rejected).toEqual([]);
  });

  it("carries no em dash", () => {
    expect(uploadLimitHint()).not.toContain("\u2014");
  });
});

describe("preflightUploads", () => {
  // Replaces a case that asserted a file of exactly MAX_UPLOAD_BYTES was
  // acceptable. It never was: the server wraps the whole multipart body in
  // MaxBytesReader(maxUploadSize) and framing counts, so that file always 413s.
  it("accepts a file at exactly the enforced total", () => {
    const r = preflightUploads([sized("edge.bin", MAX_UPLOAD_TOTAL_BYTES)]);
    expect(r.accepted.map((f) => f.name)).toEqual(["edge.bin"]);
    expect(r.rejected).toEqual([]);
  });

  it("refuses a file at the raw server ceiling, which cannot fit in a request", () => {
    const r = preflightUploads([sized("big.zip", MAX_UPLOAD_BYTES)]);
    expect(r.accepted).toEqual([]);
    expect(r.rejected.map((x) => x.name)).toEqual(["big.zip"]);
  });

  it("refuses a file one byte over, before any bytes leave", () => {
    const r = preflightUploads([sized("big.zip", MAX_UPLOAD_TOTAL_BYTES + 1)]);
    expect(r.accepted).toEqual([]);
    expect(r.rejected).toEqual([{ name: "big.zip", reason: "over the 49 MB limit" }]);
  });

  // The batch case the per-file-only check let through: two files each well
  // under the limit whose combined request is over it. Both used to pass
  // pre-flight and then fail together against the one limit the server applies.
  it("refuses the file that would take the batch over the total", () => {
    const thirty = 30 * 1024 * 1024;
    const r = preflightUploads([sized("a.bin", thirty), sized("b.bin", thirty)]);
    expect(r.accepted.map((f) => f.name)).toEqual(["a.bin"]);
    expect(r.rejected).toEqual([{ name: "b.bin", reason: "over the 49 MB total for one upload" }]);
  });

  // A batch that exactly fills the budget is legal: the reserve is what pays
  // for the framing, so nothing has to be left over here.
  it("accepts a batch that exactly fills the total", () => {
    const half = MAX_UPLOAD_TOTAL_BYTES / 2;
    const r = preflightUploads([sized("a.bin", half), sized("b.bin", half)]);
    expect(r.accepted.map((f) => f.name)).toEqual(["a.bin", "b.bin"]);
    expect(r.rejected).toEqual([]);
  });

  // The total is spent by the ACCEPTED files only: a rejection must not
  // consume budget a later file could have used.
  it("charges only accepted files against the total", () => {
    const r = preflightUploads([
      sized("too-big.bin", MAX_UPLOAD_TOTAL_BYTES + 1),
      sized("fits.bin", MAX_UPLOAD_TOTAL_BYTES),
    ]);
    expect(r.accepted.map((f) => f.name)).toEqual(["fits.bin"]);
    expect(r.rejected.map((x) => x.name)).toEqual(["too-big.bin"]);
  });

  it("keeps the good files in a mixed batch and preserves order", () => {
    const r = preflightUploads([
      sized("a.txt", 10),
      sized("big.zip", MAX_UPLOAD_TOTAL_BYTES + 1),
      sized("b.txt", 20),
    ]);
    expect(r.accepted.map((f) => f.name)).toEqual(["a.txt", "b.txt"]);
    expect(r.rejected.map((x) => x.name)).toEqual(["big.zip"]);
  });

  it("accepts an empty file: zero bytes is a legal file", () => {
    const r = preflightUploads([sized("empty", 0)]);
    expect(r.accepted).toHaveLength(1);
    expect(r.rejected).toEqual([]);
  });

  it("caps the batch, because a dropped folder is a batch nobody chose", () => {
    const files = Array.from({ length: MAX_UPLOAD_FILES + 3 }, (_, i) =>
      sized(`f${String(i)}.txt`, 1),
    );
    const r = preflightUploads(files);
    expect(r.accepted).toHaveLength(MAX_UPLOAD_FILES);
    expect(r.rejected).toHaveLength(3);
    expect(r.rejected[0]?.reason).toContain("at once");
  });

  it("passes an empty input through without inventing a rejection", () => {
    expect(preflightUploads([])).toEqual({ accepted: [], rejected: [] });
  });
});

describe("preflightMessage", () => {
  it("is empty when nothing was refused, so the caller skips the toast", () => {
    expect(preflightMessage([])).toBe("");
  });

  it("names the single refusal and its reason", () => {
    expect(preflightMessage([{ name: "big.zip", reason: "over the 49 MB limit" }])).toBe(
      "Skipped big.zip: over the 49 MB limit",
    );
  });

  it("counts a multi-file refusal and names the first", () => {
    expect(
      preflightMessage([
        { name: "a.zip", reason: "over the 49 MB limit" },
        { name: "b.zip", reason: "over the 49 MB limit" },
      ]),
    ).toBe("Skipped 2 files, starting with a.zip: over the 49 MB limit");
  });
});
