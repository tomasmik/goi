const test = require("node:test");
const assert = require("node:assert/strict");

const {
  captionFromSegmentGroups,
  captureErrorMessage,
  directCaptureInput
} = require("../shared/caption-model.js");

test("builds direct capture input with the full caption as context", () => {
  assert.deepEqual(
    directCaptureInput(
      "勉強",
      "毎日、日本語を勉強する。",
      {
        sourceTitle: "Japanese lesson",
        sourceURL: "https://www.youtube.com/watch?v=example",
        sourcePositionMs: 48210
      }
    ),
    {
      rawText: "勉強",
      expression: "勉強",
      contextText: "毎日、日本語を勉強する。",
      sourceKind: "video",
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=example",
      sourcePositionMs: 48210
    }
  );
});

test("reconstructs the latest caption without dropping repeats or adding Japanese spaces", () => {
  assert.equal(
    captionFromSegmentGroups([
      ["old caption"],
      ["go", " ", "go", "!"],
    ]),
    "go go!"
  );
  assert.equal(
    captionFromSegmentGroups([["今日は", "日本語", "です。"]]),
    "今日は日本語です。"
  );
  assert.equal(
    captionFromSegmentGroups([["面白いところがたくさんあります\n", "から、一緒に歩きましょう！"]]),
    "面白いところがたくさんあります\nから、一緒に歩きましょう！"
  );
});

test("gives a specific prompt when Goi is not connected", () => {
  assert.equal(captureErrorMessage("not_connected"), "Connect Goi first");
  assert.equal(captureErrorMessage("queue_full"), "Queue full — retry when Goi is reachable");
  assert.equal(captureErrorMessage("network"), "Could not reach Goi");
});
