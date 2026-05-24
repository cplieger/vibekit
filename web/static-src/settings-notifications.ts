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
import { $ } from "./dom.js";
import { isIOS, isStandalone } from "./platform.js";

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

  notifyToggle.addEventListener("change", () => {
    setNotificationsEnabled(notifyToggle.checked);
    notifyHint.classList.add("hidden");
    if (notifyToggle.checked) {
      finishedToggle.checked = true;
      permissionToggle.checked = true;
      setAgentFinishedEnabled(true);
      setPermissionNeededEnabled(true);
      // Pass all 3 toggles as inputs so a failed PATCH rolls all of
      // them back together (the multi-key rollback fix).
      void patchSettings({
        notifications_enabled: true,
        notify_agent_finished: true,
        notify_permission: true,
      }, notifyToggle);
      // Remaining toggles get registered too via subsequent calls
      // (patchSettings dedups inputs across the debounce window).
      void patchSettings({}, finishedToggle);
      void patchSettings({}, permissionToggle);
      const hint = requestPermission();
      if (hint !== null) {
        notifyHint.textContent = hint;
        notifyHint.classList.remove("hidden");
      }
    } else {
      void patchSettings({ notifications_enabled: false }, notifyToggle);
      unregisterPush();
    }
    updateSub();
  });

  const onSubChange = (): void => {
    setAgentFinishedEnabled(finishedToggle.checked);
    setPermissionNeededEnabled(permissionToggle.checked);
    void patchSettings({
      notify_agent_finished: finishedToggle.checked,
      notify_permission: permissionToggle.checked,
    }, finishedToggle);
    void patchSettings({}, permissionToggle);
    if (!finishedToggle.checked && !permissionToggle.checked) {
      notifyToggle.checked = false;
      setNotificationsEnabled(false);
      void patchSettings({ notifications_enabled: false }, notifyToggle);
      unregisterPush();
      updateSub();
    }
  };
  finishedToggle.addEventListener("change", onSubChange);
  permissionToggle.addEventListener("change", onSubChange);
}
