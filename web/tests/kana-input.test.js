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
  assert.equal(convertKana("nna", false), "んな");
  assert.equal(convertKana("nnya", false), "んにゃ");
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

test("commits repeated n input without leaving romaji behind", () => {
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
    assert.equal(type("nnn"), "んん");
    assert.equal(type("nnnn"), "んんん");
    assert.equal(type("nna"), "んな");
    assert.equal(type("nnya"), "んにゃ");
    assert.equal(type("konnichiha"), "こんにちは");
    assert.equal(type("nna", null), "んな");
  } finally {
    globalThis.HTMLInputElement = previousInput;
  }
});

test("keeps a batched double n reusable for the next kana", () => {
  const previousInput = globalThis.HTMLInputElement;
  globalThis.HTMLInputElement = KanaInput;
  try {
    const input = new KanaInput();
    initKanaInputs({ querySelectorAll() { return [input]; } });

    input.type("nn", "nn");
    assert.equal(input.value, "ん");
    input.type("a");
    assert.equal(input.value, "んな");
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
  } finally {
    globalThis.HTMLInputElement = previousInput;
  }
});
