import test from "node:test";
import assert from "node:assert/strict";

import { initConfirmations } from "../static/js/confirmation.js";

function confirmationHarness(answer) {
  const confirmation = { checked: false, required: true };
  const classes = new Set();
  let submit;
  let submitBindings = 0;
  const form = {
    dataset: { confirm: "Delete permanently?" },
    classList: {
      add(value) {
        classes.add(value);
      },
    },
    querySelector(selector) {
      return selector === "[data-confirm-field]" ? confirmation : null;
    },
    addEventListener(type, listener) {
      if (type === "submit") {
        submitBindings += 1;
        submit = listener;
      }
    },
  };
  const root = {
    querySelectorAll() {
      return [form];
    },
  };
  initConfirmations(root, () => answer);
  return { classes, confirmation, form, root, submit: () => submit, submitBindings: () => submitBindings };
}

test("accepted enhanced confirmations set the server confirmation field", function () {
  const harness = confirmationHarness(true);
  let prevented = false;

  harness.submit()({ preventDefault() { prevented = true; } });

  assert.equal(prevented, false);
  assert.equal(harness.confirmation.checked, true);
  assert.equal(harness.confirmation.required, false);
  assert.equal(harness.classes.has("confirmation-enhanced"), true);
});

test("cancelled enhanced confirmations cannot leave a stale confirmation", function () {
  const harness = confirmationHarness(false);
  harness.confirmation.checked = true;
  let prevented = false;

  harness.submit()({ preventDefault() { prevented = true; } });

  assert.equal(prevented, true);
  assert.equal(harness.confirmation.checked, false);
});
