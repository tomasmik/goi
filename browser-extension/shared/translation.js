(function (root, factory) {
  const api = factory();
  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.translation = api;
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
})(globalThis, function () {
  "use strict";

  const MAX_TEXT_LENGTH = 8000;
  const CACHE_LIMIT = 100;

  function create(options) {
    const config = options || {};
    const translatorAPI = config.translatorAPI || globalThis.Translator;
    const remote = config.remote;
    const cache = new Map();
    const pending = new Map();
    let translatorPromise;

    async function translate(value, settings) {
      const text = String(value || "").trim();
      if (!text) {
        throw translationError("empty_text", "Enter Japanese text to translate.");
      }
      if (Array.from(text).length > MAX_TEXT_LENGTH) {
        throw translationError("text_too_long", "Translation text is limited to 8,000 characters.");
      }
      const cached = cache.get(text);
      if (cached) {
        cache.delete(text);
        cache.set(text, cached);
        return { ...cached, cached: true };
      }
      if (pending.has(text)) {
        return pending.get(text);
      }
      const request = runTranslation(text, settings || {}).then(function (result) {
        cache.set(text, result);
        while (cache.size > CACHE_LIMIT) {
          cache.delete(cache.keys().next().value);
        }
        return { ...result, cached: false };
      }).finally(function () {
        pending.delete(text);
      });
      pending.set(text, request);
      return request;
    }

    async function runTranslation(text, settings) {
      let localError;
      if (translatorAPI && typeof translatorAPI.create === "function") {
        let availability = "available";
        try {
          availability = typeof translatorAPI.availability === "function"
            ? await translatorAPI.availability({ sourceLanguage: "ja", targetLanguage: "en" })
            : "available";
          if (["unavailable", "no"].includes(availability)) {
            localError = translationError(
              "on_device_unavailable",
              "Chrome does not support Japanese-to-English translation on this device."
            );
          } else {
            const translator = await getTranslator(settings.onProgress);
            const translated = String(await translator.translate(text, { signal: settings.signal })).trim();
            if (translated) {
              return { translation: translated, provider: "chrome" };
            }
            localError = translationError("on_device_failed", "Chrome's on-device translator returned no text.");
          }
        } catch (error) {
          if (settings.signal?.aborted) {
            throw error;
          }
          translatorPromise = undefined;
          localError = onDeviceError(error, availability);
        }
      } else {
        localError = translationError(
          "on_device_missing",
          "On-device translation is not available in this browser. It requires Google Chrome 138 or newer on desktop."
        );
      }
      if (typeof remote !== "function") {
        throw localError;
      }
      settings.onProgress?.({ state: "remote", message: "Translating with Goi…" });
      const response = await remote(text, settings.signal);
      const translation = String(response?.translation || "").trim();
      if (!translation) {
        const remoteError = response?.error || failureText(response?.errorCode);
        throw translationError(
          response?.errorCode || "translation_failed",
          [localError?.message, remoteError].filter(Boolean).join(" ")
        );
      }
      return { translation, provider: response.provider || "goi" };
    }

    function getTranslator(onProgress) {
      if (!translatorPromise) {
        translatorPromise = Promise.resolve(translatorAPI.create({
          sourceLanguage: "ja",
          targetLanguage: "en",
          monitor(monitor) {
            monitor.addEventListener("downloadprogress", function (event) {
              const progress = Number(event.loaded);
              const percent = Number.isFinite(progress) ? Math.round(progress * 100) : null;
              onProgress?.({
                state: "downloading",
                percent,
                message: percent === null
                  ? "Downloading the translation model…"
                  : "Downloading the translation model… " + percent + "%"
              });
            });
          }
        }));
      }
      return translatorPromise;
    }

    return { translate };
  }

  function translationError(code, message) {
    const error = new Error(message);
    error.code = code;
    return error;
  }

  function onDeviceError(error, availability) {
    if (error?.name === "NotAllowedError") {
      return translationError(
        "on_device_needs_activation",
        "Chrome needs a click before it can download the Japanese translation model. Click Retry."
      );
    }
    if (["downloadable", "downloading", "after-download"].includes(availability)) {
      return translationError(
        "on_device_download_failed",
        "Chrome could not download its Japanese translation model. Check the network connection and click Retry."
      );
    }
    return translationError("on_device_failed", "Chrome's on-device translator could not start.");
  }

  function failureText(code) {
    if (code === "not_connected") {
      return "Goi is not connected.";
    }
    if (code === "unauthorized") {
      return "Goi rejected the extension token.";
    }
    if (code === "translation_unavailable") {
      return "Goi's translation provider is not configured.";
    }
    if (code === "network") {
      return "Could not reach Goi's translation provider.";
    }
    return "Goi could not translate this text.";
  }

  function selectedText(selection, container, lineSelector) {
    if (!selection || selection.isCollapsed || selection.rangeCount === 0 || !container) {
      return "";
    }
    const range = selection.getRangeAt(0);
    return Array.from(container.querySelectorAll(lineSelector)).filter(function (line) {
      return range.intersectsNode(line);
    }).map(function (line) {
      return String(line.dataset.translationText || "").trim();
    }).filter(Boolean).join("\n");
  }

  async function translateInto(translator, value, result, trigger, status, isCurrent) {
    const text = String(value || "").trim();
    const defaultLabel = trigger?.textContent;
    const defaultTitle = trigger?.title;
    if (!result.hidden && result.dataset.sourceText === text) {
      result.hidden = true;
      return true;
    }
    if (trigger) {
      trigger.disabled = true;
    }
    setStatus(status, "Translating…", false);
    try {
      const translated = await translator.translate(text, {
        onProgress(progress) {
          if (isCurrent && !isCurrent()) {
            return;
          }
          setStatus(status, progress.message, false);
          if (trigger) {
            trigger.title = progress.message;
          }
          if (trigger && progress.state === "downloading") {
            trigger.textContent = Number.isFinite(progress.percent) ? progress.percent + "%" : "…";
          }
        }
      });
      if (isCurrent && !isCurrent()) {
        return false;
      }
      result.textContent = translated.translation;
      result.dataset.sourceText = text;
      result.hidden = false;
      setStatus(status, translated.provider === "chrome" ? "Translated on this device" : "Translated by Goi", false);
      return true;
    } catch (error) {
      if (isCurrent && !isCurrent()) {
        return false;
      }
      if (status) {
        setStatus(status, error.message || "Could not translate this text.", true);
      } else {
        result.textContent = error.message || "Could not translate this text.";
        result.hidden = false;
      }
      return false;
    } finally {
      if (trigger) {
        trigger.disabled = false;
        trigger.textContent = defaultLabel;
        if (defaultTitle) {
          trigger.title = defaultTitle;
        } else {
          trigger.removeAttribute("title");
        }
      }
    }
  }

  function schedulePasted(state, translator, elements, delay) {
    (state.cancel || clearTimeout)(state.timer);
    const text = elements.input.value.trim();
    const version = ++state.version;
    elements.result.hidden = true;
    elements.status.textContent = "";
    elements.retry.hidden = true;
    if (!text) {
      return;
    }
    const translate = async function () {
      const current = function () { return version === state.version; };
      const translated = await translateInto(
        translator,
        text,
        elements.result,
        elements.retry,
        elements.status,
        current
      );
      if (current()) {
        elements.retry.hidden = translated;
      }
    };
    if (delay === 0) {
      return translate();
    }
    state.timer = (state.schedule || setTimeout)(translate, delay);
  }

  function setStatus(element, text, error) {
    if (!element) {
      return;
    }
    element.textContent = text;
    element.classList.toggle("error", Boolean(error));
  }

  return { CACHE_LIMIT, MAX_TEXT_LENGTH, create, failureText, schedulePasted, selectedText, translateInto };
});
