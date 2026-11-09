const test = require("node:test");
const assert = require("node:assert/strict");

const {
  sanitizeSettings,
  sanitizeSettingsPatch,
} = require("../shared/settings-model.js");

test("sanitizes and clamps an allowlisted settings patch", () => {
  assert.deepEqual(
    sanitizeSettingsPatch({
      overlayEnabled: false,
      fontSizePx: "120",
      verticalPercent: 4.2,
      backgroundOpacity: 0.75,
      pauseBehavior: "after_capture",
      hideNativeCaptions: false,
      displayMode: "unknown_only",
      automaticCaptionMode: "live",
      furiganaEnabled: true,
      hoverLookupEnabled: true,
      token: "must-not-pass",
    }),
    {
      overlayEnabled: false,
      hideNativeCaptions: false,
      displayMode: "unknown_only",
      automaticCaptionMode: "live",
      furiganaEnabled: true,
      hoverLookupEnabled: true,
      fontSizePx: 96,
      verticalPercent: 10,
      backgroundOpacity: 0.75,
      pauseBehavior: "after_capture",
    },
  );
});

test("ignores invalid values without inventing patch fields", () => {
  assert.deepEqual(
    sanitizeSettingsPatch({
      overlayEnabled: "yes",
      fontSizePx: "",
      verticalPercent: NaN,
      backgroundOpacity: Infinity,
      pauseBehavior: "sometimes",
      hideNativeCaptions: 1,
      automaticCaptionMode: "sometimes",
    }),
    {},
  );
});

test("merges sanitized input over defaults", () => {
  assert.deepEqual(sanitizeSettings({ fontSizePx: 18, pauseBehavior: "on_selection" }), {
    overlayEnabled: true,
    fontSizePx: 18,
    verticalPercent: 78,
    backgroundOpacity: 0.65,
    pauseBehavior: "on_selection",
    hideNativeCaptions: true,
    displayMode: "always",
    automaticCaptionMode: "full",
    coverageDisplay: "full",
    furiganaEnabled: false,
    hoverLookupEnabled: false,
  });
});
