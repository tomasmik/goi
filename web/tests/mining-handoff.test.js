import assert from "node:assert/strict";
import test from "node:test";

import { initMiningCapture, validPayload } from "../static/js/mining-handoff.js";

function capture(overrides = {}) {
  return {
    type: "goi:mining-capture",
    version: 1,
    expression: "勉強",
    context_text: "毎日、日本語を勉強する。",
    source_kind: "video",
    source_title: "Japanese lesson",
    source_url: "https://example.com/watch?v=1",
    source_position_seconds: "48.21",
    ...overrides,
  };
}

function handoffHarness() {
  const fields = new Map();
  for (const selector of [
    "[data-mining-expression]",
    "[data-mining-context]",
    "[data-mining-title]",
    "[data-mining-url]",
    "[data-mining-source-kind]",
    "[data-mining-position]",
  ]) {
    fields.set(selector, {
      value: "",
      focused: false,
      focus() {
        this.focused = true;
      },
    });
  }

  const form = {
    dataset: {},
    querySelector(selector) {
      return fields.get(selector) || null;
    },
  };
  const message = { textContent: "" };
  const root = {
    querySelector(selector) {
      if (selector === "[data-mining-capture-form]") {
        return form;
      }
      if (selector === "[data-mining-message]") {
        return message;
      }
      return null;
    },
  };

  const listeners = new Map();
  const readyMessages = [];
  const opener = {
    postMessage(message, origin) {
      readyMessages.push({ message, origin });
    },
  };
  const browser = {
    opener,
    addEventListener(type, listener) {
      listeners.set(type, listener);
    },
    removeEventListener(type, listener) {
      if (listeners.get(type) === listener) {
        listeners.delete(type);
      }
    },
  };

  return { browser, fields, form, listeners, message, readyMessages, root };
}

test("accepts a bounded same-origin capture", () => {
  assert.equal(validPayload(capture(), "https://example.com"), true);
});

test("rejects malformed or untrusted capture data", () => {
  assert.equal(validPayload(capture({ expression: " " }), "https://example.com"), false);
  assert.equal(validPayload(capture({ source_url: "https://other.example/watch" }), "https://example.com"), false);
  assert.equal(validPayload(capture({ source_position_seconds: "-1" }), "https://example.com"), false);
  assert.equal(validPayload(capture({ source_position_seconds: "later" }), "https://example.com"), false);
  assert.equal(validPayload(capture({ source_kind: "manual" }), "https://example.com"), false);
  assert.equal(validPayload(capture({ expression: "語".repeat(257) }), "https://example.com"), false);
});

test("binds the opener handoff once and fills the capture form", () => {
  const harness = handoffHarness();

  const controller = initMiningCapture(harness.root, harness.browser);
  assert.ok(controller);
  assert.equal(initMiningCapture(harness.root, harness.browser), null);
  assert.deepEqual(harness.readyMessages, [{
    message: { type: "goi:mining-ready", version: 1 },
    origin: "*",
  }]);

  harness.listeners.get("message")({
    source: harness.browser.opener,
    origin: "https://example.com",
    data: capture(),
  });

  assert.equal(harness.fields.get("[data-mining-expression]").value, "勉強");
  assert.equal(harness.fields.get("[data-mining-context]").value, "毎日、日本語を勉強する。");
  assert.equal(harness.fields.get("[data-mining-title]").value, "Japanese lesson");
  assert.equal(harness.fields.get("[data-mining-url]").value, "https://example.com/watch?v=1");
  assert.equal(harness.fields.get("[data-mining-source-kind]").value, "video");
  assert.equal(harness.fields.get("[data-mining-position]").value, "48.21");
  assert.equal(harness.fields.get("[data-mining-expression]").focused, true);
  assert.equal(harness.message.textContent, "Capture loaded. Review it, then save to your inbox.");
  assert.equal(harness.listeners.has("message"), false);
  assert.equal(harness.listeners.has("pagehide"), false);
});

test("keeps listening after rejecting an opener payload", () => {
  const harness = handoffHarness();
  initMiningCapture(harness.root, harness.browser);

  harness.listeners.get("message")({
    source: harness.browser.opener,
    origin: "https://other.example",
    data: capture(),
  });

  assert.equal(harness.message.textContent, "Capture data was rejected. Paste the word manually.");
  assert.equal(harness.listeners.has("message"), true);
});
