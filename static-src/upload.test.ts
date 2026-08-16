import { describe, expect, it } from "vitest";

import { batchFailureMessage, resolvePaths, uploadedNames } from "./upload.js";

/** A FileList over the given names, which is all these helpers read. */
function fileList(...names: string[]): FileList {
  const files = names.map((n) => new File(["x"], n));
  return {
    length: files.length,
    item: (i: number) => files[i] ?? null,
    ...Object.fromEntries(files.map((f, i) => [i, f])),
    [Symbol.iterator]: () => files[Symbol.iterator](),
  } as unknown as FileList;
}

describe("resolvePaths", () => {
  it("prefixes the target directory, which is the attachment path", () => {
    expect(resolvePaths("/workspace/uploads", ["a.png", "b.txt"])).toEqual([
      "/workspace/uploads/a.png",
      "/workspace/uploads/b.txt",
    ]);
  });

  it("does not double the separator on a trailing slash", () => {
    expect(resolvePaths("/workspace/uploads/", ["a.png"])).toEqual(["/workspace/uploads/a.png"]);
  });

  it("leaves a bare name bare for the workspace-root targets", () => {
    expect(resolvePaths("", ["a.png"])).toEqual(["a.png"]);
    expect(resolvePaths(".", ["a.png"])).toEqual(["a.png"]);
  });
});

describe("uploadedNames", () => {
  it("recovers the partial batch from an error body", () => {
    expect(uploadedNames('{"error":"upload too large","uploaded":["a.txt","b.txt"]}')).toEqual([
      "a.txt",
      "b.txt",
    ]);
  });

  it("returns nothing for a body that carries no names", () => {
    expect(uploadedNames('{"error":"upload failed"}')).toEqual([]);
    expect(uploadedNames("")).toEqual([]);
    expect(uploadedNames("<html>502 Bad Gateway</html>")).toEqual([]);
  });

  it("drops a non-string entry rather than trusting the body's shape", () => {
    expect(uploadedNames('{"uploaded":["a.txt",7,null,"b.txt"]}')).toEqual(["a.txt", "b.txt"]);
  });
});

describe("batchFailureMessage", () => {
  it("names how far the batch got and which file stopped it", () => {
    const files = fileList("a.txt", "b.txt", "c.txt", "big.zip", "e.txt");
    expect(batchFailureMessage(files, 3, "upload too large")).toBe(
      "3 of 5 uploaded, then big.zip failed: upload too large",
    );
  });

  it("says only what the server said when nothing landed", () => {
    const files = fileList("big.zip");
    expect(batchFailureMessage(files, 0, "upload too large")).toBe("upload too large");
  });

  it("says only what the server said when everything landed", () => {
    // A post-write failure (a dir fsync, say) leaves no file to blame.
    const files = fileList("a.txt");
    expect(batchFailureMessage(files, 1, "upload failed")).toBe("upload failed");
  });

  it("falls back rather than naming undefined when the count outruns the list", () => {
    const files = fileList("a.txt", "b.txt");
    expect(batchFailureMessage(files, 5, "upload failed")).toBe("upload failed");
  });

  it("carries no em dash", () => {
    const files = fileList("a.txt", "b.txt");
    expect(batchFailureMessage(files, 1, "upload failed")).not.toContain("\u2014");
  });
});
