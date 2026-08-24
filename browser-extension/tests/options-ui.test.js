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
    classList: { toggle() {} },
    disabled: false,
    hidden: false,
    placeholder: "",
    textContent: "",
    value: "",
    ...properties,
    addEventListener(type, listener) {
      listeners.set(type, listener);
    },
    dispatch(type) {
      listeners.get(type)?.({ preventDefault() {} });
    },
  };
}

function createHarness(options = {}) {
  const elements = {
    "connection-form": control(),
    "base-url": control({ value: options.baseURL || "http://192.168.1.20:8080" }),
    token: control({ value: options.token || "goi_ext_v1_test-token" }),
    "save-connection": control(),
    "test-connection": control({ disabled: true }),
    disconnect: control({ hidden: true }),
    "connection-summary": control(),
    status: control(),
  };
  const messages = [];
  const permissionCalls = [];
  const connection = options.connection || { baseUrl: "", connected: false };
  const context = {
    GoiExtension: { captureModel, popupModel },
    document: {
      getElementById(id) { return elements[id]; },
    },
    chrome: {
      permissions: {
        async contains(permission) {
          permissionCalls.push(["contains", permission]);
          return false;
        },
        async request(permission) {
          permissionCalls.push(["request", permission]);
          return true;
        },
        async remove(permission) {
          permissionCalls.push(["remove", permission]);
          return true;
        },
      },
      runtime: {
        async sendMessage(message) {
          messages.push(message);
          if (message.type === "goi.connection.get") {
            return { ok: true, connection };
          }
          if (message.type === "goi.connection.test") {
            return options.testResponse || { ok: true };
          }
          if (message.type === "goi.connection.verify") {
            return options.verifyResponse || { ok: true };
          }
          return { ok: true };
        },
      },
    },
  };
  const source = fs.readFileSync(path.join(__dirname, "../options/options.js"), "utf8");
  vm.runInNewContext(source, context, { filename: "options.js" });
  return { elements, messages, permissionCalls };
}

async function settle() {
  await new Promise(setImmediate);
  await new Promise(setImmediate);
}

test("wires the extension options page", function () {
  const manifest = JSON.parse(fs.readFileSync(path.join(__dirname, "../manifest.json"), "utf8"));
  const html = fs.readFileSync(path.join(__dirname, "../options/options.html"), "utf8");

  assert.equal(manifest.options_ui.page, "options/options.html");
  assert.match(html, /id="test-connection"[^>]*>Test connection/u);
});

test("saves and tests a private HTTP connection", async function () {
  const harness = createHarness();
  await settle();

  harness.elements["connection-form"].dispatch("submit");
  await settle();

  assert.deepEqual(JSON.parse(JSON.stringify(harness.permissionCalls)), [
    ["contains", { origins: ["http://192.168.1.20/*"] }],
    ["request", { origins: ["http://192.168.1.20/*"] }],
  ]);
  const connectionMessages = harness.messages.filter(function (message) {
    return message.type.startsWith("goi.connection.") && message.type !== "goi.connection.get";
  });
  assert.deepEqual(connectionMessages.map(function (message) { return message.type; }), [
    "goi.connection.verify",
    "goi.connection.save",
  ]);
  assert.equal(harness.elements.status.textContent, "Connected to Goi.");
});

test("does not save a connection that fails verification", async function () {
  const harness = createHarness({
    verifyResponse: { ok: false, error: "Goi rejected the token" },
  });
  await settle();

  harness.elements["connection-form"].dispatch("submit");
  await settle();

  assert.equal(harness.messages.some(function (message) {
    return message.type === "goi.connection.save";
  }), false);
  assert.equal(harness.elements.status.textContent, "Goi rejected the token");
});

test("reports transport rejection separately from token rejection", async function () {
  const harness = createHarness({
    connection: { baseUrl: "http://goi.example", connected: true },
    testResponse: { ok: false, errorCode: "insecure_transport" },
  });
  await settle();

  harness.elements["test-connection"].dispatch("click");
  await settle();

  assert.match(harness.elements.status.textContent, /rejected plain HTTP/u);
  assert.doesNotMatch(harness.elements.status.textContent, /token/u);
});
