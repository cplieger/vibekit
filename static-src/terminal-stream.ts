// ---------------------------------------------------------------------------
// Agent-terminal live stream → the tool card that spawned it.
//
// This module replaced agent-terminal.ts, which rendered agent commands as TABS
// in the shell panel. That was the wrong container and the app had the evidence:
// a terminal is created by a tool call, lives for that tool call and is released
// when the agent finishes reading it, so the tool call is the thing it belongs
// to. The panel, meanwhile, was global — its handler received the chat id and
// discarded it (`(_chatID, p) => createTab(...)`), so a command started by one
// chat appeared unlabelled while you read another — and it opened for nobody: no
// badge, no auto-open, only an aria-live announcement, which is why the feature
// could sit broken for a month without anyone noticing.
//
// Routing the same stream to the card means there is nothing to keep in sync.
// The output appears where the command appears, per chat by construction,
// visible without opening anything.
//
// terminal_created and terminal_exited carry no RENDERING here. The card already
// carries the command as its title and the outcome as its status glyph, and a
// second rendering of either would be a second thing to keep true. terminal_exited
// is still subscribed, for the one thing only it can say: that a terminal will
// send nothing more, which is when an unclaimed hold can be released.
// ---------------------------------------------------------------------------

import { onSSE } from "./bus.js";
import { appendTerminalChunk, forgetTerminal } from "./messages-tools.js";
import { registerCleanup } from "./actions/index.js";

export function initTerminalStream(): void {
  const unsubOutput = onSSE("terminal_output", (_chatID, p) => {
    appendTerminalChunk(p.terminal_id, p.data, p.spans ?? [], p.offset);
  });
  // terminal_exited is handled for ONE reason, and it is not rendering: it is
  // the only moment the server says a terminal will send nothing more, so it is
  // when an unclaimed hold can be released. A terminal that never links to a
  // card would otherwise keep its held chunks until the whole chat was disposed.
  // The outcome itself still comes from the card's own status glyph.
  const unsubExited = onSSE("terminal_exited", (_chatID, p) => {
    forgetTerminal(p.terminal_id);
  });
  registerCleanup(() => {
    unsubOutput();
    unsubExited();
  });
}
