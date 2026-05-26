// ---------------------------------------------------------------------------
// Minimal reactive signals: signal, effect, batch.
// MessageChannel batching for streaming smoothness (React scheduler pattern).
// ---------------------------------------------------------------------------

type Subscriber = { execute(): void };
let tracking: Subscriber | null = null;
let batchDepth = 0;
const pending = new Set<Subscriber>();
const channel = new MessageChannel();
let scheduled = false;

channel.port1.onmessage = () => {
  scheduled = false;
  const fns = [...pending];
  pending.clear();
  for (const s of fns) s.execute();
};

function notify(subs: Set<Subscriber>): void {
  for (const s of subs) {
    if (batchDepth > 0) pending.add(s);
    else s.execute();
  }
}

function schedulePending(): void {
  if (pending.size === 0 || scheduled) return;
  scheduled = true;
  channel.port2.postMessage(null);
}

interface Signal<T> { value: T; peek(): T }

export function signal<T>(initial: T): Signal<T> {
  let val = initial;
  const subs = new Set<Subscriber>();
  return {
    get value(): T {
      if (tracking !== null) subs.add(tracking);
      return val;
    },
    set value(v: T) {
      if (Object.is(val, v)) return;
      val = v;
      notify(subs);
      if (batchDepth > 0) schedulePending();
    },
    peek(): T { return val; },
  };
}

export function effect(fn: () => void | (() => void)): () => void {
  let cleanup: (() => void) | void;
  const sub: Subscriber = {
    execute() {
      if (cleanup) cleanup();
      const prev = tracking;
      tracking = sub;
      cleanup = fn();
      tracking = prev;
    },
  };
  sub.execute();
  return () => { if (cleanup) cleanup(); };
}

export function batch(fn: () => void): void {
  batchDepth++;
  try { fn(); } finally {
    batchDepth--;
    if (batchDepth === 0) schedulePending();
  }
}
