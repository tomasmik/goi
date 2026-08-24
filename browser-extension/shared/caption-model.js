(function (root, factory) {
  const exports = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = exports;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.captionModel = exports;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  function directCaptureInput(surface, fullCaption, source) {
    const sourceFields = source && typeof source === "object" ? source : {};
    const selectedSurface = String(surface || "");

    return {
      rawText: selectedSurface,
      expression: selectedSurface,
      contextText: String(fullCaption == null ? "" : fullCaption),
      sourceKind: "video",
      sourceTitle: sourceFields.sourceTitle,
      sourceURL: sourceFields.sourceURL,
      sourcePositionMs: sourceFields.sourcePositionMs
    };
  }

  function captionFromSegmentGroups(groups) {
    if (!Array.isArray(groups)) {
      return "";
    }
    for (let index = groups.length - 1; index >= 0; index -= 1) {
      if (!Array.isArray(groups[index])) {
        continue;
      }
      const caption = groups[index]
        .map(function (segment) {
          return String(segment == null ? "" : segment);
        })
        .join("")
        .replace(/\r\n?/gu, "\n")
        .split("\n")
        .map(function (line) {
          return line.replace(/[^\S\n]+/gu, " ").trim();
        })
        .filter(Boolean)
        .join("\n")
        .trim();
      if (caption) {
        return caption;
      }
    }
    return "";
  }

  function captureErrorMessage(code) {
    if (code === "unauthorized") {
      return "Check the Goi token";
    }
    if (code === "invalid_capture") {
      return "Could not save that word";
    }
    if (code === "not_connected") {
      return "Connect Goi first";
    }
    if (code === "queue_full") {
      return "Queue full — retry when Goi is reachable";
    }
    return "Could not reach Goi";
  }

  return {
    captionFromSegmentGroups: captionFromSegmentGroups,
    captureErrorMessage: captureErrorMessage,
    directCaptureInput: directCaptureInput
  };
});
