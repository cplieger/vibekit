// THE CARD'S AUTH ROW AND ITS SEPARATOR ARE ONE FACT, and a refused logout restores
// the VERDICT rather than the address.
//
// Two changes meet here. The row became an ERROR row: "signed in" said what the
// address on the trigger already says, so the `signed_in` arm renders EMPTY and the
// row hides — and the two arms left are exactly the ones that leave that address
// blank, so this row is what explains the blank. And the row's SEPARATOR now hides
// with it, because a hidden row above a visible rule leaves the card pointing at
// nothing; `setAuthLine` writes both, so no caller can forget one.
//
// The rollback arm is the one worth the most: `signed_out` and `unavailable` BOTH
// render an empty address, so an op carrying the address could not tell them apart
// and a refused logout from `unavailable` wrote "not signed in" where "unknown" had
// been. Carrying the whole verdict is what makes all three arms restorable.
//
// `settings.ts` is imported for real here — that is the module under test — so this
// file mocks the heavy side of its graph rather than the writer it is about.
import { vi, describe, it, expect, beforeEach } from "vitest";
import { resetActionFramework } from "./actions/__test-helpers__/action-test-setup.js";

// Everything `settings.ts` reaches that touches the network, the shell, the file
// browser or the chat view. None of it is this file's subject, and one of them
// resolves `#messages` at module scope.
vi.mock("./modals.js", () => ({ initAllModals: vi.fn(), showLoginModal: vi.fn() }));
// The COMPLETE tabs mock, spread. Browser Mode links ESM for real, so every name ANY
// module in this graph reaches has to exist — a partial factory works until the graph
// widens and then fails without naming what went wrong (see
// __test-helpers__/tabs-mock.ts).
vi.mock("./tabs.js", async () => ({
  ...(await import("./__test-helpers__/tabs-mock.js")).tabsMock(),
}));
vi.mock("./git-badge.js", () => ({ initGitBadge: vi.fn() }));
vi.mock("./git-tabs.js", () => ({ getGitTab: vi.fn(() => "changes") }));
vi.mock("./files.js", () => ({ noteDefaultBrowsePath: vi.fn() }));
vi.mock("./shell.js", () => ({ restoreShell: vi.fn() }));
vi.mock("./tools.js", () => ({ initTools: vi.fn(), loadToolsList: vi.fn() }));
vi.mock("./notify.js", () => ({ restoreNotifications: vi.fn() }));
vi.mock("./permissions-ui.js", () => ({
  initPermissionsUI: vi.fn(),
  initNativePolicyUI: vi.fn(),
  loadNativePolicy: vi.fn(),
}));
vi.mock("./mcp-ui.js", () => ({ initMCP: vi.fn() }));
vi.mock("./knowledge.js", () => ({ initKnowledge: vi.fn(), loadKnowledge: vi.fn() }));
vi.mock("./settings-steering.js", () => ({
  initSteeringEditor: vi.fn(),
  loadSteeringDoc: vi.fn(),
}));
vi.mock("./settings-notifications.js", () => ({ initNotificationToggles: vi.fn() }));
vi.mock("./api-client.js", () => ({ apiGet: vi.fn(async () => null), apiPost: vi.fn() }));
vi.mock("./actions/tools.js", () => ({ runDiagnostics: vi.fn() }));

vi.mock("./toast.js", () => import("./__test-helpers__/toast-mock.js").then((m) => m.toastMock()));

const mockFetch = vi.fn();

const { renderIdentity, currentIdentity } = await import("./settings.js");
const { logout } = await import("./actions/settings.js");

/** The card's three rows as `static/index.html` authors them. `#st-auth-sep` is the
 *  fixture element THIS file needs and `status-versions.test.ts` does not: `status.ts`
 *  never reaches `setAuthLine`, whose only callers are in `settings.ts`. */
function seedCard(): { row: HTMLElement; sep: HTMLElement; addr: HTMLElement } {
  document.body.innerHTML = `
    <button type="button" id="account-btn">
      <span id="status-dot"></span><span id="user-email"></span>
    </button>
    <span id="status-card">
      <span class="pill-detail" id="st-ws">-</span>
      <span class="pill-sep"></span>
      <span class="pill-detail" id="st-kiro">-</span>
      <span class="pill-sep" id="st-auth-sep" hidden></span>
      <span class="pill-detail" id="st-auth" hidden></span>
    </span>`;
  return {
    row: document.getElementById("st-auth") as HTMLElement,
    sep: document.getElementById("st-auth-sep") as HTMLElement,
    addr: document.getElementById("user-email") as HTMLElement,
  };
}

beforeEach(() => {
  // The action framework holds module state (the registry, in-flight ops), so it is
  // reset per case the way every other action test does — otherwise a dedupe or an
  // in-flight entry from an earlier case decides the next one.
  resetActionFramework();
  mockFetch.mockReset();
  vi.stubGlobal("fetch", mockFetch);
  seedCard();
});

describe("the row and its separator are one fact", () => {
  it("hides BOTH for signed_in, because the address already says it", () => {
    const { row, sep, addr } = seedCard();
    renderIdentity({ state: "signed_in", email: "someone@example.invalid" });
    expect(addr.textContent, "the address carries the statement").toBe("someone@example.invalid");
    expect(row.textContent, "the row says nothing").toBe("");
    expect(row.hidden, "so the row hides").toBe(true);
    expect(sep.hidden, "and its separator hides with it").toBe(true);
  });

  it.each([
    ["signed_out", { state: "signed_out" } as const, "not signed in"],
    ["unavailable", { state: "unavailable", reason: "whoami unreachable" } as const, "unknown"],
  ])("shows BOTH for %s, which is what explains the blank address", (_name, verdict, wording) => {
    const { row, sep, addr } = seedCard();
    renderIdentity(verdict);
    expect(addr.textContent, "these are exactly the arms that name nobody").toBe("");
    expect(row.textContent).toBe(wording);
    expect(row.hidden).toBe(false);
    expect(sep.hidden, "the separator appears with the row").toBe(false);
  });

  it("never leaves one visible without the other, across every transition", () => {
    // The invariant `setAuthLine` exists to hold, driven over every ordered pair of
    // arms rather than over one sequence: a second writer reintroduces this defect by
    // forgetting one element on ONE path.
    const { row, sep } = seedCard();
    const arms = [
      { state: "signed_in", email: "a@b.invalid" },
      { state: "signed_out" },
      { state: "unavailable", reason: "x" },
    ] as const;
    for (const from of arms) {
      for (const to of arms) {
        renderIdentity(from);
        renderIdentity(to);
        expect(
          sep.hidden,
          `${from.state} -> ${to.state}: the separator disagreed with the row`,
        ).toBe(row.hidden);
      }
    }
  });
});

describe("currentIdentity", () => {
  it("answers what renderIdentity last rendered", () => {
    // The export exists for the logout action's rollback, which receives the VALUE.
    // Nothing else retains the verdict: `boot.ts` and `app.ts` both call
    // `renderIdentity` and drop it, so before this the only record was the address
    // string in the DOM.
    seedCard();
    const v = { state: "signed_in", email: "someone@example.invalid" } as const;
    renderIdentity(v);
    expect(currentIdentity()).toEqual(v);
    const u = { state: "unavailable", reason: "whoami unreachable" } as const;
    renderIdentity(u);
    expect(currentIdentity()).toEqual(u);
  });
});

describe("logout through the REAL action", () => {
  it("leaves the row and its separator visible on success", async () => {
    mockFetch.mockResolvedValue(new Response(JSON.stringify({}), { status: 200 }));
    const { row, sep, addr } = seedCard();
    renderIdentity({ state: "signed_in", email: "someone@example.invalid" });

    await logout.dispatch({ render: renderIdentity, prev: currentIdentity() });

    expect(addr.textContent, "the address clears").toBe("");
    expect(row.textContent).toBe("not signed in");
    expect(row.hidden, "and the row is what explains the blank").toBe(false);
    expect(sep.hidden).toBe(false);
  });

  it("restores UNKNOWN, not 'not signed in', when a logout from unavailable is refused", async () => {
    // THE ARM THE ADDRESS-CARRYING OP COULD NOT EXPRESS. `signed_out` and
    // `unavailable` both render an empty address, so an op holding
    // `emailEl.textContent` restored `""` for either and the old rollback then guessed
    // "not signed in" from it — writing that where "unknown" had been.
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "nope" }), { status: 500 }));
    const { row, sep } = seedCard();
    renderIdentity({ state: "unavailable", reason: "whoami unreachable" });
    expect(row.textContent, "the premise: the row reads unknown").toBe("unknown");

    await logout.dispatch({ render: renderIdentity, prev: currentIdentity() });

    expect(row.textContent, "the refusal restores the verdict it replaced").toBe("unknown");
    expect(row.hidden).toBe(false);
    expect(sep.hidden).toBe(false);
    expect(currentIdentity().state).toBe("unavailable");
  });

  it("restores the SIGNED-IN verdict, address and all, on a refusal", async () => {
    // The third arm, so all three are covered and the case above cannot pass by
    // rollback simply doing nothing.
    mockFetch.mockResolvedValue(new Response(JSON.stringify({ error: "nope" }), { status: 500 }));
    const { row, sep, addr } = seedCard();
    renderIdentity({ state: "signed_in", email: "someone@example.invalid" });

    await logout.dispatch({ render: renderIdentity, prev: currentIdentity() });

    expect(addr.textContent).toBe("someone@example.invalid");
    expect(row.hidden, "signed_in hides the row again").toBe(true);
    expect(sep.hidden).toBe(true);
  });
});
