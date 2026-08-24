(function (root, factory) {
  const api = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.playerTranscript = api;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  function uniqueUnknownCount(cue) {
    return new Set((cue.unknowns || []).map(function (word) {
      return word.expression || word.surface;
    }).filter(Boolean)).size;
  }

  function classificationText(cue) {
    if (cue.classification === "pending") {
      return "Checking vocabulary";
    }
    if (cue.classification === "unavailable") {
      return "Coverage unavailable";
    }
    const count = uniqueUnknownCount(cue);
    return count === 0
      ? "All known"
      : count + (count === 1 ? " unknown word" : " unknown words");
  }

  function visibleCues(cues, unknownOnly) {
    if (!unknownOnly) {
      return cues;
    }
    return cues.filter(cueVisibleInUnknownFilter);
  }

  function cueVisibleInUnknownFilter(cue) {
    return cue.classification !== "ready" || (cue.unknowns || []).length > 0;
  }

  function hasVisibleCurrentCue(currentCueIDs, cueByID, unknownOnly) {
    return Array.from(currentCueIDs).some(function (cueID) {
      const cue = cueByID.get(cueID);
      return cue && (!unknownOnly || cueVisibleInUnknownFilter(cue));
    });
  }

  function overlayCueIDs(options) {
    const state = options || {};
    if (state.displayMode === "hidden") {
      return [];
    }
    let ids = Array.from(state.currentCueIDs || []);
    if (state.displayMode === "pause_reveal") {
      if (!state.paused) {
        return [];
      }
      if (!ids.length) {
        ids = Array.from(state.pauseRevealCueIDs || []);
      }
    }
    if (state.displayMode === "unknown_only") {
      ids = ids.filter(function (cueID) {
        const cue = state.cueByID.get(cueID);
        return cue && cueVisibleInUnknownFilter(cue);
      });
    }
    return ids;
  }

  return {
    classificationText,
    hasVisibleCurrentCue,
    overlayCueIDs,
    uniqueUnknownCount,
    visibleCues
  };
});
