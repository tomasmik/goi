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

test("drops a queued capture after a permanent retry failure", async function () {
  let attempts = 0;
  const client = {
    create() {
      return {
        async capture() {
          attempts += 1;
          const error = new Error(attempts === 1 ? "network unavailable" : "invalid capture");
          if (attempts > 1) {
            error.status = 422;
          }
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

  await harness.operations.captureDirect({
    expression: "書く",
    contextText: "手紙を書く。",
  }, {
    tab: { id: 6 },
    frameId: 0,
    url: "https://www.youtube.com/watch?v=example",
  });
  await harness.operations.retryCaptureOutbox(true);

  assert.equal(attempts, 2);
  assert.equal(harness.storage.captureOutboxV2, undefined);
});

test("retains and backs off queued captures after unknown 4xx responses", async function () {
  for (const status of [404, 405, 410]) {
    const client = {
      create() {
        return {
          async capture() {
            const error = new Error("unexpected client response");
            error.status = status;
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
    await harness.operations.enqueueCapture(captureModel.buildCapturePayload({
      expression: "保留" + status,
      contextText: "保留する。",
    }, String(status).padStart(32, "0")), 1, "https://goi.example");
    const before = Date.now();

    await harness.operations.retryCaptureOutbox(true);

    assert.equal(harness.storage.captureOutboxV2.length, 1, "status " + status);
    assert.equal(harness.storage.captureOutboxV2[0].attempts, 2, "status " + status);
    assert.ok(harness.storage.captureOutboxV2[0].nextAttemptAt > before, "status " + status);
  }
});

test("deletes queued captures for explicit capture-specific permanent responses", async function () {
  for (const status of [400, 409, 413, 422]) {
    const client = {
      create() {
        return {
          async capture() {
            const error = new Error("permanent capture response");
            error.status = status;
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
    await harness.operations.enqueueCapture(captureModel.buildCapturePayload({
      expression: "削除" + status,
      contextText: "削除する。",
    }, String(status).padStart(32, "0")), 1, "https://goi.example");

    await harness.operations.retryCaptureOutbox(true);

    assert.equal(harness.storage.captureOutboxV2, undefined, "status " + status);
  }
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
  await harness.operations.enqueueCapture(captureModel.buildCapturePayload({
    expression: "古い語",
    contextText: "古い文脈。",
  }, "b".repeat(32)), 1, "https://old.example");

  await harness.operations.saveConnection("https://new.example", "new-token");
  await harness.operations.retryCaptureOutbox(true);

  assert.deepEqual(sends, []);
  assert.equal(harness.storage.captureOutboxV2[0].baseUrl, "https://old.example");

  await harness.operations.saveConnection("https://old.example", "replacement-token");
  await harness.operations.retryCaptureOutbox(false);

  assert.deepEqual(sends, [{ baseUrl: "https://old.example", expression: "古い語" }]);
  assert.equal(harness.storage.captureOutboxV2, undefined);
});

test("removes malformed current outbox entries instead of retrying them", async function () {
  const harness = workerHarness({ baseUrl: "https://goi.example", token: "token" });
  harness.storage.captureOutboxV2 = [{
    payload: captureModel.buildCapturePayload({
      expression: "出所不明",
      contextText: "出所不明の文脈。",
    }, "3".repeat(32)),
    baseUrl: "",
    createdAt: Date.now(),
    attempts: 1,
    nextAttemptAt: Date.now(),
  }];

  await harness.operations.retryCaptureOutbox(true);

  assert.equal(harness.storage.captureOutboxV2, undefined);
  assert.deepEqual(
    JSON.parse(JSON.stringify(await harness.operations.captureOutboxStatus())),
    { pending: 0, destinations: [] },
  );
});

test("lets the user discard the durable capture outbox", async function () {
  const harness = workerHarness({ baseUrl: "https://goi.example", token: "token" });
  await harness.operations.enqueueCapture(captureModel.buildCapturePayload({
    expression: "保留",
    contextText: "保留した文脈。",
  }, "4".repeat(32)), 1, "https://goi.example");
  assert.equal((await harness.operations.captureOutboxStatus()).pending, 1);

  await harness.operations.discardCaptureOutbox();

  assert.equal(harness.storage.captureOutboxV2, undefined);
  assert.equal(harness.alarms.has("goi-capture-outbox"), false);
});

test("disconnect stops an in-progress outbox batch before the next capture", async function () {
  const sent = [];
  let firstSignal;
  let releaseFirst;
  let firstStarted;
  const started = new Promise(function (resolve) {
    firstStarted = resolve;
  });
  const client = {
    create() {
      return {
        async capture(payload, signal) {
          sent.push(payload.expression);
          if (sent.length === 1) {
            firstSignal = signal;
            firstStarted();
            await new Promise(function (resolve) {
              releaseFirst = resolve;
            });
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
  await harness.operations.enqueueCapture(captureModel.buildCapturePayload({
    expression: "一",
    contextText: "一つ目。",
  }, "f".repeat(32)), 1, "https://goi.example");
  await harness.operations.enqueueCapture(captureModel.buildCapturePayload({
    expression: "二",
    contextText: "二つ目。",
  }, "1".repeat(32)), 1, "https://goi.example");

  const retry = harness.operations.retryCaptureOutbox(true);
  await started;
  await harness.operations.disconnectConnection();

  assert.equal(firstSignal.aborted, true);
  releaseFirst();
  await retry;

  assert.deepEqual(sent, ["一"]);
  assert.equal(harness.storage.captureOutboxV2.length, 1);
  assert.equal(harness.storage.captureOutboxV2[0].payload.expression, "二");
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
  await harness.operations.retryCaptureOutbox(true);
  assert.equal(harness.storage.captureOutboxV2.length, 1);

  mode = "success";
  await harness.operations.saveConnection("https://goi.example", "fresh-token");
  await harness.operations.retryCaptureOutbox(false);
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
  await harness.operations.enqueueCapture(captureModel.buildCapturePayload({
    expression: "失敗",
    contextText: "失敗する。",
  }, "c".repeat(32)), 1, "https://goi.example");
  await harness.operations.enqueueCapture(captureModel.buildCapturePayload({
    expression: "成功",
    contextText: "成功する。",
  }, "d".repeat(32)), 1, "https://goi.example");

  await harness.operations.retryCaptureOutbox(true);

  assert.deepEqual(attempted, ["失敗", "成功"]);
  assert.equal(harness.storage.captureOutboxV2.length, 1);
  assert.equal(harness.storage.captureOutboxV2[0].payload.expression, "失敗");
});

test("an unexpected retry failure installs a fallback alarm", async function () {
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "token" },
  );
  harness.chrome.storage.local.get = async function () {
    throw new Error("temporary storage failure");
  };

  const before = Date.now();
  await harness.operations.runCaptureOutboxRetry(false);

  const alarm = harness.alarms.get("goi-capture-outbox");
  assert.ok(alarm.when >= before + (60 * 60 * 1000));
});

test("refuses a new capture when the durable outbox is full", async function () {
  const harness = workerHarness({ baseUrl: "https://goi.example", token: "token" });

  for (let index = 0; index < 100; index += 1) {
    await harness.operations.enqueueCapture(captureModel.buildCapturePayload({
      expression: "語" + index,
      contextText: "文" + index,
    }, String(index).padStart(32, "0")), 1, "https://goi.example");
  }
  await assert.rejects(
    harness.operations.enqueueCapture(captureModel.buildCapturePayload({
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
  await harness.operations.enqueueCapture(captureModel.buildCapturePayload({
    expression: "古い",
    contextText: "古い文。",
  }, "a".repeat(32)), 1, "https://goi.example");
  harness.storage.captureOutboxV2[0].createdAt = Date.now() - (8 * 24 * 60 * 60 * 1000);

  await harness.operations.retryCaptureOutbox(true);

  assert.equal(sends, 1);
  assert.equal(harness.storage.captureOutboxV2, undefined);
});

