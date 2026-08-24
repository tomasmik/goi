import assert from "node:assert/strict";
import test from "node:test";

import {
  initMiningEnrichment,
  selectMiningCandidate,
  translateInBrowser,
} from "../static/js/mining-enrichment.js";

test("candidate selection shows one editor and updates the pressed choice", () => {
  function classList(initial = []) {
    const values = new Set(initial);
    return {
      contains(value) { return values.has(value); },
      toggle(value, enabled) { enabled ? values.add(value) : values.delete(value); },
    };
  }

  const choices = ["candidate-1", "candidate-2"].map((id, index) => ({
    dataset: { miningCandidateChoice: id },
    classList: classList(index === 0 ? ["is-selected"] : []),
    attributes: { "aria-pressed": index === 0 ? "true" : "false" },
    setAttribute(name, value) { this.attributes[name] = value; },
  }));
  const editors = ["candidate-1", "candidate-2"].map((id, index) => ({
    id,
    hidden: index !== 0,
  }));
  const root = {
    querySelectorAll(selector) {
      return selector === "[data-mining-candidate-choice]" ? choices : editors;
    },
  };

  const selected = selectMiningCandidate(root, "candidate-2");

  assert.equal(selected, editors[1]);
  assert.equal(choices[0].classList.contains("is-selected"), false);
  assert.equal(choices[0].attributes["aria-pressed"], "false");
  assert.equal(choices[1].classList.contains("is-selected"), true);
  assert.equal(choices[1].attributes["aria-pressed"], "true");
  assert.equal(editors[0].hidden, true);
  assert.equal(editors[1].hidden, false);
});

test("uses and caches Chrome's on-device translator", async () => {
  let creates = 0;
  let translations = 0;
  const browser = {
    Translator: {
      async availability() { return "available"; },
      async create() {
        creates += 1;
        return {
          async translate(text) {
            translations += 1;
            return text === "猫がいる。" ? "There is a cat." : "";
          },
        };
      },
    },
  };

  assert.equal(await translateInBrowser(browser, "猫がいる。"), "There is a cat.");
  assert.equal(await translateInBrowser(browser, "猫がいる。"), "There is a cat.");
  assert.equal(creates, 1);
  assert.equal(translations, 1);
});

test("allows the mining form to fall back when on-device translation is unavailable", async () => {
  const browser = {
    Translator: {
      async availability() { return "unavailable"; },
      async create() { throw new Error("should not create"); },
    },
  };

  assert.equal(await translateInBrowser(browser, "犬がいる。"), null);
});

test("uses Goi's configured translator before Chrome on the mining form", async () => {
  const listeners = new Map();
  let chromeTranslations = 0;
  let submissions = 0;
  const sentence = { value: "Japanese sentence" };
  const translation = { value: "" };
  const form = {
    querySelector(selector) {
      return selector.includes("example_sentence") ? sentence : translation;
    },
    requestSubmit(button) {
      assert.equal(button.dataset.remoteTranslation, "true");
      submissions += 1;
    },
  };
  const button = {
    dataset: { remoteTranslation: "true" },
    textContent: "Translate sentence",
    addEventListener(type, listener) {
      listeners.set(type, listener);
    },
    closest() {
      return form;
    },
  };
  const root = {
    querySelectorAll(selector) {
      return selector === "[data-translate-sentence]" ? [button] : [];
    },
    querySelector() {
      return null;
    },
  };
  const browser = {
    Translator: {
      async availability() {
        return "available";
      },
      async create() {
        return {
          async translate() {
            chromeTranslations += 1;
            return "Translated sentence";
          },
        };
      },
    },
  };

  initMiningEnrichment(root, browser);
  await listeners.get("click")({ preventDefault() {} });

  assert.equal(submissions, 1);
  assert.equal(chromeTranslations, 0);
});
