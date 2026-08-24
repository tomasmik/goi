(function (root, factory) {
  const exports = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = exports;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.settingsModel = exports;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  const DEFAULT_SETTINGS = Object.freeze({
    overlayEnabled: true,
    fontSizePx: 34,
    verticalPercent: 78,
    backgroundOpacity: 0.65,
    pauseBehavior: "never",
    hideNativeCaptions: true,
    displayMode: "always",
    coverageDisplay: "full",
    furiganaEnabled: false,
    hoverLookupEnabled: false,
  });

  const PAUSE_BEHAVIORS = new Set(["never", "on_hover", "on_selection", "after_capture"]);
  const DISPLAY_MODES = new Set(["always", "hidden", "unknown_only", "pause_reveal"]);
  const COVERAGE_DISPLAYS = new Set(["full", "compact", "hidden"]);
  const STORAGE_KEY = "youtubeSettings";

  function clamp(value, minimum, maximum) {
    return Math.min(maximum, Math.max(minimum, value));
  }

  function finiteNumber(value) {
    if (typeof value === "string" && value.trim() === "") {
      return null;
    }
    const number = Number(value);
    return Number.isFinite(number) ? number : null;
  }

  function sanitizeSettingsPatch(input) {
    const patch = input && typeof input === "object" ? input : {};
    const sanitized = {};

    if (typeof patch.overlayEnabled === "boolean") {
      sanitized.overlayEnabled = patch.overlayEnabled;
    }
    if (typeof patch.hideNativeCaptions === "boolean") {
      sanitized.hideNativeCaptions = patch.hideNativeCaptions;
    }
    if (typeof patch.furiganaEnabled === "boolean") {
      sanitized.furiganaEnabled = patch.furiganaEnabled;
    }
    if (typeof patch.hoverLookupEnabled === "boolean") {
      sanitized.hoverLookupEnabled = patch.hoverLookupEnabled;
    }
    if (DISPLAY_MODES.has(patch.displayMode)) {
      sanitized.displayMode = patch.displayMode;
    }
    if (COVERAGE_DISPLAYS.has(patch.coverageDisplay)) {
      sanitized.coverageDisplay = patch.coverageDisplay;
    }

    const fontSize = finiteNumber(patch.fontSizePx);
    if (fontSize !== null) {
      sanitized.fontSizePx = Math.round(clamp(fontSize, 18, 96));
    }

    const verticalPosition = finiteNumber(patch.verticalPercent);
    if (verticalPosition !== null) {
      sanitized.verticalPercent = Math.round(clamp(verticalPosition, 10, 90));
    }

    const backgroundOpacity = finiteNumber(patch.backgroundOpacity);
    if (backgroundOpacity !== null) {
      sanitized.backgroundOpacity = clamp(backgroundOpacity, 0, 0.9);
    }

    if (PAUSE_BEHAVIORS.has(patch.pauseBehavior)) {
      sanitized.pauseBehavior = patch.pauseBehavior;
    }

    return sanitized;
  }

  function sanitizeSettings(input) {
    return Object.assign({}, DEFAULT_SETTINGS, sanitizeSettingsPatch(input));
  }

  function applyPatch(current, patch) {
    return Object.assign({}, sanitizeSettings(current), sanitizeSettingsPatch(patch));
  }

  function verticalPositionLabel(value) {
    const position = finiteNumber(value);
    if (position === null) {
      return "";
    }
    if (position <= 20) {
      return "Top";
    }
    if (position <= 40) {
      return "Upper";
    }
    if (position <= 60) {
      return "Middle";
    }
    if (position <= 80) {
      return "Lower";
    }
    return "Bottom";
  }

  return {
    DEFAULT_SETTINGS: DEFAULT_SETTINGS,
    DISPLAY_MODES: DISPLAY_MODES,
    STORAGE_KEY: STORAGE_KEY,
    applyPatch: applyPatch,
    sanitize: sanitizeSettings,
    sanitizeSettings: sanitizeSettings,
    sanitizeSettingsPatch: sanitizeSettingsPatch,
    verticalPositionLabel: verticalPositionLabel,
  };
});
