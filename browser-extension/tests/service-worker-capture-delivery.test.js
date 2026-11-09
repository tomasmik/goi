const test = require("node:test");
const assert = require("node:assert/strict");

const {
  captureModel,
  workerHarness,
} = require("./helpers/service-worker-harness.js");

test("queues retryable failures from ordinary page selection capture", async function () {
  const client = {
    create() {
      return {
        async capture() {
          const error = new Error("temporarily unavailable");
          error.status = 503;
          throw error;
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "token" },
    undefined,
    client,
  );
  const scriptCalls = [];
  harness.chrome.scripting.executeScript = async function (input) {
    scriptCalls.push(input);
    if (scriptCalls.length === 1) {
      return [{ frameId: 4, result: true }];
    }
    if (scriptCalls.length === 2) {
      return [{
        frameId: 4,
        result: {
          focused: true,
          capture: {
            ok: true,
            capture: {
              expression: "読む",
              contextText: "本を読む。",
              sourceKind: "web",
              sourceTitle: "Reading",
              sourceURL: "https://example.com/book",
            },
          },
        },
      }];
    }
    return [];
  };

  await harness.operations.performCaptureFromTab(8, {
    frameIds: [4],
    fallbackSelection: "読む",
  });

  assert.equal(harness.storage.captureOutboxV2.length, 1);
  assert.equal(harness.storage.captureOutboxV2[0].payload.expression, "読む");
  assert.deepEqual(Array.from(scriptCalls.at(-1).args), ["queued", "queued"]);
});

test("does not queue permanent API failures", async function () {
  const client = {
    create() {
      return {
        async capture() {
          const error = new Error("invalid token");
          error.status = 401;
          throw error;
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "bad-token" },
    undefined,
    client,
  );

  await assert.rejects(
    harness.operations.captureDirect({
      expression: "見る",
      contextText: "映画を見る。",
    }, {
      tab: { id: 5 },
      frameId: 0,
      url: "https://www.youtube.com/watch?v=example",
    }),
    (error) => error.status === 401,
  );
  assert.equal(harness.storage.captureOutboxV2, undefined);
});

test("keeps queued captures bound to their original Goi server", async function () {
  const sends = [];
  const client = {
    create(_fetch, connection) {
      return {
        async capture(payload) {
          sends.push({ baseUrl: connection.baseUrl, expression: payload.expression });
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://old.example", token: "old-token" },
    undefined,
    client,
  );
  await harness.delivery.enqueue(captureModel.buildCapturePayload({
    expression: "古い語",
    contextText: "古い文脈。",
  }, "b".repeat(32)), 1, "https://old.example");

  await harness.operations.saveConnection("https://new.example", "new-token");
  await harness.delivery.retry(true);

  assert.deepEqual(sends, []);
  assert.equal(harness.storage.captureOutboxV2[0].baseUrl, "https://old.example");

  await harness.operations.saveConnection("https://old.example", "replacement-token");
  await harness.delivery.retry(false);

  assert.deepEqual(sends, [{ baseUrl: "https://old.example", expression: "古い語" }]);
  assert.equal(harness.storage.captureOutboxV2, undefined);
});

test("suspends queued captures on authentication failure and resumes after reconnect", async function () {
  let mode = "network";
  let attempts = 0;
  const client = {
    create() {
      return {
        async capture() {
          attempts += 1;
          if (mode === "network") {
            throw new TypeError("offline");
          }
          if (mode === "auth") {
            const error = new Error("expired token");
            error.status = 401;
            throw error;
          }
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "expired" },
    undefined,
    client,
  );
  await harness.operations.captureDirect({
    expression: "読む",
    contextText: "本を読む。",
  }, {
    tab: { id: 8 },
    frameId: 0,
    url: "https://www.youtube.com/watch?v=example",
  });

  mode = "auth";
  await harness.delivery.retry(true);
  assert.equal(harness.storage.captureOutboxV2.length, 1);

  mode = "success";
  await harness.operations.saveConnection("https://goi.example", "fresh-token");
  await harness.delivery.retry(false);
  assert.equal(harness.storage.captureOutboxV2, undefined);
  assert.equal(attempts, 3);
});

test("a payload-specific server failure does not block later captures", async function () {
  const attempted = [];
  const client = {
    create() {
      return {
        async capture(payload) {
          attempted.push(payload.expression);
          if (payload.expression === "失敗") {
            const error = new Error("payload-specific failure");
            error.status = 500;
            throw error;
          }
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "token" },
    undefined,
    client,
  );
  await harness.delivery.enqueue(captureModel.buildCapturePayload({
    expression: "失敗",
    contextText: "失敗する。",
  }, "c".repeat(32)), 1, "https://goi.example");
  await harness.delivery.enqueue(captureModel.buildCapturePayload({
    expression: "成功",
    contextText: "成功する。",
  }, "d".repeat(32)), 1, "https://goi.example");

  await harness.delivery.retry(true);

  assert.deepEqual(attempted, ["失敗", "成功"]);
  assert.equal(harness.storage.captureOutboxV2.length, 1);
  assert.equal(harness.storage.captureOutboxV2[0].payload.expression, "失敗");
});

test("refuses a new capture when the durable outbox is full", async function () {
  const harness = workerHarness({ baseUrl: "https://goi.example", token: "token" });

  for (let index = 0; index < 100; index += 1) {
    await harness.delivery.enqueue(captureModel.buildCapturePayload({
      expression: "語" + index,
      contextText: "文" + index,
    }, String(index).padStart(32, "0")), 1, "https://goi.example");
  }
  await assert.rejects(
    harness.delivery.enqueue(captureModel.buildCapturePayload({
      expression: "語100",
      contextText: "文100",
    }, "100".padStart(32, "0")), 1, "https://goi.example"),
    (error) => error.code === "queue_full",
  );

  assert.equal(harness.storage.captureOutboxV2.length, 100);
  assert.equal(harness.storage.captureOutboxV2[0].payload.expression, "語0");
  assert.equal(harness.storage.captureOutboxV2.at(-1).payload.expression, "語99");
});

test("retries accepted captures even after a prolonged outage", async function () {
  let sends = 0;
  const client = {
    create() {
      return {
        async capture() {
          sends += 1;
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "token" },
    undefined,
    client,
  );
  await harness.delivery.enqueue(captureModel.buildCapturePayload({
    expression: "古い",
    contextText: "古い文。",
  }, "a".repeat(32)), 1, "https://goi.example");
  harness.storage.captureOutboxV2[0].createdAt = Date.now() - (8 * 24 * 60 * 60 * 1000);

  await harness.delivery.retry(true);

  assert.equal(sends, 1);
  assert.equal(harness.storage.captureOutboxV2, undefined);
});

