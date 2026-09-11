// ---------------------------------------------------------------------------
// WHEN may this app ask the browser for notification permission, and what does a
// grant lead to. Ported from `@cplieger/web-terminal-ui`'s `features/tabs/notify.ts`
// arm/gesture model, with the one thing vibekit has that the terminal does not: a
// settings switch behind the browser's own permission.
//
// THE PROMPT NEEDS A USER GESTURE AND THERE IS NO WAY AROUND IT. Firefox 72+ and
// Safari refuse `Notification.requestPermission()` outside user activation, and MDN
// asks every caller to raise it from a gesture; Chrome tolerates a gesture-less
// prompt and penalises the origin for it. So the moment a notification WOULD have
// fired cannot itself raise the prompt — the page is usually hidden then, which is
// `notifyIfHidden`'s own precondition. The model is therefore two-part: that moment
// ARMS the ask, and the reader's next click SPENDS it.
//
// The cost is the reference's, stated the same way: the first notification of a
// session cannot be shown, because the answer has not arrived yet. Nothing is lost
// beyond the OS surface — `attention.ts` already carries the same cue on the
// document title, the app badge and the favicon.
//
// DOM-FREE by construction, like `attention.ts`: every capability arrives through
// `NotifyAskEnv`, so the decisions here are driven in `notify-ask.node.test.ts` with
// no browser permission model at all, and `notify.ts` is the one place that binds
// them to globals.
// ---------------------------------------------------------------------------

/** The capabilities the ask needs, all injected.
 *
 *  `permission` is read as a plain string rather than the DOM's
 *  `NotificationPermission` union for the reference's reason: it comes from a browser
 *  that may report something newer, and a value we do not recognise must degrade
 *  rather than be asserted. */
export interface NotifyAskEnv {
  /** Does this browser have the Notification API at all? False on iOS Safari
   *  outside an installed web app, and in a non-secure context. */
  supported: () => boolean;
  /** `"default"` | `"granted"` | `"denied"`, or anything else. */
  permission: () => string;
  /** Raise the prompt. Must be called from a user gesture. Resolves to the
   *  permission the browser settled on. */
  request: () => Promise<string>;
  /** Has this DEVICE already had its automatic ask? */
  spent: () => boolean;
  /** Record that it has. */
  markSpent: () => void;
  /** The browser granted permission: adopt it (turn the switch on, subscribe). */
  granted: () => void;
}

export interface NotifyAsk {
  /** Note that the app wanted to notify and could not. The only thing that makes a
   *  prompt worth raising at all — an app that has never had a cue to deliver is one
   *  whose user can only answer the question wrongly. */
  arm(): void;
  /** Note a user gesture: the ONLY moment a prompt may be raised. */
  gesture(): void;
  /** Record that this device's ask is answered, without raising anything. The door
   *  for every answer that arrives some other way — the Settings toggle going on
   *  (which raises the prompt itself) or going off (which is a refusal). */
  spend(): void;
}

/** createNotifyAsk holds the two flags the model needs and nothing else.
 *
 *  ONE ASK PER DEVICE, not one per page, and that is the deliberate divergence from
 *  the reference. There it is once per page because permission is the only gate, so
 *  re-asking on the next load is the only way a user ever gets notifications. Here
 *  Settings is a standing door to the same prompt, so an automatic ask that returns
 *  on every reload would be a nag with a recovery path already on screen. A
 *  dismissed prompt leaves `permission` at `"default"`, which is exactly the state a
 *  page-scoped flag would re-ask from, and browsers already penalise that.
 *
 *  The marker also carries the one distinction the wire cannot: `notifications_enabled`
 *  reaches the client as a resolved boolean, so "never opted in" and "explicitly
 *  switched off" are the same value there. Spending the ask when the switch goes OFF
 *  is what stops a later grant quietly overriding that refusal. */
export function createNotifyAsk(env: NotifyAskEnv): NotifyAsk {
  let armed = false;
  let requested = false;

  /** Every gate, re-read. Called at both ends because time passes between them: a
   *  sibling tab can be granted or denied while this one waits for a click. */
  function askable(): boolean {
    return env.supported() && env.permission() === "default" && !env.spent();
  }

  return {
    arm(): void {
      if (armed || requested || !askable()) {
        return;
      }
      armed = true;
    },
    gesture(): void {
      if (!armed || requested) {
        return;
      }
      if (!askable()) {
        armed = false;
        return;
      }
      // Both flags before the call, for the same reason: a prompt that was raised is
      // raised whatever comes back, including a browser that throws rather than be
      // asked, which will throw again on the next click.
      requested = true;
      armed = false;
      env.markSpent();
      void (async () => {
        let answer: string;
        try {
          answer = await env.request();
        } catch {
          return; // a browser that refuses to be asked is a badge-and-title browser
        }
        // Outside the try, so a throw from the adoption is not read as a refused prompt.
        if (answer === "granted") {
          env.granted();
        }
      })();
    },
    spend(): void {
      armed = false;
      requested = true;
      env.markSpent();
    },
  };
}
