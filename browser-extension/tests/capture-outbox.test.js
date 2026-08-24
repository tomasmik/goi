const test = require("node:test");
const assert = require("node:assert/strict");

const captureModel = require("../shared/capture-model.js");
const outbox = require("../background/capture-outbox.js");

function entry(nonce, overrides = {}) {
  return {
    payload: captureModel.buildCapturePayload({
      expression: "語",
      context_text: "語を勉強する。"
    }, nonce),
    baseUrl: "https://goi.example/",
    createdAt: 100,
    attempts: 1,
    nextAttemptAt: 200,
    ...overrides
  };
}

test("normalizes, deduplicates, and orders durable captures", function () {
  const values = [
    entry("b".repeat(32), { createdAt: 200 }),
    entry("a".repeat(32), { createdAt: 100 }),
    entry("a".repeat(32), { createdAt: 300 }),
    { payload: {} }
  ];
  const entries = outbox.normalizeEntries(values, 500, captureModel);
  assert.deepEqual(entries.map(function (value) {
    return value.payload.capture_nonce;
  }), ["a".repeat(32), "b".repeat(32)]);
  assert.deepEqual(outbox.status(entries), {
    pending: 2,
    oldestAt: 100,
    destinations: ["https://goi.example"]
  });
});

test("classifies retry policy and bounds exponential backoff", function () {
  assert.equal(outbox.isRetryableError({ status: 503 }), true);
  assert.equal(outbox.isRetryableError({ status: 422 }), false);
  assert.equal(outbox.isRetryableError({ code: "not_connected" }), false);
  assert.equal(outbox.isAuthenticationError({ status: 401 }), true);
  assert.equal(outbox.isPermanentError({ status: 409 }), true);
  assert.equal(outbox.isConnectionWideRetry({ status: 429 }), true);
  assert.equal(outbox.retryDelay(100), outbox.RETRY_MAX_MS);
});
