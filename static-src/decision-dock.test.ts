// @vitest-environment happy-dom
// ---------------------------------------------------------------------------
// Tests for decision-dock.ts — the queue, not the cards.
//
// Each case pins something the three modals got wrong or could not do:
//   - the permission modal had NO queue, so a second request overwrote the
//     first and its callback was dropped, leaving KAS waiting on an id nothing
//     would ever answer
//   - the SSE handlers gated on the active chat, so an ask raised on a
//     background chat vanished until a reconnect happened to replay it
//   - SSE reconnect replays every unanswered permission, so a re-delivery must
//     not stack a duplicate
//   - answering twice on one request id is worse than a dropped click
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";

// The store is NOT mocked: the dock's chat-switch trigger is an effect over the
// real `activeSession` computed, and a stubbed signal would test the stub's
// reactivity rather than the wiring that ships. Only the two leaves that reach
// for DOM the dock does not own are mocked.
vi.mock("./editor-openers.js", () => ({ openFileGitDiff: vi.fn() }));
vi.mock("./actions/permissions.js", () => ({ editNativeRule: { dispatch: vi.fn() } }));

import { mountDecisionDock, pushDecision, dropDecisions, _resetForTest } from "./decision-dock.js";
import { setSessions, setActive } from "./store.js";
import type { PermissionNeededPayload, Session } from "./types.js";

function session(id: string): Session {
  return {
    id,
    name: id,
    model: "",
    acp_session_id: "",
    current_mode_id: "",
    available_modes: [],
    available_models: [],
    usage: {
      context_pct: 0,
      context_size: 0,
      credits: 0,
      turn_count: 0,
      last_turn_ms: 0,
      has_real_data: false,
    },
    message_count: 0,
    messages: [],
    has_more: false,
    thinking: false,
    working_label: "Thinking",
  };
}

function host(): HTMLElement {
  return document.getElementById("decision-dock") as HTMLElement;
}

function perm(over: Partial<PermissionNeededPayload> = {}): PermissionNeededPayload {
  return {
    request_id: 1,
    options: [
      { option_id: "allow_once", name: "Allow", kind: "allow_once" },
      { option_id: "reject_once", name: "Reject", kind: "reject_once" },
    ],
    title: "ls -la",
    kind: "execute",
    ...over,
  };
}

function pushPerm(chatID: string, requestID: number, submit = vi.fn()): typeof submit {
  pushDecision({
    kind: "permission",
    chatID,
    requestID,
    payload: perm({ request_id: requestID }),
    submit,
  });
  return submit;
}

function clickButton(label: string): void {
  const btn = [...host().querySelectorAll<HTMLButtonElement>("button")].find(
    (b) => b.textContent === label,
  );
  btn?.click();
}

beforeEach(() => {
  _resetForTest();
  document.body.innerHTML = `<div id="decision-dock" class="hidden"></div>`;
  setSessions([session("c1"), session("c2")]);
  setActive("c1");
  mountDecisionDock(host());
});

describe("the dock's visibility", () => {
  it("stays hidden and empty until something asks", () => {
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
  });

  it("reveals on a decision and hides again once answered", () => {
    pushPerm("c1", 1);
    expect(host().classList.contains("hidden")).toBe(false);
    expect(host().querySelector(".dock-card")).not.toBeNull();

    clickButton("Allow");
    expect(host().classList.contains("hidden")).toBe(true);
    expect(host().children.length).toBe(0);
  });
});

describe("the queue", () => {
  it("shows one at a time and advances on answer, instead of losing the first", () => {
    const first = pushPerm("c1", 1);
    const second = pushPerm("c1", 2);

    // Only the head is rendered, and the depth line reports the rest.
    expect(host().querySelectorAll(".dock-card").length).toBe(1);
    expect(host().querySelector(".dock-depth")?.textContent).toBe("1 more waiting");

    clickButton("Allow");
    expect(first).toHaveBeenCalledWith("allow_once", undefined);
    expect(second).not.toHaveBeenCalled();

    // The second is now the head, and its depth line is empty.
    expect(host().querySelectorAll(".dock-card").length).toBe(1);
    expect(host().querySelector(".dock-depth")?.classList.contains("hidden")).toBe(true);
    clickButton("Reject");
    expect(second).toHaveBeenCalledWith("reject_once", undefined);
  });

  it("answers a request at most once", () => {
    const submit = pushPerm("c1", 1);
    const allow = [...host().querySelectorAll<HTMLButtonElement>("button")].find(
      (b) => b.textContent === "Allow",
    );
    // The card is detached by the first click; clicking its retained handle
    // again must not produce a second reply on the same request id.
    allow?.click();
    allow?.click();
    expect(submit).toHaveBeenCalledTimes(1);
  });

  it("ignores a re-delivered request (SSE reconnect replays unanswered asks)", () => {
    pushPerm("c1", 7);
    pushPerm("c1", 7);
    expect(host().querySelector(".dock-depth")?.classList.contains("hidden")).toBe(true);
  });
});

describe("per-chat routing", () => {
  it("holds a background chat's ask instead of dropping it, and shows it on switch", () => {
    const bg = pushPerm("c2", 1);
    // Nothing on screen: it is not this chat's ask.
    expect(host().classList.contains("hidden")).toBe(true);

    setActive("c2");
    expect(host().classList.contains("hidden")).toBe(false);
    clickButton("Allow");
    expect(bg).toHaveBeenCalledWith("allow_once", undefined);
  });

  it("keeps an unanswered ask across a switch away and back", () => {
    pushPerm("c1", 1);
    setActive("c2");
    expect(host().classList.contains("hidden")).toBe(true);
    setActive("c1");
    expect(host().querySelector(".dock-card")).not.toBeNull();
  });

  it("dropDecisions clears a chat's asks without answering them", () => {
    const submit = pushPerm("c1", 1);
    dropDecisions("c1");
    expect(host().classList.contains("hidden")).toBe(true);
    expect(submit).not.toHaveBeenCalled();
  });
});

describe("turn approval", () => {
  const files = [
    { path: "a.ts", action_id: "act-1" },
    { path: "b.ts", action_id: "act-2" },
  ];

  it("sends a decision for every offered action, because an omitted id is a rollback", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 5,
      payload: perm({ request_id: 5, title: "Review changes", files }),
      submit,
    });
    clickButton("Keep selected");
    expect(submit).toHaveBeenCalledWith("allow_once", { "act-1": true, "act-2": true });
  });

  it("an unchecked row becomes a false decision, not an absent one", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 5,
      payload: perm({ request_id: 5, title: "Review changes", files }),
      submit,
    });
    const boxes = host().querySelectorAll<HTMLInputElement>(".dock-file-check");
    expect(boxes.length).toBe(2);
    boxes[1]?.click(); // uncheck b.ts
    clickButton("Keep selected");
    expect(submit).toHaveBeenCalledWith("allow_once", { "act-1": true, "act-2": false });
  });

  it("groups files sharing one action id into a single undividable row", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 6,
      payload: perm({
        request_id: 6,
        title: "Review changes",
        // A multi-file semantic rename: KAS keys the decision map by action, so
        // these two paths cannot disagree.
        files: [
          { path: "old.py", action_id: "ren-1" },
          { path: "new.py", action_id: "ren-1" },
        ],
      }),
      submit,
    });
    expect(host().querySelectorAll(".dock-file-row").length).toBe(1);
    expect(host().querySelectorAll(".dock-file-check").length).toBe(1);
    expect(host().querySelector(".dock-file-atomic")).not.toBeNull();
    clickButton("Keep selected");
    expect(submit).toHaveBeenCalledWith("allow_once", { "ren-1": true });
  });

  it("Roll back all answers with the reject option and no map", () => {
    const submit = vi.fn();
    pushDecision({
      kind: "permission",
      chatID: "c1",
      requestID: 5,
      payload: perm({ request_id: 5, title: "Review changes", files }),
      submit,
    });
    clickButton("Roll back all");
    expect(submit).toHaveBeenCalledWith("reject_once", undefined);
  });
});
