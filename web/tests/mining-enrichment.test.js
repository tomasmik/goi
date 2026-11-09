import assert from "node:assert/strict";
import test from "node:test";

import {
  initMiningEnrichment,
  translateInBrowser,
} from "../static/js/mining-enrichment.js";

test("a failed translator initialization can be retried and then reused", async () => {
  let creates = 0;
  const browser = {
    Translator: {
      async create() {
        creates += 1;
        if (creates === 1) {
          throw new Error("Model download interrupted");
        }
        return { async translate(text) { return `Translated: ${text}`; } };
      },
    },
  };

  await assert.rejects(translateInBrowser(browser, "雨が降る。"), /Model download interrupted/);
  assert.equal(await translateInBrowser(browser, "雨が降る。"), "Translated: 雨が降る。");
  assert.equal(await translateInBrowser(browser, "雪が降る。"), "Translated: 雪が降る。");
  assert.equal(creates, 2);
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
