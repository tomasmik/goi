const test = require("node:test");
const assert = require("node:assert/strict");

const {
  classNames,
  createHarness,
  descendants,
  settingsModel,
} = require("./helpers/overlay-harness.js");

test("listening mode hides both caption layers while coverage keeps running", async () => {
  const harness = createHarness("猫", {
    persistedSettings: { hideNativeCaptions: false },
    coverageResponse(blocks) {
      const block = blocks[0];
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 1,
          unknown_unique: 0,
          excluded_names: 0,
        },
        blocks: [{
          id: block.id,
          tokens: [{
            surface: block.text,
            expression: block.text,
            start_utf16: 0,
            end_utf16: block.text.length,
            status: "known",
          }],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();
  const captionText = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-text")
  );
  const overlay = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay")
  );
  const visibility = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay-visibility")
  );

  assert.equal(visibility.textContent, "Hide");
  assert.equal(visibility["aria-label"], "Hide captions, keep mining");
  assert.equal(visibility["aria-pressed"], "false");
  assert.equal(classNames(harness.player).has("goi-ext-hide-native-captions"), false);

  visibility.dispatch("click");
  await new Promise(setImmediate);

  assert.equal(captionText.hidden, true);
  assert.equal(overlay.hidden, false);
  assert.equal(classNames(overlay).has("goi-ext-overlay--captions-hidden"), true);
  assert.equal(classNames(harness.player).has("goi-ext-hide-native-captions"), true);
  assert.equal(visibility.textContent, "Show");
  assert.equal(visibility["aria-label"], "Show captions");
  assert.equal(visibility["aria-pressed"], "true");
  assert.equal(classNames(visibility).has("goi-ext-overlay-visibility--active"), true);
  assert.deepEqual(JSON.parse(JSON.stringify(harness.messages.at(-1).patch)), {
    displayMode: "hidden",
  });

  harness.setCaption("犬");
  harness.readCaption();
  await harness.analyzeCaption();
  assert.equal(harness.coverage().captionsAnalyzed, 2);
  assert.equal(captionText.hidden, true);

  visibility.dispatch("click");
  await new Promise(setImmediate);
  assert.equal(captionText.hidden, false);
  assert.equal(classNames(harness.player).has("goi-ext-hide-native-captions"), false);
  assert.equal(visibility["aria-label"], "Hide captions, keep mining");
  assert.equal(visibility["aria-pressed"], "false");
});

test("persists a pause behavior with one radio choice", async () => {
  const harness = createHarness();
  await harness.start();
  const pauseChoice = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay-radio") && node.value === "on_selection"
  );

  pauseChoice.checked = true;
  pauseChoice.dispatch("change");
  await new Promise(setImmediate);

  assert.deepEqual(JSON.parse(JSON.stringify(harness.messages.at(-1))), {
    type: "goi.settings.patch",
    version: 1,
    patch: { pauseBehavior: "on_selection" },
  });
  const selected = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-overlay-radio") &&
      node.name === "goi-ext-pauseBehavior" && node.checked
  );
  assert.deepEqual(selected.map((control) => control.value), ["on_selection"]);
});

test("exposes and synchronizes the caption settings disclosure state", async () => {
  const harness = createHarness();
  await harness.start();
  const menuButton = descendants(harness.player).find((node) =>
    node["aria-label"] === "Show Goi caption controls"
  );
  const settingsButton = descendants(harness.player).find((node) => node.textContent === "Settings");
  const rail = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay-rail")
  );
  const coverage = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-coverage")
  );
  const controls = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay-controls")
  );
  const overlay = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay")
  );

  assert.equal(settingsButton["aria-controls"], controls.id);
  assert.equal(settingsButton["aria-expanded"], "false");
  assert.equal(controls.hidden, true);
  assert.equal(classNames(rail).has("goi-ext-overlay-rail--expanded"), false);
  assert.equal(coverage.parentElement, rail);

  menuButton.dispatch("click");
  assert.equal(menuButton["aria-expanded"], "true");
  assert.equal(classNames(rail).has("goi-ext-overlay-rail--expanded"), true);
  settingsButton.dispatch("click");
  assert.equal(settingsButton["aria-expanded"], "true");
  assert.equal(controls.hidden, false);

  const closeSettings = descendants(controls).find((node) =>
    node["aria-label"] === "Close caption settings"
  );
  assert.ok(closeSettings);
  closeSettings.dispatch("click");
  assert.equal(settingsButton["aria-expanded"], "false");
  assert.equal(controls.hidden, true);
  assert.equal(settingsButton.focused, true);

  settingsButton.dispatch("click");

  overlay.dispatch("keydown", { key: "Escape" });
  assert.equal(settingsButton["aria-expanded"], "false");
  assert.equal(controls.hidden, true);
  assert.equal(settingsButton.focused, true);

  overlay.dispatch("keydown", { key: "Escape" });
  assert.equal(menuButton["aria-expanded"], "false");
  assert.equal(classNames(rail).has("goi-ext-overlay-rail--expanded"), false);
});

test("restores the stored pause choice when a settings write fails", async () => {
  const harness = createHarness("", {
    patchFailure: "response",
    persistedSettings: { pauseBehavior: "never" },
  });
  await harness.start();
  const pauseChoice = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay-radio") && node.value === "after_capture"
  );

  pauseChoice.checked = true;
  pauseChoice.dispatch("change");
  await new Promise(setImmediate);

  const selected = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-overlay-radio") &&
      node.name === "goi-ext-pauseBehavior" && node.checked
  );
  assert.deepEqual(selected.map((control) => control.value), ["never"]);
});

test("pointer cancellation finishes a drag and removes document listeners", async () => {
  const harness = createHarness();
  await harness.start();
  const handle = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay-handle")
  );
  assert.equal(handle.tagName, "SPAN");
  assert.equal(handle["aria-hidden"], "true");

  handle.dispatch("pointerdown", { pointerId: 7 });
  assert.equal(handle.hasPointerCapture(7), true);
  assert.equal(harness.document.listenerCount("pointermove"), 1);
  assert.equal(harness.document.listenerCount("pointercancel"), 1);

  harness.document.dispatch("pointermove", { pointerId: 7, clientY: 25 });
  harness.document.dispatch("pointercancel", { pointerId: 7 });

  assert.equal(handle.hasPointerCapture(7), false);
  assert.equal(harness.document.listenerCount("pointermove"), 0);
  assert.equal(harness.document.listenerCount("pointerup"), 0);
  assert.equal(harness.document.listenerCount("pointercancel"), 0);
  assert.deepEqual(JSON.parse(JSON.stringify(harness.messages.at(-1))), {
    type: "goi.settings.patch",
    version: 1,
    patch: { verticalPercent: 25 },
  });
});

test("hides native captions only while a nonempty mirror is rendered", async () => {
  const harness = createHarness("日本語を勉強する。");
  await harness.start();
  assert.equal(classNames(harness.player).has("goi-ext-hide-native-captions"), true);

  harness.clearCaption();
  harness.readCaption();
  harness.runTimer(500);

  assert.equal(classNames(harness.player).has("goi-ext-hide-native-captions"), false);
});

test("does not read captions when the persisted overlay setting is disabled", async () => {
  const harness = createHarness("日本語を勉強する。", {
    persistedSettings: { overlayEnabled: false },
  });

  await harness.start();
  harness.document.dispatch("yt-navigate-finish");

  assert.equal(harness.captionReadCount(), 0);
  assert.equal(harness.activeCaption(), "");
  assert.deepEqual(harness.pendingTimerIDs(16), []);
  assert.equal(classNames(harness.player).has("goi-ext-hide-native-captions"), false);
});

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

