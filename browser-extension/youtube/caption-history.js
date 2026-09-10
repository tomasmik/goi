(function (root, factory) {
  const api = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }
  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.captionHistory = api;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  const MAX_OBSERVED_SUBTITLE_LINES = 300;
  const MAX_OBSERVED_SUBTITLE_CHARACTERS = 120000;

  function create(subtitleModel) {
    let lines = [];
    let characters = 0;

    function lineFromOccurrence(occurrence) {
      const text = String(occurrence && occurrence.analysisText || "").trim();
      if (!text) {
        return null;
      }
      return {
        id: occurrence.id,
        text,
        sourcePositionMs: Number.isSafeInteger(occurrence.sourcePositionMs)
          ? occurrence.sourcePositionMs
          : null,
        sourceTitle: String(occurrence.sourceTitle || "").slice(0, 500),
        sourceURL: String(occurrence.sourceURL || "").slice(0, 4096),
        classification: occurrence.coverageState === "ready"
          ? "ready"
          : occurrence.coverageState === "unavailable"
            ? "unavailable"
            : "pending",
        unknowns: subtitleModel.unknownWords(text, occurrence.coverageBlock),
        words: subtitleModel.words(text, occurrence.coverageBlock)
      };
    }

    function publish(occurrence) {
      if (occurrence.historyReplay) {
        return false;
      }
      const line = lineFromOccurrence(occurrence);
      if (!line) {
        return false;
      }
      const existingIndex = lines.findIndex(function (candidate) {
        return candidate.id === line.id;
      });
      if (existingIndex >= 0) {
        characters -= lines[existingIndex].text.length;
        lines[existingIndex] = line;
      } else {
        lines.push(line);
      }
      characters += line.text.length;
      while (lines.length > MAX_OBSERVED_SUBTITLE_LINES ||
          characters > MAX_OBSERVED_SUBTITLE_CHARACTERS) {
        characters -= lines.shift().text.length;
      }
      return true;
    }

    function snapshot(activeOccurrence) {
      const active = activeOccurrence && !activeOccurrence.historyReplay
        ? lineFromOccurrence(activeOccurrence)
        : null;
      const selected = [];
      let characters = active ? active.text.length : 0;
      for (let index = lines.length - 1; index >= 0; index -= 1) {
        const line = lines[index];
        if (selected.length >= MAX_OBSERVED_SUBTITLE_LINES - Number(Boolean(active)) ||
            characters + line.text.length > MAX_OBSERVED_SUBTITLE_CHARACTERS) {
          break;
        }
        selected.unshift(line);
        characters += line.text.length;
      }
      if (active) {
        selected.push(active);
      }
      return selected;
    }

    function get(lineID) {
      return lines.find(function (line) { return line.id === lineID; }) || null;
    }

    function clear() {
      lines = [];
      characters = 0;
    }

    return { line: lineFromOccurrence, publish, snapshot, get, clear };
  }

  return { create };
});
