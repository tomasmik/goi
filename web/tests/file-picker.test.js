import assert from "node:assert/strict";
import test from "node:test";

import { initFilePickers } from "../static/js/file-picker.js";

test("file pickers show selected file names", () => {
  let change;
  const name = { textContent: "" };
  const input = {
    dataset: {},
    files: [],
    addEventListener(type, listener) { if (type === "change") change = listener; },
    closest() { return { querySelector() { return name; } }; },
  };
  initFilePickers({ querySelectorAll() { return [input]; } });
  assert.equal(name.textContent, "No file selected");

  input.files = [{ name: "pronunciation.mp3" }];
  change();
  assert.equal(name.textContent, "pronunciation.mp3");
});

test("file pickers summarize multiple selected files", () => {
  const name = { textContent: "" };
  const input = {
    dataset: {},
    files: [{ name: "one.webm" }, { name: "two.webm" }],
    addEventListener() {},
    closest() { return { querySelector() { return name; } }; },
  };
  initFilePickers({ querySelectorAll() { return [input]; } });
  assert.equal(name.textContent, "2 files selected");
});
