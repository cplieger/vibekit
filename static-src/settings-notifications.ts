// ---------------------------------------------------------------------------
// Notification toggles: iOS detection, permission requests, sub-option
// visibility, push/unregister lifecycle. Extracted from settings.ts.
//
// There is ONE sub-option, not two. The permission-ask channel used to have its
// own off switch and it was removed: an ask blocks the turn and has no per-tab
// marker, so muting it stalled every later turn with no signal. See the "no
// notify_permission key" note in internal/settings/defaults.go for the floor,
// and Settings -> Permissions for the relaxation that replaced it.
// ---------------------------------------------------------------------------

import {
  requestPermission,
  setNotificationsEnabled,
  areNotificationsEnabled,
  unregisterPush,
  isAgentFinishedEnabled,
  setAgentFinishedEnabled,
  setNotifyUICallback,
} from "./notify.js";
import { patchSettings } from "./persist.js";
import { createDisclosure } from "@cplieger/ui-primitives/disclosure";
import { $ } from "./dom.js";
import { isIOS, isStandalone } from "./platform.js";
import { bindLoadingState, registerCleanup } from "./actions/index.js";

export function initNotificationToggles(): void {
  const notifyToggle = $.notifyToggle;
  const notifyHint = $.notifyHint;
  const notifySubOptions = $.notifySubOptions;
  const finishedToggle = $.notifyFinishedToggle;

  // iOS Safari (non-PWA): Web Push is only available when the app is
  // added to the home screen. Disable the toggle and show a permanent
  // hint so users don't wonder why notifications don't work.
  if (isIOS && !isStandalone) {
    notifyToggle.checked = false;
    notifyToggle.disabled = true;
    notifyHint.textContent =
      'Push notifications require adding this app to your home screen. Tap the share button, then "Add to Home Screen".';
    notifyHint.classList.remove("hidden");
    return;
  }

  notifyToggle.checked = areNotificationsEnabled();
  finishedToggle.checked = isAgentFinishedEnabled();
  bindLoadingState("notify.register_push", notifyToggle);
  const unbindPatch = bindLoadingState("settings.patch", notifyToggle, { preserveDisabled: true });
  registerCleanup(unbindPatch);

  // Listen for registration-failed events from the action layer to
  // roll back the toggle without coupling the action to this DOM element.
  const notifyAC = new AbortController();
  document.addEventListener(
    "notify:registration-failed",
    () => {
      notifyToggle.checked = false;
    },
    { signal: notifyAC.signal },
  );
  registerCleanup(() => {
    notifyAC.abort();
  });

  // Region-only disclosure (trigger: null) — the documented shape for a
  // checkbox-revealed section: the checkbox's checked state conveys the
  // collapse (no aria-expanded belongs on it), the primitive drives the
  // animated height + aria-hidden/inert. Normalize the authored hidden class.
  notifySubOptions.classList.remove("hidden");
  const subCtl = createDisclosure(null, notifySubOptions, { open: notifyToggle.checked });
  const updateSub = (): void => {
    if (notifyToggle.checked) {
      subCtl.open();
    } else {
      subCtl.close();
    }
  };

  setNotifyUICallback(() => {
    notifyToggle.checked = areNotificationsEnabled();
    finishedToggle.checked = isAgentFinishedEnabled();
    updateSub();
    notifyHint.classList.add("hidden");
    if (notifyToggle.checked && "Notification" in window && Notification.permission === "denied") {
      notifyHint.textContent =
        "Notifications were blocked on this device. Allow them in your browser settings.";
      notifyHint.classList.remove("hidden");
    }
  });

  // Set initial sub-option visibility based on current toggle state.
  updateSub();

  notifyToggle.addEventListener("change", () => {
    notifyHint.classList.add("hidden");
    if (notifyToggle.checked) {
      // Capture which sub-toggles actually changed so we only register
      // them for rollback if they were mutated (Bug 2 fix).
      const mutatedInputs: HTMLInputElement[] = [notifyToggle];
      if (!finishedToggle.checked) {
        finishedToggle.checked = true;
        mutatedInputs.push(finishedToggle);
      }
      // Show sub-options optimistically.
      updateSub();
      // Defer in-memory state updates until PATCH succeeds (Bug 3 fix).
      void patchSettings(
        {
          notifications_enabled: true,
          notify_agent_finished: true,
        },
        ...mutatedInputs,
      ).then((r) => {
        if (r === null) {
          // Action framework handles input rollback; tear down any push
          // subscription that requestPermission may have started.
          setNotificationsEnabled(false);
          setAgentFinishedEnabled(false);
          unregisterPush();
          updateSub();
        } else {
          setNotificationsEnabled(true);
          setAgentFinishedEnabled(true);
          updateSub();
          // Only prompt for browser permission after server confirms enable.
          const hint = requestPermission();
          if (hint !== null) {
            notifyHint.textContent = hint;
            notifyHint.classList.remove("hidden");
          } else if ("Notification" in window && Notification.permission !== "granted") {
            notifyHint.textContent =
              "Browser permission is pending. You may need to allow notifications when prompted.";
            notifyHint.classList.remove("hidden");
          }
        }
      });
    } else {
      void patchSettings({ notifications_enabled: false }, notifyToggle).then((r) => {
        if (r === null) {
          // Action framework rolls back the toggle input; no extra work needed.
        } else {
          setNotificationsEnabled(false);
          unregisterPush();
          updateSub();
        }
      });
    }
  });

  finishedToggle.addEventListener("change", () => {
    if (finishedToggle.checked === isAgentFinishedEnabled()) {
      // Nothing actually changed — skip the server round-trip.
      return;
    }
    // With one sub-option left, switching it off IS switching notifications
    // off: there is no second channel for the master toggle to keep alive.
    const allOff = !finishedToggle.checked;
    const mutatedInputs: HTMLInputElement[] = [finishedToggle];
    if (allOff) {
      notifyToggle.checked = false;
      mutatedInputs.push(notifyToggle);
    }
    void patchSettings(
      allOff
        ? { notify_agent_finished: false, notifications_enabled: false }
        : { notify_agent_finished: true },
      ...mutatedInputs,
    ).then((r) => {
      if (r === null) {
        // Action framework rolls back the toggle inputs; no extra work needed.
      } else {
        setAgentFinishedEnabled(finishedToggle.checked);
        if (allOff) {
          setNotificationsEnabled(false);
          unregisterPush();
          updateSub();
        }
      }
    });
  });
}
