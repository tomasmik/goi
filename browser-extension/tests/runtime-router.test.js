const test = require("node:test");
const assert = require("node:assert/strict");

const runtimeRouter = require("../background/runtime-router.js");

function route(options = {}) {
  return runtimeRouter.create({
    runtimeID: "goi-test",
    routes: {
      allowed: { allow: options.allow, handle(message) {
        return { ok: true, value: message.value };
      } },
      failed: { handle() {
        throw new Error("failed");
      } },
    },
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

test("enforces each route's sender predicate before dispatch", function () {
  const handler = route({ allow: function () { return false; } });
  const responses = [];
  assert.equal(handler(
    { type: "allowed", version: 1 },
    { id: "goi-test", tab: { id: 1 }, frameId: 0 },
    function (response) { responses.push(response); },
  ), false);
  assert.deepEqual(responses, [{ ok: false, errorCode: "unavailable_page" }]);
});

test("rejects an allowed page message from a child frame", function () {
  const handler = route({ allow: function (sender) { return sender.frameId === 0; } });
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
