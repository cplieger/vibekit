// @vitest-environment happy-dom
import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("./api-client.js", () => ({
  apiGet: vi.fn(),
  apiPost: vi.fn(() => Promise.resolve(null)),
  apiPut: vi.fn(() => Promise.resolve(null)),
  apiDelete: vi.fn(() => Promise.resolve(null)),
}));

vi.mock("./confirm.js", () => ({
  confirm: vi.fn(() => Promise.resolve(true)),
}));

import { renderForgesPanel } from "./forge-auth.js";
import { apiGet, apiPost, apiDelete } from "./api-client.js";
import { confirm as confirmDialog } from "./confirm.js";

const mockedApiGet = vi.mocked(apiGet);
const mockedApiPost = vi.mocked(apiPost);
const mockedApiDelete = vi.mocked(apiDelete);
const mockedConfirm = vi.mocked(confirmDialog);

function setupDOM(): void {
  document.body.innerHTML = `<div id="forges-panel"></div>`;
}

function panel(): HTMLElement {
  return document.getElementById("forges-panel") as HTMLElement;
}

describe("forge-auth: 4-section layout", () => {
  beforeEach(() => {
    setupDOM();
    vi.clearAllMocks();
  });

  it("renders one section per supported forge kind", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const sections = panel().querySelectorAll<HTMLElement>(".forge-kind-section");
    expect(sections.length).toBe(4);
    const kinds = [...sections].map((s) => s.dataset["kind"]);
    expect(kinds).toEqual(["github", "gitlab", "codeberg", "gitea"]);
  });

  it("each section renders an Add account button (no empty-state filler)", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    for (const s of panel().querySelectorAll<HTMLElement>(".forge-kind-section")) {
      // Empty sections render NO "no accounts connected" filler;
      // the section is just header + the + button.
      expect(s.querySelector(".forge-account-empty")).toBeNull();
      expect(s.querySelector("[data-forge-add]")).not.toBeNull();
    }
  });

  it("clicking the + button opens a unified add pane (no separate Add-a-PAT button)", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    // No section should expose a separate Add-a-PAT trigger any more.
    for (const k of ["github", "gitlab", "codeberg", "gitea"]) {
      const pat = panel().querySelector(`.forge-kind-section[data-kind='${k}'] [data-forge-add-pat]`);
      expect(pat, `${k} should not have a separate Add-a-PAT button`).toBeNull();
    }
  });

  it("renders one slim row per connected account, prefers email over username", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        { id: "github:github.com", kind: "github", host: "github.com", username: "alice", email: "alice@example.com", connected: true },
        { id: "gitlab:gitlab.com", kind: "gitlab", host: "gitlab.com", username: "bob", connected: true },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();

    const ghRow = panel().querySelector<HTMLElement>(".forge-kind-section[data-kind='github'] .forge-account-row");
    expect(ghRow).not.toBeNull();
    expect(ghRow!.querySelector(".forge-account-primary")?.textContent).toBe("alice@example.com");
    expect(ghRow!.querySelector(".forge-account-meta")?.textContent).toContain("@alice");
    expect(ghRow!.querySelector(".forge-account-meta")?.textContent).toContain("github.com");

    const glRow = panel().querySelector<HTMLElement>(".forge-kind-section[data-kind='gitlab'] .forge-account-row");
    expect(glRow!.querySelector(".forge-account-primary")?.textContent).toBe("bob");
  });

  it("each account row has a Manage link and a Sign out button", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        { id: "github:github.com", kind: "github", host: "github.com", username: "alice", email: "a@x.io", connected: true },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const row = panel().querySelector<HTMLElement>(".forge-account-row")!;
    const manage = row.querySelector<HTMLAnchorElement>(".forge-account-manage");
    const signOut = [...row.querySelectorAll<HTMLButtonElement>("button")]
      .find((b) => b.textContent === "Sign out");
    expect(manage).not.toBeNull();
    expect(manage!.href).toContain("github.com");
    expect(manage!.href).toContain("settings/profile");
    expect(signOut).toBeDefined();
  });

  it("shows error styling and last_error text on a disconnected account", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        { id: "github:github.com", kind: "github", host: "github.com", username: "alice", connected: false, last_error: "token expired" },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const row = panel().querySelector<HTMLElement>(".forge-account-row")!;
    expect(row.classList.contains("forge-account-row-error")).toBe(true);
    expect(row.querySelector(".forge-account-error")?.textContent).toBe("token expired");
  });

  it("renders a list error message when /api/forges fails", async () => {
    mockedApiGet.mockResolvedValueOnce(null);
    await renderForgesPanel();
    expect(panel().querySelector(".forge-error")).not.toBeNull();
  });

  it("renders an SVG icon (not a letter) in each kind badge", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    for (const k of ["github", "gitlab", "codeberg", "gitea"]) {
      const badge = panel().querySelector<HTMLElement>(`.forge-kind-section[data-kind='${k}'] .forge-kind-badge`);
      expect(badge).not.toBeNull();
      const svg = badge!.querySelector("svg");
      expect(svg, `${k} badge should contain an svg`).not.toBeNull();
      expect(svg!.getAttribute("viewBox")).toBe("0 0 24 24");
      // No leftover letter text inside the badge.
      expect(badge!.textContent?.trim() ?? "").toBe("");
    }
  });

  it("PAT form hides the host field for github + codeberg, shows it for gitlab + gitea", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    // Open the unified add pane on every section via the single
    // "+" button. The pane includes a PAT form for every kind.
    for (const k of ["github", "gitlab", "codeberg", "gitea"]) {
      const add = panel().querySelector<HTMLButtonElement>(`.forge-kind-section[data-kind='${k}'] [data-forge-add]`);
      add!.click();
    }
    const inputs: Record<string, HTMLInputElement | null> = {};
    for (const k of ["github", "gitlab", "codeberg", "gitea"]) {
      const form = panel().querySelector<HTMLFormElement>(`.forge-kind-section[data-kind='${k}'] form.forge-pat-form`);
      expect(form, `${k} PAT form should be open`).not.toBeNull();
      inputs[k] = form!.querySelector<HTMLInputElement>("input");
    }
    expect(inputs["github"]!.type).toBe("hidden");
    expect(inputs["github"]!.value).toBe("github.com");
    expect(inputs["codeberg"]!.type).toBe("hidden");
    expect(inputs["codeberg"]!.value).toBe("codeberg.org");
    expect(inputs["gitlab"]!.type).toBe("text");
    expect(inputs["gitea"]!.type).toBe("text");
    expect(inputs["gitea"]!.value).toBe("");
    expect(inputs["gitea"]!.placeholder).toBe("your-host.example.com");
  });

  it("the gitea section is labeled 'Gitea / Forgejo'", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const title = panel().querySelector(".forge-kind-section[data-kind='gitea'] .forge-kind-title")?.textContent;
    expect(title).toBe("Gitea / Forgejo");
  });

  it("does not duplicate @username in meta when the primary line is already the username", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        // Account with no email — primary should be the username, meta should NOT add @cplieger.
        { id: "github:github.com", kind: "github", host: "github.com", username: "cplieger", connected: true },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const row = panel().querySelector<HTMLElement>(".forge-account-row")!;
    expect(row.querySelector(".forge-account-primary")?.textContent).toBe("cplieger");
    const meta = row.querySelector(".forge-account-meta")?.textContent ?? "";
    expect(meta).not.toContain("@cplieger");
    expect(meta).toBe("github.com");
  });

  it("clicking 'Add account' twice toggles the form open then closed", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    const section = panel().querySelector<HTMLElement>(".forge-kind-section[data-kind='gitlab']")!;
    const btn = section.querySelector<HTMLButtonElement>("[data-forge-add]")!;
    const slot = section.querySelector<HTMLElement>("[data-forge-slot]")!;
    btn.click();
    expect(slot.querySelector("form.forge-pat-form"), "first click opens").not.toBeNull();
    expect(slot.dataset["mode"]).toBe("add");
    btn.click();
    expect(slot.querySelector("form.forge-pat-form"), "second click closes").toBeNull();
    expect(slot.dataset["mode"]).toBeUndefined();
  });

  it("re-probes connected accounts in the background on page open", async () => {
    // Initial: two connected accounts.
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        { id: "github:github.com",   kind: "github",   host: "github.com",   username: "alice", connected: true },
        { id: "codeberg:codeberg.org", kind: "codeberg", host: "codeberg.org", username: "bob",   connected: true },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    // Post-probe re-fetch — only need to return so the re-paint is reachable.
    mockedApiGet.mockResolvedValueOnce({
      forges: [],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    // Allow the void revalidateInBackground microtasks to flush.
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

    // Two probes fired (one per connected account), pointed at the right URLs.
    const probeCalls = mockedApiPost.mock.calls.filter(
      ([path]) => typeof path === "string" && path.includes("/probe"),
    );
    expect(probeCalls.length).toBe(2);
    expect(probeCalls.some(([p]) => (p as string).includes("github%3Agithub.com"))).toBe(true);
    expect(probeCalls.some(([p]) => (p as string).includes("codeberg%3Acodeberg.org"))).toBe(true);
  });

  it("does not probe disconnected accounts on page open", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        { id: "github:github.com", kind: "github", host: "github.com", username: "alice", connected: false, last_error: "expired" },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    await renderForgesPanel();
    await Promise.resolve(); await Promise.resolve();

    const probeCalls = mockedApiPost.mock.calls.filter(
      ([path]) => typeof path === "string" && path.includes("/probe"),
    );
    expect(probeCalls.length).toBe(0);
  });

  it("sign-out uses the custom styled confirm dialog (not the native popup)", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        { id: "github:github.com", kind: "github", host: "github.com", username: "alice", email: "a@x.io", connected: true },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    // Empty re-fetch after delete.
    mockedApiGet.mockResolvedValue({ forges: [], kinds: ["github", "gitlab", "codeberg", "gitea"] });
    mockedConfirm.mockResolvedValueOnce(true);

    await renderForgesPanel();
    const signOutBtn = [...panel().querySelectorAll<HTMLButtonElement>(".forge-account-row button")]
      .find((b) => b.textContent === "Sign out")!;
    signOutBtn.click();
    // Allow handler microtasks to run.
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

    expect(mockedConfirm).toHaveBeenCalled();
    const [msg, label, variant] = mockedConfirm.mock.calls[0]!;
    expect(msg).toContain("Sign out of a@x.io");
    expect(label).toBe("Sign out");
    expect(variant).toBe("destructive");
    expect(mockedApiDelete).toHaveBeenCalledWith("/api/forges/github%3Agithub.com");
  });

  it("sign-out cancellation does not delete", async () => {
    mockedApiGet.mockResolvedValueOnce({
      forges: [
        { id: "github:github.com", kind: "github", host: "github.com", username: "alice", connected: true },
      ],
      kinds: ["github", "gitlab", "codeberg", "gitea"],
    });
    mockedConfirm.mockResolvedValueOnce(false);
    mockedApiDelete.mockClear();

    await renderForgesPanel();
    const signOutBtn = [...panel().querySelectorAll<HTMLButtonElement>(".forge-account-row button")]
      .find((b) => b.textContent === "Sign out")!;
    signOutBtn.click();
    await Promise.resolve(); await Promise.resolve(); await Promise.resolve();

    expect(mockedConfirm).toHaveBeenCalled();
    expect(mockedApiDelete).not.toHaveBeenCalled();
  });
});
