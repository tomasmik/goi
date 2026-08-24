const test = require("node:test");
const assert = require("node:assert/strict");

const coverage = require("../player/player-coverage.js");

function response(tokens, excludedNames = 0) {
  const known = tokens.filter(function (token) { return token.status !== "unknown"; }).length;
  return {
    blocks: [{ id: 1, tokens }],
    summary: {
      known_occurrences: known,
      total_occurrences: tokens.length,
      excluded_names: excludedNames
    }
  };
}

test("validates coverage tokens against the requested UTF-16 text", function () {
  const batch = [{ id: 1, text: "日本語" }];
  const valid = response([
    { surface: "日本語", start_utf16: 0, end_utf16: 3, status: "known" }
  ], 1);
  assert.equal(coverage.validBatchResponse(batch, valid), true);

  assert.equal(coverage.validBatchResponse(batch, {
    ...valid,
    blocks: [{ id: 1, tokens: [
      { surface: "日本", start_utf16: 0, end_utf16: 3, status: "known" }
    ] }]
  }), false);
  assert.equal(coverage.validBatchResponse(batch, {
    ...valid,
    blocks: [{ id: 1, tokens: [
      { surface: "日本語", start_utf16: 0, end_utf16: 3, status: "maybe" }
    ] }]
  }), false);
});

test("counts active and suspended leeches as known while preserving their status", function () {
  const batch = [{ id: 1, text: "猫犬" }];
  const result = response([
    { surface: "猫", start_utf16: 0, end_utf16: 1, status: "leech" },
    { surface: "犬", start_utf16: 1, end_utf16: 2, status: "suspended_leech" }
  ]);

  assert.equal(coverage.validBatchResponse(batch, result), true);
  assert.equal(result.summary.known_occurrences, 2);
});

test("aggregates batches without mutating the previous state", function () {
  const state = { ...coverage.emptyState(), total: 2, running: true };
  const result = response([
    { surface: "日本語", start_utf16: 0, end_utf16: 3, status: "known" }
  ], 2);
  const next = coverage.addResult(state, result);

  assert.equal(state.completed, 0);
  assert.deepEqual(next, {
    ...state,
    completed: 1,
    knownOccurrences: 1,
    totalOccurrences: 1,
    excludedNames: 2
  });
});

test("formats running, complete, partial, and empty coverage summaries", function () {
  assert.equal(coverage.summaryText(coverage.emptyState(), 0), "Waiting for subtitles");
  assert.equal(coverage.summaryText({
    ...coverage.emptyState(), total: 2, running: true
  }, 0), "Checking subtitles…");
  assert.equal(coverage.summaryText({
    ...coverage.emptyState(), total: 2, completed: 2,
    knownOccurrences: 3, totalOccurrences: 4, excludedNames: 1
  }, 1), "75% known · 1 unknown · 2/2 checked · 1 name excluded");
  assert.equal(coverage.summaryText({
    ...coverage.emptyState(), total: 1, completed: 1,
    knownOccurrences: 199, totalOccurrences: 200
  }, 1), "99.5% known · 1 unknown · 1/1 checked");
  assert.equal(coverage.summaryText({
    ...coverage.emptyState(), total: 2, completed: 1, failed: true
  }, 0), "No classifiable Japanese · 1/2 checked · partial");
});
