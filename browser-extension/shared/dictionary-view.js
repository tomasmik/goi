(function (root, factory) {
  const exports = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = exports;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.dictionaryView = exports;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  function element(documentObject, tagName, className, text) {
    const node = documentObject.createElement(tagName);
    node.className = className;
    if (text !== undefined) {
      node.textContent = text;
    }
    return node;
  }

  function render(container, view, options) {
    const settings = options || {};
    const documentObject = settings.document || container.ownerDocument || globalThis.document;
    container.replaceChildren();
    if (!view.candidates.length) {
      container.appendChild(element(
        documentObject,
        "p",
        "goi-dictionary-message",
        view.message
      ));
      return;
    }
    if (view.candidates.length > 1) {
      container.appendChild(element(
        documentObject,
        "p",
        "goi-dictionary-summary",
        view.candidates.length + " matching entries"
      ));
    }
    view.candidates.forEach(function (candidate) {
      const entry = element(documentObject, "article", "goi-dictionary-entry");
      if (candidate.entrySequence !== null) {
        entry.dataset.entrySequence = String(candidate.entrySequence);
      }
      const heading = element(documentObject, "div", "goi-dictionary-heading");
      const headword = element(documentObject, "div", "goi-dictionary-headword");
      const term = element(
        documentObject,
        "strong",
        "goi-dictionary-term",
        candidate.written || view.query
      );
      term.lang = "ja";
      headword.appendChild(term);
      if (candidate.reading && candidate.reading !== candidate.written) {
        const reading = element(
          documentObject,
          "span",
          "goi-dictionary-reading",
          candidate.reading
        );
        reading.lang = "ja";
        headword.appendChild(reading);
      }
      heading.appendChild(headword);
      const frequencies = element(documentObject, "div", "goi-dictionary-frequencies");
      [["G", "Global", candidate.globalRank], ["N", "Novel", candidate.novelRank]].forEach(function (source) {
        const [letter, name, rank] = source;
        const label = rank == null ? "Jiten " + name + ": no rank available"
          : "Jiten " + name + " rank " + rank + "; lower means more frequent";
        const badge = element(documentObject, "span", "goi-dictionary-frequency",
          letter + " " + (rank == null ? "—" : String(rank).padStart(3, "0")));
        badge.title = label;
        badge.setAttribute("aria-label", label);
        frequencies.appendChild(badge);
      });
      heading.appendChild(frequencies);
      entry.appendChild(heading);
      candidate.senses.forEach(function (sense) {
        const senseNode = element(documentObject, "div", "goi-dictionary-sense");
        if (sense.partsOfSpeech.length) {
          const parts = element(documentObject, "div", "goi-dictionary-parts");
          sense.partsOfSpeech.forEach(function (part) {
            parts.appendChild(element(documentObject, "span", "goi-dictionary-part", part));
          });
          senseNode.appendChild(parts);
        }
        const meanings = element(documentObject, "ol", "goi-dictionary-meanings");
        sense.meanings.forEach(function (meaning) {
          meanings.appendChild(element(documentObject, "li", "", meaning));
        });
        senseNode.appendChild(meanings);
        entry.appendChild(senseNode);
      });
      if (typeof settings.onSelect === "function" && candidate.entrySequence !== null) {
        const selected = candidate.entrySequence === settings.selectedEntrySequence;
        const actionLabel = String(settings.actionLabel || "Use this entry");
        entry.classList.toggle("is-selected", selected);
        const action = element(
          documentObject,
          "button",
          "goi-dictionary-select",
          selected ? "Selected" : actionLabel
        );
        action.type = "button";
        action.setAttribute("aria-pressed", selected ? "true" : "false");
        action.addEventListener("click", function (event) {
          if (event && event.isTrusted === false) {
            return;
          }
          if (settings.onSelect(candidate) === false) {
            return;
          }
          settings.selectedEntrySequence = candidate.entrySequence;
          render(container, view, settings);
        });
        entry.appendChild(action);
      }
      container.appendChild(entry);
    });
  }

  return { render };
});
