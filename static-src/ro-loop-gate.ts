// Fails the run on a `ResizeObserver loop completed with undelivered notifications`
// error: the engine defers the observations a resize callback's own write invalidated, so
// the affected suite still PASSES and no assertion looks for it. A `window` `error`
// listener because the engine reports the loop as an uncaught error and calls no callback
// for what it dropped; reported from `afterEach` because a listener throw names no test.
import { afterEach } from "vitest";

const seen: string[] = [];

/** Deliberately never removed: the gate is the whole run's, and a listener
 *  removed per file would stop covering the frames after the last test. */
window.addEventListener("error", (e) => {
  if (e.message.includes("ResizeObserver loop")) {
    seen.push(e.message);
  }
});

afterEach(() => {
  if (seen.length === 0) {
    return;
  }
  const messages = seen.join("; ");
  const count = seen.length;
  // Emptied BEFORE throwing, or one loop fails every remaining case in the file
  // and the report names the wrong test as often as the right one.
  seen.length = 0;
  throw new Error(
    `${String(count)} ResizeObserver loop error(s) during this file: ${messages}. ` +
      `A resize callback wrote something that produced an undeliverable observation — ` +
      `move the write out of the delivery (see scroll.ts's gutter observer).`,
  );
});
