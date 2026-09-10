import assert from "node:assert/strict";
import test from "node:test";

import { initBackupSettings } from "../static/js/backup-settings.js";

test("backup controls follow the daily backup toggle", () => {
  const listeners = {};
  const toggle = {
    checked: false,
    addEventListener(type, listener) { listeners[`toggle:${type}`] = listener; },
  };
  const controls = [{ disabled: false }, { disabled: false }];
  const form = {
    querySelector() { return toggle; },
    querySelectorAll() { return controls; },
    addEventListener(type, listener) { listeners[`form:${type}`] = listener; },
  };
  initBackupSettings({ querySelectorAll() { return [form]; } });
  assert.deepEqual(controls.map((control) => control.disabled), [true, true]);

  toggle.checked = true;
  listeners["toggle:change"]();
  assert.deepEqual(controls.map((control) => control.disabled), [false, false]);

  toggle.checked = false;
  listeners["toggle:change"]();
  listeners["form:submit"]();
  assert.deepEqual(controls.map((control) => control.disabled), [false, false]);
});

test("a completed backup refreshes the available backup list", async () => {
  let poll;
  let reloads = 0;
  const status = { textContent: "A backup is queued." };
  const button = { disabled: true };
  const panel = {
    dataset: { backupStateUrl: "/settings/backups/state" },
    querySelector(selector) { return selector.includes("status") ? status : button; },
  };
  const root = {
    querySelectorAll(selector) { return selector === "[data-backup-live]" ? [panel] : []; },
  };
  const environment = {
    async fetch() {
      return {
        ok: true,
        async json() { return { busy: false, state: { status: "success" } }; },
      };
    },
    setTimeout(callback) { poll = callback; return 1; },
    clearTimeout() {},
    addEventListener() {},
    location: { reload() { reloads++; } },
  };

  initBackupSettings(root, environment);
  await poll();
  assert.equal(reloads, 1);
});
