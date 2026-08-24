const test = require("node:test");
const assert = require("node:assert/strict");

const runtimeRouter = require("../background/runtime-router.js");

function route(options = {}) {
  return runtimeRouter.create({
    runtimeID: "goi-test",
    handlers: {
      allowed(message) {
        return { ok: true, value: message.value };
      },
      failed() {
        throw new Error("failed");
      },
    },
    popupOnly: new Set(options.popupOnly || []),
    topFrameOnly: new Set(options.topFrameOnly || []),
    popupSender: options.popupSender || function () { return false; },
    errorResponse(type) {
      return { ok: false, type };
    },
  });
}

function responseFrom(handler, message, sender) {
  return new Promise(function (resolve) {
    const asynchronous = handler(message, sender, resolve);
    assert.equal(asynchronous, true);
  });
}

test("ignores unknown protocols and foreign extensions", function () {
  const handler = route();
  assert.equal(handler({ type: "allowed", version: 2 }, { id: "goi-test" }, function () {}), false);
  assert.equal(handler({ type: "allowed", version: 1 }, { id: "other" }, function () {}), false);
  assert.equal(handler({ type: "unknown", version: 1 }, { id: "goi-test" }, function () {}), false);
});

test("enforces popup and top-frame policies before dispatch", function () {
  const handler = route({ popupOnly: ["allowed"], topFrameOnly: ["allowed"] });
  const responses = [];
  assert.equal(handler(
    { type: "allowed", version: 1 },
    { id: "goi-test", tab: { id: 1 }, frameId: 0 },
    function (response) { responses.push(response); },
  ), false);
  assert.deepEqual(responses, [{ ok: false, errorCode: "unavailable_page" }]);
});

test("rejects an allowed page message from a child frame", function () {
  const handler = route({ topFrameOnly: ["allowed"] });
  const responses = [];
  assert.equal(handler(
    { type: "allowed", version: 1 },
    { id: "goi-test", tab: { id: 1 }, frameId: 2 },
    function (response) { responses.push(response); },
  ), false);
  assert.deepEqual(responses, [{ ok: false, errorCode: "unavailable_page" }]);
});

test("dispatches asynchronously and maps handler failures", async function () {
  const handler = route();
  const sender = { id: "goi-test", tab: { id: 1 }, frameId: 0 };
  assert.deepEqual(
    await responseFrom(handler, { type: "allowed", version: 1, value: 4 }, sender),
    { ok: true, value: 4 },
  );
  assert.deepEqual(
    await responseFrom(handler, { type: "failed", version: 1 }, sender),
    { ok: false, type: "failed" },
  );
});
