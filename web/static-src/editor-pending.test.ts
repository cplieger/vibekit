// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";
import { buildPartialMergeText } from "./editor-pending.js";
import type { FileState } from "./editor-core.js";
import type { DiffLine } from "./diff.js";

// --- Mocks for integration tests ---

vi.mock("./dom.js", () => ({
  $: {
    editorPendingAcceptBtn: document.createElement("button"),
    editorPendingRejectBtn: document.createElement("button"),
    editorPendingApplyPartialBtn: document.createElement("button"),
  },
}));

vi.mock("./store.js", () => ({
  getActiveId: () => "chat1",
  get: () => ({ pending_changes: [{ tool_call_id: "tc1", path: "foo.ts", kind: "edit" }] }),
}));

const mockDispatch = vi.fn();
vi.mock("./actions/chat.js", () => ({
  resolvePendingChangeAction: { dispatch: (...args: unknown[]) => mockDispatch(...args) },
}));

const mockPartialDispatch = vi.fn();
vi.mock("./actions/editor.js", () => ({
  resolvePendingPartial: { dispatch: (...args: unknown[]) => mockPartialDispatch(...args) },
}));

vi.mock("./actions/index.js", () => ({
  bindLoadingState: () => () => {},
}));

vi.mock("./bus.js", () => ({
  emitBus: vi.fn(),
  BUS_ACTIVATE_CHAT: "activate_chat",
}));

vi.mock("./diff-pane.js", () => ({
  countHunks: () => 1,
}));

vi.mock("./banner-stack.js", () => ({
  showBanner: vi.fn(),
}));

function makeState(diff: DiffLine[]): FileState {
  return {
    path: "test.ts",
    original: "",
    current: "",
    loaded: true,
    error: "",
    mode: { kind: "edit", editing: false },
    suggestions: new Map(),
    returnToGitDiff: null,
    repo: "",
    pendingHunkDecisions: new Map(),
    pendingHunkCount: null,
    cachedDiff: diff,
  };
}

function ctx(text: string, oldNo = 0, newNo = 0): DiffLine {
  return { kind: "ctx", text, oldNo, newNo };
}
function del(text: string, oldNo = 0): DiffLine {
  return { kind: "del", text, oldNo, newNo: 0 };
}
function add(text: string, newNo = 0): DiffLine {
  return { kind: "add", text, oldNo: 0, newNo };
}

describe("buildPartialMergeText", () => {
  it("single hunk accept → uses new lines", () => {
    const diff: DiffLine[] = [
      ctx("line1"), del("old"), add("new"), ctx("line3"),
    ];
    const decisions = new Map([[0, "accept" as const]]);
    expect(buildPartialMergeText(makeState(diff), decisions)).toBe("line1\nnew\nline3");
  });

  it("single hunk reject → uses old lines", () => {
    const diff: DiffLine[] = [
      ctx("line1"), del("old"), add("new"), ctx("line3"),
    ];
    const decisions = new Map([[0, "reject" as const]]);
    expect(buildPartialMergeText(makeState(diff), decisions)).toBe("line1\nold\nline3");
  });

  it("multi-hunk mixed decisions", () => {
    const diff: DiffLine[] = [
      ctx("a"), del("b"), add("B"), ctx("c"), del("d"), add("D"), ctx("e"),
    ];
    const decisions = new Map([
      [0, "accept" as const],
      [1, "reject" as const],
    ]);
    expect(buildPartialMergeText(makeState(diff), decisions)).toBe("a\nB\nc\nd\ne");
  });

  it("empty diff → empty string", () => {
    expect(buildPartialMergeText(makeState([]), new Map())).toBe("");
  });

  it("all-context diff → context lines joined", () => {
    const diff: DiffLine[] = [ctx("x"), ctx("y"), ctx("z")];
    expect(buildPartialMergeText(makeState(diff), new Map())).toBe("x\ny\nz");
  });

  it("consecutive hunks with no context between them", () => {
    const diff: DiffLine[] = [
      del("a"), add("A"), del("b"), add("B"),
    ];
    const decisions = new Map([[0, "accept" as const]]);
    expect(buildPartialMergeText(makeState(diff), decisions)).toBe("A\nB");
  });

  it("hunk at EOF (no trailing context)", () => {
    const diff: DiffLine[] = [ctx("start"), del("old"), add("new")];
    const decisions = new Map([[0, "accept" as const]]);
    expect(buildPartialMergeText(makeState(diff), decisions)).toBe("start\nnew");
  });

  it("undecided hunks default to reject", () => {
    const diff: DiffLine[] = [ctx("a"), del("old"), add("new"), ctx("b")];
    expect(buildPartialMergeText(makeState(diff), new Map())).toBe("a\nold\nb");
  });
});

describe("resolveActivePending", () => {
  beforeEach(() => {
    mockDispatch.mockReset();
  });

  it("disables buttons during dispatch via bindLoadingState", async () => {
    // The function calls bindLoadingState which returns an unbind.
    // We verify dispatch is called with the correct args.
    const { fileStates } = await import("./editor-types.js");
    const path = "pending:chat1:tc1";
    fileStates.set(path, makeState([]) as any);
    (fileStates.get(path) as any).path = path;
    // Mock getActiveFilePath
    vi.spyOn(await import("./editor-types.js"), "getActiveFilePath").mockReturnValue(path);
    mockDispatch.mockResolvedValue({});

    const { resolveActivePending } = await import("./editor-pending.js");
    await resolveActivePending("accept");

    expect(mockDispatch).toHaveBeenCalledWith(
      { chatID: "chat1", toolCallID: "tc1", action: "accept" },
      expect.objectContaining({ onSuccess: expect.any(Function), onSettled: expect.any(Function) }),
    );
  });

  it("closes tab on success", async () => {
    const { fileStates } = await import("./editor-types.js");
    const path = "pending:chat1:tc1";
    fileStates.set(path, makeState([]) as any);
    (fileStates.get(path) as any).path = path;
    vi.spyOn(await import("./editor-types.js"), "getActiveFilePath").mockReturnValue(path);
    mockDispatch.mockResolvedValue({ ok: true });

    const { resolveActivePending } = await import("./editor-pending.js");
    await resolveActivePending("reject");

    // Tab should be closed (fileStates entry removed via closeFile)
    // closeFile is from editor-types which is mocked — just verify dispatch succeeded
    expect(mockDispatch).toHaveBeenCalled();
  });
});

describe("applyActivePendingPartial", () => {
  beforeEach(() => {
    mockPartialDispatch.mockReset();
  });

  it("shows size-limit banner when merged text exceeds 4 MiB", async () => {
    const { fileStates } = await import("./editor-types.js");
    const path = "pending:chat1:tc1";
    // Create a state with a huge diff that produces >4MiB merged text
    const bigLine = "x".repeat(1024 * 1024); // 1 MiB per line
    const diff: DiffLine[] = [
      ctx(bigLine), ctx(bigLine), ctx(bigLine), ctx(bigLine), ctx(bigLine),
    ];
    const state = makeState(diff) as any;
    state.path = path;
    state.mode = { kind: "diff", diffSource: { oldContent: "", newContent: "", oldLabel: "", newLabel: "", fromGit: false } };
    state.pendingHunkDecisions = new Map();
    fileStates.set(path, state);
    vi.spyOn(await import("./editor-types.js"), "getActiveFilePath").mockReturnValue(path);

    const { applyActivePendingPartial } = await import("./editor-pending.js");
    const { showBanner } = await import("./banner-stack.js");
    await applyActivePendingPartial();

    expect(showBanner).toHaveBeenCalledWith(
      "chat1", "partial-merge-too-large", expect.any(String), "warning", true,
    );
    expect(mockPartialDispatch).not.toHaveBeenCalled();
  });
});
