(function (root, factory) {
  const api = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.runtimeRouter = api;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  function create(options) {
    const routes = options.routes;

    return function handleRuntimeMessage(message, sender, sendResponse) {
      if (!message || message.version !== 1 || !sender || sender.id !== options.runtimeID) {
        return false;
      }
      if (typeof message.type !== "string" ||
          !Object.prototype.hasOwnProperty.call(routes, message.type)) {
        return false;
      }
      const route = routes[message.type];
      if (route.allow && !route.allow(sender)) {
        sendResponse({ ok: false, errorCode: "unavailable_page" });
        return false;
      }

      Promise.resolve()
        .then(function () {
          return route.handle(message, sender);
        })
        .then(sendResponse)
        .catch(function (error) {
          sendResponse(options.errorResponse(message.type, error));
        });
      return true;
    };
  }

  return { create };
});
