const test = require("node:test");
const assert = require("node:assert/strict");

const translation = require("../shared/translation.js");

test("uses Chrome on-device translation and caches repeated text", async function () {
  let creates = 0;
  let translations = 0;
  let remoteCalls = 0;
  const service = translation.create({
    translatorAPI: {
      async availability() { return "available"; },
      async create() {
        creates += 1;
        return {
          async translate(text) {
            translations += 1;
            return text === "本を読みます。" ? "I read a book." : "";
          }
        };
      }
    },
    async remote() {
      remoteCalls += 1;
      return { translation: "remote", provider: "goi" };
    }
  });

  const first = await service.translate("本を読みます。");
  const second = await service.translate(" 本を読みます。 ");

  assert.deepEqual(first, { translation: "I read a book.", provider: "chrome", cached: false });
  assert.deepEqual(second, { translation: "I read a book.", provider: "chrome", cached: true });
  assert.equal(creates, 1);
  assert.equal(translations, 1);
  assert.equal(remoteCalls, 0);
});

test("falls back to Goi when the on-device language pair is unavailable", async function () {
  const service = translation.create({
    translatorAPI: {
      async availability() { return "unavailable"; },
      async create() { throw new Error("should not create"); }
    },
    async remote(text) {
      assert.equal(text, "ゲームをします。");
      return { translation: "I play a game.", provider: "goi" };
    }
  });

  assert.deepEqual(await service.translate("ゲームをします。"), {
    translation: "I play a game.", provider: "goi", cached: false
  });
});

test("extracts only subtitle text from a multi-line selection", function () {
  const lines = [
    { dataset: { translationText: "最初の行。" } },
    { dataset: { translationText: "次の行。" } },
    { dataset: { translationText: "選ばれていない。" } }
  ];
  const range = { intersectsNode(line) { return line !== lines[2]; } };
  const selection = { isCollapsed: false, rangeCount: 1, getRangeAt() { return range; } };
  const container = { querySelectorAll() { return lines; } };

  assert.equal(translation.selectedText(selection, container, ".line-text"), "最初の行。\n次の行。");
});

test("runs pasted translation only for the latest input", async function () {
  const state = { timer: undefined, version: 0 };
  const elements = {
    input: { value: "猫です。" },
    result: { dataset: {}, hidden: true, textContent: "" },
    retry: { hidden: true, disabled: false, textContent: "Retry", removeAttribute() {} },
    status: { textContent: "", classList: { toggle() {} } }
  };
  const pending = new Map();
  const translator = {
    translate(text) {
      return new Promise(function (resolve) { pending.set(text, resolve); });
    }
  };

  const first = translation.schedulePasted(state, translator, elements, 0);
  elements.input.value = "犬です。";
  const second = translation.schedulePasted(state, translator, elements, 0);

  pending.get("犬です。")({ translation: "It is a dog.", provider: "goi" });
  await second;
  pending.get("猫です。")({ translation: "It is a cat.", provider: "goi" });
  await first;

  assert.equal(elements.result.textContent, "It is a dog.");
  assert.equal(elements.result.hidden, false);
  assert.equal(elements.retry.hidden, true);
});
