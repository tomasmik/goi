const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const captureModel = require("../shared/capture-model.js");
const popupModel = require("../shared/popup-model.js");

function control(properties = {}) {
  const listeners = new Map();
  return {
    checked: false,
    classList: {
      toggle() {},
    },
    placeholder: "",
    type: "text",
    value: "",
    ...properties,
    addEventListener(type, listener) {
      if (!listeners.has(type)) {
        listeners.set(type, []);
      }
      listeners.get(type).push(listener);
    },
    dispatch(type, event = {}) {
      for (const listener of listeners.get(type) || []) {
        listener({ preventDefault() {}, ...event });
      }
    },
  };
}

function createHarness(options = {}) {
  const pauseChoices = [
    control({ type: "radio", value: "never", checked: true }),
    control({ type: "radio", value: "on_selection" }),
    control({ type: "radio", value: "after_capture" }),
  ];
  const displayChoices = [
    control({ type: "radio", value: "always", checked: true }),
    control({ type: "radio", value: "hidden" }),
    control({ type: "radio", value: "unknown_only" }),
    control({ type: "radio", value: "pause_reveal" }),
  ];
  const elements = {
    "base-url": control({ type: "url" }),
    token: control({ type: "password" }),
    "overlay-enabled-control": control({ hidden: false }),
    "overlay-enabled": control({ type: "checkbox" }),
    "hide-native": control({ type: "checkbox" }),
    "hide-native-detail": control({ hidden: true }),
    "open-local-player": control({ type: "button" }),
    "open-subtitle-browser": control({ type: "button", hidden: true }),
    "youtube-hover-lookup": control({ type: "checkbox" }),
    "analyze-section": control({ hidden: false, appendChild() {} }),
    "youtube-settings": control({ hidden: true, appendChild() {} }),
    "font-size": control({ type: "range", value: "34" }),
    "font-size-value": control(),
    "vertical-position": control({ type: "range", value: "78" }),
    "vertical-position-value": control(),
    "background-opacity": control({ type: "range", value: "0.65" }),
    "background-opacity-value": control(),
    "coverage-display": control({ type: "select-one", value: "full" }),
    "site-auto-control": control({ hidden: true }),
    "site-auto": control({ type: "checkbox", disabled: true }),
    "site-auto-label": control(),
    "site-auto-detail": control(),
    "outbox-status": control({ hidden: true }),
    status: control(),
    "open-connection-settings": control({ type: "button" }),
    "analyze-page": control({ type: "button" }),
  };
  const messages = [];
  let settings = {
    overlayEnabled: true,
    hideNativeCaptions: true,
    displayMode: "always",
    fontSizePx: 34,
    verticalPercent: 78,
    backgroundOpacity: 0.65,
    coverageDisplay: "full",
    pauseBehavior: "after_capture",
    ...(options.settings || {}),
  };
  let siteAuto = {
    ok: true,
    available: true,
    enabled: false,
    kind: "web",
    origin: "https://reader.example",
    permissionPattern: "https://reader.example/*",
    ...(options.siteAuto || {}),
  };
  const permissionCalls = [];
  let resolvePermissionCheck;
  let optionsOpenCount = 0;
  const context = {
    GoiExtension: { captureModel, popupModel },
    document: {
      getElementById(id) {
        return elements[id];
      },
      querySelectorAll(selector) {
        if (selector === 'input[name="pause-behavior"]') {
          return pauseChoices;
        }
        assert.equal(selector, 'input[name="display-mode"]');
        return displayChoices;
      },
    },
    chrome: {
      permissions: {
        contains: async (permission) => {
          permissionCalls.push(["contains", permission]);
          if (options.deferPermissionCheck) {
            return new Promise(function (resolve) {
              resolvePermissionCheck = resolve;
            });
          }
          return Boolean(options.permissionAlreadyGranted);
        },
        request: async (permission) => {
          permissionCalls.push(["request", permission]);
          return options.permissionGranted !== false;
        },
        remove: async (permission) => {
          permissionCalls.push(["remove", permission]);
          return true;
        },
      },
      runtime: {
        async openOptionsPage() {
          optionsOpenCount += 1;
        },
        sendMessage(message) {
          messages.push(message);
          if (message.type === "goi.connection.get") {
            return Promise.resolve({
              ok: true,
              connection: { baseUrl: "http://127.0.0.1:8080", connected: true },
            });
          }
          if (message.type === "goi.settings.get") {
            return Promise.resolve({ ok: true, settings });
          }
          if (message.type === "goi.capture.outbox-status") {
            return Promise.resolve({
              ok: true,
              pending: options.pending || 0,
            });
          }
          if (message.type === "goi.site-auto.get") {
            return Promise.resolve({ ...siteAuto });
          }
          if (message.type === "goi.site-auto.set") {
            if (options.siteAutoSetFailure) {
              return Promise.resolve({ ok: false, errorCode: "storage" });
            }
            siteAuto = { ...siteAuto, enabled: Boolean(message.enabled) };
            return Promise.resolve({ ...siteAuto });
          }
          if (message.type === "goi.settings.patch") {
            if (options.patchFailure) {
              return Promise.resolve({ ok: false, error: "storage unavailable" });
            }
            settings = { ...settings, ...message.patch };
            return Promise.resolve({ ok: true, settings });
          }
          if (message.type === "goi.coverage.analyze-page") {
            return Promise.resolve({
              ok: true,
              summary: options.coverageSummary || {
                known_occurrences: 8,
                total_occurrences: 10,
                unknown_unique: 2,
                excluded_names: 0,
              },
            });
          }
          if (message.type === "goi.player.open" && options.playerOpenFailure) {
            return Promise.resolve({ ok: false, errorCode: "server" });
          }
          return Promise.resolve({ ok: true });
        },
      },
    },
  };
  const source = fs.readFileSync(path.join(__dirname, "../popup/popup.js"), "utf8");
  vm.runInNewContext(source, context, { filename: "popup.js" });
  return {
    elements,
    messages,
    displayChoices,
    pauseChoices,
    permissionCalls,
    get optionsOpenCount() { return optionsOpenCount; },
    finishPermissionCheck(value) {
      resolvePermissionCheck(value);
    },
  };
}

async function settle() {
  await new Promise(setImmediate);
  await new Promise(setImmediate);
}

test("reports captures waiting to retry", async () => {
  const harness = createHarness({ pending: 2 });
  await settle();

  assert.equal(
    harness.elements["outbox-status"].textContent,
    "2 captures waiting to retry",
  );
  assert.equal(harness.elements["outbox-status"].hidden, false);
});

test("opens stable connection settings instead of editing in the popup", async () => {
  const harness = createHarness();
  await settle();

  harness.elements["open-connection-settings"].dispatch("click");
  await settle();

  assert.equal(harness.optionsOpenCount, 1);
  assert.equal(harness.elements["open-connection-settings"].textContent, "Change");
});

test("analyzes the active page and reports the known percentage", async () => {
  const harness = createHarness();
  await settle();

  harness.elements["analyze-page"].dispatch("click");
  await settle();

  assert.equal(harness.messages.at(-1).type, "goi.coverage.analyze-page");
  assert.equal(harness.elements.status.textContent, "80% known · Unknown words highlighted.");
});

test("does not report 100 percent while the page contains an unknown word", async () => {
  const harness = createHarness({
    coverageSummary: {
      known_occurrences: 199,
      total_occurrences: 200,
      unknown_unique: 1,
      excluded_names: 0,
    },
  });
  await settle();

  harness.elements["analyze-page"].dispatch("click");
  await settle();

  assert.equal(harness.elements.status.textContent, "99.5% known · Unknown words highlighted.");
});

test("opens the local video player from every popup context", async () => {
  const harness = createHarness({
    siteAuto: {
      available: false,
      enabled: false,
      kind: "unavailable",
      origin: "",
      permissionPattern: "",
    },
  });
  await settle();

  harness.elements["open-local-player"].dispatch("click");
  await settle();

  assert.deepEqual(JSON.parse(JSON.stringify(harness.messages.at(-1))), {
    type: "goi.player.open",
    version: 1,
  });
  assert.equal(harness.elements.status.textContent, "Player opened.");
  assert.equal(harness.elements["open-local-player"].disabled, false);
});

test("reports a local player launch failure without hiding the action", async () => {
  const harness = createHarness({ playerOpenFailure: true });
  await settle();

  harness.elements["open-local-player"].dispatch("click");
  await settle();

  assert.equal(harness.elements.status.textContent, "Could not open the local video player. Try again.");
  assert.equal(harness.elements["open-local-player"].disabled, false);
});

test("does not claim to show highlights when every word is known", async () => {
  const harness = createHarness({
    coverageSummary: {
      known_occurrences: 10,
      total_occurrences: 10,
      unknown_unique: 0,
      excluded_names: 0,
    },
  });
  await settle();

  harness.elements["analyze-page"].dispatch("click");
  await settle();

  assert.equal(harness.elements.status.textContent, "100% known · No unknown words.");
});

test("disables page analysis when the active tab cannot be inspected", async () => {
  const harness = createHarness({
    siteAuto: {
      available: false,
      enabled: false,
      kind: "unavailable",
      origin: "",
      permissionPattern: "",
    },
  });
  await settle();

  assert.equal(harness.elements["analyze-page"].disabled, true);
  assert.equal(harness.elements["analyze-page"].textContent, "This page can’t be analyzed");
  assert.equal(harness.elements["site-auto-control"].hidden, true);
});

test("exposes recovery and management controls for hidden extension state", function () {
  const html = fs.readFileSync(path.join(__dirname, "../popup/popup.html"), "utf8");

  for (const expected of [
    'id="analyze-unavailable-reason"',
    'id="outbox-retry"',
    'id="outbox-discard"',
    'id="managed-sites-list"',
    'id="ignored-words-list"',
    'id="capture-shortcut"',
  ]) {
    assert.match(html, new RegExp(expected, "u"));
  }
});

test("requests web-site permission before enabling automatic analysis", async () => {
  const harness = createHarness();
  await settle();

  assert.equal(harness.elements["site-auto-control"].hidden, false);
  assert.equal(harness.elements["site-auto-label"].textContent, "Enable automatically on this site");
  harness.elements["site-auto"].checked = true;
  harness.elements["site-auto"].dispatch("change");
  await settle();

  assert.deepEqual(JSON.parse(JSON.stringify(harness.permissionCalls)), [
    ["contains", { origins: ["https://reader.example/*"] }],
    ["request", { origins: ["https://reader.example/*"] }],
  ]);
  assert.deepEqual(JSON.parse(JSON.stringify(harness.messages.at(-1))), {
    type: "goi.site-auto.set",
    version: 1,
    enabled: true,
  });
  assert.equal(harness.elements.status.textContent, "Enabled on this site.");
});

test("waits for the site permission check before requesting access", async () => {
  const harness = createHarness({ deferPermissionCheck: true });
  await settle();

  harness.elements["site-auto"].checked = true;
  harness.elements["site-auto"].dispatch("change");

  assert.deepEqual(harness.permissionCalls.map((call) => call[0]), ["contains"]);
  harness.finishPermissionCheck(false);
  await settle();
  assert.deepEqual(harness.permissionCalls.map((call) => call[0]), ["contains", "request"]);
  assert.equal(harness.messages.at(-1).type, "goi.site-auto.set");
});

test("rolls back newly granted site permission when automatic enabling fails", async () => {
  const harness = createHarness({ siteAutoSetFailure: true });
  await settle();

  harness.elements["site-auto"].checked = true;
  harness.elements["site-auto"].dispatch("change");
  await settle();

  assert.deepEqual(harness.permissionCalls.map((call) => call[0]), ["contains", "request", "remove"]);
  assert.equal(harness.elements["site-auto"].checked, false);
  assert.equal(
    harness.elements.status.textContent,
    "Could not save this setting. The previous value was restored.",
  );
});

test("preserves existing site permission when automatic enabling fails", async () => {
  const harness = createHarness({
    permissionAlreadyGranted: true,
    siteAutoSetFailure: true,
  });
  await settle();

  harness.elements["site-auto"].checked = true;
  harness.elements["site-auto"].dispatch("change");
  await settle();

  assert.deepEqual(harness.permissionCalls.map((call) => call[0]), ["contains"]);
  assert.equal(harness.elements["site-auto"].checked, false);
});

test("disabling automatic analysis leaves existing site access granted", async () => {
  const harness = createHarness({ siteAuto: { enabled: true } });
  await settle();

  harness.elements["site-auto"].checked = false;
  harness.elements["site-auto"].dispatch("change");
  await settle();

  assert.deepEqual(harness.permissionCalls, []);
  assert.equal(harness.messages.at(-1).type, "goi.site-auto.set");
  assert.equal(harness.messages.at(-1).enabled, false);
  assert.equal(harness.elements.status.textContent, "Disabled on this site.");
});

test("uses YouTube caption copy without requesting optional permission", async () => {
  const harness = createHarness({
    settings: { overlayEnabled: false },
    siteAuto: {
      kind: "youtube",
      origin: "https://www.youtube.com",
      permissionPattern: "https://www.youtube.com/*",
    },
  });
  await settle();

  assert.equal(harness.elements["site-auto-label"].textContent, "Use Goi on YouTube");
  assert.equal(harness.elements["analyze-section"].hidden, true);
  assert.equal(harness.elements["open-subtitle-browser"].hidden, false);
  harness.elements["site-auto"].checked = true;
  harness.elements["site-auto"].dispatch("change");
  await settle();

  assert.deepEqual(harness.permissionCalls, []);
  assert.equal(harness.messages.some((message) => message.type === "goi.site-auto.set"), true);
  assert.equal(harness.elements.status.textContent, "Enabled on YouTube.");
});

test("opens the subtitle browser from a YouTube popup", async () => {
  const harness = createHarness({
    siteAuto: {
      kind: "youtube",
      origin: "https://www.youtube.com",
      permissionPattern: "https://www.youtube.com/*",
    },
  });
  await settle();

  harness.elements["open-subtitle-browser"].dispatch("click");
  await settle();

  assert.equal(harness.messages.at(-1).type, "goi.companion.open");
  assert.equal(harness.elements.status.textContent, "Transcript and mining opened.");
});

test("toggles delayed caption definitions from the YouTube popup", async () => {
  const harness = createHarness({
    siteAuto: {
      kind: "youtube",
      origin: "https://www.youtube.com",
      permissionPattern: "https://www.youtube.com/*",
    },
  });
  await settle();

  harness.elements["youtube-hover-lookup"].checked = true;
  harness.elements["youtube-hover-lookup"].dispatch("change");
  await settle();

  const message = harness.messages.find((entry) =>
    entry.type === "goi.settings.patch" && entry.patch.hoverLookupEnabled === true
  );
  assert.ok(message);
  assert.equal(harness.elements.status.textContent, "Definitions will open after a short hover.");
});

test("hides the subtitle browser away from a YouTube video", async () => {
  const harness = createHarness({
    siteAuto: {
      kind: "youtube",
      videoAvailable: false,
      origin: "https://www.youtube.com",
      permissionPattern: "https://www.youtube.com/*",
    },
  });
  await settle();

  assert.equal(harness.elements["open-subtitle-browser"].hidden, true);
});
