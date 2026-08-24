(function (root, factory) {
  const api = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.dictionaryClient = api;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  function lookup(runtime, expression) {
    return runtime.sendMessage({
      type: "goi.dictionary.lookup",
      version: 1,
      expression: String(expression || "").trim()
    });
  }

  return { lookup };
});
