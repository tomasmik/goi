const test = require("node:test");
const assert = require("node:assert/strict");

const {
  CAPTURE_PATH,
  COVERAGE_PATH,
  DICTIONARY_PATH,
  KNOWN_PATH,
  STATUS_PATH,
  TRANSLATE_PATH,
  create
} = require("../background/api-client.js");

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
              global_rank: 80,
              novel_rank: 180,
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
