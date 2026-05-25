// ---------------------------------------------------------------------------
// Notification toggles: iOS detection, permission requests, sub-option
// visibility, push/unregister lifecycle. Extracted from settings.ts.
// ---------------------------------------------------------------------------

import {
  requestPermission, setNotificationsEnabled, areNotificationsEnabled,
  unregisterPush, isAgentFinishedEnabled, setAgentFinishedEnabled,
  isPermissionNeededEnabled, setPermissionNeededEnabled,
  setNotifyUICallback,
} from "./notify.js";
import { patchSettings } from "./persist.js";
import type { AppSettings } from "./persist.js";
import { $ } from "./dom.js";
import { isIOS, isStandalone } from "./platform.js";
import { bindLoadingState } from "./actions/index.js";

export function initNotificationToggles(): void {
  const notifyToggle = $.notifyToggle;
  const notifyHint = $.notifyHint;
  const notifySubOptions = $.notifySubOptions;
  const finishedToggle = $.notifyFinishedToggle;
  const permissionToggle = $.notifyPermissionToggle;

  // iOS Safari (non-PWA): Web Push is only available when the app is
  // added to the home screen. Disable the toggle and show a permanent
  // hint so users don't wonder why notifications don't work.
  if (isIOS && !isStandalone) {
    notifyToggle.checked = false;
    notifyToggle.disabled = true;
    notifyHint.textContent = "Push notifications require adding this app to your home screen. Tap the share button, then \"Add to Home Screen\".";
    notifyHint.classList.remove("hidden");
    return;
  }

  notifyToggle.checked = areNotificationsEnabled();
  finishedToggle.checked = isAgentFinishedEnabled();
  permissionToggle.checked = isPermissionNeededEnabled();
  bindLoadingState("notify.register_push", notifyToggle);

  const updateSub = (): void => {
    notifySubOptions.classList.toggle("hidden", !notifyToggle.checked);
  };

  setNotifyUICallback(() => {
    notifyToggle.checked = areNotificationsEnabled();
    finishedToggle.checked = isAgentFinishedEnabled();
    permissionToggle.checked = isPermissionNeededEnabled();
    updateSub();
    notifyHint.classList.add("hidden");
    if (notifyToggle.checked && "Notification" in window && Notification.permission === "denied") {
      notifyHint.textContent = "Notifications were blocked on this device. Allow them in your browser settings.";
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
      if (!permissionToggle.checked) {
        permissionToggle.checked = true;
        mutatedInputs.push(permissionToggle);
      }
      // Show sub-options optimistically.
      updateSub();
      // Defer in-memory state updates until PATCH succeeds (Bug 3 fix).
      void patchSettings({
        notifications_enabled: true,
        notify_agent_finished: true,
        notify_permission: true,
      }, ...mutatedInputs).then((r) => {
        if (r === null) {
          // Action framework handles input rollback; tear down any push
          // subscription that requestPermission may have started.
          setNotificationsEnabled(false);
          setAgentFinishedEnabled(false);
          setPermissionNeededEnabled(false);
          unregisterPush();
          updateSub();
        } else {
          setNotificationsEnabled(true);
          setAgentFinishedEnabled(true);
          setPermissionNeededEnabled(true);
          updateSub();
          // Only prompt for browser permission after server confirms enable.
          const hint = requestPermission();
          if (hint !== null) {
            notifyHint.textContent = hint;
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

  const onSubChange = (): void => {
    // Only register inputs that actually changed for rollback (Bug 2 fix).
    const mutatedInputs: HTMLInputElement[] = [];
    if (finishedToggle.checked !== isAgentFinishedEnabled()) mutatedInputs.push(finishedToggle);
    if (permissionToggle.checked !== isPermissionNeededEnabled()) mutatedInputs.push(permissionToggle);

    const patch: Partial<AppSettings> = {
      notify_agent_finished: finishedToggle.checked,
      notify_permission: permissionToggle.checked,
    };

    const bothOff = !finishedToggle.checked && !permissionToggle.checked;
    if (bothOff) {
      notifyToggle.checked = false;
      mutatedInputs.push(notifyToggle);
      patch.notifications_enabled = false;
    }

    void patchSettings(patch, ...mutatedInputs).then((r) => {
      if (r === null) {
        // Action framework rolls back the toggle inputs; no extra work needed.
      } else {
        setAgentFinishedEnabled(finishedToggle.checked);
        setPermissionNeededEnabled(permissionToggle.checked);
        if (bothOff) {
          setNotificationsEnabled(false);
          unregisterPush();
          updateSub();
        }
      }
    });
  };
  finishedToggle.addEventListener("change", onSubChange);
  permissionToggle.addEventListener("change", onSubChange);
}
