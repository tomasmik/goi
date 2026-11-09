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
    children: [],
    append(...nodes) { this.children.push(...nodes); },
    appendChild(node) { this.children.push(node); },
    replaceChildren(...nodes) { this.children = nodes; },
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
  const html = fs.readFileSync(path.join(__dirname, "../popup/popup.html"), "utf8");
  const elements = Object.fromEntries(Array.from(html.matchAll(/<[^>]+\bid="([^"]+)"[^>]*>/gu), (match) => [
    match[1], control({ hidden: /\bhidden\b/u.test(match[0]) }),
  ]));
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
      createElement() { return control(); },
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
          if (message.type === "goi.site-auto.list") return Promise.resolve({ ok: true, origins: [] });
          if (message.type === "goi.coverage.ignore.list") return Promise.resolve({ ok: true, words: [] });
          if (message.type === "goi.player.open" || message.type === "goi.companion.open") return Promise.resolve({ ok: true });
          return Promise.reject(new Error("Unexpected popup message: " + message.type));
        },
      },
    },
  };
  const source = fs.readFileSync(path.join(__dirname, "../popup/popup.js"), "utf8");
  vm.runInNewContext(source, context, { filename: "popup.js" });
  return {
    elements,
    messages,
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
