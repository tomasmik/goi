import assert from "node:assert/strict";
import test from "node:test";

import { copyTarget, initCopyButtons } from "../static/js/clipboard.js";

function target(value = "goi_token") {
  return {
    value,
    focused: false,
    selected: false,
    focus() {
      this.focused = true;
    },
    select() {
      this.selected = true;
    },
  };
}

test("copies with the Clipboard API when it is available", async () => {
  const field = target();
  let copied = "";

  const success = await copyTarget(field, {
    async writeText(value) {
      copied = value;
    },
  });

  assert.equal(success, true);
  assert.equal(copied, field.value);
  assert.equal(field.selected, false);
});

test("falls back to selecting and copying the token", async () => {
  const field = target();
  let legacyCopyCalled = false;

  const success = await copyTarget(
    field,
    { async writeText() { throw new Error("clipboard unavailable"); } },
    () => {
      legacyCopyCalled = true;
      return true;
    }
  );

  assert.equal(success, true);
  assert.equal(field.focused, true);
  assert.equal(field.selected, true);
  assert.equal(legacyCopyCalled, true);
});

test("reports when manual copying is still required", async () => {
  const field = target();

  const success = await copyTarget(field, undefined, () => false);

  assert.equal(success, false);
  assert.equal(field.selected, true);
});

test("copy button enhancement binds each button once", () => {
  let clickBindings = 0;
  const button = {
    dataset: { copyTarget: "#token" },
    addEventListener(type) {
      if (type === "click") {
        clickBindings += 1;
      }
    },
  };
  const root = {
    querySelectorAll() {
      return [button];
    },
  };

  initCopyButtons(root);
  initCopyButtons(root);

  assert.equal(clickBindings, 1);
  assert.equal(button.dataset.copyBound, "true");
});
