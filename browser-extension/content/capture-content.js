(function () {
  "use strict";

  if (globalThis.GoiCapture) {
    return;
  }

  const captureModel = globalThis.GoiExtension.captureModel;
  const BLOCK_SELECTOR = "p, li, blockquote, article, section, [role='article']";
  const CAPTION_SELECTOR = "[data-goi-caption-text], .caption-window, .ytp-caption-window-container";
  const toastMessages = {
    saving: "Saving to Goi…",
    saved: "Saved to Goi",
    queued: "Queued — Goi will retry",
    not_connected: "Connect Goi from the extension button",
    no_selection: "Select a word first",
    unauthorized: "Goi rejected the extension token",
    invalid_capture: "That selection could not be saved",
    idempotency_conflict: "This capture conflicts with an earlier save",
    rate_limited: "Goi is busy; try again shortly",
    queue_full: "Capture queue full — retry when Goi is reachable",
    network: "Could not reach Goi",
    server: "Goi could not save this capture",
    unavailable_page: "This page does not allow capture"
  };
  let toastTimer;

  function visible(element) {
    return Boolean(element && element.getClientRects().length);
  }

  function sentenceFromBlock(block, selectedText, preferredStart) {
    if (!block) {
      return "";
    }
    const blockText = block.textContent || "";
    let start = Number.isInteger(preferredStart) ? preferredStart : -1;
    if (start < 0 || blockText.slice(start, start + selectedText.length) !== selectedText) {
      start = blockText.indexOf(selectedText);
    }
    if (start < 0) {
      return "";
    }
    return captureModel.sentenceContext(
      blockText,
      start,
      start + selectedText.length,
      document.documentElement.lang || navigator.language
    );
  }

  function findFallbackContext(selectedText, selectedElement) {
    const nearest = selectedElement && selectedElement.closest(BLOCK_SELECTOR);
    const nearestContext = sentenceFromBlock(nearest, selectedText);
    if (nearestContext) {
      return nearestContext;
    }

    const blocks = document.querySelectorAll(BLOCK_SELECTOR);
    for (const block of blocks) {
      if (visible(block)) {
        const context = sentenceFromBlock(block, selectedText);
        if (context) {
          return context;
        }
      }
    }
    return sentenceFromBlock(document.body, selectedText) || selectedText;
  }

  function selectionDetails(fallbackSelection) {
    const selection = window.getSelection();
    const liveSelection = captureModel.normalizeWhitespace(
      selection && selection.rangeCount ? selection.toString() : ""
    );
    const selectedText = liveSelection || captureModel.normalizeWhitespace(fallbackSelection);
    if (!selectedText) {
      return null;
    }

    let contextText = selectedText;
    let selectedElement = null;
    let selectionInCaption = false;
    if (selection && selection.rangeCount) {
      const range = selection.getRangeAt(0);
      selectedElement = range.commonAncestorContainer.nodeType === Node.ELEMENT_NODE
        ? range.commonAncestorContainer
        : range.commonAncestorContainer.parentElement;
      const selectedCaption = selectedElement && selectedElement.closest(CAPTION_SELECTOR);
      selectionInCaption = Boolean(selectedCaption);
      if (selectedCaption) {
        contextText = captureModel.normalizeWhitespace(selectedCaption.textContent);
      } else if (liveSelection) {
        const block = (selectedElement && selectedElement.closest(BLOCK_SELECTOR)) || document.body;
        if (block) {
          const prefix = range.cloneRange();
          prefix.selectNodeContents(block);
          prefix.setEnd(range.startContainer, range.startOffset);
          const blockText = block.textContent || "";
          const start = prefix.toString().length;
          contextText = sentenceFromBlock(block, range.toString(), start) || selectedText;
        }
      } else {
        contextText = findFallbackContext(selectedText, selectedElement);
      }
    } else {
      contextText = findFallbackContext(selectedText, null);
    }

    const activeCaption =
      location.hostname === "www.youtube.com" && globalThis.GoiYouTubeOverlay
        ? globalThis.GoiYouTubeOverlay.getActiveCaption()
        : "";
    const youtubeOverlay = location.hostname === "www.youtube.com"
      ? globalThis.GoiYouTubeOverlay
      : null;
    const primaryVideo = youtubeOverlay && typeof youtubeOverlay.getActiveVideo === "function"
      ? youtubeOverlay.getActiveVideo()
      : location.hostname === "www.youtube.com"
        ? document.querySelector(".html5-video-player video")
        : null;
    const video = primaryVideo || (selectionInCaption ? document.querySelector("video") : null);
    const attribution = captureModel.resolveCaptureAttribution({
      contextText,
      activeCaption,
      selectionInCaption,
      hostname: location.hostname,
      pathname: location.pathname,
      hasVideo: Boolean(video),
    });
    const sourcePositionMs =
      attribution.sourceKind === "video" && video && Number.isFinite(video.currentTime)
        ? Math.max(0, Math.round(video.currentTime * 1000))
        : null;
    return {
      rawText: selectedText,
      expression: selectedText,
      contextText: attribution.contextText,
      sourceKind: attribution.sourceKind,
      sourceTitle: document.title,
      sourceURL: location.href,
      sourcePositionMs
    };
  }

  function collect(fallbackSelection) {
    const capture = selectionDetails(fallbackSelection);
    return capture ? { ok: true, capture } : { ok: false, errorCode: "no_selection" };
  }

  function showToast(state, messageCode) {
    let toast = document.getElementById("goi-ext-toast");
    if (!toast) {
      toast = document.createElement("div");
      toast.id = "goi-ext-toast";
      toast.setAttribute("role", "status");
      toast.setAttribute("aria-live", "polite");
      document.documentElement.appendChild(toast);
    }
    clearTimeout(toastTimer);
    toast.className = "goi-ext-toast goi-ext-toast--" + state;
    toast.textContent = toastMessages[messageCode] || toastMessages.server;
    toast.hidden = false;

    if (state === "saved") {
      document.dispatchEvent(new CustomEvent("goi-ext-capture-saved"));
    }
    toastTimer = setTimeout(function () {
      toast.hidden = true;
    }, state === "error" ? 6000 : 2600);
  }

  globalThis.GoiCapture = { collect, showToast };
})();
