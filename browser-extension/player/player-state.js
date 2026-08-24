(function (root, factory) {
  const api = factory();
  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.playerState = api;
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
})(globalThis, function () {
  "use strict";

  function create(storage, options = {}) {
    const key = options.key || "goiLocalPlayerMediaStateV1";
    const limit = Number.isSafeInteger(options.limit) && options.limit > 0 ? options.limit : 20;
    const clampOffset = typeof options.clampOffset === "function"
      ? options.clampOffset
      : function (value) { return Number(value) || 0; };

    function readAll() {
      try {
        const value = JSON.parse(storage.getItem(key) || "{}");
        return value && typeof value === "object" && !Array.isArray(value) ? value : {};
      } catch (_error) {
        return {};
      }
    }

    function get(identity) {
      const saved = readAll()[identity];
      const value = saved && typeof saved === "object" && !Array.isArray(saved) ? saved : {};
      const playbackSeconds = Number(value.playbackSeconds);
      return {
        offsetMs: clampOffset(value.offsetMs || 0),
        playbackSeconds: Number.isFinite(playbackSeconds) && playbackSeconds > 0
          ? playbackSeconds
          : 0
      };
    }

    function update(identity, videoName, changes) {
      if (!identity) {
        return;
      }
      try {
        const state = readAll();
        const current = state[identity];
        const newestSavedAt = Object.values(state).reduce(function (latest, value) {
          return Math.max(latest, Number(value?.updatedAt) || 0);
        }, 0);
        state[identity] = {
          ...(current && typeof current === "object" && !Array.isArray(current) ? current : {}),
          ...changes,
          videoName,
          updatedAt: Math.max(Date.now(), newestSavedAt + 1)
        };
        const recent = Object.entries(state)
          .filter(function (entry) {
            return entry[1] && typeof entry[1] === "object" && !Array.isArray(entry[1]);
          })
          .sort(function (left, right) {
            return (Number(right[1].updatedAt) || 0) - (Number(left[1].updatedAt) || 0);
          })
          .slice(0, limit);
        storage.setItem(key, JSON.stringify(Object.fromEntries(recent)));
      } catch (_error) {
        return;
      }
    }

    return { get, update };
  }

  function shouldWarnBeforeUnload(state) {
    return Boolean(
      state && (state.captureDraftDirty || state.captureBusy || state.sendingBatch)
    );
  }

  return { create, shouldWarnBeforeUnload };
});
