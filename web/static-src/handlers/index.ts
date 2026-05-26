// ---------------------------------------------------------------------------
// Handler barrel: imports all SSE handler modules so a single
// `import "./handlers/index.js"` in app.ts wires every onSSE listener.
// Order is irrelevant — each module registers independently via onSSE.
// ---------------------------------------------------------------------------

import "./chat.js";
import "./messages.js";
import "./pending.js";
import "./turn.js";
import "./system.js";
