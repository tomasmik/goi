const test = require("node:test");
const assert = require("node:assert/strict");

const transcript = require("../player/player-transcript.js");

function cue(id, classification, unknowns = []) {
  return { id, classification, unknowns };
}

test("describes coverage without counting duplicate dictionary forms", function () {
  const pending = cue(1, "pending");
  const unavailable = cue(2, "unavailable");
  const known = cue(3, "ready");
  const unknown = cue(4, "ready", [
    { surface: "食べ", expression: "食べる" },
    { surface: "食べる", expression: "食べる" },
    { surface: "猫", expression: "猫" }
  ]);

  assert.equal(transcript.classificationText(pending), "Checking vocabulary");
  assert.equal(transcript.classificationText(unavailable), "Coverage unavailable");
  assert.equal(transcript.classificationText(known), "All known");
  assert.equal(transcript.classificationText(unknown), "2 unknown words");
});

test("keeps unchecked lines visible while coverage is incomplete", function () {
  const cues = [
    cue(1, "ready"),
    cue(2, "pending"),
    cue(3, "unavailable"),
    cue(4, "ready", [{ surface: "猫", expression: "猫" }])
  ];

  assert.deepEqual(transcript.visibleCues(cues, false), cues);
  assert.deepEqual(transcript.visibleCues(cues, true).map(function (item) {
    return item.id;
  }), [2, 3, 4]);
});

test("applies adaptive subtitle modes to current and paused dialogue", function () {
  const cueByID = new Map([
    [1, cue(1, "ready")],
    [2, cue(2, "ready", [{ surface: "猫", expression: "猫" }])]
  ]);
  const base = {
    cueByID,
    currentCueIDs: new Set([1, 2]),
    pauseRevealCueIDs: [2],
    paused: false
  };

  assert.deepEqual(transcript.overlayCueIDs({ ...base, displayMode: "hidden" }), []);
  assert.deepEqual(transcript.overlayCueIDs({ ...base, displayMode: "always" }), [1, 2]);
  assert.deepEqual(transcript.overlayCueIDs({ ...base, displayMode: "unknown_only" }), [2]);
  assert.deepEqual(transcript.overlayCueIDs({ ...base, displayMode: "pause_reveal" }), []);
  assert.deepEqual(transcript.overlayCueIDs({
    ...base,
    currentCueIDs: new Set(),
    displayMode: "pause_reveal",
    paused: true
  }), [2]);
});

test("reports whether the current line survives the transcript filter", function () {
  const cueByID = new Map([
    [1, cue(1, "ready")],
    [2, cue(2, "pending")]
  ]);

  assert.equal(transcript.hasVisibleCurrentCue(new Set([1]), cueByID, false), true);
  assert.equal(transcript.hasVisibleCurrentCue(new Set([1]), cueByID, true), false);
  assert.equal(transcript.hasVisibleCurrentCue(new Set([2]), cueByID, true), true);
});
