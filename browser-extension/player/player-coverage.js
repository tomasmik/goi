(function (root, factory) {
  const captureModel = typeof module === "object" && module.exports
    ? require("../shared/capture-model.js")
    : root.GoiExtension.captureModel;
  const subtitleModel = typeof module === "object" && module.exports
    ? require("../shared/subtitle-model.js")
    : root.GoiExtension.subtitleModel;
  const api = factory(captureModel, subtitleModel);

  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.playerCoverage = api;
})(typeof globalThis === "undefined" ? self : globalThis, function (captureModel, subtitleModel) {
  "use strict";

  function emptyState() {
    return {
      completed: 0,
      total: 0,
      knownOccurrences: 0,
      totalOccurrences: 0,
      excludedNames: 0,
      failed: false,
      running: false
    };
  }

  function addResult(state, result) {
    const summary = result.summary;
    return {
      ...state,
      completed: state.completed + result.blocks.length,
      knownOccurrences: state.knownOccurrences + summary.known_occurrences,
      totalOccurrences: state.totalOccurrences + summary.total_occurrences,
      excludedNames: state.excludedNames + summary.excluded_names
    };
  }

  function summaryText(state, unknownCount) {
    if (!state.total) {
      return "Waiting for subtitles";
    }
    const progress = state.completed + "/" + state.total + " checked";
    const excluded = state.excludedNames > 0
      ? " · " + state.excludedNames +
        (state.excludedNames === 1 ? " name excluded" : " names excluded")
      : "";
    const partial = state.failed ? " · partial" : "";
    if (state.totalOccurrences > 0) {
      const percent = captureModel.coveragePercent(
        state.knownOccurrences,
        state.totalOccurrences
      );
      return percent + "% known · " + unknownCount + " unknown · " + progress + excluded + partial;
    }
    if (state.completed > 0) {
      return "No classifiable Japanese · " + progress + excluded + partial;
    }
    if (state.running) {
      return "Checking subtitles…";
    }
    return state.failed ? "Coverage unavailable" : progress;
  }

  return { addResult, emptyState, summaryText, validBatchResponse: subtitleModel.validCoverageBatch };
});
