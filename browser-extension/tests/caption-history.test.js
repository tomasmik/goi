const test = require("node:test");
const assert = require("node:assert/strict");
const captionHistory = require("../youtube/caption-history.js");
const subtitleModel = require("../shared/subtitle-model.js");

function occurrence(id, analysisText, fields = {}) {
  return { id, analysisText, sourcePositionMs: id * 1000, ...fields };
}

test("replaces an observed occurrence in place after classification and keeps repeated dialogue", function () {
  const history = captionHistory.create(subtitleModel);
  history.publish(occurrence(1, "猫"));
  history.publish(occurrence(2, "猫"));
  history.publish(occurrence(1, "猫", {
    coverageState: "ready",
    coverageBlock: { tokens: [{
      start_utf16: 0, end_utf16: 1, surface: "猫", expression: "猫", status: "unknown"
    }] }
  }));

  const lines = history.snapshot();
  assert.deepEqual(lines.map((line) => line.id), [1, 2]);
  assert.equal(lines[0].classification, "ready");
  assert.equal(lines[0].unknowns[0].expression, "猫");
  assert.equal(lines[1].classification, "pending");
  assert.equal(history.publish(occurrence(3, "猫", { historyReplay: true })), false);
  assert.deepEqual(history.snapshot(occurrence(3, "猫", { historyReplay: true })), lines);
});

test("reserves the active caption's slot without evicting stored history, then clears it on navigation", function () {
  const history = captionHistory.create(subtitleModel);
  for (let id = 1; id <= 301; id += 1) history.publish(occurrence(id, "字幕"));

  assert.equal(history.get(1), null);
  const snapshot = history.snapshot(occurrence(302, "今の字幕"));
  assert.equal(snapshot.length, 300);
  assert.equal(snapshot[0].id, 3);
  assert.equal(snapshot.at(-1).id, 302);
  assert.equal(history.get(2).text, "字幕");
  assert.equal(history.get(302), null);

  history.clear();
  assert.deepEqual(history.snapshot(), []);
  assert.equal(history.get(2), null);
  history.publish(occurrence(303, "新しい動画"));
  assert.deepEqual(history.snapshot().map((line) => line.id), [303]);
});

test("accounts for UTF-16 text replacement and active-caption space at the character limit", function () {
  const history = captionHistory.create(subtitleModel);
  const text = "猫".repeat(10000) + "😀".repeat(4000);
  for (let id = 1; id <= 6; id += 1) history.publish(occurrence(id, text));
  history.publish(occurrence(1, "猫"));
  history.publish(occurrence(7, text));
  assert.equal(history.snapshot().length, 7);
  history.publish(occurrence(1, text));
  assert.equal(history.get(1), null);
  assert.equal(history.snapshot().length, 6);

  const snapshot = history.snapshot(occurrence(8, text));
  assert.deepEqual(snapshot.map((line) => line.id), [3, 4, 5, 6, 7, 8]);
  assert.equal(snapshot.reduce((sum, line) => sum + line.text.length, 0), 108000);
  assert.equal(history.get(2).text, text);
});
