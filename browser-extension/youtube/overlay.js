(function () {
  "use strict";

  if (globalThis.GoiYouTubeOverlay) {
    return;
  }

  const settingsModel = globalThis.GoiExtension.settingsModel;
  const captionModel = globalThis.GoiExtension.captionModel;
  const captureModel = globalThis.GoiExtension.captureModel;
  const subtitleModel = globalThis.GoiExtension.subtitleModel;
  const subtitleView = globalThis.GoiExtension.subtitleView;
  const dictionaryClient = globalThis.GoiExtension.dictionaryClient;
  const dictionaryRenderer = globalThis.GoiExtension.dictionaryView;
  const subtitleFileModel = globalThis.GoiExtension.subtitleFileModel;
  const translationModel = globalThis.GoiExtension.translation || {
    create: function () {
      return { translate: function () { return Promise.reject(new Error("Translation is unavailable.")); } };
    }
  };
  const COVERAGE_DELAY_MS = 80;
  const COVERAGE_RETRY_MS = 30000;
  const MAX_COVERAGE_TEXT = 18000;
  const MAX_PENDING_COVERAGE = 100;
  const MAX_RETAINED_CAPTIONS = 100;
  const MAX_OBSERVED_SUBTITLE_LINES = 300;
  const MAX_OBSERVED_SUBTITLE_CHARACTERS = 120000;
  const FULL_TRANSCRIPT_FIRST_ID = 1000000;
  const TRANSCRIPT_RETRY_DELAYS = [500, 1500, 3000, 5000, 10000, 15000, 30000];
  const HOVER_LOOKUP_DELAY_MS = 120;
  const HOVER_LOOKUP_CLOSE_MS = 180;

  function ignoreDictionaryPrefetchFailure() {}

  const subtitleSessionID = typeof crypto === "object" && typeof crypto.randomUUID === "function"
    ? crypto.randomUUID()
    : String(Date.now()) + "-" + String(Math.random()).slice(2);
  let settings = settingsModel.sanitize();
  let persistedSettings = settings;
  let settingsRevision = 0;
  let settingsReady = false;
  let player;
  let playerSelectionDirty = true;
  let overlay;
  let captionStage;
  let captionText;
  let captionTranslateAction;
  let captionTranslation;
  let selectionAction;
  let selectionKnownAction;
  let selectionLookup;
  let selectionLookupContent;
  let captionStatus;
  let captionCoverage;
  let controls;
  let controlRail;
  let controlMenuButton;
  let settingsButton;
  let quickControlsExpanded = false;
  let captionVisibilityButton;
  let caption = "";
  let lastObservedCaption;
  let captionMirrorReady = false;
  let captionClearTimer;
  let captionReadTimer;
  let captionReadFrame;
  let framePending = false;
  let dragging = false;
  let dragHandle;
  let dragPointerID;
  let capturePending = false;
  let captureSequence = 0;
  let lookupSequence = 0;
  let selectedCapture;
  const coverageRuntime = {
    timer: undefined,
    retryTimer: undefined,
    generation: 0,
    retryAt: 0,
    unavailable: false,
    summary: emptyCoverageSummary(),
    committedSummary: emptyCoverageSummary(),
    captionsAnalyzed: 0,
    sampled: false,
    byCaption: new Map(),
    byText: new Map(),
    committedUnknownExpressions: new Set(),
    occurrences: new Map(),
    activeOccurrence: undefined,
    nextOccurrenceID: 1,
    scheduledTask: undefined,
    queue: [],
    queuedByOccurrence: new Map(),
    workerGeneration: -1
  };
  const transcriptRuntime = {
    observedLines: [],
    observedCharacters: 0,
    observedRevision: 0,
    generation: 0,
    retryTimer: undefined,
    state: "loading",
    reason: "",
    lines: [],
    coverageByText: new Map(),
    summary: emptyCoverageSummary(),
    automatic: false,
    publishedLineID: undefined,
    historySeek: undefined
  };
  let observedVideo;
  let restoreDisplayMode = "always";
  let captionHovered = false;
  let captionPointerActive = false;
  let captionRenderPending = false;
  let captionRenderTimer;
  let hoverPausedVideo;
  let hoverLookupTimer;
  let hoverLookupCloseTimer;
  let lookupPinned = false;
  const settingControls = {};
  const settingValueOutputs = {};

  const translator = translationModel.create({
    remote: async function (text) {
      const response = await chrome.runtime.sendMessage({
        type: "goi.translation.remote",
        version: 1,
        text
      }).catch(function () {
        return { ok: false, errorCode: "network" };
      });
      if (!response?.ok) {
        return { errorCode: response?.errorCode, error: translationModel.failureText(response?.errorCode) };
      }
      return response.result;
    }
  });

  function emptyCoverageSummary() {
    return {
      known_occurrences: 0,
      total_occurrences: 0,
      unknown_unique: 0,
      excluded_names: 0
    };
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text !== undefined) {
      node.textContent = text;
    }
    return node;
  }

  function button(label, title, onClick) {
    const node = element("button", "goi-ext-overlay-button", label);
    node.type = "button";
    node.title = title;
    node.setAttribute("aria-label", title);
    node.addEventListener("click", function (event) {
      event.stopPropagation();
      onClick();
    });
    return node;
  }

  function rangeControl(settingName, labelText, min, max, step, value, formatValue, onInput) {
    const label = element("label", "goi-ext-overlay-field");
    label.appendChild(element("span", "goi-ext-overlay-label", labelText));
    const input = element("input", "goi-ext-overlay-range");
    input.id = "goi-ext-overlay-" + settingName;
    input.type = "range";
    input.min = String(min);
    input.max = String(max);
    input.step = String(step);
    input.value = String(value);
    settingControls[settingName] = input;
    const valueOutput = element("output", "goi-ext-overlay-value");
    valueOutput.setAttribute("for", input.id);
    settingValueOutputs[settingName] = {
      format: formatValue,
      node: valueOutput
    };
    input.addEventListener("input", function () {
      onInput(Number(input.value), false);
    });
    input.addEventListener("change", function () {
      onInput(Number(input.value), true);
    });
    label.appendChild(input);
    label.appendChild(valueOutput);
    return label;
  }

  function radioControl(settingName, labelText, choices, value, onChange) {
    const fieldset = element("fieldset", "goi-ext-overlay-choice-field");
    fieldset.appendChild(element("legend", "goi-ext-overlay-label", labelText));
    const group = element("div", "goi-ext-overlay-choice-group");
    group.classList.add("goi-ext-overlay-choice-group--" + settingName);
    const inputs = choices.map(function (choice) {
      const label = element("label", "goi-ext-overlay-choice");
      const input = element("input", "goi-ext-overlay-radio");
      input.type = "radio";
      input.name = "goi-ext-" + settingName;
      input.value = choice[0];
      input.checked = input.value === value;
      input.addEventListener("change", function () {
        if (input.checked) {
          onChange(input.value);
        }
      });
      label.appendChild(input);
      label.appendChild(element("span", "", choice[1]));
      group.appendChild(label);
      return input;
    });
    settingControls[settingName] = inputs;
    fieldset.appendChild(group);
    return fieldset;
  }

  function checkboxControl(settingName, labelText, value, onChange) {
    const label = element("label", "goi-ext-overlay-check");
    const input = element("input", "goi-ext-overlay-checkbox");
    input.type = "checkbox";
    input.checked = value;
    input.addEventListener("change", function () {
      onChange(input.checked);
    });
    settingControls[settingName] = input;
    label.appendChild(input);
    label.appendChild(element("span", "", labelText));
    return label;
  }

  function createOverlayShell() {
    overlay = element("section", "goi-ext-overlay");
    overlay.dataset.goiCoverageUi = "true";
    overlay.setAttribute("role", "region");
    overlay.setAttribute("aria-label", "Goi selectable captions");
    overlay.setAttribute("aria-live", "off");
  }

  function createCaptionStage() {
    captionStage = element("div", "goi-ext-caption-stage");
    captionText = element("div", "goi-ext-caption-text");
    captionText.dataset.goiCaptionText = "true";
    captionText.lang = "ja";
    captionText.addEventListener("pointerenter", function () {
      captionHovered = true;
      pauseForHover();
    });
    captionText.addEventListener("pointerleave", function () {
      captionHovered = false;
      if (!selectedCapture) {
        resumeAfterHover();
      }
    });
    captionText.addEventListener("pointerdown", function (event) {
      captionPointerActive = true;
      document.addEventListener("pointerup", finishCaptionPointer);
      document.addEventListener("pointercancel", finishCaptionPointer);
      event.stopPropagation();
      clearSelectedCapture();
    });
    captionText.addEventListener("pointerup", function (event) {
      handleCaptionSelection(event);
      finishCaptionPointer();
    });
    captionText.addEventListener("click", function (event) {
      event.stopPropagation();
    });
    captionStage.appendChild(captionText);

    captionTranslateAction = element("button", "goi-ext-caption-translate", "EN");
    captionTranslateAction.type = "button";
    captionTranslateAction.title = "Translate this subtitle";
    captionTranslateAction.setAttribute("aria-label", "Translate this subtitle");
    captionTranslateAction.addEventListener("click", async function (event) {
      if (!event.isTrusted) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      if (!captionTranslation.hidden) {
        captionTranslation.hidden = true;
        return;
      }
      const source = caption;
      captionTranslateAction.disabled = true;
      captionTranslateAction.textContent = "…";
      try {
        const translated = await translator.translate(source, {
          onProgress(progress) {
            captionTranslateAction.title = progress.message;
            captionTranslation.textContent = progress.message;
            captionTranslation.hidden = false;
          }
        });
        if (caption !== source) {
          return;
        }
        captionTranslation.textContent = translated.translation;
        captionTranslation.hidden = false;
      } catch (error) {
        if (caption !== source) {
          return;
        }
        captionTranslation.textContent = error.message || "Could not translate this subtitle.";
        captionTranslation.hidden = false;
      } finally {
        captionTranslateAction.disabled = false;
        captionTranslateAction.textContent = "EN";
        captionTranslateAction.title = "Translate this subtitle";
      }
    });
    captionStage.appendChild(captionTranslateAction);

    captionTranslation = element("div", "goi-ext-caption-translation");
    captionTranslation.lang = "en";
    captionTranslation.hidden = true;
    captionStage.appendChild(captionTranslation);
    overlay.appendChild(captionStage);
  }

  function createDictionaryLookup() {
    captionCoverage = element("span", "goi-ext-caption-coverage");
    captionCoverage.setAttribute("role", "status");
    captionCoverage.setAttribute("aria-live", "polite");
    captionCoverage.hidden = true;

    selectionLookup = element("div", "goi-ext-caption-lookup");
    selectionLookup.setAttribute("role", "dialog");
    selectionLookup.setAttribute("aria-label", "Dictionary lookup");
    selectionLookup.hidden = true;
    selectionLookup.addEventListener("pointerdown", function (event) {
      event.stopPropagation();
    });
    selectionLookup.addEventListener("pointerenter", cancelHoverLookupClose);
    selectionLookup.addEventListener("pointerleave", scheduleHoverLookupClose);
    selectionLookupContent = element("div", "goi-ext-dictionary-content");
    selectionLookupContent.setAttribute("role", "status");
    selectionLookupContent.setAttribute("aria-live", "polite");
    selectionLookup.appendChild(selectionLookupContent);

    const selectionActions = element("div", "goi-ext-caption-actions");
    selectionKnownAction = element("button", "goi-ext-caption-known", "Mark as known");
    selectionKnownAction.type = "button";
    selectionKnownAction.hidden = true;
    selectionKnownAction.addEventListener("click", function (event) {
      if (!event.isTrusted) {
        return;
      }
      event.stopPropagation();
      markSelectedWordKnown();
    });
    selectionActions.appendChild(selectionKnownAction);

    selectionAction = element("button", "goi-ext-caption-selection");
    selectionAction.type = "button";
    selectionAction.hidden = true;
    selectionAction.addEventListener("click", function (event) {
      if (!event.isTrusted) {
        return;
      }
      event.stopPropagation();
      captureSelectedText();
    });
    selectionActions.appendChild(selectionAction);
    selectionLookup.appendChild(selectionActions);
    overlay.appendChild(selectionLookup);
  }

  function createCaptureFeedback() {
    const captureFeedback = element("div", "goi-ext-caption-feedback");
    captionStatus = element("span", "goi-ext-caption-status");
    captionStatus.setAttribute("role", "status");
    captionStatus.setAttribute("aria-live", "polite");
    captionStatus.hidden = true;
    captureFeedback.appendChild(captionStatus);

    overlay.appendChild(captureFeedback);
  }

  function createControlRail() {
    controlRail = element("div", "goi-ext-overlay-rail");
    controlRail.setAttribute("role", "toolbar");
    controlRail.setAttribute("aria-label", "Goi caption controls");
    const addRailItem = function (node) {
      node.classList.add("goi-ext-overlay-rail-item");
      controlRail.appendChild(node);
      return node;
    };
    controlMenuButton = button("語", "Show Goi caption controls", function () {
      setQuickControlsExpanded(!quickControlsExpanded);
    });
    controlMenuButton.classList.add("goi-ext-overlay-menu-button");
    controlMenuButton.setAttribute("aria-expanded", "false");
    controlRail.appendChild(controlMenuButton);

    const handle = element("span", "goi-ext-overlay-button goi-ext-overlay-handle", "↕");
    handle.title = "Drag captions vertically";
    handle.setAttribute("aria-hidden", "true");
    handle.addEventListener("pointerdown", startDrag);
    addRailItem(handle);
    addRailItem(button("−5s", "Replay previous 5 seconds", replayFiveSeconds));
    addRailItem(button("Play", "Play or pause video", togglePlayback));
    addRailItem(button("A−", "Decrease caption text size", function () {
      changeCaptionSize(-2);
    }));
    addRailItem(button("A+", "Increase caption text size", function () {
      changeCaptionSize(2);
    }));
    addRailItem(button("Transcript", "Open transcript browser", openSubtitleBrowser));
    captionVisibilityButton = button("Hide", "Hide captions", toggleCaptionVisibility);
    captionVisibilityButton.setAttribute("aria-pressed", "false");
    captionVisibilityButton.classList.add("goi-ext-overlay-visibility");
    addRailItem(captionVisibilityButton);
    settingsButton = button("Settings", "Show caption settings", function () {
      setCaptionSettingsExpanded(controls.hidden);
    });
    settingsButton.setAttribute("aria-expanded", "false");
    settingsButton.setAttribute("aria-controls", "goi-ext-overlay-controls");
    addRailItem(settingsButton);
    addRailItem(captionCoverage);
    overlay.appendChild(controlRail);
  }

  function createSettingsPanel() {
    controls = element("div", "goi-ext-overlay-controls");
    controls.id = "goi-ext-overlay-controls";
    controls.hidden = true;
    controls.addEventListener("click", function (event) {
      event.stopPropagation();
    });
    const settingsHeader = element("div", "goi-ext-overlay-controls-header");
    settingsHeader.appendChild(element("strong", "", "Caption settings"));
    const closeSettings = button("×", "Close caption settings", function () {
      setCaptionSettingsExpanded(false);
      settingsButton.focus();
    });
    closeSettings.classList.add("goi-ext-overlay-controls-close");
    settingsHeader.appendChild(closeSettings);
    controls.appendChild(settingsHeader);
    controls.appendChild(rangeControl("fontSizePx", "Text", 18, 96, 1, settings.fontSizePx, function (value) {
      return Math.round(value) + " px";
    }, function (value, persist) {
      setSettings({ fontSizePx: value }, persist);
    }));
    controls.appendChild(rangeControl("verticalPercent", "Vertical position", 10, 90, 1, settings.verticalPercent, function (value) {
      return settingsModel.verticalPositionLabel(value);
    }, function (value, persist) {
      setSettings({ verticalPercent: value }, persist);
    }));
    controls.appendChild(rangeControl("backgroundOpacity", "Background", 0, 0.9, 0.05, settings.backgroundOpacity, function (value) {
      return Math.round(value * 100) + "%";
    }, function (value, persist) {
      setSettings({ backgroundOpacity: value }, persist);
    }));
    controls.appendChild(checkboxControl(
      "furiganaEnabled",
      "Furigana on unknown words",
      settings.furiganaEnabled,
      function (value) { setSettings({ furiganaEnabled: value }, true); }
    ));
    controls.appendChild(checkboxControl(
      "hoverLookupEnabled",
      "Show definitions on hover",
      settings.hoverLookupEnabled,
      function (value) { setSettings({ hoverLookupEnabled: value }, true); }
    ));

    controls.appendChild(radioControl("displayMode", "Display", [
      ["always", "Always"],
      ["hidden", "Hidden (keep mining)"],
      ["unknown_only", "Unknown"],
      ["pause_reveal", "On pause"]
    ], settings.displayMode, function (value) {
      setSettings({ displayMode: value }, true);
    }));

    controls.appendChild(radioControl("automaticCaptionMode", "Auto captions", [
      ["full", "Full lines (timed)"],
      ["live", "Live (rolling)"]
    ], settings.automaticCaptionMode, function (value) {
      setSettings({ automaticCaptionMode: value }, true);
    }));

    controls.appendChild(radioControl("pauseBehavior", "Pause", [
      ["never", "Never"],
      ["on_hover", "On hover"],
      ["on_selection", "On select"],
      ["after_capture", "After save"]
    ], settings.pauseBehavior, function (value) {
      setSettings({ pauseBehavior: value }, true);
    }));
    controls.appendChild(radioControl("coverageDisplay", "Coverage", [
      ["full", "Details"],
      ["compact", "Percent"],
      ["hidden", "Off"]
    ], settings.coverageDisplay, function (value) {
      setSettings({ coverageDisplay: value }, true);
    }));
    const resetButton = button("Reset captions", "Reset caption settings", resetCaptionSettings);
    resetButton.classList.add("goi-ext-overlay-reset");
    controls.appendChild(resetButton);
    overlay.appendChild(controls);
  }

  function bindOverlayKeyboard() {
    overlay.addEventListener("keydown", function (event) {
      if (event.key !== "Escape") {
        return;
      }
      if (!selectionAction.hidden) {
        clearSelectedCapture();
      }
      if (!controls.hidden) {
        setCaptionSettingsExpanded(false);
        settingsButton.focus();
      } else if (quickControlsExpanded) {
        setQuickControlsExpanded(false);
        controlMenuButton.focus();
      }
    });
  }

  function createOverlay() {
    createOverlayShell();
    createCaptionStage();
    createDictionaryLookup();
    createCaptureFeedback();
    createControlRail();
    createSettingsPanel();
    bindOverlayKeyboard();
    return overlay;
  }

  function setQuickControlsExpanded(expanded) {
    if (!controlRail || !controlMenuButton) {
      return;
    }
    quickControlsExpanded = expanded;
    controlRail.classList.toggle("goi-ext-overlay-rail--expanded", expanded);
    controlMenuButton.setAttribute("aria-expanded", String(expanded));
    const label = expanded ? "Hide Goi caption controls" : "Show Goi caption controls";
    controlMenuButton.title = label;
    controlMenuButton.setAttribute("aria-label", label);
    if (!expanded) {
      setCaptionSettingsExpanded(false);
    }
  }

  function setCaptionSettingsExpanded(expanded) {
    if (!controls || !settingsButton) {
      return;
    }
    controls.hidden = !expanded;
    settingsButton.setAttribute("aria-expanded", String(expanded));
    const label = expanded ? "Hide caption settings" : "Show caption settings";
    settingsButton.title = label;
    settingsButton.setAttribute("aria-label", label);
  }

  function activePlayer() {
    let candidates = typeof document.querySelectorAll === "function"
      ? Array.from(document.querySelectorAll(".html5-video-player"))
      : [];
    if (!candidates.length && typeof document.querySelector === "function") {
      const candidate = document.querySelector(".html5-video-player");
      if (candidate) {
        candidates = [candidate];
      }
    }
    const visible = candidates.filter(function (candidate) {
      if (!candidate || !candidate.isConnected || typeof candidate.getBoundingClientRect !== "function") {
        return false;
      }
      return visiblePlayerArea(candidate.getBoundingClientRect()) > 0;
    });
    const ranked = visible.length ? visible : candidates.filter(function (candidate) {
      return candidate && candidate.isConnected;
    });
    const fullscreen = document.fullscreenElement;
    return ranked.map(function (candidate, index) {
      const video = candidate.querySelector && candidate.querySelector("video");
      const bounds = candidate.getBoundingClientRect ? candidate.getBoundingClientRect() : {};
      return {
        candidate,
        fullscreen: Boolean(fullscreen &&
          (fullscreen === candidate || (candidate.contains && candidate.contains(fullscreen)) ||
            (fullscreen.contains && fullscreen.contains(candidate)))),
        playing: Boolean(video && !video.paused && !video.ended),
        area: visiblePlayerArea(bounds),
        current: candidate === player,
        index
      };
    }).sort(function (left, right) {
      return Number(right.fullscreen) - Number(left.fullscreen) ||
        Number(right.playing) - Number(left.playing) ||
        right.area - left.area ||
        Number(right.current) - Number(left.current) ||
        left.index - right.index;
    }).map(function (entry) {
      return entry.candidate;
    })[0];
  }

  function visiblePlayerArea(bounds) {
    const box = bounds || {};
    const width = Math.max(0, Number(box.width) || 0);
    const height = Math.max(0, Number(box.height) || 0);
    const viewportWidth = Number(globalThis.innerWidth) || 0;
    const viewportHeight = Number(globalThis.innerHeight) || 0;
    if (!viewportWidth || !viewportHeight || !Number.isFinite(box.left) ||
        !Number.isFinite(box.right) || !Number.isFinite(box.top) || !Number.isFinite(box.bottom)) {
      return width * height;
    }
    const visibleWidth = Math.max(0, Math.min(box.right, viewportWidth) - Math.max(box.left, 0));
    const visibleHeight = Math.max(0, Math.min(box.bottom, viewportHeight) - Math.max(box.top, 0));
    return visibleWidth * visibleHeight;
  }

  function ensureOverlay() {
    const nextPlayer = player && player.isConnected && !playerSelectionDirty
      ? player
      : activePlayer();
    let overlayChanged = false;
    playerSelectionDirty = false;
    if (!nextPlayer) {
      if (player) {
        clearMirroredCaption();
        captionHovered = false;
        clearSelectedCapture();
        player.classList.remove("goi-ext-hide-native-captions");
        player = undefined;
        observeVideo(undefined);
        if (overlay && overlay.isConnected) {
          overlay.remove();
        }
      }
      return;
    }
    if (!overlay) {
      createOverlay();
      overlayChanged = true;
    }
    if (player !== nextPlayer || !overlay.isConnected) {
      const previousPlayer = player;
      if (previousPlayer) {
        previousPlayer.classList.remove("goi-ext-hide-native-captions");
      }
      player = nextPlayer;
      player.appendChild(overlay);
      overlayChanged = true;
      observeVideo(nextPlayer.querySelector && nextPlayer.querySelector("video"));
      if (previousPlayer && previousPlayer !== nextPlayer) {
        clearMirroredCaption();
      }
    }
    if (overlayChanged) {
      updateCoverageLabel();
      applySettings();
    }
  }

  function applySettings() {
    if (!overlay || !player) {
      return;
    }
    overlay.hidden = !settings.overlayEnabled;
    overlay.style.setProperty("--goi-ext-font-size", settings.fontSizePx + "px");
    overlay.style.setProperty("--goi-ext-caption-top", settings.verticalPercent + "%");
    overlay.style.setProperty("--goi-ext-background", "rgb(0 0 0 / " + settings.backgroundOpacity + ")");
    overlay.classList.toggle("goi-ext-overlay--dock-above", settings.verticalPercent >= 65);
    overlay.classList.toggle("goi-ext-overlay--furigana", settings.furiganaEnabled);
    applyCaptionVisibility();
    Object.keys(settingControls).forEach(function (name) {
      const control = settingControls[name];
      if (Array.isArray(control)) {
        control.forEach(function (input) {
          input.checked = input.value === settings[name];
        });
        return;
      }
      if (control.type === "checkbox") {
        control.checked = Boolean(settings[name]);
        return;
      }
      control.value = String(settings[name]);
      const valueOutput = settingValueOutputs[name];
      if (valueOutput) {
        const formattedValue = valueOutput.format(settings[name]);
        valueOutput.node.value = formattedValue;
        valueOutput.node.textContent = formattedValue;
        control.setAttribute("aria-valuetext", formattedValue);
      }
    });
    updateCaptionVisibilityButton();
    applyCoverageDisplay();
  }

  function applyCaptionVisibility() {
    if (!overlay || !player || !captionText) {
      return;
    }
    const captionVisible = shouldShowCaption();
    overlay.classList.toggle(
      "goi-ext-overlay--captions-hidden",
      settings.displayMode !== "always" && !captionVisible
    );
    captionText.hidden = !captionVisible;
    captionStage.hidden = !captionVisible;
    player.classList.toggle(
      "goi-ext-hide-native-captions",
      settings.overlayEnabled && (settings.displayMode !== "always" ||
        (settings.hideNativeCaptions && captionMirrorReady))
    );
  }

  function shouldShowCaption() {
    if (!caption) {
      return false;
    }
    if (settings.displayMode === "hidden") {
      return false;
    }
    if (settings.displayMode === "unknown_only") {
      const cached = cachedCaptionCoverage(caption);
      if (coverageRuntime.unavailable || !cached || !cached.block || !Array.isArray(cached.block.tokens)) {
        return true;
      }
      return cached.block.tokens.some(function (token) { return token.status === "unknown"; });
    }
    if (settings.displayMode === "pause_reveal") {
      const video = player && player.querySelector("video");
      return Boolean(video && video.paused);
    }
    return true;
  }

  function updateCaptionVisibilityButton() {
    if (!captionVisibilityButton) {
      return;
    }
    const captionsHidden = settings.displayMode === "hidden";
    const label = captionsHidden ? "Show captions" : "Hide captions, keep mining";
    captionVisibilityButton.textContent = captionsHidden ? "Show" : "Hide";
    captionVisibilityButton.title = label;
    captionVisibilityButton.setAttribute("aria-label", label);
    captionVisibilityButton.setAttribute("aria-pressed", String(captionsHidden));
    captionVisibilityButton.classList.toggle("goi-ext-overlay-visibility--active", captionsHidden);
  }

  function clearMirroredCaption() {
    clearTimeout(captionClearTimer);
    captionClearTimer = undefined;
    finishActiveCoverageOccurrence();
    caption = "";
    captionMirrorReady = false;
    if (captionTranslation) {
      captionTranslation.hidden = true;
      captionTranslation.textContent = "";
    }
    if (captionText) {
      if (captionPointerActive) {
        captionRenderPending = true;
      } else {
        captionText.replaceChildren();
      }
    }
    if (player) {
      player.classList.toggle(
        "goi-ext-hide-native-captions",
        Boolean(settings.overlayEnabled && settings.displayMode !== "always")
      );
    }
  }

  function stopCaptionReading() {
    clearTimeout(captionReadTimer);
    captionReadTimer = undefined;
    if (captionReadFrame !== undefined && typeof cancelAnimationFrame === "function") {
      cancelAnimationFrame(captionReadFrame);
    }
    captionReadFrame = undefined;
    framePending = false;
    clearMirroredCaption();
    captionHovered = false;
    clearSelectedCapture();
  }

  function replaceSettings(nextSettings) {
    const wasOverlayEnabled = settings.overlayEnabled;
    const wasHoverPause = settings.pauseBehavior === "on_hover";
    const furiganaChanged = settings.furiganaEnabled !== nextSettings.furiganaEnabled;
    const automaticCaptionModeChanged = settings.automaticCaptionMode !== nextSettings.automaticCaptionMode;
    if (nextSettings.displayMode !== "hidden") {
      restoreDisplayMode = nextSettings.displayMode;
    }
    settings = nextSettings;
    if (!settings.hoverLookupEnabled && !lookupPinned) {
      clearSelectedCapture();
    }
    if (wasHoverPause && settings.pauseBehavior !== "on_hover") {
      resumeAfterHover();
    }
    if (wasOverlayEnabled !== settings.overlayEnabled) {
      transcriptRuntime.observedRevision += 1;
    }
    if (!settings.overlayEnabled) {
      stopFullTranscript();
      stopCaptionReading();
      if (wasOverlayEnabled) {
        resetCoverage();
      }
    }
    if (automaticCaptionModeChanged && settings.overlayEnabled) {
      lastObservedCaption = undefined;
      clearMirroredCaption();
      clearSelectedCapture();
      scheduleCaptionRead();
    }
    applySettings();
    if (furiganaChanged && caption) {
      renderCaption();
    }
    if (!wasOverlayEnabled && settings.overlayEnabled) {
      loadFullTranscript();
      scheduleCaptionRead();
    }
  }

  function visibleCaption() {
    const searchRoots = player ? [player, document] : [document];
    for (const root of searchRoots) {
      if (!root || typeof root.querySelectorAll !== "function") {
        continue;
      }
      const windows = Array.from(root.querySelectorAll(".caption-window"));
      const containers = Array.from(root.querySelectorAll(".ytp-caption-window-container"));
      const candidates = (windows.length ? windows : containers).filter(function (node) {
        if (!node.getClientRects().length || node.closest(".goi-ext-overlay")) {
          return false;
        }
        const owner = node.closest(".html5-video-player");
        return root !== document || !player || !owner || owner === player;
      });
      const visibleCaptions = candidates.map(function (container) {
        const segmentNodes = Array.from(container.querySelectorAll(".ytp-caption-segment"));
        const segments = segmentNodes.map(function (node) {
          return node.textContent || "";
        });
        const text = captionModel.captionFromSegmentGroups([segments]);
        return text ? {
          text,
          cueNode: container,
          contentNode: segmentNodes[segmentNodes.length - 1] || container
        } : null;
      }).filter(Boolean);
      if (visibleCaptions.length) {
        return {
          text: visibleCaptions.map(function (visible) { return visible.text; }).join("\n"),
          cueNode: visibleCaptions.map(function (visible) { return visible.cueNode; }),
          contentNode: visibleCaptions.map(function (visible) { return visible.contentNode; })
        };
      }
    }

    const segments = Array.from(document.querySelectorAll(".ytp-caption-segment")).filter(function (node) {
      if (!node.getClientRects().length || node.closest(".goi-ext-overlay")) {
        return false;
      }
      const owner = node.closest(".html5-video-player");
      return !player || !owner || owner === player;
    });
    return {
      text: captionModel.captionFromSegmentGroups([segments.map(function (node) {
        return node.textContent || "";
      })]),
      cueNode: segments.length ? segments[segments.length - 1] : undefined,
      contentNode: segments.length ? segments[segments.length - 1] : undefined
    };
  }

  function readCaption() {
    captionReadFrame = undefined;
    framePending = false;
    if (!settings.overlayEnabled) {
      return;
    }
    ensureOverlay();
    if (!player) {
      return;
    }
    const visible = visibleCaption();
    const transcriptLine = transcriptRuntime.automatic && settings.automaticCaptionMode === "full"
      ? currentTranscriptLine()
      : undefined;
    const nextCaption = transcriptLine ? transcriptLine.text : visible.text;
    if (nextCaption && nextCaption !== caption) {
      clearTimeout(captionClearTimer);
      captionClearTimer = undefined;
      caption = nextCaption;
      lastObservedCaption = {
        text: nextCaption,
        sourcePositionMs: currentVideoPosition()
      };
      renderCaption();
    } else if (nextCaption) {
      clearTimeout(captionClearTimer);
      captionClearTimer = undefined;
    } else if (caption && captionClearTimer === undefined) {
      finishActiveCoverageOccurrence();
      captionClearTimer = setTimeout(function () {
        captionClearTimer = undefined;
        if (settings.displayMode === "pause_reveal" && currentVideoPaused()) {
          applySettings();
          return;
        }
        clearMirroredCaption();
        applySettings();
      }, 500);
    }
    if (nextCaption) {
      observeCoverageCaption(nextCaption, visible.cueNode, visible.contentNode);
    }
  }

  function renderCaption() {
    if (!captionText) {
      return;
    }
    if (captionPointerActive) {
      captionRenderPending = true;
      return;
    }
    captionRenderPending = false;
    const cached = cachedCaptionCoverage(caption);
    const nodes = captionNodes(caption, cached && cached.block);
    captionText.replaceChildren(...nodes);
    captionTranslation.hidden = true;
    captionTranslation.textContent = "";
    captionMirrorReady = Boolean(caption);
    applyCaptionVisibility();
  }

  function cachedCaptionCoverage(text) {
    return coverageRuntime.byCaption.get(text) || transcriptRuntime.coverageByText.get(text);
  }

  function currentVideoPosition() {
    const video = player && player.querySelector("video");
    return video && Number.isFinite(video.currentTime)
      ? Math.max(0, Math.round(video.currentTime * 1000))
      : null;
  }

  function currentVideoPaused() {
    const video = player && player.querySelector("video");
    return Boolean(video && video.paused);
  }

  function observeVideo(video) {
    if (observedVideo === video) {
      return;
    }
    if (observedVideo && observedVideo.removeEventListener) {
      observedVideo.removeEventListener("pause", handlePlaybackState);
      observedVideo.removeEventListener("play", handlePlaybackState);
      observedVideo.removeEventListener("seeking", handleVideoSeeking);
      observedVideo.removeEventListener("timeupdate", handleVideoTimeUpdate);
    }
    observedVideo = video;
    transcriptRuntime.observedRevision += 1;
    if (observedVideo && observedVideo.addEventListener) {
      observedVideo.addEventListener("pause", handlePlaybackState);
      observedVideo.addEventListener("play", handlePlaybackState);
      observedVideo.addEventListener("seeking", handleVideoSeeking);
      observedVideo.addEventListener("timeupdate", handleVideoTimeUpdate);
    }
  }

  function handlePlaybackState() {
    if (observedVideo && !observedVideo.paused && hoverPausedVideo === observedVideo) {
      hoverPausedVideo = undefined;
    }
    transcriptRuntime.observedRevision += 1;
    if (settings.displayMode === "pause_reveal" && currentVideoPaused() && !caption && lastObservedCaption) {
      caption = lastObservedCaption.text;
      renderCaption();
    } else {
      applySettings();
    }
  }

  function handleVideoSeeking() {
    lastObservedCaption = undefined;
    clearMirroredCaption();
    clearSelectedCapture();
  }

  function handleVideoTimeUpdate() {
    if (transcriptRuntime.automatic && settings.automaticCaptionMode === "full") {
      scheduleCaptionRead();
    }
  }

  function captionNodes(text, coverage) {
    const words = subtitleModel.lookupWords(text, subtitleModel.words(text, coverage));
    const nodes = [];
    let offset = 0;
    words.forEach(function (token) {
      const start = token.start;
      const end = token.end;
      if (start > offset) {
        nodes.push(document.createTextNode(text.slice(offset, start)));
      }
      const surface = text.slice(start, end);
      const word = element("span", "goi-ext-caption-word goi-ext-caption-word--" + token.status);
      subtitleView.renderWord(word, surface, token, {
        furiganaEnabled: settings.furiganaEnabled,
        rubyClass: "goi-ext-furigana",
      });
      word.title = token.status === "unknown"
        ? "Unknown in Goi — click to look up"
        : token.status === "suspended_leech"
          ? "Suspended leech — click to look up or mine again"
          : token.status === "leech"
            ? "Leech — click to look up"
            : "Click to look up";
      word.tabIndex = 0;
      word.setAttribute("role", "button");
      word.setAttribute("aria-label", "Look up " + surface);
      const expression = token.expression || token.surface;
      word.addEventListener("pointerenter", function () {
        prefetchDictionary(expression);
        scheduleHoverLookup(token, text, word);
      });
      word.addEventListener("pointerleave", function () {
        scheduleHoverLookupClose();
      });
      word.addEventListener("focus", function () {
        prefetchDictionary(expression);
      });
      function activate(event) {
        if (!event.isTrusted) {
          return;
        }
        event.preventDefault();
        event.stopPropagation();
        const selection = window.getSelection();
        if (selection && !selection.isCollapsed && selection.rangeCount &&
            captionText.contains(selection.anchorNode) && captionText.contains(selection.focusNode)) {
          updateSelectedCapture();
          return;
        }
        cancelHoverLookup();
        selectCaptionWord(token, text, word, true);
      }
      word.addEventListener("click", activate);
      word.addEventListener("keydown", function (event) {
        if (event.key === "Enter" || event.key === " ") {
          activate(event);
        }
      });
      nodes.push(word);
      offset = end;
    });
    if (offset < text.length) {
      nodes.push(document.createTextNode(text.slice(offset)));
    }
    return nodes.length ? nodes : [document.createTextNode(text)];
  }

  function selectCaptionWord(token, fullCaption, anchor, pinned) {
    const video = player && player.querySelector("video");
    const lineID = coverageRuntime.activeOccurrence &&
      (coverageRuntime.activeOccurrence.historyReplayLineID || coverageRuntime.activeOccurrence.id);
    selectedCapture = {
      surface: token.surface,
      expression: token.expression || token.surface,
      caption: activeCaptureSentence(fullCaption),
      sourceTitle: document.title,
      sourceURL: location.href,
      status: token.status,
      lineID,
      sourcePositionMs: video && Number.isFinite(video.currentTime)
        ? Math.max(0, Math.round(video.currentTime * 1000))
        : null
    };
    lookupPinned = Boolean(pinned);
    showSelectionAction(selectedCapture.surface, anchor);
    if (lookupPinned) {
      pauseForSelection(lineID);
    }
  }

  function scheduleHoverLookup(token, fullCaption, anchor) {
    cancelHoverLookup();
    cancelHoverLookupClose();
    if (!settings.hoverLookupEnabled || lookupPinned) {
      return;
    }
    hoverLookupTimer = setTimeout(function () {
      hoverLookupTimer = undefined;
      if (anchor && anchor.isConnected === false && (!overlay || !overlay.contains(anchor))) {
        return;
      }
      selectCaptionWord(token, fullCaption, anchor, false);
    }, HOVER_LOOKUP_DELAY_MS);
  }

  function cancelHoverLookup() {
    clearTimeout(hoverLookupTimer);
    hoverLookupTimer = undefined;
  }

  function cancelHoverLookupClose() {
    clearTimeout(hoverLookupCloseTimer);
    hoverLookupCloseTimer = undefined;
  }

  function scheduleHoverLookupClose() {
    cancelHoverLookup();
    cancelHoverLookupClose();
    if (lookupPinned) {
      return;
    }
    hoverLookupCloseTimer = setTimeout(function () {
      hoverLookupCloseTimer = undefined;
      if (!lookupPinned) {
        clearSelectedCapture();
      }
    }, HOVER_LOOKUP_CLOSE_MS);
  }

  function prefetchDictionary(expression) {
    dictionaryClient.lookup(chrome.runtime, expression).catch(ignoreDictionaryPrefetchFailure);
  }

  function activeCaptureSentence(fallback) {
    const line = coverageRuntime.activeOccurrence && subtitleLine(coverageRuntime.activeOccurrence);
    return line ? line.text : String(fallback || "");
  }

  function pauseForSelection(lineID) {
    if (settings.pauseBehavior === "on_hover") {
      pauseForHover();
      return;
    }
    if (settings.pauseBehavior !== "on_selection") {
      return;
    }
    pauseVideo();
  }

  function pauseForHover() {
    if (settings.pauseBehavior !== "on_hover") {
      return;
    }
    const video = player && player.querySelector("video");
    if (!video || video.paused) {
      return;
    }
    hoverPausedVideo = video;
    video.pause();
  }

  function resumeAfterHover() {
    const video = hoverPausedVideo;
    hoverPausedVideo = undefined;
    if (!video || !video.paused || video.ended) {
      return;
    }
    video.play().catch(function () {
      showCaptureStatus("Playback blocked", true);
    });
  }

  function showSelectionAction(surface, anchor) {
    clearTimeout(showCaptureStatus.timer);
    captionStatus.hidden = true;
    selectionAction.textContent = "";
    selectionAction.setAttribute("aria-label", "");
    selectionAction.hidden = true;
    selectionKnownAction.hidden = selectedCapture && selectedCapture.status !== "unknown";
    selectionLookup.hidden = false;
    positionSelectionLookup(anchor);
    lookupSelectedWord(selectedCapture && selectedCapture.expression || surface);
  }

  async function markSelectedWordKnown() {
    if (capturePending || !selectedCapture) {
      return;
    }
    const expression = selectedCapture.expression || selectedCapture.surface;
    capturePending = true;
    selectionAction.disabled = true;
    selectionKnownAction.disabled = true;
    try {
      const response = await chrome.runtime.sendMessage({
        type: "goi.vocabulary.known",
        version: 1,
        expression
      });
      if (!response || !response.ok || !response.result) {
        showCaptureStatus(captionModel.captureErrorMessage(response && response.errorCode), true);
        return;
      }
      if (response.result.state === "in_lessons") {
        showCaptureStatus("This word is already waiting in lessons", false);
        return;
      }
      showCaptureStatus(
        response.result.state === "already_known" ? "Already marked as known" : "Marked as known",
        false
      );
      clearSelectedCapture();
      refreshCoverageAfterVocabularyChange();
    } catch (_error) {
      showCaptureStatus("Could not reach Goi", true);
    } finally {
      capturePending = false;
      if (selectionAction) {
        selectionAction.disabled = false;
      }
      if (selectionKnownAction) {
        selectionKnownAction.disabled = false;
      }
    }
  }

  function refreshCoverageAfterVocabularyChange() {
    resetCoverage();
    scheduleCaptionRead();
    if (!transcriptRuntime.lines.length) {
      loadFullTranscript(0);
      return;
    }
    clearTimeout(transcriptRuntime.retryTimer);
    transcriptRuntime.retryTimer = undefined;
    const generation = ++transcriptRuntime.generation;
    transcriptRuntime.state = "checking";
    transcriptRuntime.reason = "";
    transcriptRuntime.coverageByText = new Map();
    transcriptRuntime.summary = emptyCoverageSummary();
    transcriptRuntime.lines.forEach(function (line) {
      line.classification = "pending";
      line.coverageBlock = undefined;
      line.unknowns = [];
      line.words = [];
    });
    transcriptRuntime.observedRevision += 1;
    updateCoverageLabel();
    analyzeFullTranscript(generation, 0);
  }

  function positionSelectionLookup(anchor) {
    const below = settings.verticalPercent < 45;
    selectionLookup.classList.toggle("goi-ext-caption-lookup--below", below);
    let anchorRect = anchor;
    if (anchor && typeof anchor.getBoundingClientRect === "function") {
      anchorRect = anchor.getBoundingClientRect();
    }
    if (!anchorRect || !Number.isFinite(anchorRect.top) ||
        !overlay || typeof overlay.getBoundingClientRect !== "function") {
      selectionLookup.style.setProperty("--goi-ext-lookup-left", "50%");
      selectionLookup.style.setProperty("--goi-ext-lookup-top", below ? "100%" : "0px");
      return;
    }
    const overlayRect = overlay.getBoundingClientRect();
    const width = Number(overlayRect.width) || 0;
    const anchorLeft = Number(anchorRect.left) || 0;
    const anchorWidth = Number(anchorRect.width) || 0;
    const rawLeft = anchorLeft + anchorWidth / 2 - (Number(overlayRect.left) || 0);
    const halfPopup = Math.min(240, width / 2);
    const left = width > 0
      ? Math.max(halfPopup, Math.min(width - halfPopup, rawLeft))
      : rawLeft;
    const anchorEdge = below ? anchorRect.bottom : anchorRect.top;
    const top = Number(anchorEdge) - (Number(overlayRect.top) || 0) + (below ? 10 : -10);
    selectionLookup.style.setProperty("--goi-ext-lookup-left", Math.round(left) + "px");
    selectionLookup.style.setProperty("--goi-ext-lookup-top", Math.round(top) + "px");
  }

  async function lookupSelectedWord(expression) {
    const sequence = ++lookupSequence;
    selectionLookupContent.replaceChildren(
      element("p", "goi-ext-dictionary-message", "Looking up “" + expression + "”…")
    );
    selectionLookup.hidden = false;
    try {
      const response = await dictionaryClient.lookup(chrome.runtime, expression);
      if (sequence !== lookupSequence || !selectedCapture) {
        return;
      }
      renderDictionaryResponse(response);
    } catch (_error) {
      if (sequence === lookupSequence && selectedCapture) {
        selectionLookupContent.replaceChildren(
          element("p", "goi-ext-dictionary-message", "Could not contact the extension. Reload this page.")
        );
        showFallbackCaptureAction();
      }
    }
  }

  function showFallbackCaptureAction() {
    if (!selectedCapture) {
      return;
    }
    selectionAction.textContent = "Send to mining";
    selectionAction.setAttribute("aria-label", "Send “" + selectedCapture.surface + "” to mining");
    selectionAction.hidden = false;
  }

  function renderDictionaryResponse(response) {
    const view = subtitleModel.dictionaryView(response);
    if (!view.candidates.length) {
      selectionLookupContent.replaceChildren(
        element("p", "goi-ext-dictionary-message", view.message)
      );
      showFallbackCaptureAction();
      return;
    }
    dictionaryRenderer.render(selectionLookupContent, view, {
      document,
      actionLabel: "Mine",
      selectedEntrySequence: null,
      onSelect(candidate) {
        if (!selectedCapture) {
          return;
        }
        selectedCapture.suggestedEntrySequence = candidate.entrySequence;
        void captureSelectedText();
        return false;
      }
    });
  }

  function observeCoverageCaption(text, cueNode, contentNode) {
    if (coverageRuntime.activeOccurrence) {
      if (coverageRuntime.activeOccurrence.text === text &&
          sameNodeIdentity(coverageRuntime.activeOccurrence.cueNode, cueNode) &&
          sameNodeIdentity(coverageRuntime.activeOccurrence.contentNode, contentNode)) {
        return;
      }
      if (sameCaptionCue(coverageRuntime.activeOccurrence, text, cueNode, contentNode)) {
        const previousAnalysisText = coverageRuntime.activeOccurrence.analysisText;
        const existingStart = previousAnalysisText.startsWith(text, coverageRuntime.activeOccurrence.visibleStart)
          ? coverageRuntime.activeOccurrence.visibleStart
          : -1;
        if (existingStart >= 0) {
          coverageRuntime.activeOccurrence.text = text;
          coverageRuntime.activeOccurrence.cueNode = cueNode;
          coverageRuntime.activeOccurrence.contentNode = contentNode;
          coverageRuntime.activeOccurrence.visibleStart = existingStart;
          coverageRuntime.activeOccurrence.revision += 1;
          touchObservedSubtitleRevision();
          rebuildCoverageSummary();
          updateCoverageLabel();
          scheduleCoverage(coverageRuntime.activeOccurrence);
          return;
        }
        const overlap = captionOverlap(previousAnalysisText, text);
        const analysisText = previousAnalysisText + text.slice(overlap);
        if (analysisText.length > MAX_COVERAGE_TEXT) {
          coverageRuntime.sampled = true;
          finishActiveCoverageOccurrence(true);
          startCoverageOccurrence(text, cueNode, contentNode, true);
          return;
        }
        coverageRuntime.activeOccurrence.text = text;
        coverageRuntime.activeOccurrence.cueNode = cueNode;
        coverageRuntime.activeOccurrence.contentNode = contentNode;
        coverageRuntime.activeOccurrence.visibleStart = previousAnalysisText.length - overlap;
        coverageRuntime.activeOccurrence.analysisText = analysisText;
        coverageRuntime.activeOccurrence.revision += 1;
        touchObservedSubtitleRevision();
        rebuildCoverageSummary();
        updateCoverageLabel();
        scheduleCoverage(coverageRuntime.activeOccurrence);
        return;
      }
      finishActiveCoverageOccurrence();
    }
    startCoverageOccurrence(text, cueNode, contentNode);
  }

  function startCoverageOccurrence(text, cueNode, contentNode, partial) {
    const historyReplayLineID = historySeekReplayLineID(text);
    coverageRuntime.activeOccurrence = {
      id: coverageRuntime.nextOccurrenceID,
      revision: 1,
      text,
      analysisText: text,
      visibleStart: 0,
      cueNode,
      contentNode,
      sourcePositionMs: currentVideoPosition(),
      sourceTitle: document.title,
      sourceURL: location.href,
      coverageState: "pending",
      finalized: false,
      analyzed: false,
      historyReplay: Boolean(historyReplayLineID),
      historyReplayLineID
    };
    coverageRuntime.nextOccurrenceID += 1;
    touchObservedSubtitleRevision();
    coverageRuntime.occurrences.set(coverageRuntime.activeOccurrence.id, coverageRuntime.activeOccurrence);
    scheduleCoverage(coverageRuntime.activeOccurrence);
  }

  function historySeekReplayLineID(text) {
    if (!transcriptRuntime.historySeek || Date.now() > transcriptRuntime.historySeek.expiresAt) {
      transcriptRuntime.historySeek = undefined;
      return 0;
    }
    const position = currentVideoPosition();
    const matches = text === transcriptRuntime.historySeek.text && Number.isSafeInteger(position) &&
      Math.abs(position - transcriptRuntime.historySeek.sourcePositionMs) <= 2000;
    const lineID = matches ? transcriptRuntime.historySeek.lineID : 0;
    if (lineID) {
      transcriptRuntime.historySeek = undefined;
    }
    return lineID;
  }

  function sameCaptionCue(previous, text, cueNode, contentNode) {
    if (previous.cueNode && cueNode && !sameNodeIdentity(previous.cueNode, cueNode)) {
      return false;
    }
    if (previous.text === text) {
      return sameNodeIdentity(previous.contentNode, contentNode);
    }
    if (previous.cueNode && !nodeIdentityIsConnected(previous.cueNode)) {
      return false;
    }
    if (text.startsWith(previous.text) || previous.text.startsWith(text)) {
      return true;
    }
    const maximum = Math.min(previous.text.length, text.length);
    const overlap = captionOverlap(previous.text, text);
    return overlap > 0 && (
      sameNodeIdentity(previous.contentNode, contentNode) || overlap * 2 >= maximum
    );
  }

  function sameNodeIdentity(previous, next) {
    if (previous === next) {
      return true;
    }
    if (!Array.isArray(previous) || !Array.isArray(next) || previous.length !== next.length) {
      return false;
    }
    return previous.every(function (node, index) {
      return node === next[index];
    });
  }

  function nodeIdentityIsConnected(identity) {
    const nodes = Array.isArray(identity) ? identity : [identity];
    return nodes.every(function (node) {
      return !node || node.isConnected !== false;
    });
  }

  function captionOverlap(previous, next) {
    const maximum = Math.min(previous.length, next.length);
    for (let length = maximum; length > 0; length -= 1) {
      if (previous.slice(-length) === next.slice(0, length)) {
        return length;
      }
    }
    return 0;
  }

  function finishActiveCoverageOccurrence() {
    if (!coverageRuntime.activeOccurrence) {
      return;
    }
    if (coverageRuntime.scheduledTask && coverageRuntime.scheduledTask.occurrenceID === coverageRuntime.activeOccurrence.id) {
      flushScheduledCoverage();
    }
    const occurrence = coverageRuntime.activeOccurrence;
    coverageRuntime.activeOccurrence = undefined;
    occurrence.finalized = true;
    if (occurrence.historyReplay) {
      coverageRuntime.occurrences.delete(occurrence.id);
      rebuildCoverageSummary();
      updateCoverageLabel();
      return;
    }
    publishSubtitleLine(occurrence);
    if (occurrence.contributionRevision === occurrence.revision) {
      commitCoverageOccurrence(occurrence);
    } else if (occurrence.droppedRevision === occurrence.revision) {
      coverageRuntime.occurrences.delete(occurrence.id);
    }
    rebuildCoverageSummary();
    updateCoverageLabel();
  }

  function coverageTask(occurrence) {
    return {
      occurrenceID: occurrence.id,
      revision: occurrence.revision,
      text: occurrence.analysisText,
      visibleText: occurrence.text,
      visibleStart: occurrence.visibleStart,
      generation: coverageRuntime.generation
    };
  }

  function scheduleCoverage(occurrence) {
    clearTimeout(coverageRuntime.timer);
    coverageRuntime.timer = undefined;
    coverageRuntime.scheduledTask = coverageTask(occurrence);
    coverageRuntime.timer = setTimeout(function () {
      coverageRuntime.timer = undefined;
      const task = coverageRuntime.scheduledTask;
      coverageRuntime.scheduledTask = undefined;
      enqueueCaptionCoverage(task);
    }, COVERAGE_DELAY_MS);
  }

  function flushScheduledCoverage() {
    if (!coverageRuntime.scheduledTask) {
      return;
    }
    clearTimeout(coverageRuntime.timer);
    coverageRuntime.timer = undefined;
    const task = coverageRuntime.scheduledTask;
    coverageRuntime.scheduledTask = undefined;
    enqueueCaptionCoverage(task);
  }

  function enqueueCaptionCoverage(task) {
    if (!task || task.generation !== coverageRuntime.generation) {
      return;
    }
    const queued = coverageRuntime.queuedByOccurrence.get(task.occurrenceID);
    const cached = coverageRuntime.byText.get(task.text) || transcriptRuntime.coverageByText.get(task.text);
    if (cached) {
      if (queued) {
        const index = coverageRuntime.queue.indexOf(queued);
        if (index >= 0) {
          coverageRuntime.queue.splice(index, 1);
        }
        coverageRuntime.queuedByOccurrence.delete(task.occurrenceID);
      }
      rememberVisibleCaptionCoverage(task, cached);
      applyCaptionCoverage(task, cached.block, cached.excludedNames);
      return;
    }
    if (queued) {
      queued.revision = task.revision;
      queued.text = task.text;
      queued.visibleText = task.visibleText;
      queued.visibleStart = task.visibleStart;
      return;
    }
    if (coverageRuntime.queue.length >= MAX_PENDING_COVERAGE) {
      discardCoverageTask(coverageRuntime.queue.shift());
    }
    coverageRuntime.queue.push(task);
    coverageRuntime.queuedByOccurrence.set(task.occurrenceID, task);
    drainCoverageQueue();
  }

  function discardCoverageTask(task) {
    coverageRuntime.queuedByOccurrence.delete(task.occurrenceID);
    const occurrence = coverageRuntime.occurrences.get(task.occurrenceID);
    if (occurrence && occurrence.revision === task.revision) {
      occurrence.droppedRevision = task.revision;
      if (occurrence.finalized) {
        coverageRuntime.occurrences.delete(occurrence.id);
      }
    }
    coverageRuntime.sampled = true;
    rebuildCoverageSummary();
    updateCoverageLabel();
  }

  function drainCoverageQueue() {
    const generation = coverageRuntime.generation;
    if (coverageRuntime.workerGeneration === generation || Date.now() < coverageRuntime.retryAt) {
      return;
    }
    coverageRuntime.workerGeneration = generation;
    Promise.resolve().then(async function () {
      while (generation === coverageRuntime.generation && coverageRuntime.queue.length && Date.now() >= coverageRuntime.retryAt) {
        const task = coverageRuntime.queue.shift();
        coverageRuntime.queuedByOccurrence.delete(task.occurrenceID);
        const cached = coverageRuntime.byText.get(task.text) || transcriptRuntime.coverageByText.get(task.text);
        if (cached) {
          rememberVisibleCaptionCoverage(task, cached);
          applyCaptionCoverage(task, cached.block, cached.excludedNames);
          continue;
        }
        const succeeded = await analyzeCaptionCoverage(task, generation);
        if (!succeeded) {
          break;
        }
      }
    }).finally(function () {
      if (coverageRuntime.workerGeneration === generation) {
        coverageRuntime.workerGeneration = -1;
      }
      if (coverageRuntime.queue.length && Date.now() >= coverageRuntime.retryAt) {
        drainCoverageQueue();
      }
    });
  }

  async function analyzeCaptionCoverage(task, generation) {
    showCoveragePending();
    try {
      const response = await chrome.runtime.sendMessage({
        type: "goi.coverage.classify",
        version: 1,
        blocks: [{ id: 1, text: task.text }]
      });
      if (generation !== coverageRuntime.generation) {
        return true;
      }
      if (!response || !response.ok || !response.result || !response.result.summary ||
          !Array.isArray(response.result.blocks)) {
        throw new Error("coverage analysis failed");
      }
      const block = response.result.blocks.find(function (candidate) {
        return candidate.id === 1;
      });
      if (!block) {
        throw new Error("coverage result is incomplete");
      }
      coverageRuntime.unavailable = false;
      coverageRuntime.retryAt = 0;
      clearTimeout(coverageRuntime.retryTimer);
      coverageRuntime.retryTimer = undefined;
      rememberCaptionCoverage(task, block, response.result.summary.excluded_names);
      applyCaptionCoverage(task, block, response.result.summary.excluded_names);
      return true;
    } catch (_error) {
      if (generation === coverageRuntime.generation) {
        coverageRuntime.retryAt = Date.now() + COVERAGE_RETRY_MS;
        const occurrence = coverageRuntime.occurrences.get(task.occurrenceID);
        if (occurrence && occurrence.revision === task.revision) {
          occurrence.coverageState = "unavailable";
          if (occurrence.finalized) {
            publishSubtitleLine(occurrence);
          } else {
            touchObservedSubtitleRevision();
          }
        }
        if (occurrence && occurrence.revision === task.revision &&
            !coverageRuntime.queuedByOccurrence.has(task.occurrenceID)) {
          if (coverageRuntime.queue.length >= MAX_PENDING_COVERAGE) {
            discardCoverageTask(coverageRuntime.queue.pop());
          }
          coverageRuntime.queue.unshift(task);
          coverageRuntime.queuedByOccurrence.set(task.occurrenceID, task);
        }
        clearTimeout(coverageRuntime.retryTimer);
        coverageRuntime.retryTimer = setTimeout(function () {
          coverageRuntime.retryTimer = undefined;
          coverageRuntime.retryAt = 0;
          drainCoverageQueue();
        }, COVERAGE_RETRY_MS);
        showCoverageUnavailable();
      }
      return false;
    }
  }

  function rememberCaptionCoverage(task, block, excludedNames) {
    coverageRuntime.byText.delete(task.text);
    coverageRuntime.byText.set(task.text, { block, excludedNames });
    while (coverageRuntime.byText.size > MAX_RETAINED_CAPTIONS) {
      coverageRuntime.byText.delete(coverageRuntime.byText.keys().next().value);
    }
    rememberVisibleCaptionCoverage(task, { block, excludedNames });
  }

  function rememberVisibleCaptionCoverage(task, cached) {
    const source = cached || coverageRuntime.byText.get(task.text);
    if (!source || !source.block || !Array.isArray(source.block.tokens)) {
      return;
    }
    const visibleEnd = task.visibleStart + task.visibleText.length;
    const block = {
      ...source.block,
      tokens: source.block.tokens.filter(function (token) {
        return token.start_utf16 >= task.visibleStart && token.end_utf16 <= visibleEnd;
      }).map(function (token) {
        return {
          ...token,
          start_utf16: token.start_utf16 - task.visibleStart,
          end_utf16: token.end_utf16 - task.visibleStart
        };
      })
    };
    coverageRuntime.byCaption.delete(task.visibleText);
    coverageRuntime.byCaption.set(task.visibleText, { block });
    while (coverageRuntime.byCaption.size > MAX_RETAINED_CAPTIONS) {
      coverageRuntime.byCaption.delete(coverageRuntime.byCaption.keys().next().value);
    }
    if (caption === task.visibleText && !selectedCapture) {
      renderCaption();
    }
  }

  function coverageContribution(block, excludedNames) {
    const contribution = {
      known_occurrences: 0,
      total_occurrences: 0,
      excluded_names: Number.isSafeInteger(excludedNames) ? excludedNames : 0,
      unknownExpressions: new Set()
    };
    const tokens = Array.isArray(block.tokens) ? block.tokens : [];
    tokens.forEach(function (token) {
      contribution.total_occurrences += 1;
      if (["known", "leech", "suspended_leech"].includes(token.status)) {
        contribution.known_occurrences += 1;
      } else if (token.status === "unknown") {
        contribution.unknownExpressions.add(token.expression || token.surface);
      }
    });
    return contribution;
  }

  function applyCaptionCoverage(task, block, excludedNames) {
    if (!task || task.generation !== coverageRuntime.generation) {
      return;
    }
    const occurrence = coverageRuntime.occurrences.get(task.occurrenceID);
    if (!occurrence || occurrence.revision !== task.revision) {
      return;
    }
    if (!occurrence.analyzed) {
      occurrence.analyzed = true;
      coverageRuntime.captionsAnalyzed += 1;
    }
    occurrence.contribution = coverageContribution(block, excludedNames);
    occurrence.contributionRevision = task.revision;
    occurrence.coverageBlock = block;
    occurrence.coverageState = "ready";
    if (occurrence.finalized) {
      publishSubtitleLine(occurrence);
    } else {
      touchObservedSubtitleRevision();
    }
    if (occurrence.finalized) {
      commitCoverageOccurrence(occurrence);
    }
    rebuildCoverageSummary();
    updateCoverageLabel();
    if (caption === task.visibleText && !selectedCapture) {
      renderCaption();
    }
  }

  function validTranscriptCue(value) {
    return Boolean(value && Number.isSafeInteger(value.startMs) && value.startMs >= 0 &&
      Number.isSafeInteger(value.endMs) && value.endMs > value.startMs &&
      typeof value.text === "string" && value.text.trim() && value.text.length <= 2000);
  }

  function transcriptLine(cue, index) {
    return {
      id: FULL_TRANSCRIPT_FIRST_ID + index,
      text: cue.text.trim(),
      sourcePositionMs: cue.startMs,
      endPositionMs: cue.endMs,
      sourceTitle: String(document.title || "").slice(0, 500),
      sourceURL: String(location.href || "").slice(0, 4096),
      classification: "pending",
      unknowns: [],
      words: [],
      coverageBlock: undefined
    };
  }

  function publicTranscriptLine(line) {
    return {
      id: line.id,
      text: line.text,
      sourcePositionMs: line.sourcePositionMs,
      classification: line.classification,
      unknowns: line.unknowns
    };
  }

  function failFullTranscript(generation, reason) {
    if (generation !== transcriptRuntime.generation) {
      return;
    }
    transcriptRuntime.state = "unavailable";
    transcriptRuntime.reason = reason || "coverage_unavailable";
    transcriptRuntime.lines.forEach(function (line) {
      if (line.classification === "pending") {
        line.classification = "unavailable";
      }
    });
    transcriptRuntime.observedRevision += 1;
    updateCoverageLabel();
  }

  function stopFullTranscript() {
    clearTimeout(transcriptRuntime.retryTimer);
    transcriptRuntime.retryTimer = undefined;
    transcriptRuntime.generation += 1;
    transcriptRuntime.lines = [];
    transcriptRuntime.coverageByText = new Map();
    transcriptRuntime.summary = emptyCoverageSummary();
    transcriptRuntime.state = "loading";
    transcriptRuntime.reason = "";
  }

  function retryFullTranscript(generation, attempt, reason) {
    if (generation !== transcriptRuntime.generation || reason === "no_japanese_track" ||
        attempt >= TRANSCRIPT_RETRY_DELAYS.length) {
      return;
    }
    transcriptRuntime.retryTimer = setTimeout(function () {
      if (generation === transcriptRuntime.generation && settings.overlayEnabled) {
        loadFullTranscript(attempt + 1);
      }
    }, TRANSCRIPT_RETRY_DELAYS[attempt]);
  }

  function failAndRetryFullTranscript(generation, attempt, reason) {
    failFullTranscript(generation, reason);
    retryFullTranscript(generation, attempt, reason);
  }

  async function analyzeFullTranscript(generation, attempt) {
    let batches;
    try {
      batches = subtitleFileModel.createCoverageBatches(transcriptRuntime.lines);
    } catch (_error) {
      failAndRetryFullTranscript(generation, attempt, "coverage_unavailable");
      return;
    }
    const summary = emptyCoverageSummary();
    const unknownExpressions = new Set();
    for (const batch of batches) {
      if (generation !== transcriptRuntime.generation) {
        return;
      }
      let response;
      try {
        response = await chrome.runtime.sendMessage({
          type: "goi.coverage.classify",
          version: 1,
          blocks: batch
        });
      } catch (_error) {
        failAndRetryFullTranscript(generation, attempt, "coverage_unavailable");
        return;
      }
      if (generation !== transcriptRuntime.generation) {
        return;
      }
      if (!response || !response.ok || !subtitleModel.validCoverageBatch(batch, response.result)) {
        failAndRetryFullTranscript(generation, attempt, response && response.errorCode || "coverage_unavailable");
        return;
      }
      const blocks = new Map(response.result.blocks.map(function (block) { return [block.id, block]; }));
      batch.forEach(function (requestBlock) {
        const line = transcriptRuntime.lines[requestBlock.id - FULL_TRANSCRIPT_FIRST_ID];
        const block = blocks.get(requestBlock.id);
        if (!line || !block) {
          return;
        }
        line.coverageBlock = block;
        line.unknowns = subtitleModel.unknownWords(line.text, block);
        line.words = subtitleModel.words(line.text, block);
        line.classification = "ready";
        transcriptRuntime.coverageByText.set(line.text, { block });
        line.unknowns.forEach(function (word) {
          unknownExpressions.add(word.expression || word.surface);
        });
      });
      summary.known_occurrences += response.result.summary.known_occurrences;
      summary.total_occurrences += response.result.summary.total_occurrences;
      summary.excluded_names += response.result.summary.excluded_names;
      if (caption && transcriptRuntime.coverageByText.has(caption) && !selectedCapture) {
        renderCaption();
      }
    }
    if (generation !== transcriptRuntime.generation) {
      return;
    }
    summary.unknown_unique = unknownExpressions.size;
    transcriptRuntime.summary = summary;
    transcriptRuntime.state = "ready";
    transcriptRuntime.reason = "";
    transcriptRuntime.observedRevision += 1;
    updateCoverageLabel();
  }

  async function loadFullTranscript(attempt) {
    clearTimeout(transcriptRuntime.retryTimer);
    transcriptRuntime.retryTimer = undefined;
    const generation = ++transcriptRuntime.generation;
    transcriptRuntime.state = "loading";
    transcriptRuntime.reason = "";
    transcriptRuntime.lines = [];
    transcriptRuntime.coverageByText = new Map();
    transcriptRuntime.summary = emptyCoverageSummary();
    transcriptRuntime.automatic = false;
    transcriptRuntime.observedRevision += 1;
    updateCoverageLabel();
    let response;
    try {
      response = await chrome.runtime.sendMessage({
        type: "goi.youtube.transcript.get",
        version: 1
      });
    } catch (_error) {
      failFullTranscript(generation, "transcript_unavailable");
      retryFullTranscript(generation, attempt || 0, "transcript_unavailable");
      return;
    }
    if (generation !== transcriptRuntime.generation) {
      return;
    }
    if (!response || !response.ok || response.state !== "ready" || !Array.isArray(response.cues)) {
      const reason = response && response.reason || "transcript_unavailable";
      failFullTranscript(generation, reason);
      retryFullTranscript(generation, attempt || 0, reason);
      return;
    }
    if (!response.cues.length || response.cues.length > subtitleFileModel.LIMITS.validCues ||
        !response.cues.every(validTranscriptCue)) {
      failFullTranscript(generation, "transcript_unavailable");
      return;
    }
    transcriptRuntime.lines = response.cues.map(transcriptLine);
    transcriptRuntime.automatic = Boolean(response.automatic);
    transcriptRuntime.state = "checking";
    transcriptRuntime.observedRevision += 1;
    updateCoverageLabel();
    readCaption();
    analyzeFullTranscript(generation, attempt || 0);
  }

  function currentTranscriptLine() {
    if (!transcriptRuntime.lines.length) {
      return undefined;
    }
    const video = player?.querySelector("video") || activePlayer()?.querySelector("video");
    if (!video || !Number.isFinite(video.currentTime)) {
      return undefined;
    }
    const currentMS = Math.max(0, Math.round(video.currentTime * 1000));
    return transcriptRuntime.lines.findLast(function (candidate) {
      return candidate.sourcePositionMs <= currentMS && candidate.endPositionMs > currentMS;
    });
  }

  function currentTranscriptLineID() {
    if (transcriptRuntime.lines.length) {
      const line = currentTranscriptLine();
      return line && line.id;
    }
    const active = coverageRuntime.activeOccurrence && !coverageRuntime.activeOccurrence.historyReplay
      ? coverageRuntime.activeOccurrence.id
      : undefined;
    return Number.isSafeInteger(active) ? active : undefined;
  }

  function subtitleLine(occurrence) {
    const text = String(occurrence && occurrence.analysisText || "").trim();
    if (!text) {
      return null;
    }
    return {
      id: occurrence.id,
      text,
      sourcePositionMs: Number.isSafeInteger(occurrence.sourcePositionMs)
        ? occurrence.sourcePositionMs
        : null,
      sourceTitle: String(occurrence.sourceTitle || "").slice(0, 500),
      sourceURL: String(occurrence.sourceURL || "").slice(0, 4096),
      classification: occurrence.coverageState === "ready"
        ? "ready"
        : occurrence.coverageState === "unavailable"
          ? "unavailable"
          : "pending",
      unknowns: subtitleModel.unknownWords(text, occurrence.coverageBlock),
      words: subtitleModel.words(text, occurrence.coverageBlock)
    };
  }

  function publishSubtitleLine(occurrence) {
    if (occurrence.historyReplay) {
      return;
    }
    const line = subtitleLine(occurrence);
    if (!line) {
      return;
    }
    const existingIndex = transcriptRuntime.observedLines.findIndex(function (candidate) {
      return candidate.id === line.id;
    });
    if (existingIndex >= 0) {
      transcriptRuntime.observedCharacters -= transcriptRuntime.observedLines[existingIndex].text.length;
      transcriptRuntime.observedLines[existingIndex] = line;
    } else {
      transcriptRuntime.observedLines.push(line);
    }
    transcriptRuntime.observedCharacters += line.text.length;
    while (transcriptRuntime.observedLines.length > MAX_OBSERVED_SUBTITLE_LINES ||
        transcriptRuntime.observedCharacters > MAX_OBSERVED_SUBTITLE_CHARACTERS) {
      transcriptRuntime.observedCharacters -= transcriptRuntime.observedLines.shift().text.length;
    }
    touchObservedSubtitleRevision();
  }

  function touchObservedSubtitleRevision() {
    if (!transcriptRuntime.lines.length) {
      transcriptRuntime.observedRevision += 1;
    }
  }

  function resetSubtitleHistory() {
    transcriptRuntime.observedLines = [];
    transcriptRuntime.observedCharacters = 0;
    transcriptRuntime.observedRevision += 1;
    lastObservedCaption = undefined;
  }

  function subtitleSnapshot() {
    const currentLineID = currentTranscriptLineID();
    if (transcriptRuntime.lines.length) {
      return {
        sessionID: subtitleSessionID,
        revision: transcriptRuntime.observedRevision,
        sourceTitle: String(document.title || "").slice(0, 500),
        sourceURL: String(location.href || "").slice(0, 4096),
        observing: settings.overlayEnabled,
        playbackPaused: currentVideoPaused(),
        currentLineID,
        transcriptState: transcriptRuntime.state,
        transcriptSource: "full",
        transcriptReason: transcriptRuntime.reason,
        comprehension: { ...transcriptRuntime.summary, line_count: transcriptRuntime.lines.length },
        lines: transcriptRuntime.lines.map(publicTranscriptLine)
      };
    }
    const active = coverageRuntime.activeOccurrence && !coverageRuntime.activeOccurrence.historyReplay
      ? subtitleLine(coverageRuntime.activeOccurrence)
      : null;
    const lines = [];
    let characters = active ? active.text.length : 0;
    for (let index = transcriptRuntime.observedLines.length - 1; index >= 0; index -= 1) {
      const line = transcriptRuntime.observedLines[index];
      if (lines.length >= MAX_OBSERVED_SUBTITLE_LINES - Number(Boolean(active)) ||
          characters + line.text.length > MAX_OBSERVED_SUBTITLE_CHARACTERS) {
        break;
      }
      lines.unshift(line);
      characters += line.text.length;
    }
    if (active) {
      lines.push(active);
    }
    return {
      sessionID: subtitleSessionID,
      revision: transcriptRuntime.observedRevision,
      sourceTitle: String(document.title || "").slice(0, 500),
      sourceURL: String(location.href || "").slice(0, 4096),
      observing: settings.overlayEnabled,
      playbackPaused: currentVideoPaused(),
      currentLineID,
      transcriptState: transcriptRuntime.state,
      transcriptSource: "observed",
      transcriptReason: transcriptRuntime.reason,
      comprehension: null,
      lines
    };
  }

  function subtitleLineByID(lineID) {
    if (!Number.isSafeInteger(lineID)) {
      return null;
    }
    if (coverageRuntime.activeOccurrence && coverageRuntime.activeOccurrence.id === lineID) {
      return subtitleLine(coverageRuntime.activeOccurrence);
    }
    const transcriptLine = transcriptRuntime.lines.find(function (line) { return line.id === lineID; });
    if (transcriptLine) {
      return transcriptLine;
    }
    return transcriptRuntime.observedLines.find(function (line) { return line.id === lineID; }) || null;
  }

  function belongsToCurrentVideo(sourceURL) {
    try {
      const source = new URL(sourceURL);
      const current = new URL(location.href);
      if (source.origin !== current.origin || source.pathname !== current.pathname) {
        return false;
      }
      return source.pathname !== "/watch" || source.searchParams.get("v") === current.searchParams.get("v");
    } catch (_error) {
      return false;
    }
  }

  function commitCoverageOccurrence(occurrence) {
    const contribution = occurrence.contribution;
    if (!contribution) {
      return;
    }
    coverageRuntime.committedSummary.known_occurrences += contribution.known_occurrences;
    coverageRuntime.committedSummary.total_occurrences += contribution.total_occurrences;
    coverageRuntime.committedSummary.excluded_names += contribution.excluded_names;
    contribution.unknownExpressions.forEach(function (expression) {
      coverageRuntime.committedUnknownExpressions.add(expression);
    });
    coverageRuntime.committedSummary.unknown_unique = coverageRuntime.committedUnknownExpressions.size;
    coverageRuntime.occurrences.delete(occurrence.id);
  }

  function rebuildCoverageSummary() {
    const unknownExpressions = new Set(coverageRuntime.committedUnknownExpressions);
    coverageRuntime.summary = { ...coverageRuntime.committedSummary };
    coverageRuntime.occurrences.forEach(function (occurrence) {
      if (!occurrence.contribution || occurrence.contributionRevision !== occurrence.revision) {
        return;
      }
      coverageRuntime.summary.known_occurrences += occurrence.contribution.known_occurrences;
      coverageRuntime.summary.total_occurrences += occurrence.contribution.total_occurrences;
      coverageRuntime.summary.excluded_names += occurrence.contribution.excluded_names;
      occurrence.contribution.unknownExpressions.forEach(function (expression) {
        unknownExpressions.add(expression);
      });
    });
    coverageRuntime.summary.unknown_unique = unknownExpressions.size;
  }

  function showCoveragePending() {
    updateCoverageLabel();
  }

  function applyCoverageDisplay() {
    if (!captionCoverage) {
      return;
    }
    captionCoverage.hidden = settings.coverageDisplay === "hidden";
    if (settings.coverageDisplay === "compact" && !captionCoverage.hidden) {
      if (transcriptRuntime.state === "ready" && transcriptRuntime.summary.total_occurrences) {
        const percent = captureModel.coveragePercent(
          transcriptRuntime.summary.known_occurrences,
          transcriptRuntime.summary.total_occurrences
        );
        captionCoverage.textContent = percent + "% known";
      } else if (transcriptRuntime.state === "ready") {
        captionCoverage.textContent = "No Japanese words";
      } else if (transcriptRuntime.state === "unavailable") {
        captionCoverage.textContent = transcriptRuntime.reason === "no_japanese_track"
          ? "No Japanese transcript"
          : "Transcript unavailable";
      } else {
        captionCoverage.textContent = "Checking transcript…";
      }
    }
  }

  function updateCoverageLabel() {
    if (!captionCoverage) {
      return;
    }
    captionCoverage.hidden = false;
    if (transcriptRuntime.state === "loading") {
      captionCoverage.textContent = "Goi · loading full transcript…";
      captionCoverage.title = "Comprehension is calculated from the complete Japanese transcript.";
      applyCoverageDisplay();
      return;
    }
    if (transcriptRuntime.state === "checking") {
      captionCoverage.textContent = "Goi · checking full transcript…";
      captionCoverage.title = transcriptRuntime.lines.length + " subtitle lines found.";
      applyCoverageDisplay();
      return;
    }
    if (transcriptRuntime.state === "unavailable") {
      captionCoverage.textContent = transcriptRuntime.reason === "no_japanese_track"
        ? "Goi · no Japanese transcript"
        : "Goi · full transcript unavailable";
      captionCoverage.title = "Live captions can still be highlighted and mined, but no overall comprehension score is shown.";
      applyCoverageDisplay();
      return;
    }
    if (!transcriptRuntime.summary.total_occurrences) {
      captionCoverage.textContent = "Goi · no classifiable Japanese · full transcript";
      captionCoverage.title = transcriptRuntime.lines.length + " subtitle lines checked.";
      applyCoverageDisplay();
      return;
    }
    const percent = captureModel.coveragePercent(
      transcriptRuntime.summary.known_occurrences,
      transcriptRuntime.summary.total_occurrences
    );
    captionCoverage.textContent = "Goi · " + percent + "% known · full transcript";
    captionCoverage.title = transcriptRuntime.summary.known_occurrences + " of " +
      transcriptRuntime.summary.total_occurrences + " vocabulary words known across " +
      transcriptRuntime.lines.length + " subtitle lines" +
      (transcriptRuntime.summary.excluded_names ? " · " + transcriptRuntime.summary.excluded_names + " names skipped" : "") +
      (transcriptRuntime.automatic ? " · automatic YouTube captions" : "");
    applyCoverageDisplay();
  }

  function showCoverageUnavailable() {
    if (!captionCoverage) {
      return;
    }
    coverageRuntime.unavailable = true;
    updateCoverageLabel();
  }

  function resetCoverage() {
    clearTimeout(coverageRuntime.timer);
    coverageRuntime.timer = undefined;
    clearTimeout(coverageRuntime.retryTimer);
    coverageRuntime.retryTimer = undefined;
    coverageRuntime.generation += 1;
    coverageRuntime.retryAt = 0;
    coverageRuntime.unavailable = false;
    coverageRuntime.summary = emptyCoverageSummary();
    coverageRuntime.committedSummary = emptyCoverageSummary();
    coverageRuntime.captionsAnalyzed = 0;
    coverageRuntime.sampled = false;
    coverageRuntime.byCaption = new Map();
    coverageRuntime.byText = new Map();
    coverageRuntime.committedUnknownExpressions = new Set();
    coverageRuntime.occurrences = new Map();
    coverageRuntime.activeOccurrence = undefined;
    coverageRuntime.scheduledTask = undefined;
    coverageRuntime.queue = [];
    coverageRuntime.queuedByOccurrence = new Map();
    coverageRuntime.workerGeneration = -1;
    updateCoverageLabel();
  }

  function handleCaptionSelection(event) {
    event.stopPropagation();
    updateSelectedCapture();
  }

  function updateSelectedCapture() {
    if (!captionText || !caption) {
      return;
    }
    const selection = window.getSelection();
    if (!selection || selection.isCollapsed || !selection.rangeCount) {
      return;
    }
    if (!captionText.contains(selection.anchorNode) || !captionText.contains(selection.focusNode)) {
      clearSelectedCapture();
      return;
    }
    const surface = selectedCaptionText(selection);
    if (!surface || !caption.includes(surface)) {
      return;
    }

    const video = player && player.querySelector("video");
    const lineID = coverageRuntime.activeOccurrence &&
      (coverageRuntime.activeOccurrence.historyReplayLineID || coverageRuntime.activeOccurrence.id);
    selectedCapture = {
      surface,
      status: "unknown",
      caption: activeCaptureSentence(caption),
      sourceTitle: document.title,
      sourceURL: location.href,
      lineID,
      sourcePositionMs: video && Number.isFinite(video.currentTime)
        ? Math.max(0, Math.round(video.currentTime * 1000))
        : null
    };
    cancelHoverLookup();
    cancelHoverLookupClose();
    lookupPinned = true;
    let anchor;
    try {
      const range = selection.getRangeAt(0);
      anchor = range && typeof range.getBoundingClientRect === "function"
        ? range.getBoundingClientRect()
        : null;
    } catch (_error) {
      anchor = null;
    }
    showSelectionAction(surface, anchor);
    pauseForSelection(lineID);
  }

  function selectedCaptionText(selection) {
    try {
      const range = selection.getRangeAt(0);
      if (range && typeof range.cloneContents === "function") {
        const contents = range.cloneContents();
        if (contents && typeof contents.querySelectorAll === "function") {
          contents.querySelectorAll("rt").forEach(function (reading) { reading.remove(); });
          const surface = String(contents.textContent || "").trim();
          if (surface) {
            return surface;
          }
        }
      }
    } catch (_error) {
      return selection.toString().trim();
    }
    return selection.toString().trim();
  }

  function clearSelectedCapture() {
    cancelHoverLookup();
    cancelHoverLookupClose();
    lookupPinned = false;
    selectedCapture = undefined;
    lookupSequence += 1;
    if (selectionAction) {
      selectionAction.hidden = true;
      selectionAction.textContent = "";
    }
    if (selectionKnownAction) {
      selectionKnownAction.hidden = true;
    }
    if (selectionLookup) {
      selectionLookup.hidden = true;
      if (selectionLookupContent) {
        selectionLookupContent.replaceChildren();
      }
    }
    if (!captionHovered) {
      resumeAfterHover();
    }
  }

  function finishCaptionPointer() {
    if (!captionPointerActive) {
      return;
    }
    captionPointerActive = false;
    document.removeEventListener("pointerup", finishCaptionPointer);
    document.removeEventListener("pointercancel", finishCaptionPointer);
    clearTimeout(captionRenderTimer);
    captionRenderTimer = setTimeout(function () {
      captionRenderTimer = undefined;
      if (captionRenderPending) {
        renderCaption();
      }
    }, 0);
  }

  async function captureSelectedText() {
    if (capturePending || !selectedCapture) {
      return;
    }
    const sequence = ++captureSequence;
    const pendingCapture = selectedCapture;
    clearSelectedCapture();
    capturePending = true;
    const line = subtitleLineByID(pendingCapture.lineID);
    const contextText = line && belongsToCurrentVideo(line.sourceURL)
      ? line.text
      : pendingCapture.caption;
    const capture = captionModel.directCaptureInput(pendingCapture.surface, contextText, {
      sourceTitle: pendingCapture.sourceTitle,
      sourceURL: pendingCapture.sourceURL,
      sourcePositionMs: pendingCapture.sourcePositionMs
    });
    capture.expression = pendingCapture.expression || capture.expression;
    capture.suggestedEntrySequence = pendingCapture.suggestedEntrySequence;
    showCaptureStatus("Saving…", false);
    try {
      const response = await chrome.runtime.sendMessage({
        type: "goi.capture.direct",
        version: 1,
        lineID: pendingCapture.lineID,
        capture
      });
      if (!response || !response.ok) {
        if (sequence !== captureSequence) {
          return;
        }
        showCaptureStatus(captionModel.captureErrorMessage(response && response.errorCode), true);
        return;
      }
      if (sequence !== captureSequence) {
        return;
      }
      showCaptureStatus(captureDeliveryText(response), false);
      if (!response.queued && settings.pauseBehavior === "after_capture") {
        pauseVideo();
      }
    } catch (_error) {
      if (sequence === captureSequence) {
        showCaptureStatus("Could not reach Goi", true);
      }
    } finally {
      if (sequence === captureSequence) {
        capturePending = false;
      }
    }
  }

  async function captureSubtitleLine(lineID, surface, suggestedEntrySequence) {
    if (capturePending) {
      return { ok: false, errorCode: "capture_pending" };
    }
    const sequence = ++captureSequence;
    const line = subtitleLineByID(lineID);
    const target = subtitleModel.captureTarget(line, surface);
    if (!line || !belongsToCurrentVideo(line.sourceURL) || !target) {
      return { ok: false, errorCode: "invalid_capture" };
    }
    capturePending = true;
    const capture = captionModel.directCaptureInput(target.surface, line.text, {
      sourceTitle: line.sourceTitle,
      sourceURL: line.sourceURL,
      sourcePositionMs: line.sourcePositionMs
    });
    capture.expression = target.expression;
    capture.suggestedEntrySequence = suggestedEntrySequence;
    showCaptureStatus("Saving…", false);
    try {
      const response = await chrome.runtime.sendMessage({
        type: "goi.capture.direct",
        version: 1,
        lineID,
        capture
      });
      if (!response || !response.ok) {
        if (sequence !== captureSequence) {
          return { ok: false, errorCode: "unavailable_page" };
        }
        const errorCode = response && response.errorCode || "server";
        showCaptureStatus(captionModel.captureErrorMessage(errorCode), true);
        return { ok: false, errorCode };
      }
      if (sequence !== captureSequence) {
        return { ok: false, errorCode: "unavailable_page" };
      }
      showCaptureStatus(captureDeliveryText(response), false);
      if (!response.queued && settings.pauseBehavior === "after_capture") {
        pauseVideo();
      }
      return { ok: true, queued: Boolean(response.queued) };
    } catch (_error) {
      if (sequence === captureSequence) {
        showCaptureStatus("Could not reach Goi", true);
      }
      return { ok: false, errorCode: "server" };
    } finally {
      if (sequence === captureSequence) {
        capturePending = false;
      }
    }
  }

  function captureDeliveryText(response) {
    return response.queued ? "Queued — Goi will retry" : "Saved";
  }

  function seekSubtitleLine(lineID) {
    const line = subtitleLineByID(lineID);
    const video = player && player.querySelector("video");
    if (!line || !belongsToCurrentVideo(line.sourceURL) || !video ||
        !Number.isSafeInteger(line.sourcePositionMs)) {
      return false;
    }
    transcriptRuntime.historySeek = {
      text: line.text,
      lineID: line.id,
      sourcePositionMs: line.sourcePositionMs,
      expiresAt: Date.now() + 4000
    };
    video.currentTime = line.sourcePositionMs / 1000;
    return true;
  }

  function showCaptureStatus(message, error) {
    if (!captionStatus) {
      ensureOverlay();
    }
    if (!captionStatus) {
      return;
    }
    captionStatus.textContent = message;
    captionStatus.classList.toggle("goi-ext-caption-status--error", error);
    captionStatus.hidden = false;
    clearTimeout(showCaptureStatus.timer);
    showCaptureStatus.timer = setTimeout(function () {
      captionStatus.hidden = true;
    }, error ? 5000 : 2200);
  }

  function scheduleCaptionRead() {
    if (!settingsReady || !settings.overlayEnabled || framePending) {
      return;
    }
    framePending = true;
    captionReadTimer = setTimeout(function () {
      captionReadTimer = undefined;
      if (!settings.overlayEnabled) {
        framePending = false;
        return;
      }
      const frame = requestAnimationFrame(readCaption);
      if (framePending) {
        captionReadFrame = frame;
      }
    }, 16);
  }

  function mutationNodeMatches(node, selector) {
    if (!node) {
      return false;
    }
    const candidate = node.nodeType === 3 ? node.parentElement : node;
    return Boolean(candidate && (
      (typeof candidate.matches === "function" && candidate.matches(selector)) ||
      (typeof candidate.closest === "function" && candidate.closest(selector)) ||
      (typeof candidate.querySelector === "function" && candidate.querySelector(selector))
    ));
  }

  function mutationNodeContains(node, selector) {
    if (!node || node.nodeType === 3) {
      return false;
    }
    return Boolean(
      (typeof node.matches === "function" && node.matches(selector)) ||
      (typeof node.querySelector === "function" && node.querySelector(selector))
    );
  }

  function handleDocumentMutations(records) {
    if (!Array.isArray(records)) {
      playerSelectionDirty = true;
      scheduleCaptionRead();
      return;
    }
    const captionSelector = ".caption-window, .ytp-caption-window-container, .ytp-caption-segment";
    const playerSelector = ".html5-video-player";
    let captionsChanged = false;
    for (const record of records) {
      if (mutationNodeMatches(record.target, captionSelector)) {
        captionsChanged = true;
      }
      const changedNodes = [
        ...Array.from(record.addedNodes || []),
        ...Array.from(record.removedNodes || [])
      ];
      if (changedNodes.some(function (node) { return mutationNodeContains(node, playerSelector); })) {
        playerSelectionDirty = true;
        captionsChanged = true;
      } else if (changedNodes.some(function (node) { return mutationNodeMatches(node, captionSelector); })) {
        captionsChanged = true;
      }
      if (captionsChanged && playerSelectionDirty) {
        break;
      }
    }
    if (captionsChanged) {
      scheduleCaptionRead();
    }
  }

  function pauseVideo() {
    const video = player && player.querySelector("video");
    if (video && !video.paused) {
      video.pause();
    }
  }

  function togglePlayback() {
    const video = player && player.querySelector("video");
    if (!video) {
      return;
    }
    if (video.paused) {
      video.play().catch(function () {
        showCaptureStatus("Playback blocked", true);
      });
    } else {
      video.pause();
    }
  }

  function replayFiveSeconds() {
    const video = player && player.querySelector("video");
    const currentTime = video && Number(video.currentTime);
    if (!video || !Number.isFinite(currentTime)) {
      return;
    }
    video.currentTime = Math.max(0, currentTime - 5);
  }

  function openSubtitleBrowser() {
    chrome.runtime.sendMessage({
      type: "goi.companion.open",
      version: 1
    }).then(function (response) {
      if (!response || !response.ok) {
        showCaptureStatus("Open a supported YouTube video first", true);
      }
    }).catch(function () {
      showCaptureStatus("Could not open the transcript browser", true);
    });
  }

  function changeCaptionSize(change) {
    const nextSize = settingsModel.applyPatch(settings, {
      fontSizePx: settings.fontSizePx + change
    }).fontSizePx;
    if (nextSize === settings.fontSizePx) {
      return;
    }
    setSettings({ fontSizePx: nextSize }, true);
  }

  function toggleCaptionVisibility() {
    const displayMode = settings.displayMode === "hidden" ? restoreDisplayMode : "hidden";
    setSettings({ displayMode }, true);
  }

  function resetCaptionSettings() {
    setSettings(Object.assign({}, settingsModel.DEFAULT_SETTINGS), true);
  }

  function setDragPointerCapture(handle, pointerID) {
    if (!handle.setPointerCapture) {
      return;
    }
    try {
      handle.setPointerCapture(pointerID);
    } catch (_error) {
      return;
    }
  }

  function releaseDragPointerCapture(handle, pointerID) {
    if (!handle || !handle.releasePointerCapture) {
      return;
    }
    try {
      handle.releasePointerCapture(pointerID);
    } catch (_error) {
      return;
    }
  }

  function startDrag(event) {
    if (dragging) {
      return;
    }
    event.preventDefault();
    event.stopPropagation();
    dragging = true;
    dragHandle = event.currentTarget;
    dragPointerID = event.pointerId;
    setDragPointerCapture(dragHandle, dragPointerID);
    document.addEventListener("pointermove", drag);
    document.addEventListener("pointerup", finishDrag);
    document.addEventListener("pointercancel", finishDrag);
  }

  function drag(event) {
    if (!dragging || event.pointerId !== dragPointerID || !player) {
      return;
    }
    const bounds = player.getBoundingClientRect();
    if (bounds.height <= 0) {
      return;
    }
    const percent = ((event.clientY - bounds.top) / bounds.height) * 100;
    setSettings({ verticalPercent: percent }, false);
  }

  function finishDrag(event) {
    if (!dragging || event.pointerId !== dragPointerID) {
      return;
    }
    const handle = dragHandle;
    const pointerID = dragPointerID;
    dragging = false;
    dragHandle = undefined;
    dragPointerID = undefined;
    document.removeEventListener("pointermove", drag);
    document.removeEventListener("pointerup", finishDrag);
    document.removeEventListener("pointercancel", finishDrag);
    releaseDragPointerCapture(handle, pointerID);
    persistSettings({ verticalPercent: settings.verticalPercent }, settingsRevision);
  }

  function setSettings(patch, persist) {
    const nextSettings = settingsModel.applyPatch(settings, patch);
    settingsRevision += 1;
    const revision = settingsRevision;
    replaceSettings(nextSettings);
    if (persist) {
      persistSettings(patch, revision);
    }
  }

  async function persistSettings(patch, revision) {
    try {
      const response = await chrome.runtime.sendMessage({
        type: "goi.settings.patch",
        version: 1,
        patch
      });
      if (!response || !response.ok || !response.settings) {
        throw new Error("settings update failed");
      }
      persistedSettings = settingsModel.sanitize(response.settings);
      if (revision === settingsRevision) {
        replaceSettings(persistedSettings);
      }
    } catch (_error) {
      await restorePersistedSettings(revision);
      showCaptureStatus("Settings not saved", true);
    }
  }

  async function restorePersistedSettings(revision) {
    try {
      const response = await chrome.runtime.sendMessage({
        type: "goi.settings.get",
        version: 1
      });
      if (response && response.ok && response.settings) {
        persistedSettings = settingsModel.sanitize(response.settings);
      }
    } catch (_error) {
      // Keep the last saved settings while the worker is unavailable.
    }
    if (revision === settingsRevision) {
      replaceSettings(persistedSettings);
    }
  }

  async function loadSettings() {
    const revision = settingsRevision;
    try {
      const response = await chrome.runtime.sendMessage({
        type: "goi.settings.get",
        version: 1
      });
      if (response && response.ok && revision === settingsRevision) {
        persistedSettings = settingsModel.sanitize(response.settings);
        settingsRevision += 1;
        replaceSettings(persistedSettings);
      }
    } catch (_error) {
      // Captions still work with defaults while the worker restarts.
    } finally {
      settingsReady = true;
      if (settings.overlayEnabled) {
        loadFullTranscript();
      }
      scheduleCaptionRead();
    }
  }

  const observer = new MutationObserver(handleDocumentMutations);
  observer.observe(document.documentElement, { childList: true, subtree: true, characterData: true });
  document.addEventListener("selectionchange", updateSelectedCapture);
  document.addEventListener("pointerdown", function (event) {
    const target = event.target;
    if (quickControlsExpanded && overlay && !overlay.contains(target)) {
      setQuickControlsExpanded(false);
    }
    if (!selectedCapture || (captionText && captionText.contains(target)) ||
        (selectionLookup && selectionLookup.contains(target))) {
      return;
    }
    clearSelectedCapture();
  });
  document.addEventListener("yt-navigate-finish", function () {
    playerSelectionDirty = true;
    captionHovered = false;
    clearMirroredCaption();
    clearSelectedCapture();
    resetCoverage();
    resetSubtitleHistory();
    if (settings.overlayEnabled) {
      loadFullTranscript();
    } else {
      stopFullTranscript();
    }
    scheduleCaptionRead();
  });
  document.addEventListener("play", function () {
    playerSelectionDirty = true;
    scheduleCaptionRead();
  }, true);
  document.addEventListener("goi-ext-capture-saved", function () {
    if (settings.pauseBehavior === "after_capture") {
      pauseVideo();
    }
  });
  chrome.runtime.onMessage.addListener(function (message, _sender, sendResponse) {
    if (!message || message.version !== 1) {
      return false;
    }
    if (message.type === "goi.youtube.session.get") {
      const currentLineID = currentTranscriptLineID();
      if (currentLineID !== transcriptRuntime.publishedLineID) {
        transcriptRuntime.publishedLineID = currentLineID;
        transcriptRuntime.observedRevision += 1;
      }
      if (message.sessionID === subtitleSessionID && message.sinceRevision === transcriptRuntime.observedRevision) {
        sendResponse({ ok: true, unchanged: true });
        return false;
      }
      sendResponse({ ok: true, session: subtitleSnapshot() });
      return false;
    }
    if (message.type === "goi.youtube.transcript.retry") {
      loadFullTranscript(0);
      sendResponse({ ok: true });
      return false;
    }
    if (message.type === "goi.youtube.capture.current") {
      const line = coverageRuntime.activeOccurrence && subtitleLine(coverageRuntime.activeOccurrence);
      const target = selectedCapture && line && selectedCapture.lineID === line.id
        ? subtitleModel.captureTarget(line, selectedCapture.surface)
        : null;
      if (!target) {
        sendResponse({ ok: true, handled: false });
        return false;
      }
      captureSelectedText().then(function () {
        sendResponse({ ok: true, handled: true });
      });
      return true;
    }
    if (message.type === "goi.youtube.line.seek") {
      sendResponse({ ok: seekSubtitleLine(message.lineID) });
      return false;
    }
    if (message.type === "goi.youtube.line.capture") {
      captureSubtitleLine(message.lineID, message.surface, message.suggestedEntrySequence).then(sendResponse);
      return true;
    }
    return false;
  });
  chrome.storage.onChanged.addListener(function (changes, area) {
    if (area === "sync" && changes[settingsModel.STORAGE_KEY]) {
      persistedSettings = settingsModel.sanitize(changes[settingsModel.STORAGE_KEY].newValue);
      settingsRevision += 1;
      replaceSettings(persistedSettings);
    }
  });

  globalThis.GoiYouTubeOverlay = {
    getActiveCaption() {
      return settings.overlayEnabled ? caption : "";
    },
    getActiveVideo() {
      const selectedPlayer = activePlayer() || player;
      return selectedPlayer && selectedPlayer.querySelector
        ? selectedPlayer.querySelector("video")
        : null;
    },
    getCoverage() {
      return {
        captionsAnalyzed: coverageRuntime.captionsAnalyzed,
        pendingAnalyses: coverageRuntime.queue.length +
          Number(coverageRuntime.workerGeneration === coverageRuntime.generation) +
          Number(Boolean(coverageRuntime.scheduledTask)),
        retainedCaptions: coverageRuntime.byCaption.size,
        sampled: coverageRuntime.sampled,
        summary: { ...coverageRuntime.summary }
      };
    },
    getSubtitleSession() {
      return subtitleSnapshot();
    }
  };
  loadSettings();
})();
