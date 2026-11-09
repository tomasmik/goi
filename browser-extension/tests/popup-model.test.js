const test = require("node:test");
const assert = require("node:assert/strict");

const captureModel = require("../shared/capture-model.js");
const {
  saveConnection,
} = require("../shared/popup-model.js");

function fakeOperations(overrides) {
  const calls = [];
  const operations = {
    normalizeOrigin: captureModel.normalizeOrigin,
    permissionPattern: captureModel.permissionPattern,
    containsPermission: async function (permission) {
      calls.push(["contains", permission]);
      return false;
    },
    requestPermission: async function (permission) {
      calls.push(["request", permission]);
      return true;
    },
    removePermission: async function (permission) {
      calls.push(["remove", permission]);
      return true;
    },
    verifyConnection: async function (connection) {
      calls.push(["verify", connection]);
      return { ok: true };
    },
    saveConnection: async function (connection) {
      calls.push(["save", connection]);
      return { ok: true };
    },
    ...(overrides || {})
  };
  return { calls, operations };
}

test("normalizes the connection with the shared origin rules", async function () {
  const fake = fakeOperations();

  const result = await saveConnection({
    baseUrl: " https://goi.example:8443/ ",
    token: "  secret-token  "
  }, fake.operations);

  assert.equal(result.baseUrl, "https://goi.example:8443");
  assert.deepEqual(fake.calls, [
    ["contains", { origins: ["https://goi.example/*"] }],
    ["request", { origins: ["https://goi.example/*"] }],
    ["verify", { baseUrl: "https://goi.example:8443", token: "secret-token" }],
    ["save", { baseUrl: "https://goi.example:8443", token: "secret-token" }]
  ]);
});

test("allows an HTTP connection and requests its host permission", async function () {
  const fake = fakeOperations();

  const result = await saveConnection(
    { baseUrl: "http://goi.example:8080", token: "token" },
    fake.operations,
  );
  assert.equal(result.baseUrl, "http://goi.example:8080");
  assert.deepEqual(fake.calls, [
    ["contains", { origins: ["http://goi.example/*"] }],
    ["request", { origins: ["http://goi.example/*"] }],
    ["verify", { baseUrl: "http://goi.example:8080", token: "token" }],
    ["save", { baseUrl: "http://goi.example:8080", token: "token" }],
  ]);
});

test("removes a newly granted permission when saving fails", async function () {
  const fake = fakeOperations({
    saveConnection: async function (connection) {
      fake.calls.push(["save", connection]);
      return { ok: false, error: "Storage is unavailable" };
    }
  });

  await assert.rejects(
    saveConnection({ baseUrl: "http://localhost:8080", token: "token" }, fake.operations),
    /Storage is unavailable/
  );
  assert.deepEqual(fake.calls.map(function (entry) { return entry[0]; }), [
    "contains", "request", "verify", "save", "remove"
  ]);
});

test("skips an already granted permission and preserves it when saving fails", async function () {
  const fake = fakeOperations({
    containsPermission: async function (permission) {
      fake.calls.push(["contains", permission]);
      return true;
    },
    saveConnection: async function (connection) {
      fake.calls.push(["save", connection]);
      throw new Error("Worker stopped");
    }
  });

  await assert.rejects(
    saveConnection({ baseUrl: "http://localhost:8080", token: "token" }, fake.operations),
    /Worker stopped/
  );
  assert.deepEqual(fake.calls.map(function (entry) { return entry[0]; }), ["contains", "verify", "save"]);
});

test("waits for the permission check before requesting host access", async function () {
  let finishPermissionCheck;
  let requested = false;
  const fake = fakeOperations({
    containsPermission: function (permission) {
      fake.calls.push(["contains", permission]);
      return new Promise(function (resolve) {
        finishPermissionCheck = resolve;
      });
    },
    requestPermission: async function (permission) {
      fake.calls.push(["request", permission]);
      requested = true;
      return true;
    }
  });

  const saving = saveConnection({
    baseUrl: "http://localhost:8080",
    token: "token"
  }, fake.operations);
  assert.equal(requested, false);

  finishPermissionCheck(false);
  await saving;
  assert.equal(requested, true);
  assert.deepEqual(fake.calls.map(function (entry) { return entry[0]; }), [
    "contains", "request", "verify", "save"
  ]);
});

test("does not replace the connection when verification fails", async function () {
  const fake = fakeOperations({
    verifyConnection: async function (connection) {
      fake.calls.push(["verify", connection]);
      return { ok: false, error: "Goi rejected the token" };
    }
  });

  await assert.rejects(
    saveConnection({ baseUrl: "https://goi.example", token: "wrong" }, fake.operations),
    /rejected the token/u
  );
  assert.deepEqual(fake.calls.map(function (entry) { return entry[0]; }), [
    "contains", "request", "verify", "remove"
  ]);
});

test("keeps the save error if permission rollback also fails", async function () {
  const fake = fakeOperations({
    saveConnection: async function () {
      throw new Error("Save failed");
    },
    removePermission: async function () {
      throw new Error("Remove failed");
    }
  });

  await assert.rejects(
    saveConnection({ baseUrl: "http://localhost:8080", token: "token" }, fake.operations),
    /Save failed/
  );
});

test("does not save when host permission is denied", async function () {
  const fake = fakeOperations({
    requestPermission: async function (permission) {
      fake.calls.push(["request", permission]);
      return false;
    }
  });

  await assert.rejects(
    saveConnection({ baseUrl: "http://localhost:8080", token: "token" }, fake.operations),
    /access was not granted/
  );
  assert.deepEqual(fake.calls.map(function (entry) { return entry[0]; }), ["contains", "request"]);
});
