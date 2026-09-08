// Fails the run on a `ResizeObserver loop completed with undelivered
// notifications` error, for every suite in the browser project.
//
// That message is the ENGINE's own verdict that a resize callback wrote something
// which produced a new observation the same loop could not deliver — so it had to
// defer them, having invalidated style for whatever the write reached. It is a
// real defect wherever it appears, and it is exactly the class no assertion looks
// for: the affected suite still passes, because the deferred observations arrive
// on the next frame and the DOM ends up correct. `block-virtualization` printed
// this line on stderr for as long as the transcript's gutter write sat inside the
// resize delivery, and nothing failed.
//
// A `window` `error` listener rather than a wrapped `ResizeObserver`: the engine
// reports the loop as an uncaught error and never calls a callback for the
// observations it dropped, so the callback side cannot see it at all.
//
// The report is deferred to `afterEach` rather than thrown from the listener,
// which would surface as an unhandled error attributed to no test. A message that
// arrives after its own test has finished is still reported, by the next case's
// hook — late and named as such, rather than lost.
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
