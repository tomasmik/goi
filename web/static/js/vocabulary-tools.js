function parsedKnownWords(value) {
  const parts = value.split(/[\s,、;；，]+/u).map((part) => part.trim()).filter(Boolean);
  return Array.from(new Set(parts));
}

export function initVocabularyTools() {
  const knownInput = document.querySelector("[data-known-words-input]");
  const knownPreview = document.querySelector("[data-known-words-preview]");
  if (knownInput && knownPreview) {
    let previewTimer;
    let previewRevision = 0;
    const updatePreview = () => {
      const words = parsedKnownWords(knownInput.value);
      window.clearTimeout(previewTimer);
      const revision = ++previewRevision;
      if (!words.length) {
        knownPreview.textContent = "Paste words to preview what will change.";
        return;
      }
      knownPreview.textContent = `${words.length} unique ${words.length === 1 ? "word" : "words"} · checking your vocabulary…`;
      previewTimer = window.setTimeout(async () => {
        try {
          const body = new FormData(knownInput.form);
          const response = await fetch("/vocabulary/known/preview", { method: "POST", body });
          const result = await response.json();
          if (revision !== previewRevision) {
            return;
          }
          if (!response.ok) {
            knownPreview.textContent = result.error || "Could not preview this list.";
            return;
          }
          const parts = [];
          if (result.created) parts.push(`${result.created} new`);
          if (result.markedKnown) parts.push(`${result.markedKnown} existing ${result.markedKnown === 1 ? "word" : "words"} will be marked known`);
          if (result.alreadyKnown) parts.push(`${result.alreadyKnown} already counted`);
          if (result.skippedActiveLesson) parts.push(`${result.skippedActiveLesson} kept in an active lesson`);
          knownPreview.textContent = `${words.length} unique · ${parts.join(" · ")}`;
        } catch (_error) {
          if (revision === previewRevision) {
            knownPreview.textContent = `${words.length} unique ${words.length === 1 ? "word" : "words"}. Preview unavailable; nothing has been changed.`;
          }
        }
      }, 250);
    };
    knownInput.addEventListener("input", updatePreview);
    updatePreview();
  }

  const sort = document.querySelector("[data-vocabulary-sort]");
  if (sort) {
    sort.addEventListener("change", () => sort.form?.requestSubmit());
  }

  const filters = document.querySelector(".filter-tabs");
  const activeFilter = filters?.querySelector('[aria-current="page"]');
  if (filters && activeFilter) {
    const target = activeFilter.offsetLeft - (filters.clientWidth - activeFilter.offsetWidth) / 2;
    filters.scrollLeft = Math.max(0, target);
  }

  const expression = document.querySelector("[data-vocabulary-expression]");
  const lookup = document.querySelector("[data-dictionary-lookup]");
  if (expression && lookup) {
    const updateLookup = () => {
      const value = expression.value.trim();
      lookup.href = value ? `/mining/capture?expression=${encodeURIComponent(value)}` : "/mining/capture";
    };
    expression.addEventListener("input", updateLookup);
    updateLookup();
  }
}
