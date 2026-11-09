import assert from "node:assert/strict";
import test from "node:test";

import { validPayload } from "../static/js/mining-handoff.js";

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
