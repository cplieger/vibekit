// ---------------------------------------------------------------------------
// The permission card's pointer at the workspace relaxation (D115 caller: "the
// permission relaxation (D103)").
//
// The whole reason this is a link and not a button that grants something: D103's
// third ruling is that the Settings pane is the ONLY writer, with no path where
// answering a permission prompt widens the policy as a side effect. So the tests
// below assert both halves — the pointer reaches the switch, and clicking it
// writes nothing.
// ---------------------------------------------------------------------------

import { vi, describe, it, expect, beforeEach } from "vitest";
import type { PermissionNeededPayload } from "./types.js";

const mocks = vi.hoisted(() => ({
  openSetting: vi.fn(),
  editDispatch: vi.fn(),
}));

vi.mock("./settings-highlight.js", () => ({ openSetting: mocks.openSetting }));
vi.mock("./actions/permissions.js", () => ({
  editNativeRule: { dispatch: mocks.editDispatch },
}));
vi.mock("./navigate.js", () => ({ openChange: vi.fn() }));

import { buildPermissionCard } from "./permission.js";

function ask(over: Partial<PermissionNeededPayload> = {}): PermissionNeededPayload {
  return {
    request_id: 1,
    title: "grep -rn foo",
    kind: "execute",
    options: [
      { option_id: "a", name: "Allow", kind: "allow_once" },
      { option_id: "r", name: "Reject", kind: "reject_once" },
    ],
    ...over,
  } as PermissionNeededPayload;
}

function pointer(card: HTMLElement): HTMLButtonElement | null {
  return card.querySelector<HTMLButtonElement>(".approval-policy-link");
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("the permission card's policy pointer", () => {
  it("opens the relaxation switch in Settings", () => {
    const card = buildPermissionCard("chat-1", ask(), vi.fn());
    const link = pointer(card);
    expect(link).not.toBeNull();
    link?.click();
    expect(mocks.openSetting).toHaveBeenCalledWith("permissions", "workspace-relax-checkbox");
  });

  // D103 ruling 3, asserted rather than assumed: the pointer NAVIGATES. It must
  // not answer the ask and must not write a rule — the user still has to switch
  // the relaxation on in Settings and clear its confirm there.
  it("neither answers the ask nor writes a policy rule", () => {
    const onSelect = vi.fn();
    const card = buildPermissionCard("chat-1", ask(), onSelect);
    pointer(card)?.click();
    expect(onSelect).not.toHaveBeenCalled();
    expect(mocks.editDispatch).not.toHaveBeenCalled();
  });

  it("sits outside the answer row, so it cannot read as a third option", () => {
    const card = buildPermissionCard("chat-1", ask(), vi.fn());
    const actions = card.querySelector(".approval-actions");
    expect(actions).not.toBeNull();
    expect(actions?.querySelector(".approval-policy-link")).toBeNull();
  });

  // A mode switch grants no capability, so the capability policy has nothing to
  // say about it and the pointer would be noise.
  it("is absent on a mode-switch card", () => {
    const card = buildPermissionCard(
      "chat-1",
      ask({ kind: "switch_mode", title: "spec" }),
      vi.fn(),
    );
    expect(pointer(card)).toBeNull();
  });

  // A turn approval is a review of writes that already landed, not a request for
  // a capability, so widening the policy would not remove it.
  it("is absent on a turn-approval card", () => {
    const card = buildPermissionCard(
      "chat-1",
      ask({ files: [{ path: "a.go", action_id: "act-1" }] }),
      vi.fn(),
    );
    expect(pointer(card)).toBeNull();
  });
});
