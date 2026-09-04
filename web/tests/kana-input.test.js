import assert from "node:assert/strict";
import test from "node:test";

import { convertKana, initKanaInputs } from "../static/js/kana-input.js";

class KanaInput {
  constructor() {
    this.dataset = {};
    this.listeners = {};
    this.parentElement = null;
    this.form = null;
    this.value = "";
    this.selectionStart = 0;
    this.selectionEnd = 0;
  }

  addEventListener(type, listener) {
    this.listeners[type] = listener;
  }

  dispatchEvent(event) {
    this.listeners[event.type]?.(event);
    return true;
  }

  setSelectionRange(start, end) {
    this.selectionStart = start;
    this.selectionEnd = end;
  }

  type(character, data = character, options = {}) {
    let prevented = false;
    if (options.beforeInput !== false) {
      this.listeners.beforeinput?.({
        cancelable: options.cancelable !== false,
        data,
        inputType: "insertText",
        isComposing: false,
        preventDefault() {
          prevented = true;
        }
      });
    }
    if (prevented) {
      return;
    }

    const start = this.selectionStart;
    this.value = this.value.slice(0, start) + character + this.value.slice(this.selectionEnd);
    this.setSelectionRange(start + character.length, start + character.length);
    this.listeners.input({ data, isComposing: false });
  }
}

test("converts the core romaji input cases", () => {
  assert.equal(convertKana("taberu", true), "たべる");
  assert.equal(convertKana("TABERU", true), "タベル");
  assert.equal(convertKana("gakkou", true), "がっこう");
  assert.equal(convertKana("kan", false), "かn");
  assert.equal(convertKana("kan", true), "かん");
  assert.equal(convertKana("nn", false), "ん");
  assert.equal(convertKana("nna", false), "んあ");
  assert.equal(convertKana("nnya", false), "んや");
  assert.equal(convertKana("nnnya", false), "んにゃ");
  assert.equal(convertKana("honnyu", true), "ほんゆ");
  assert.equal(convertKana("honnyuu", true), "ほんゆう");
  assert.equal(convertKana("HONNYUU", true), "ホンユウ");
  assert.equal(convertKana("hon'yuu", true), "ほんゆう");
  assert.equal(convertKana("スーパー", true), "スーパー");
});

test("matches server conversion for modern kana combinations", () => {
  const cases = [
    ["chekku", "ちぇっく"],
    ["SHEFU", "シェフ"],
    ["je", "じぇ"],
    ["THI", "ティ"],
    ["DHI", "ディ"],
    ["wi", "うぃ"],
    ["WE", "ウェ"],
    ["tsa", "つぁ"],
    ["twu", "とぅ"],
    ["kwa", "くぁ"],
    ["VYO", "ヴョ"]
  ];

  for (const [input, expected] of cases) {
    assert.equal(convertKana(input, true), expected, input);
  }
});

test("commits each double n without reusing it for the next syllable", () => {
  const previousInput = globalThis.HTMLInputElement;
  globalThis.HTMLInputElement = KanaInput;
  try {
    const type = (value, data = value) => {
      const input = new KanaInput();
      initKanaInputs({ querySelectorAll() { return [input]; } });
      for (let index = 0; index < value.length; index += 1) {
        const eventData = data === null ? null : value[index];
        input.type(value[index], eventData);
      }
      return input.value;
    };

    assert.equal(type("nn"), "ん");
    assert.equal(type("nnn"), "んn");
    assert.equal(type("nnnn"), "んん");
    assert.equal(type("nna"), "んあ");
    assert.equal(type("nnya"), "んや");
    assert.equal(type("nnnya"), "んにゃ");
    assert.equal(type("konnnichiha"), "こんにちは");
    assert.equal(type("nna", null), "んあ");
    assert.equal(type("honnyu"), "ほんゆ");
    assert.equal(type("honnyuu"), "ほんゆう");
    assert.equal(type("HONNYUU"), "ホンユウ");
    assert.equal(type("honnyuu", null), "ほんゆう");
    assert.equal(type("hon'yuu"), "ほんゆう");
  } finally {
    globalThis.HTMLInputElement = previousInput;
  }
});

test("keeps input after a batched double n visible until it converts", () => {
  const previousInput = globalThis.HTMLInputElement;
  globalThis.HTMLInputElement = KanaInput;
  try {
    const input = new KanaInput();
    initKanaInputs({ querySelectorAll() { return [input]; } });

    input.type("nn", "nn");
    assert.equal(input.value, "ん");
    input.type("y");
    assert.equal(input.value, "んy");
    input.type("u");
    assert.equal(input.value, "んゆ");
  } finally {
    globalThis.HTMLInputElement = previousInput;
  }
});

test("falls back to input events when beforeinput cannot be handled", () => {
  const previousInput = globalThis.HTMLInputElement;
  globalThis.HTMLInputElement = KanaInput;
  try {
    const input = new KanaInput();
    initKanaInputs({ querySelectorAll() { return [input]; } });

    input.type("n", "n", { cancelable: false });
    input.type("n", "n", { cancelable: false });
    assert.equal(input.value, "ん");
    input.type("y", "y", { cancelable: false });
    input.type("u", "u", { cancelable: false });
    assert.equal(input.value, "んゆ");
  } finally {
    globalThis.HTMLInputElement = previousInput;
  }
});
