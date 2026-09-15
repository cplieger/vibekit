// The SharedWorker entry: one stream per browser profile. Bundled by cmd/bundle as a
// classic script at a content-hashed URL, which the page bundle receives through
// `__SSE_WORKER_URL__` and constructs (`sse-adapter.ts`). Every tab of the profile
// attaches over the port this worker is handed on `connect`.

import { createSSEHost } from "./sse-worker-host.js";

// The DOM lib the app is type-checked under has no SharedWorkerGlobalScope; this is the
// one member the entry uses.
interface SharedScope {
  onconnect: ((event: MessageEvent) => void) | null;
}

const host = createSSEHost();

(globalThis as unknown as SharedScope).onconnect = (event) => {
  const port = event.ports[0];
  if (port !== undefined) {
    host.attach(port);
  }
};
