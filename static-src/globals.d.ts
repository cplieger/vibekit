// Build-time constants esbuild's Define injects into the page bundle (cmd/bundle).

/** The content-hashed URL path of the SSE worker script (`/chunks/sse-worker-<hash>.js`),
 *  learned from the worker build that runs before the page build. The empty string
 *  means the build shipped no worker, and the page runs the per-tab stream. */
declare const __SSE_WORKER_URL__: string;
