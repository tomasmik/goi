const test = require("node:test");
const assert = require("node:assert/strict");

const dictionaryClient = require("../shared/dictionary-client.js");

test("sends normalized dictionary lookups through extension messaging", async function () {
  let sent;
  const runtime = {
    async sendMessage(message) {
      sent = message;
      return { ok: true };
    },
  };

  assert.deepEqual(await dictionaryClient.lookup(runtime, "  読む  "), { ok: true });
  assert.deepEqual(sent, {
    type: "goi.dictionary.lookup",
    version: 1,
    expression: "読む",
  });
});
