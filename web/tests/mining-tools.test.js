import assert from "node:assert/strict";
import test from "node:test";

import { initMiningTools } from "../static/js/mining-tools.js";

test("mining bulk actions appear only after a capture is selected", () => {
  let change;
  let active = false;
  const inputs = [
    { checked: false, dataset: { clearMatch: "true" } },
    { checked: false, dataset: { clearMatch: "false" } },
  ];
  const summary = { textContent: "" };
  const toolbar = { classList: { toggle(_name, value) { active = value; } } };
  const readyAction = { disabled: false, hasAttribute(name) { return name === "data-ready-only"; } };
  const otherAction = { disabled: false, hasAttribute() { return false; } };
  const bulk = {
    querySelector(selector) {
      if (selector === "[data-mining-selection-summary]") return summary;
      if (selector === "[data-mining-bulk-toolbar]") return toolbar;
      return null;
    },
    querySelectorAll(selector) {
      if (selector === '[name="capture_id"]' || selector === 'input[name="capture_id"]') return inputs;
      if (selector === 'input[name="capture_id"]:checked') return inputs.filter((input) => input.checked);
      if (selector === "[data-mining-bulk-action]") return [readyAction, otherAction];
      return [];
    },
    addEventListener(type, listener) { if (type === "change") change = listener; },
  };
  const root = {
    querySelector(selector) { return selector === "[data-mining-bulk]" ? bulk : null; },
  };

  initMiningTools(root, { location: { origin: "http://example.test" } });
  assert.equal(active, false);
  assert.equal(summary.textContent, "0 selected");
  assert.equal(readyAction.disabled, true);
  assert.equal(otherAction.disabled, true);

  inputs[0].checked = true;
  change();
  assert.equal(active, true);
  assert.equal(summary.textContent, "1 selected · 1 ready · 0 need review");
  assert.equal(readyAction.disabled, false);
  assert.equal(otherAction.disabled, false);
});
