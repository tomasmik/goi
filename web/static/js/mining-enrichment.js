export function selectMiningCandidate(root, candidateID) {
  const choices = root.querySelectorAll("[data-mining-candidate-choice]");
  const editors = root.querySelectorAll("[data-mining-candidate-editor]");
  let selectedEditor = null;

  choices.forEach((choice) => {
    const selected = choice.dataset.miningCandidateChoice === candidateID;
    choice.classList.toggle("is-selected", selected);
    choice.setAttribute("aria-pressed", selected ? "true" : "false");
  });
  editors.forEach((editor) => {
    const selected = editor.id === candidateID;
    editor.hidden = !selected;
    if (selected) {
      selectedEditor = editor;
    }
  });

  return selectedEditor;
}

function initCandidatePicker(root) {
  const choices = root.querySelectorAll("[data-mining-candidate-choice]");
  choices.forEach((choice) => {
    if (choice.dataset.miningCandidateBound === "true") {
      return;
    }
    choice.dataset.miningCandidateBound = "true";
    choice.addEventListener("click", () => {
      selectMiningCandidate(root, choice.dataset.miningCandidateChoice);
    });
  });
}

function initSentenceTranslation(root, browser) {
  root.querySelectorAll("[data-translate-sentence]").forEach((button) => {
    if (button.dataset.translateSentenceBound === "true") {
      return;
    }
    button.dataset.translateSentenceBound = "true";
    button.addEventListener("click", async (event) => {
      event.preventDefault();
      const form = button.closest("form");
      const sentence = form?.querySelector('[name="example_sentence"]');
      const translation = form?.querySelector('[name="example_translation"]');
      const text = sentence?.value.trim();
      if (!text) {
        sentence?.focus();
        browser.alert("Enter a Japanese sentence first.");
        return;
      }
      if (translation?.value.trim() && !browser.confirm("Replace the current English translation?")) {
        return;
      }
      if (button.dataset.remoteTranslation === "true") {
        form.requestSubmit(button);
        return;
      }

      const label = button.textContent;
      button.disabled = true;
      try {
        const translated = await translateInBrowser(browser, text, (message) => {
          button.textContent = message;
        });
        if (translated) {
          translation.value = translated;
          if (typeof browser.Event === "function") {
            translation.dispatchEvent(new browser.Event("input", { bubbles: true }));
          }
          return;
        }
        browser.alert("Translation is unavailable. Use Chrome 138 or newer, or configure Translation and examples in Goi Settings.");
      } catch (error) {
        browser.alert(error?.message || "Could not translate this sentence.");
      } finally {
        button.disabled = false;
        button.textContent = label;
      }
    });
  });
}

export function initMiningEnrichment(root = document, browser = window) {
  initCandidatePicker(root);
  initSentenceTranslation(root, browser);
}

const browserTranslationCache = new Map();
const browserTranslatorPromises = new WeakMap();

export async function translateInBrowser(browser, value, onProgress = () => {}) {
  const text = String(value || "").trim();
  const translatorAPI = browser.Translator;
  if (!text || !translatorAPI || typeof translatorAPI.create !== "function") {
    return null;
  }
  if (browserTranslationCache.has(text)) {
    return browserTranslationCache.get(text);
  }

  const options = { sourceLanguage: "ja", targetLanguage: "en" };
  const availability = typeof translatorAPI.availability === "function"
    ? await translatorAPI.availability(options)
    : "available";
  if (availability === "unavailable" || availability === "no") {
    return null;
  }

  let translatorPromise = browserTranslatorPromises.get(translatorAPI);
  if (!translatorPromise) {
    translatorPromise = Promise.resolve(translatorAPI.create({
      ...options,
      monitor(monitor) {
        monitor.addEventListener("downloadprogress", (event) => {
          const progress = Number(event.loaded);
          const suffix = Number.isFinite(progress) ? ` ${Math.round(progress * 100)}%` : "";
          onProgress(`Downloading translator…${suffix}`);
        });
      },
    }));
    browserTranslatorPromises.set(translatorAPI, translatorPromise);
  }
  onProgress("Translating…");
  const translator = await translatorPromise;
  const translated = String(await translator.translate(text)).trim();
  if (!translated) {
    throw new Error("The translator returned an empty result.");
  }
  browserTranslationCache.set(text, translated);
  return translated;
}
