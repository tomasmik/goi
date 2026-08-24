const test = require("node:test");
const assert = require("node:assert/strict");

const {
  CAPTURE_PATH,
  COVERAGE_PATH,
  DEFAULT_TIMEOUT_MS,
  DICTIONARY_PATH,
  KNOWN_PATH,
  STATUS_PATH,
  TRANSLATE_PATH,
  TRANSLATION_TIMEOUT_MS,
  create
} = require("../background/api-client.js");

test("translation requests allow time for remote providers", function () {
  assert.equal(DEFAULT_TIMEOUT_MS, 10000);
  assert.equal(TRANSLATION_TIMEOUT_MS, 70000);
});

test("uses only the fixed extension API paths", async () => {
  const calls = [];
  const client = create(async function (url, options) {
    calls.push({ url, options });
    const response = url.endsWith(STATUS_PATH)
      ? { ok: true }
      : url.endsWith(TRANSLATE_PATH)
        ? { translation: "I read a book.", provider: "goi" }
      : url.includes(DICTIONARY_PATH)
        ? {
            query: "読む",
            state: "ready",
            candidates: [{
              written: "読む",
              reading: "よむ",
              commonness: 8,
              commonness_score: 80,
              meanings: ["to read"],
              senses: [{ parts_of_speech: ["Godan verb"], meanings: ["to read"] }]
            }]
          }
      : url.endsWith(COVERAGE_PATH)
        ? {
            summary: { known_occurrences: 2, total_occurrences: 2, unknown_unique: 0, excluded_names: 0 },
            blocks: [{ id: 1, tokens: [
              { surface: "読", expression: "読む", reading: "よ", start_utf16: 0, end_utf16: 1, status: "leech" },
              { surface: "む", expression: "読む", start_utf16: 1, end_utf16: 2, status: "suspended_leech" }
            ] }]
          }
        : url.endsWith(KNOWN_PATH)
          ? { state: "marked_known" }
        : { id: 1, revision: 3, status: "pending", replayed: false, review_url: "/mining/captures/1" };
    return { ok: true, status: 200, json: async function () { return response; } };
  }, { baseUrl: "https://goi.example:8443", token: "secret" });

  await client.status();
  const capture = await client.capture({ capture_nonce: "a".repeat(32) });
  await client.coverage([{ id: 1, text: "読む" }]);
  await client.dictionary("読む");
  await client.markKnown("読む");
  await client.translate("本を読みます。");

  assert.deepEqual(calls.map((call) => call.url), [
    "https://goi.example:8443" + STATUS_PATH,
    "https://goi.example:8443" + CAPTURE_PATH,
    "https://goi.example:8443" + COVERAGE_PATH,
    "https://goi.example:8443" + DICTIONARY_PATH + "?expression=%E8%AA%AD%E3%82%80",
    "https://goi.example:8443" + KNOWN_PATH,
    "https://goi.example:8443" + TRANSLATE_PATH
  ]);
  assert.equal(calls[0].options.headers.Authorization, "Bearer secret");
  assert.equal(calls[1].options.credentials, "omit");
  assert.equal(capture.revision, 3);
});

test("preserves the server's flat error code", async () => {
  const client = create(async function () {
    return {
      ok: false,
      status: 401,
      json: async function () {
        return { code: "unauthorized", error: "invalid extension token" };
      }
    };
  }, { baseUrl: "https://goi.example", token: "bad" });

  await assert.rejects(client.status(), function (error) {
    return error.status === 401 && error.code === "unauthorized";
  });
});

test("rejects successful HTTP responses with the wrong API shape", async () => {
  const client = create(async function () {
    return {
      ok: true,
      status: 200,
      json: async function () {
        return {};
      }
    };
  }, { baseUrl: "https://goi.example", token: "secret" });

  await assert.rejects(client.status(), { code: "unexpected_response", status: 502 });
  await assert.rejects(
    client.capture({ capture_nonce: "a".repeat(32) }),
    { code: "unexpected_response", status: 502 }
  );
  await assert.rejects(
    client.coverage([{ id: 1, text: "読む" }]),
    { code: "unexpected_response", status: 502 }
  );
  await assert.rejects(client.dictionary("読む"), { code: "unexpected_response", status: 502 });
  await assert.rejects(client.markKnown("読む"), { code: "unexpected_response", status: 502 });
  await assert.rejects(client.translate("本を読みます。"), { code: "unexpected_response", status: 502 });
});

test("rejects a non-string coverage reading", async () => {
  const client = create(async function () {
    return {
      ok: true,
      status: 200,
      json: async function () {
        return {
          summary: { known_occurrences: 0, total_occurrences: 1, unknown_unique: 1, excluded_names: 0 },
          blocks: [{ id: 1, tokens: [{
            surface: "読む",
            expression: "読む",
            reading: 123,
            start_utf16: 0,
            end_utf16: 2,
            status: "unknown",
          }] }],
        };
      },
    };
  }, { baseUrl: "https://goi.example", token: "secret" });

  await assert.rejects(
    client.coverage([{ id: 1, text: "読む" }]),
    { code: "unexpected_response", status: 502 }
  );
});

test("allows a queued capture retry to be cancelled", async () => {
  let requestSignal;
  const client = create(async function (_url, options) {
    requestSignal = options.signal;
    await new Promise(function (_resolve, reject) {
      options.signal.addEventListener("abort", function () {
        reject(new Error("request aborted"));
      }, { once: true });
    });
  }, { baseUrl: "https://goi.example", token: "secret" });
  const controller = new AbortController();

  const capture = client.capture({ capture_nonce: "a".repeat(32) }, controller.signal);
  controller.abort();

  await assert.rejects(capture, /request aborted/);
  assert.equal(requestSignal.aborted, true);
});
