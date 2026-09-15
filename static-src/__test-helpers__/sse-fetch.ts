// A scripted `fetch` for the SSE adapter's tests: every `/api/events` GET becomes a
// stream the test writes frames into, and every other request is answered by a
// handler the test installs. Installed with `vi.stubGlobal("fetch", scripted.fetch)`,
// which the standard `unstubGlobals` reverses; the library and `@cplieger/fetch` both
// read `globalThis.fetch` at call time, so nothing needs injecting.

/** One open `/api/events` connection, driven frame by frame. */
interface SSEConnection {
  readonly url: string;
  readonly headers: Headers;
  /** Write the hello that opens the stream. Defaults spell a fresh hello at offset 0. */
  hello(over?: Partial<HelloFields>): void;
  /** Write one application frame: the envelope as JSON, unnamed, with an optional id. */
  frame(envelope: unknown, id?: string): void;
  /** Write one named frame verbatim (the keepalive, a reset). */
  named(event: string, data: string): void;
  /** End the body, as a server dropping the connection would. */
  close(): void;
  /** Whether the client aborted this request. */
  aborted(): boolean;
}

interface HelloFields {
  wire: number;
  epoch: string;
  floor: string;
  head: string;
  resumed: boolean;
  verdict: string;
  keepalive_ms: number;
  keepalive_event: string;
}

/** One request the scripted fetch answered from a handler. */
interface ScriptedRequest {
  readonly url: string;
  readonly method: string;
  readonly headers: Headers;
  readonly body: string | null;
  readonly signal: AbortSignal | null | undefined;
}

type Responder = (req: ScriptedRequest) => Response | Promise<Response>;

export interface ScriptedFetch {
  readonly fetch: typeof fetch;
  /** Every `/api/events` connection, oldest first. */
  readonly connections: SSEConnection[];
  /** Every non-stream request, oldest first. */
  readonly requests: ScriptedRequest[];
  /** Answer requests whose URL starts with `prefix`. Later installs win. */
  respond(prefix: string, responder: Responder): void;
}

export const EPOCH_A = "0123456789abcdef";
export const EPOCH_B = "fedcba9876543210";

const encoder = new TextEncoder();

function requestURL(input: RequestInfo | URL): string {
  if (typeof input === "string") {
    return input;
  }
  if (input instanceof URL) {
    return input.toString();
  }
  return input.url;
}

function requestHeaders(input: RequestInfo | URL, init: RequestInit | undefined): Headers {
  if (input instanceof Request) {
    return new Headers(input.headers);
  }
  return new Headers(init?.headers);
}

async function requestBody(
  input: RequestInfo | URL,
  init: RequestInit | undefined,
): Promise<string | null> {
  if (init?.body !== undefined && init.body !== null) {
    return typeof init.body === "string" ? init.body : String(init.body);
  }
  if (input instanceof Request && input.body !== null) {
    return input.text();
  }
  return null;
}

/** A JSON 200 for a responder. */
export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

export function createScriptedFetch(): ScriptedFetch {
  const connections: SSEConnection[] = [];
  const requests: ScriptedRequest[] = [];
  const responders: { prefix: string; responder: Responder }[] = [];

  function openStream(url: string, headers: Headers, signal: AbortSignal | null | undefined) {
    let controller: ReadableStreamDefaultController<Uint8Array> | null = null;
    let closed = false;
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        controller = c;
      },
      cancel() {
        closed = true;
      },
    });
    const write = (text: string): void => {
      if (closed || controller === null) {
        return;
      }
      controller.enqueue(encoder.encode(text));
    };
    const conn: SSEConnection = {
      url,
      headers,
      hello(over = {}) {
        const fields: HelloFields = {
          wire: 1,
          epoch: EPOCH_A,
          floor: "0",
          head: "0",
          resumed: false,
          verdict: "fresh",
          keepalive_ms: 15_000,
          keepalive_event: "heartbeat",
          ...over,
        };
        write(`retry: 1500\nevent: sse:hello\ndata: ${JSON.stringify(fields)}\n\n`);
      },
      frame(envelope, id) {
        const idLine = id === undefined ? "" : `id: ${id}\n`;
        write(`${idLine}data: ${JSON.stringify(envelope)}\n\n`);
      },
      named(event, data) {
        write(`event: ${event}\ndata: ${data}\n\n`);
      },
      close() {
        if (!closed && controller !== null) {
          closed = true;
          controller.close();
        }
      },
      aborted: () => signal?.aborted === true,
    };
    signal?.addEventListener("abort", () => {
      closed = true;
    });
    connections.push(conn);
    return new Response(body, { status: 200, headers: { "content-type": "text/event-stream" } });
  }

  const scripted: typeof fetch = async (input, init) => {
    const url = requestURL(input);
    const headers = requestHeaders(input, init);
    const signal = init?.signal ?? (input instanceof Request ? input.signal : undefined);
    const method = init?.method ?? (input instanceof Request ? input.method : "GET");
    // The stream is the GET on the events path; the acknowledgement POST beneath it
    // (/api/events/alive) is an ordinary recorded request.
    if (method === "GET" && url.split("?")[0] === "/api/events") {
      return openStream(url, headers, signal);
    }
    const req: ScriptedRequest = {
      url,
      method,
      headers,
      body: await requestBody(input, init),
      signal,
    };
    requests.push(req);
    for (let i = responders.length - 1; i >= 0; i--) {
      const entry = responders[i];
      if (entry !== undefined && url.startsWith(entry.prefix)) {
        return entry.responder(req);
      }
    }
    return new Response("not scripted", { status: 404 });
  };

  return {
    fetch: scripted,
    connections,
    requests,
    respond(prefix, responder) {
      responders.push({ prefix, responder });
    },
  };
}

/** Yield to the event loop until `predicate` holds or `tries` macrotasks have passed.
 *  Stream bytes cross a `ReadableStream` reader and a `TextDecoder`, so a written frame
 *  reaches `onFrame` a few ticks later; a poll on the product's own output keeps the
 *  test free of a sleep. The budget is wall-clock, not a tick count: under a loaded
 *  host a macrotask tick can take long enough that 200 of them pass before the
 *  stream's own microtasks drain, which read as a failed predicate. */
export async function until(predicate: () => boolean, timeoutMs = 5_000): Promise<void> {
  const deadline = performance.now() + timeoutMs;
  while (!predicate()) {
    if (performance.now() >= deadline) {
      throw new Error("until: predicate never held");
    }
    await new Promise<void>((resolve) => {
      setTimeout(resolve, 0);
    });
  }
}
