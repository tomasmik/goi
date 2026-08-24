(function (root, factory) {
  if (typeof module === "object" && module.exports) {
    module.exports = factory(require("./subtitle-model.js"));
    return;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.subtitleView = factory(root.GoiExtension.subtitleModel);
})(typeof globalThis === "undefined" ? self : globalThis, function (subtitleModel) {
  "use strict";

  function renderWord(container, surface, word, options) {
    const config = options || {};
    const status = config.status || word.status;
    if (!config.furiganaEnabled || status !== "unknown" || !word.reading) {
      container.textContent = surface;
      return;
    }

    const document = container.ownerDocument;
    subtitleModel.furiganaParts(surface, word.reading).forEach(function (part) {
      if (!part.reading) {
        container.appendChild(document.createTextNode(part.text));
        return;
      }
      const ruby = document.createElement("ruby");
      if (config.rubyClass) {
        ruby.className = config.rubyClass;
      }
      ruby.appendChild(document.createTextNode(part.text));
      const reading = document.createElement("rt");
      reading.textContent = part.reading;
      reading.setAttribute("aria-hidden", "true");
      ruby.appendChild(reading);
      container.appendChild(ruby);
    });
  }

  return { renderWord: renderWord };
});
