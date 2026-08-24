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
    const handlers = options.handlers;
    const popupOnly = options.popupOnly || new Set();
    const topFrameOnly = options.topFrameOnly || new Set();

    return function handleRuntimeMessage(message, sender, sendResponse) {
      if (!message || message.version !== 1 || !sender || sender.id !== options.runtimeID) {
        return false;
      }
      if (typeof message.type !== "string" ||
          !Object.prototype.hasOwnProperty.call(handlers, message.type)) {
        return false;
      }
      if (popupOnly.has(message.type) && !options.popupSender(sender)) {
        sendResponse({ ok: false, errorCode: "unavailable_page" });
        return false;
      }
      if (topFrameOnly.has(message.type) && (!sender.tab || sender.frameId !== 0)) {
        sendResponse({ ok: false, errorCode: "unavailable_page" });
        return false;
      }

      Promise.resolve()
        .then(function () {
          return handlers[message.type](message, sender);
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
