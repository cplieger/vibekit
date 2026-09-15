// The two-frame publish and the stamp it must not carry, against the REAL vibekit binary
// (Part IV item 15). One saved Mutate publishes a header frame (`chat_updated`, stamped
// `chats`) and then its transcript frames (`message_appended`, or `message_appended` then
// `draft_changed`), with the ONE `chat` stamp on the last of them. A client that loses the
// stream between the frames holds the transcript one message short, and must not hold a
// `chat` version that says otherwise.
//
// Each case arms the test binary's close-after hook so the stream dies between the frames,
// reconnects FRESH (cursor dropped first, so no replay can repair it), and requires the
// fresh hello's digest to answer `changed` for chat:X off the version the client held before
// the kill. A server that stamped the header frame `chat`, or every frame of one Mutate,
// answers `unchanged` here and leaves the client one message (or one stale draft) behind
// until the next unrelated mutation.
//
// The client is the library's stream over vibekit's envelope door and version map, the
// three pieces `sse-adapter.ts` composes minus the page: the page modules the adapter's
// loaders reach need the SPA's DOM, and the property under test is the server's stamping
// seen through the client's map, not the store. Node rather than the browser because the
// fixture answers a cross-site POST with 403 and sets no CORS header, so a page on vite's
// origin could neither command it nor read its stream.

import { afterEach, beforeEach, describe, expect, inject, it } from "vitest";

import {
  type DigestResult,
  type LifecycleEvent,
  type Stream,
  createDigestClient,
  createOnlineManager,
  createStream,
  createVisibilityManager,
} from "@cplieger/sse";

import { decodeEnvelope } from "../bus.js";
import { _resetForTest, observeStamp, versionMap } from "../subject-versions.js";
import type { ServerEvent } from "../types.js";
import { decodeSubjectStamp } from "../wire/decoders.gen.js";

const SKIP_REASON =
  "vibekit fixture not started: set SSE_FIXTURE to a vibekit binary built with -tags vibekit_test";
const FIXTURE = process.env["SSE_FIXTURE"];
if (FIXTURE === undefined || FIXTURE === "") {
  console.warn(`[vitest] ${SKIP_REASON}`);
}

/** Frames a FRESH connect writes ahead of any live frame: hello, connected, pending_snapshot,
 *  status_snapshot. The close-after hook counts data frames, hello included, so a live
 *  frame's ordinal is HOOK_FRAMES + its position. */
const HOOK_FRAMES = 4;

interface Client {
  readonly stream: Stream;
  readonly frames: ServerEvent[];
  readonly lifecycle: LifecycleEvent[];
  readonly digests: DigestResult[];
}

interface ChatRead {
  readonly version: string;
  readonly draft: string;
  readonly messageIDs: string[];
}

function until(predicate: () => boolean, what: string, timeoutMs = 10_000): Promise<void> {
  return new Promise((resolve, reject) => {
    const deadline = Date.now() + timeoutMs;
    const tick = (): void => {
      if (predicate()) {
        resolve();
        return;
      }
      if (Date.now() > deadline) {
        reject(new Error(`timed out waiting for ${what}`));
        return;
      }
      setTimeout(tick, 10);
    };
    tick();
  });
}

function messageID(): string {
  return `m-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

function subjects(states: readonly { kind: string; ref: string }[]): string[] {
  return states.map((s) => `${s.kind}:${s.ref}`);
}

describe.skipIf(FIXTURE === undefined || FIXTURE === "")("the two-frame publish", () => {
  const base = (): string => inject("vibekitURL");
  let client: Client | null = null;

  const unlisten = (): (() => void) => () => undefined;
  const visibility = createVisibilityManager({ visible: () => true, listen: unlisten });
  const online = createOnlineManager({ online: () => true, listen: unlisten });

  async function command(type: string, chatID: string, payload: unknown): Promise<unknown> {
    const res = await fetch(`${base()}/api/command`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ type, chat_id: chatID, payload }),
    });
    if (!res.ok) {
      throw new Error(`${type} answered ${String(res.status)}: ${await res.text()}`);
    }
    return res.json();
  }

  async function createChat(): Promise<string> {
    const body = (await command("create_chat", "", { name: "two-frame" })) as {
      chat: { id: string };
    };
    return body.chat.id;
  }

  /** `GET /api/chats/{id}`: the client's own load, observing the stamp as store-load does. */
  async function loadChat(id: string): Promise<ChatRead> {
    const res = await fetch(`${base()}/api/chats/${encodeURIComponent(id)}?limit=50`);
    const body = (await res.json()) as {
      subject?: unknown;
      draft?: unknown;
      messages?: { id: string }[];
    };
    const stamp = decodeSubjectStamp(body.subject);
    observeStamp(stamp);
    return {
      version: stamp.version,
      draft: typeof body.draft === "string" ? body.draft : "",
      messageIDs: (body.messages ?? []).map((m) => m.id),
    };
  }

  async function closeAfter(n: number): Promise<void> {
    const res = await fetch(`${base()}/api/test/sse/close-after`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ after: n }),
    });
    expect(res.status).toBe(204);
  }

  function connect(): Client {
    const frames: ServerEvent[] = [];
    const lifecycle: LifecycleEvent[] = [];
    const digests: DigestResult[] = [];
    const digest = createDigestClient({ url: `${base()}/api/sync`, timeoutMs: 10_000 });
    const versions = versionMap();
    const stream = createStream({
      url: `${base()}/api/events`,
      headers: { "SSE-Client": "e2e_two_frame_publish_" },
      versions,
      visibility,
      online,
      onFrame(frame) {
        const evt = decodeEnvelope(JSON.parse(frame.data));
        frames.push(evt);
        // The adapter's rule, with the chat always resident here.
        observeStamp(evt.subject);
      },
      onLifecycle(ev) {
        lifecycle.push(ev);
      },
      async revalidate(ctx) {
        const snapshot = versions.snapshot();
        if (ctx.full || snapshot.held.length === 0) {
          return;
        }
        digests.push(await digest.check(snapshot, ctx.signal));
      },
    });
    const c: Client = { stream, frames, lifecycle, digests };
    stream.start();
    return c;
  }

  function hellos(c: Client): Extract<LifecycleEvent, { kind: "hello" }>[] {
    return c.lifecycle.filter((e) => e.kind === "hello");
  }

  function dropped(c: Client): number {
    return c.lifecycle.filter((e) => e.kind === "state" && e.from === "open").length;
  }

  function hooksSeen(c: Client): number {
    return c.frames.filter((f) => f.type === "status_snapshot").length;
  }

  function heldVersion(id: string): string | undefined {
    return versionMap()
      .snapshot()
      .held.find((h) => h.kind === "chat" && h.ref === id)?.version;
  }

  /** Arms the cut for the NEXT connection, then makes this client's fresh reconnect be it. */
  async function reconnectArmed(c: Client, after: number): Promise<void> {
    const before = hellos(c).length;
    await closeAfter(after);
    c.stream.resetCursor();
    c.stream.reconnect();
    await until(() => hooksSeen(c) === before + 1, "the armed connection's hook");
  }

  /** Reconnects fresh and returns the digest that hello runs. */
  async function reconnectFresh(c: Client): Promise<DigestResult> {
    const before = c.digests.length;
    c.stream.resetCursor();
    c.stream.reconnect();
    await until(() => c.digests.length > before, "the fresh hello's digest");
    const hello = hellos(c).at(-1);
    expect(hello?.resumed).toBe(false);
    const verdict = c.digests.at(-1);
    if (verdict === undefined) {
      throw new Error("no digest");
    }
    return verdict;
  }

  beforeEach(() => {
    _resetForTest();
  });

  afterEach(() => {
    client?.stream.stop();
    client = null;
  });

  it("transcript variant: the stream dies after the header frame, the fresh digest says changed", async () => {
    client = connect();
    const c = client;
    await until(() => hooksSeen(c) === 1, "the first hook");

    const id = await createChat();
    await until(() => c.frames.some((f) => f.type === "chat_created"), "chat_created");
    const before = await loadChat(id);
    expect(before.messageIDs).toEqual([]);
    expect(heldVersion(id)).toBe(before.version);

    // Frame 5 is the header frame of the publish below; frame 6, the transcript frame, is
    // the write that fails.
    await reconnectArmed(c, HOOK_FRAMES + 1);
    const seenBefore = c.frames.length;
    const dropsBefore = dropped(c);

    // A prompt persists and broadcasts the user row before the bridge answers; the fixture's
    // kiro-cli never does, so this Mutate is the only chat:X mutation.
    const sent = messageID();
    await command("prompt", id, { text: "hello", message_id: sent });
    await until(() => dropped(c) === dropsBefore + 1, "the armed connection to be cut");

    const delivered = c.frames.slice(seenBefore).map((f) => f.type);
    expect(delivered).toContain("chat_updated");
    expect(delivered).not.toContain("message_appended");
    // The header frame carried `chats`, never `chat`: the held chat version is unmoved.
    expect(heldVersion(id)).toBe(before.version);

    const verdict = await reconnectFresh(c);
    expect(verdict.kind).toBe("ok");
    if (verdict.kind === "ok") {
      // chat:X moved; the header the client DID receive keeps `chats` current.
      expect(subjects(verdict.changed)).toEqual([`chat:${id}`]);
      expect(verdict.removed).toEqual([]);
    }
    // And the refetch lands the message the stream lost.
    const after = await loadChat(id);
    expect(after.messageIDs).toEqual([sent]);
    expect(after.version).not.toBe(before.version);
  });

  it("composer variant: device A holds B's draft, the stream dies before draft_changed", async () => {
    client = connect();
    const c = client;
    await until(() => hooksSeen(c) === 1, "the first hook");

    const id = await createChat();
    // Device B types; A loads the chat and holds the draft with its version.
    await command("set_draft", id, { text: "typed on B" });
    await until(() => c.frames.some((f) => f.type === "draft_changed"), "B's draft_changed");
    const before = await loadChat(id);
    expect(before.draft).toBe("typed on B");
    expect(heldVersion(id)).toBe(before.version);

    // Frame 5 is the header, frame 6 `message_appended` (unstamped, because a
    // `draft_changed` follows for the same Mutate), frame 7 `draft_changed`: the cut.
    await reconnectArmed(c, HOOK_FRAMES + 2);
    const seenBefore = c.frames.length;
    const dropsBefore = dropped(c);

    // B sends the prompt: the text leaves the composer and the draft is cleared in the
    // same Mutate.
    const sent = messageID();
    await command("prompt", id, { text: "typed on B", message_id: sent });
    await until(() => dropped(c) === dropsBefore + 1, "the armed connection to be cut");

    const delivered = c.frames.slice(seenBefore);
    expect(delivered.map((f) => f.type)).toContain("message_appended");
    expect(delivered.map((f) => f.type)).not.toContain("draft_changed");
    // The message_appended of a Mutate that also clears the draft carries NO stamp.
    const appended = delivered.find((f) => f.type === "message_appended");
    expect(appended?.subject).toBeUndefined();
    expect(heldVersion(id)).toBe(before.version);

    const verdict = await reconnectFresh(c);
    expect(verdict.kind).toBe("ok");
    if (verdict.kind === "ok") {
      expect(subjects(verdict.changed)).toEqual([`chat:${id}`]);
      expect(verdict.removed).toEqual([]);
    }
    // The refetch clears A's composer and lands the message.
    const after = await loadChat(id);
    expect(after.draft).toBe("");
    expect(after.messageIDs).toEqual([sent]);
    expect(after.version).not.toBe(before.version);
  });
});
