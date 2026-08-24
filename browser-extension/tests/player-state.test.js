const test = require("node:test");
const assert = require("node:assert/strict");

const playerState = require("../player/player-state.js");

function storage(initial = {}) {
  const values = { ...initial };
  return {
    getItem(key) { return values[key] || null; },
    setItem(key, value) { values[key] = value; },
    values
  };
}

test("loads bounded player timing from valid storage", function () {
  const key = "state";
  const store = storage({
    [key]: JSON.stringify({ video: { offsetMs: 90000, playbackSeconds: 12.5 } })
  });
  const state = playerState.create(store, {
    key,
    clampOffset(value) { return Math.min(1000, Number(value) || 0); }
  });
  assert.deepEqual(state.get("video"), { offsetMs: 1000, playbackSeconds: 12.5 });
});

test("ignores corrupt state and keeps only recent object entries", function () {
  const key = "state";
  const store = storage({ [key]: "not json" });
  const state = playerState.create(store, { key, limit: 2 });
  assert.deepEqual(state.get("video"), { offsetMs: 0, playbackSeconds: 0 });

  state.update("one", "one.mp4", { playbackSeconds: 1 });
  state.update("two", "two.mp4", { playbackSeconds: 2 });
  state.update("three", "three.mp4", { playbackSeconds: 3 });
  const saved = JSON.parse(store.values[key]);
  assert.deepEqual(Object.keys(saved), ["three", "two"]);
});

test("warns only for unfinished mining work", function () {
  assert.equal(playerState.shouldWarnBeforeUnload(), false);
  assert.equal(playerState.shouldWarnBeforeUnload({}), false);
  assert.equal(playerState.shouldWarnBeforeUnload({ videoReady: true, playbackSeconds: 42 }), false);
  assert.equal(playerState.shouldWarnBeforeUnload({ captureDraftDirty: true }), true);
  assert.equal(playerState.shouldWarnBeforeUnload({ captureBusy: true }), true);
  assert.equal(playerState.shouldWarnBeforeUnload({ sendingBatch: true }), true);
});
