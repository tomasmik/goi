(function (root, factory) {
  const api = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.dictionaryCache = api;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  function create(options) {
    const values = new Map();
    const pending = new Map();
    const now = options.now || Date.now;
    let generation = 0;

    async function lookup(key, load, canStore) {
      const cached = values.get(key);
      if (cached && now() - cached.savedAt < options.ttlMs) {
        values.delete(key);
        values.set(key, cached);
        return cached.value;
      }
      values.delete(key);

      if (pending.has(key)) {
        return pending.get(key);
      }

      const startedInGeneration = generation;
      const request = Promise.resolve().then(load);
      pending.set(key, request);
      try {
        const value = await request;
        if (generation === startedInGeneration && (!canStore || canStore())) {
          values.set(key, { value, savedAt: now() });
          while (values.size > options.limit) {
            values.delete(values.keys().next().value);
          }
        }
        return value;
      } finally {
        if (pending.get(key) === request) {
          pending.delete(key);
        }
      }
    }

    function clear() {
      generation += 1;
      values.clear();
      pending.clear();
    }

    return { lookup, clear };
  }

  return { create };
});
