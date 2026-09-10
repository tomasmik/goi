import assert from "node:assert/strict";
import test from "node:test";

import { initLocalTimes, initTimeZoneControls } from "../static/js/local-time.js";

test("connected-browser times use Goi's configured timezone", () => {
  let options;
  const time = {
    dateTime: "2026-08-06T12:00:00Z",
    dataset: { timeZone: "Europe/Vilnius" },
    textContent: "",
  };
  const intl = {
    DateTimeFormat: function (_locale, nextOptions) {
      options = nextOptions;
      return { format() { return "Aug 6, 2026, 3:00 PM"; } };
    },
  };
  initLocalTimes({ querySelectorAll() { return [time]; } }, intl);
  assert.equal(options.timeZone, "Europe/Vilnius");
  assert.equal(time.textContent, "Aug 6, 2026, 3:00 PM");
});

test("timezone detection fills the browser timezone", () => {
  let click;
  let inputListener;
  const input = {
    id: "time-zone",
    value: "UTC",
    getAttribute() { return null; },
    addEventListener(type, listener) { if (type === "input") inputListener = listener; },
  };
  const current = { textContent: "UTC" };
  const button = {
    addEventListener(type, listener) { if (type === "click") click = listener; },
  };
  const root = {
    querySelectorAll() { return [input]; },
    querySelector(selector) { return selector.includes("current") ? current : button; },
  };
  const intl = {
    DateTimeFormat() {
      return { resolvedOptions() { return { timeZone: "Europe/Vilnius" }; } };
    },
  };
  initTimeZoneControls(root, intl);
  click();
  assert.equal(input.value, "Europe/Vilnius");
  assert.equal(current.textContent, "Europe/Vilnius");

  input.value = "Asia/Tokyo";
  inputListener();
  assert.equal(current.textContent, "Asia/Tokyo");
});
