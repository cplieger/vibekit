// @vitest-environment happy-dom
// Tests for tangent.ts UI wiring: fork pill + merge-tangent pill visibility
// based on session state, and dispatch routing on click.
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./toast.js", () => ({
  info: vi.fn(), success: vi.fn(), error: vi.fn(), showToast: vi.fn(),
}));
vi.mock("./transport.js", () => ({ send: vi.fn() }));
vi.mock("./api-client.js", () => ({
  API_TIMEOUT_MS: 30_000,
  withTimeout: (signal: AbortSignal | undefined) => signal ?? new AbortController().signal,
}));
vi.mock("./chat.js", () => ({
  openChatTab: vi.fn(),
  activateChatView: vi.fn(),
}));

// Use the actual store but stub loadList to skip the network path —
// we don't care about the chat list reload here, only the merge dispatch
// and its UI-state wiring.
vi.mock("./store.js", async () => {
  const actual = await vi.importActual<typeof import("./store.js")>("./store.js");
  return { ...actual, loadList: vi.fn().mockResolvedValue(true) };
});

import { setSessions, setActive } from "./store.js";
import { _resetForTest as resetDefine } from "./actions/define.js";
import { _resetForTest as resetRegistry } from "./actions/registry.js";
import { _resetForTest as resetCleanup } from "./actions/cleanup.js";
import { send as transportSend } from "./transport.js";
import { activateChatView } from "./chat.js";
import { initTangent } from "./tangent.js";
import type { Session } from "./types.js";

const mockSend = transportSend as unknown as ReturnType<typeof vi.fn>;
const mockActivateView = activateChatView as unknown as ReturnType<typeof vi.fn>;

function setupDOM(): { forkBtn: HTMLButtonElement; mergeBtn: HTMLButtonElement } {
  document.body.innerHTML = `
    <button id="fork-pill" class="pill hidden" type="button"></button>
    <button id="merge-tangent-pill" class="pill hidden" type="button"></button>
    <form id="prompt-form"></form>
  `;
  return {
    forkBtn: document.getElementById("fork-pill") as HTMLButtonElement,
    mergeBtn: document.getElementById("merge-tangent-pill") as HTMLButtonElement,
  };
}

function makeSession(overrides: Partial<Session> = {}): Session {
  return {
    id: "c1",
    name: "Chat 1",
    agent: "kiro_default",
    is_tangent: false,
    frozen: false,
    message_count: 0,
    summary: "",
    updated_at: 0,
    ...overrides,
  } as Session;
}

beforeEach(() => {
  resetDefine();
  resetRegistry();
  resetCleanup();
  vi.clearAllMocks();
  document.body.innerHTML = "";
});

describe("tangent pills — visibility", () => {
  it("fork pill visible on plain chat; merge pill hidden", async () => {
    const { forkBtn, mergeBtn } = setupDOM();
    setSessions([makeSession({ id: "c1", is_tangent: false, frozen: false })]);
    setActive("c1");
    initTangent();
    await Promise.resolve();
    expect(forkBtn.classList.contains("hidden")).toBe(false);
    expect(mergeBtn.classList.contains("hidden")).toBe(true);
  });

  it("fork pill hidden on parent of tangent (frozen=true); merge pill stays hidden", async () => {
    const { forkBtn, mergeBtn } = setupDOM();
    setSessions([makeSession({ id: "c1", is_tangent: false, frozen: true })]);
    setActive("c1");
    initTangent();
    await Promise.resolve();
    expect(forkBtn.classList.contains("hidden")).toBe(true);
    expect(mergeBtn.classList.contains("hidden")).toBe(true);
  });

  it("fork pill hidden on tangent chat; merge pill visible", async () => {
    const { forkBtn, mergeBtn } = setupDOM();
    setSessions([makeSession({ id: "t1", is_tangent: true, frozen: false, parent_chat_id: "p1" })]);
    setActive("t1");
    initTangent();
    await Promise.resolve();
    expect(forkBtn.classList.contains("hidden")).toBe(true);
    expect(mergeBtn.classList.contains("hidden")).toBe(false);
  });

  it("frozen class added to prompt form when frozen", async () => {
    setupDOM();
    setSessions([makeSession({ id: "c1", frozen: true })]);
    setActive("c1");
    initTangent();
    await Promise.resolve();
    const form = document.getElementById("prompt-form") as HTMLFormElement;
    expect(form.classList.contains("frozen")).toBe(true);
  });
});

describe("tangent pills — dispatch routing", () => {
  it("merge pill click dispatches merge_tangent for the active tangent", async () => {
    const { mergeBtn } = setupDOM();
    setSessions([makeSession({ id: "t1", is_tangent: true, parent_chat_id: "p1" })]);
    setActive("t1");
    initTangent();
    await Promise.resolve();

    mockSend.mockResolvedValue({ ok: true, status: 200 });
    mergeBtn.click();
    await Promise.resolve();
    await Promise.resolve();

    expect(mockSend).toHaveBeenCalledWith(
      expect.objectContaining({ type: "merge_tangent", chat_id: "t1" }),
      expect.anything(),
    );
  });

  it("merge pill click is no-op when active chat is not a tangent", async () => {
    const { mergeBtn } = setupDOM();
    setSessions([makeSession({ id: "c1", is_tangent: false })]);
    setActive("c1");
    initTangent();
    await Promise.resolve();

    mergeBtn.click();
    await Promise.resolve();
    expect(mockSend).not.toHaveBeenCalled();
  });

  it("merge success activates parent chat", async () => {
    const { mergeBtn } = setupDOM();
    setSessions([makeSession({ id: "t1", is_tangent: true, parent_chat_id: "p1" })]);
    setActive("t1");
    initTangent();
    await Promise.resolve();

    mockSend.mockResolvedValue({ ok: true, status: 200 });
    mergeBtn.click();
    // Wait microtasks for dispatch + onSuccess
    await new Promise((r) => setTimeout(r, 10));

    expect(mockActivateView).toHaveBeenCalledWith("p1");
  });

  it("merge with missing parent ID still dispatches but skips activation", async () => {
    const { mergeBtn } = setupDOM();
    setSessions([makeSession({ id: "t1", is_tangent: true })]);  // no parent_chat_id
    setActive("t1");
    initTangent();
    await Promise.resolve();

    mockSend.mockResolvedValue({ ok: true, status: 200 });
    mergeBtn.click();
    await new Promise((r) => setTimeout(r, 10));

    expect(mockSend).toHaveBeenCalled();
    expect(mockActivateView).not.toHaveBeenCalled();
  });
});

describe("tangent pills — DOM-absent graceful degradation", () => {
  it("initTangent doesn't throw when fork-pill is absent", () => {
    document.body.innerHTML = `<button id="merge-tangent-pill" class="pill hidden"></button>`;
    setSessions([makeSession({ id: "c1" })]);
    setActive("c1");
    expect(() => initTangent()).not.toThrow();
  });

  it("initTangent doesn't throw when merge-tangent-pill is absent", () => {
    document.body.innerHTML = `<button id="fork-pill" class="pill hidden"></button>`;
    setSessions([makeSession({ id: "c1" })]);
    setActive("c1");
    expect(() => initTangent()).not.toThrow();
  });
});
