const test = require("node:test");
const assert = require("node:assert/strict");

const {
  classNames,
  createHarness,
  descendants,
  settingsModel,
} = require("./helpers/overlay-harness.js");

test("synced settings cancel a pending read and wake observation once", async () => {
  const harness = createHarness("日本語を勉強する。");
  await harness.start();
  harness.document.dispatch("yt-navigate-finish");
  const readsBeforeDisabling = harness.captionReadCount();

  harness.storageListener({
    [settingsModel.STORAGE_KEY]: { newValue: { overlayEnabled: false } },
  }, "sync");

  assert.deepEqual(harness.pendingTimerIDs(16), []);
  assert.equal(harness.activeCaption(), "");

  harness.storageListener({
    [settingsModel.STORAGE_KEY]: { newValue: { overlayEnabled: true } },
  }, "sync");
  harness.storageListener({
    [settingsModel.STORAGE_KEY]: { newValue: { overlayEnabled: true } },
  }, "sync");

  assert.equal(harness.pendingTimerIDs(16).length, 1);
  harness.runTimer(16);
  assert.equal(harness.captionReadCount(), readsBeforeDisabling + 1);
  assert.equal(harness.activeCaption(), "日本語を勉強する。");
});

test("does not postpone caption clearing while unrelated mutations continue", async () => {
  const harness = createHarness("日本語を勉強する。");
  await harness.start();
  harness.clearCaption();
  harness.readCaption();
  const clearTimer = harness.pendingTimerIDs(500);

  harness.readCaption();

  assert.deepEqual(harness.pendingTimerIDs(500), clearTimer);
  harness.runTimer(500);
  assert.equal(classNames(harness.player).has("goi-ext-hide-native-captions"), false);
});

test("does not postpone coverage while the same caption keeps mutating", async () => {
  const harness = createHarness("日本語を勉強する。", {
    coverageResponse: {
      summary: {
        known_occurrences: 1,
        total_occurrences: 1,
        unknown_unique: 0,
        excluded_names: 0,
      },
      blocks: [{ id: 1, tokens: [] }],
    },
  });
  await harness.start();
  const coverageTimer = harness.pendingTimerIDs(180);

  harness.readCaption();

  assert.deepEqual(harness.pendingTimerIDs(180), coverageTimer);
  await harness.analyzeCaption();
  assert.equal(harness.coverage().captionsAnalyzed, 1);
});

for (const [failure, description] of [
  ["response", "returns a failure"],
  ["reject", "rejects"],
]) {
  test(`restores persisted settings when a settings write ${description}`, async () => {
    const harness = createHarness("", {
      patchFailure: failure,
      persistedSettings: { fontSizePx: 34 },
    });
    await harness.start();
    const fontSize = descendants(harness.player).find((node) =>
      classNames(node).has("goi-ext-overlay-range")
    );

    fontSize.value = "48";
    fontSize.dispatch("change");
    assert.equal(fontSize.value, "48");
    await new Promise(setImmediate);

    const status = descendants(harness.player).find((node) =>
      classNames(node).has("goi-ext-caption-status")
    );
    assert.equal(fontSize.value, "34");
    assert.equal(status.textContent, "Settings not saved");
    assert.equal(classNames(status).has("goi-ext-caption-status--error"), true);
    assert.equal(status.hidden, false);
  });
}

