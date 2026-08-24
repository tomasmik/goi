(function () {
  "use strict";

  const settingsModel = globalThis.GoiExtension.settingsModel;
  const subtitleModel = globalThis.GoiExtension.subtitleModel;
  const subtitleView = globalThis.GoiExtension.subtitleView;
  const subtitleFileModel = globalThis.GoiExtension.subtitleFileModel;
  const playerCoverageModel = globalThis.GoiExtension.playerCoverage;
  const playerTranscriptModel = globalThis.GoiExtension.playerTranscript;
  const playerStateModel = globalThis.GoiExtension.playerState;
  const dictionaryClient = globalThis.GoiExtension.dictionaryClient;
  const dictionaryRenderer = globalThis.GoiExtension.dictionaryView;
  const translationModel = globalThis.GoiExtension.translation || {
    create: function () {
      return { translate: function () { return Promise.reject(new Error("Translation is unavailable.")); } };
    },
    selectedText: function (selection) { return selection.toString().trim(); }
  };
  const HOVER_LOOKUP_DELAY_MS = 120;
  const displayModeInputs = Array.from(document.querySelectorAll('input[name="display-mode"]'));
  const pauseBehaviorInputs = Array.from(document.querySelectorAll('input[name="pause-behavior"]'));
  const elements = {
    connectionStatus: document.getElementById("connection-status"),
    connectionSetup: document.getElementById("connection-setup"),
    pageStatus: document.getElementById("page-status"),
    videoFile: document.getElementById("video-file"),
    emptyOpenVideo: document.getElementById("empty-open-video"),
    subtitleFile: document.getElementById("subtitle-file"),
    subtitleTrackControls: document.getElementById("subtitle-track-controls"),
    subtitleTrack: document.getElementById("subtitle-track"),
    subtitleTrackRemove: document.getElementById("subtitle-track-remove"),
    videoName: document.getElementById("video-name"),
    videoStage: document.getElementById("video-stage"),
    video: document.getElementById("video"),
    videoEmpty: document.getElementById("video-empty"),
    subtitleOverlay: document.getElementById("subtitle-overlay"),
    wordLookup: document.getElementById("word-lookup"),
    wordLookupTitle: document.getElementById("word-lookup-title"),
    wordLookupContent: document.getElementById("word-lookup-content"),
    wordLookupKnown: document.getElementById("word-lookup-known"),
    wordLookupMine: document.getElementById("word-lookup-mine"),
    wordLookupClose: document.getElementById("word-lookup-close"),
    fullscreen: document.getElementById("fullscreen"),
    videoStatus: document.getElementById("video-status"),
    offsetDescription: document.getElementById("offset-description"),
    offsetEarlier: document.getElementById("offset-earlier"),
    offsetInput: document.getElementById("offset-input"),
    offsetLater: document.getElementById("offset-later"),
    offsetReset: document.getElementById("offset-reset"),
    settingsStatus: document.getElementById("settings-status"),
    fontSizePx: document.getElementById("font-size"),
    fontSizeValue: document.getElementById("font-size-value"),
    verticalPercent: document.getElementById("vertical-position"),
    verticalPercentValue: document.getElementById("vertical-position-value"),
    backgroundOpacity: document.getElementById("background-opacity"),
    backgroundOpacityValue: document.getElementById("background-opacity-value"),
    furiganaEnabled: document.getElementById("furigana"),
    hoverLookupEnabled: document.getElementById("hover-lookup"),
    captureForm: document.getElementById("capture-form"),
    captureTarget: document.getElementById("capture-target"),
    dictionaryLookup: document.getElementById("dictionary-lookup"),
    captureSentence: document.getElementById("capture-sentence"),
    selectedTime: document.getElementById("selected-time"),
    captureSubmit: document.getElementById("capture-submit"),
    jumpSelected: document.getElementById("jump-selected"),
    captureStatus: document.getElementById("capture-status"),
    unknownOnly: document.getElementById("unknown-only"),
    transcriptSearch: document.getElementById("transcript-search"),
    coverageReadiness: document.getElementById("coverage-readiness"),
    jumpCurrent: document.getElementById("jump-current"),
    lineCount: document.getElementById("line-count"),
    coverageSummary: document.getElementById("coverage-summary"),
    coverageProgress: document.getElementById("coverage-progress"),
    coverageRetry: document.getElementById("coverage-retry"),
    partialImport: document.getElementById("partial-import"),
    batchOneTarget: document.getElementById("batch-one-target"),
    batchConfirmation: document.getElementById("batch-confirmation"),
    batchConfirmationText: document.getElementById("batch-confirmation-text"),
    batchConfirm: document.getElementById("batch-confirm"),
    batchCancel: document.getElementById("batch-cancel"),
    subtitleLines: document.getElementById("subtitle-lines"),
    transcriptEmpty: document.getElementById("transcript-empty"),
    transcriptEmptyTitle: document.getElementById("transcript-empty-title"),
    transcriptEmptyDetail: document.getElementById("transcript-empty-detail"),
    loadMore: document.getElementById("load-more"),
    viewerShell: document.getElementById("viewer-shell"),
    workspacePanel: document.getElementById("workspace-panel"),
    panelTitle: document.getElementById("panel-title"),
    panelClose: document.getElementById("panel-close"),
    translateSelection: document.getElementById("translate-selection"),
    translationInput: document.getElementById("translation-input"),
    translatePasted: document.getElementById("translate-pasted"),
    translationStatus: document.getElementById("translation-status"),
    translationResult: document.getElementById("translation-result")
  };
  const panelButtons = Array.from(document.querySelectorAll("[data-panel-target]"));
  const panelViews = Array.from(document.querySelectorAll("[data-player-panel]"));
  const panelTitles = {
    transcript: "Transcript",
    capture: "Mine",
    settings: "Subtitle settings"
  };

  const PRESENTATION_KEYS = [
    "displayMode",
    "fontSizePx",
    "verticalPercent",
    "backgroundOpacity",
    "furiganaEnabled",
    "hoverLookupEnabled",
    "pauseBehavior"
  ];
  const PLAYER_SETTINGS_KEY = "goiLocalPlayerDisplayV1";
  const TRANSCRIPT_PAGE_SIZE = 200;
  const MAX_BATCH_CAPTURES = 100;

  function ignoreDictionaryPrefetchFailure() {}

  const playerState = playerStateModel.create(localStorage, {
    clampOffset: subtitleFileModel.clampOffsetMilliseconds
  });
  const playerInstanceID = randomID();
  let playerTabID;
  let connectionReady = false;
  let settings = settingsModel.sanitize();
  let videoURL = "";
  let videoFileName = "";
  let videoIdentity = "";
  let savedOffsetMs = 0;
  let savedPlaybackSeconds = 0;
  let playbackRestored = false;
  let lastSavedPlaybackBucket = -1;
  let videoReady = false;
  let videoDurationMs = null;
  let subtitleGeneration = 0;
  let nextSubtitleTrackID = 1;
  let subtitleTracks = [];
  let activeSubtitleTrackID;
  let sessionGeneration = 0;
  let nextCueID = 1;
  let cues = [];
  let cueTimeline = subtitleFileModel.createCueTimeline(cues);
  let cueByID = new Map();
  let offsetMs = 0;
  let subtitleReadRevision = 0;
  let coverageRevision = 0;
  let coverageState = playerCoverageModel.emptyState();
  let selectedCueID;
  let currentCueIDs = new Set();
  let lastDialogueCueIDs = [];
  let pauseRevealCueIDs = [];
  let previousVideoMs = null;
  let playbackDiscontinuity = false;
  let seeking = false;
  let frameCallbackID;
  let animationFrameID;
  let renderedLimit = TRANSCRIPT_PAGE_SIZE;
  const renderedLines = new Map();
  const submittedCueIDs = new Set();
  let sendingBatch = false;
  const workspaceRuntime = {
    captureRevision: 0,
    captureBusy: false,
    activePanel: undefined,
    initialTarget: "",
    draftDirty: false,
    suggestedEntrySequence: undefined
  };
  let shuttingDown = false;
  let overlayRenderKey = "";
  const lookupRuntime = {
    revision: 0,
    timer: undefined,
    hoverPauseLocked: false,
    hoverPausedVideo: false,
    selection: undefined,
    suggestedEntrySequence: undefined,
    wordRevision: 0,
    pinned: false,
    hoverTimer: undefined,
    closeTimer: undefined
  };
  const translationRuntime = {
    selectedText: "",
    timer: undefined,
    version: 0,
    schedule: setTimeout,
    cancel: clearTimeout
  };

  function showPanel(name) {
    if (!panelTitles[name]) {
      return;
    }
    if (!elements.wordLookup.hidden) {
      closeWordLookup(true);
    }
    workspaceRuntime.activePanel = name;
    elements.workspacePanel.hidden = false;
    elements.viewerShell.classList.add("panel-open");
    elements.panelTitle.textContent = panelTitles[name];
    panelViews.forEach(function (view) {
      view.hidden = view.dataset.playerPanel !== name;
    });
    panelButtons.forEach(function (button) {
      button.setAttribute("aria-expanded", String(button.dataset.panelTarget === name));
    });
  }

  function closePanel(returnFocus) {
    const activeButton = panelButtons.find(function (button) {
      return button.dataset.panelTarget === workspaceRuntime.activePanel;
    });
    workspaceRuntime.activePanel = undefined;
    elements.workspacePanel.hidden = true;
    elements.viewerShell.classList.remove("panel-open");
    panelButtons.forEach(function (button) {
      button.setAttribute("aria-expanded", "false");
    });
    resumeAfterSubtitleHover();
    if (returnFocus) {
      activeButton?.focus();
    }
  }

  function togglePanel(name) {
    if (workspaceRuntime.activePanel === name && !elements.workspacePanel.hidden) {
      closePanel(false);
      return;
    }
    showPanel(name);
  }

  function randomID() {
    const bytes = crypto.getRandomValues(new Uint8Array(16));
    return Array.from(bytes, function (byte) {
      return byte.toString(16).padStart(2, "0");
    }).join("");
  }

  function saveMediaState(changes) {
    playerState.update(videoIdentity, videoFileName, changes);
  }

  function saveOffset(value) {
    savedOffsetMs = value;
    saveMediaState({ offsetMs: value });
  }

  function savePlaybackPosition(force) {
    if (!videoReady || seeking || !Number.isFinite(elements.video.currentTime)) {
      return;
    }
    const seconds = elements.video.ended ? 0 : Math.max(0, elements.video.currentTime);
    const bucket = Math.floor(seconds / 5);
    if (!force && bucket === lastSavedPlaybackBucket) {
      return;
    }
    lastSavedPlaybackBucket = bucket;
    saveMediaState({ playbackSeconds: seconds });
  }

  function currentSessionID() {
    return playerInstanceID + "_" + sessionGeneration;
  }

  function message(type, extra) {
    return chrome.runtime.sendMessage({ type, version: 1, ...(extra || {}) }).catch(function () {
      return { ok: false, errorCode: "network" };
    });
  }

  const translator = translationModel.create({
    remote: async function (text) {
      const response = await message("goi.translation.remote", { text });
      if (!response?.ok) {
        return { errorCode: response?.errorCode, error: translationModel.failureText(response?.errorCode) };
      }
      return response.result;
    }
  });

  function schedulePastedTranslation(delay) {
    translationModel.schedulePasted(translationRuntime, translator, {
      input: elements.translationInput,
      result: elements.translationResult,
      retry: elements.translatePasted,
      status: elements.translationStatus
    }, delay);
  }

  function updateSelectionTranslation() {
    const selection = window.getSelection();
    const insideTranscript = selection && !selection.isCollapsed && selection.rangeCount > 0 &&
      elements.subtitleLines.contains(selection.anchorNode) && elements.subtitleLines.contains(selection.focusNode);
    translationRuntime.selectedText = insideTranscript
      ? translationModel.selectedText(selection, elements.subtitleLines, ".line-text")
      : "";
    elements.translateSelection.hidden = !translationRuntime.selectedText;
  }

  function showStatus(element, text, error) {
    element.textContent = text;
    element.classList.toggle("error", Boolean(error));
  }

  function errorText(code, fallback) {
    if (code === "not_connected") {
      return "Connect the extension to Goi.";
    }
    if (code === "unauthorized") {
      return "The Goi extension token is no longer valid.";
    }
    if (code === "queue_full") {
      return "The offline capture queue is full. Reconnect Goi and retry.";
    }
    if (code === "invalid_capture") {
      return "That word or sentence could not be saved.";
    }
    if (code === "rate_limited") {
      return "Goi is receiving too many requests. Wait a moment and retry.";
    }
    return fallback;
  }

  function refreshConnection() {
    elements.connectionSetup.disabled = true;
    return message("goi.player.connection.get").then(function (response) {
      if (!response.ok || !response.connection) {
        connectionReady = false;
        elements.connectionStatus.textContent = "Could not check Goi";
        elements.connectionStatus.className = "connection-status disconnected";
        elements.connectionSetup.hidden = false;
        elements.connectionSetup.disabled = false;
        elements.connectionStatus.parentElement.classList.add("connection-problem");
        updateCaptureWorkspace();
        updateBatchButton();
        return false;
      }
      playerTabID = response.tabId;
      connectionReady = Boolean(response.connection.connected);
      elements.connectionStatus.textContent = connectionReady
        ? "Connected to " + response.connection.baseUrl
        : "Not connected";
      elements.connectionStatus.className = "connection-status " +
        (connectionReady ? "connected" : "disconnected");
      elements.connectionSetup.hidden = connectionReady;
      elements.connectionSetup.disabled = connectionReady;
      elements.connectionStatus.parentElement.classList.toggle("connection-problem", !connectionReady);
      updateCaptureWorkspace();
      updateBatchButton();
      return connectionReady;
    });
  }

  async function openConnectionSetup() {
    try {
      await chrome.runtime.openOptionsPage();
    } catch (_error) {
      showStatus(elements.pageStatus, "Open Goi Capture from the Chrome toolbar to connect.", true);
    }
  }

  function fieldValue(field) {
    if (Array.isArray(field)) {
      return field.find(function (input) { return input.checked; })?.value || "";
    }
    if (field.type === "checkbox") {
      return field.checked;
    }
    return field.type === "range" ? Number(field.value) : field.value;
  }

  function setFieldValue(field, value) {
    if (Array.isArray(field)) {
      field.forEach(function (input) {
        input.checked = input.value === value;
      });
      return;
    }
    if (field.type === "checkbox") {
      field.checked = Boolean(value);
    } else {
      field.value = String(value);
    }
  }

  function settingField(key) {
    return {
      displayMode: displayModeInputs,
      pauseBehavior: pauseBehaviorInputs,
      fontSizePx: elements.fontSizePx,
      verticalPercent: elements.verticalPercent,
      backgroundOpacity: elements.backgroundOpacity,
      furiganaEnabled: elements.furiganaEnabled,
      hoverLookupEnabled: elements.hoverLookupEnabled
    }[key];
  }

  function updateSettingOutputs() {
    elements.fontSizeValue.value = settings.fontSizePx + " px";
    elements.fontSizeValue.textContent = elements.fontSizeValue.value;
    elements.verticalPercentValue.value = settingsModel.verticalPositionLabel(settings.verticalPercent);
    elements.verticalPercentValue.textContent = elements.verticalPercentValue.value;
    elements.backgroundOpacityValue.value = Math.round(settings.backgroundOpacity * 100) + "%";
    elements.backgroundOpacityValue.textContent = elements.backgroundOpacityValue.value;
  }

  function applySettings(nextSettings) {
    const wasHoverPause = settings.pauseBehavior === "on_hover";
    settings = settingsModel.sanitize(nextSettings);
    if (!settings.hoverLookupEnabled && !lookupRuntime.pinned) {
      closeWordLookup(true);
    }
    if (wasHoverPause && settings.pauseBehavior !== "on_hover") {
      resumeAfterSubtitleHover();
    }
    PRESENTATION_KEYS.forEach(function (key) {
      setFieldValue(settingField(key), settings[key]);
    });
    updateSettingOutputs();
    elements.subtitleOverlay.style.setProperty("--subtitle-font-size", settings.fontSizePx + "px");
    elements.subtitleOverlay.style.setProperty("--subtitle-top", settings.verticalPercent + "%");
    elements.subtitleOverlay.style.setProperty(
      "--subtitle-background",
      "rgb(0 0 0 / " + settings.backgroundOpacity + ")"
    );
    elements.subtitleOverlay.classList.toggle("dock-above", settings.verticalPercent >= 65);
    elements.subtitleOverlay.classList.toggle("furigana-enabled", settings.furiganaEnabled);
    renderOverlay();
    renderTranscript();
  }

  function pauseForSubtitleHover() {
    if (settings.pauseBehavior !== "on_hover" || elements.video.paused) {
      return;
    }
    lookupRuntime.hoverPausedVideo = true;
    elements.video.pause();
  }

  function lockSubtitleHoverPause() {
    if (settings.pauseBehavior !== "on_hover") {
      return;
    }
    lookupRuntime.hoverPauseLocked = true;
    pauseForSubtitleHover();
  }

  function resumeAfterSubtitleHover() {
    lookupRuntime.hoverPauseLocked = false;
    if (!lookupRuntime.hoverPausedVideo) {
      return;
    }
    lookupRuntime.hoverPausedVideo = false;
    if (!elements.video.paused || elements.video.ended) {
      return;
    }
    elements.video.play().catch(function () {
      showStatus(elements.pageStatus, "Playback blocked.", true);
    });
  }

  function saveSetting(key, value) {
    applySettings({ ...settings, [key]: value });
    try {
      localStorage.setItem(PLAYER_SETTINGS_KEY, JSON.stringify(settings));
      showStatus(elements.settingsStatus, "Saved.", false);
    } catch (_error) {
      showStatus(elements.settingsStatus, "Saved until this tab closes.", true);
    }
  }

  async function loadSettings() {
    const response = await message("goi.settings.get");
    if (!response.ok || !response.settings) {
      applySettings(settings);
      showStatus(elements.settingsStatus, "Could not load shared settings.", true);
      return;
    }
    let localSettings;
    try {
      localSettings = JSON.parse(localStorage.getItem(PLAYER_SETTINGS_KEY) || "null");
    } catch (_error) {
      localSettings = null;
    }
    applySettings(localSettings || response.settings);
  }

  function invalidateSession(clearSelection) {
    sessionGeneration += 1;
    submittedCueIDs.clear();
    sendingBatch = false;
    hideBatchConfirmation();
    workspaceRuntime.captureRevision += 1;
    if (clearSelection) {
      clearSelectionWorkspace();
    }
  }

  function resetPlaybackTracking() {
    pauseRevealCueIDs = [];
    lastDialogueCueIDs = [];
    previousVideoMs = null;
    playbackDiscontinuity = true;
  }

  async function replaceVideo(file) {
    if (!file) {
      return;
    }
    elements.video.pause();
    invalidateSession(true);
    cancelPlaybackFrame();
    if (videoURL) {
      URL.revokeObjectURL(videoURL);
      videoURL = "";
    }
    elements.video.removeAttribute("src");
    elements.video.load();
    videoReady = false;
    videoDurationMs = null;
    videoFileName = String(file.name || "Local video");
    videoIdentity = [videoFileName, file.size, file.lastModified].join(":");
    const savedState = playerState.get(videoIdentity);
    savedOffsetMs = savedState.offsetMs;
    savedPlaybackSeconds = savedState.playbackSeconds;
    playbackRestored = false;
    lastSavedPlaybackBucket = -1;
    elements.videoName.textContent = videoFileName;
    elements.videoEmpty.hidden = true;
    elements.fullscreen.disabled = false;
    offsetMs = savedOffsetMs;
    subtitleTracks.forEach(function (track) {
      track.offsetMs = savedOffsetMs;
    });
    resetPlaybackTracking();
    updateTimingControls();
    updateCueAvailability();
    renderTranscript();
    updatePlayback(true);
    updateBatchButton();
    showStatus(elements.videoStatus, "Loading video…", false);
    try {
      videoURL = URL.createObjectURL(file);
      elements.video.src = videoURL;
      elements.video.load();
    } catch (_error) {
      showStatus(elements.videoStatus, "Chrome could not open that video file.", true);
    }
  }

  function activeSubtitleTrack() {
    return subtitleTracks.find(function (track) {
      return track.id === activeSubtitleTrackID;
    });
  }

  function updateSubtitleTrackControls() {
    elements.subtitleTrack.replaceChildren();
    subtitleTracks.forEach(function (track) {
      const option = document.createElement("option");
      option.value = String(track.id);
      option.textContent = track.name + " · " + track.cues.length +
        (track.cues.length === 1 ? " line" : " lines");
      option.selected = track.id === activeSubtitleTrackID;
      elements.subtitleTrack.appendChild(option);
    });
    elements.subtitleTrackControls.hidden = subtitleTracks.length === 0;
    elements.subtitleTrackRemove.disabled = subtitleTracks.length === 0;
  }

  function showPartialImport(track) {
    elements.partialImport.hidden = !track || track.skippedCueCount === 0;
    elements.partialImport.textContent = track && track.skippedCueCount
      ? "Partial subtitle import · " + track.skippedCueCount +
        (track.skippedCueCount === 1 ? " malformed cue skipped" : " malformed cues skipped")
      : "";
  }

  function activateSubtitleTrack(trackID, announce) {
    const track = subtitleTracks.find(function (candidate) {
      return candidate.id === Number(trackID);
    });
    if (!track || track.id === activeSubtitleTrackID) {
      updateSubtitleTrackControls();
      return;
    }
    const previous = activeSubtitleTrack();
    if (previous) {
      previous.offsetMs = offsetMs;
    }
    invalidateSession(true);
    activeSubtitleTrackID = track.id;
    subtitleGeneration = track.subtitleGeneration;
    offsetMs = track.offsetMs;
    cues = track.cues;
    cueTimeline = subtitleFileModel.createCueTimeline(cues);
    cueByID = new Map(cues.map(function (cue) { return [cue.id, cue]; }));
    currentCueIDs = new Set();
    renderedLimit = TRANSCRIPT_PAGE_SIZE;
    clearRenderedTranscript();
    resetPlaybackTracking();
    showPartialImport(track);
    updateSubtitleTrackControls();
    updateTimingControls();
    updateCueAvailability();
    renderTranscript();
    updatePlayback(true);
    startCoverage(cues);
    if (announce) {
      showStatus(elements.pageStatus, "Using " + track.name + ".", false);
    }
  }

  function clearSubtitleTrack() {
    invalidateSession(true);
    coverageRevision += 1;
    activeSubtitleTrackID = undefined;
    subtitleGeneration = 0;
    cues = [];
    cueTimeline = subtitleFileModel.createCueTimeline(cues);
    cueByID = new Map();
    currentCueIDs = new Set();
    renderedLimit = TRANSCRIPT_PAGE_SIZE;
    clearRenderedTranscript();
    resetPlaybackTracking();
    coverageState = playerCoverageModel.emptyState();
    showPartialImport(null);
    updateSubtitleTrackControls();
    updateTimingControls();
    updateCoverageUI();
    updateCueAvailability();
    renderTranscript();
    updatePlayback(true);
  }

  async function readSubtitleFiles(files) {
    const selectedFiles = Array.from(files || []);
    if (selectedFiles.length === 0) {
      return;
    }
    const readRevision = ++subtitleReadRevision;
    showStatus(elements.pageStatus, "Reading subtitle tracks…", false);
    const imported = [];
    const errors = [];
    let nextID = nextCueID;
    let nextGeneration = Math.max(
      subtitleGeneration,
      ...subtitleTracks.map(function (track) { return track.subtitleGeneration; })
    );
    for (const file of selectedFiles) {
      try {
        if (file.size > subtitleFileModel.LIMITS.sourceBytes) {
          throw new subtitleFileModel.SubtitleFileError(
            "source_too_large",
            "Subtitle files must be 5 MiB or smaller."
          );
        }
        const bytes = await file.arrayBuffer();
        if (readRevision !== subtitleReadRevision || shuttingDown) {
          return;
        }
        nextGeneration += 1;
        const parsed = subtitleFileModel.parseSubtitleFile(bytes, {
          firstCueID: nextID,
          subtitleGeneration: nextGeneration
        });
        nextID = parsed.nextCueID;
        imported.push({
          id: nextSubtitleTrackID + imported.length,
          name: String(file.name || "Subtitles"),
          subtitleGeneration: nextGeneration,
          cues: parsed.cues,
          skippedCueCount: parsed.skippedCueCount,
          duplicateCueCount: parsed.duplicateCueCount,
          offsetMs: savedOffsetMs
        });
      } catch (error) {
        errors.push(error instanceof subtitleFileModel.SubtitleFileError
          ? error.message
          : "Could not read " + String(file.name || "that subtitle file") + ".");
      }
    }
    if (readRevision !== subtitleReadRevision || shuttingDown) {
      return;
    }
    if (imported.length === 0) {
      showStatus(elements.pageStatus, errors[0] || "Could not read those subtitle files.", true);
      return;
    }
    nextSubtitleTrackID += imported.length;
    nextCueID = nextID;
    subtitleTracks.push(...imported);
    activateSubtitleTrack(imported[0].id, false);
    const duplicateCount = imported.reduce(function (total, track) {
      return total + track.duplicateCueCount;
    }, 0);
    let status = imported.length === 1
      ? imported[0].name + " added."
      : imported.length + " subtitle tracks added.";
    if (duplicateCount) {
      status += " " + duplicateCount + " exact " +
        (duplicateCount === 1 ? "duplicate was" : "duplicates were") + " removed.";
    }
    if (errors.length) {
      status += " " + errors.length + (errors.length === 1 ? " file was" : " files were") + " skipped.";
    }
    showStatus(elements.pageStatus, status, errors.length > 0);
  }

  function updateTimingControls() {
    const enabled = cues.length > 0;
    elements.offsetEarlier.disabled = !enabled;
    elements.offsetInput.disabled = !enabled;
    elements.offsetLater.disabled = !enabled;
    elements.offsetReset.disabled = !enabled || offsetMs === 0;
    elements.offsetInput.value = String(offsetMs);
    const description = subtitleFileModel.describeOffsetMilliseconds(offsetMs);
    elements.offsetDescription.textContent = description;
    elements.offsetInput.setAttribute("aria-valuetext", description);
  }

  function requestOffset(value) {
    const next = subtitleFileModel.clampOffsetMilliseconds(value);
    if (next === offsetMs) {
      updateTimingControls();
      return;
    }
    offsetMs = next;
    const track = activeSubtitleTrack();
    if (track) {
      track.offsetMs = offsetMs;
    }
    if (videoIdentity) {
      saveOffset(offsetMs);
    }
    resetPlaybackTracking();
    invalidateSession(false);
    updateTimingControls();
    updateCueAvailability();
    renderTranscript();
    updatePlayback(true);
  }

  function updateCueAvailability() {
    panelButtons.forEach(function (button) {
      const needsSubtitles = button.dataset.panelTarget === "transcript" || button.dataset.panelTarget === "capture";
      button.hidden = needsSubtitles ? cues.length === 0 : !videoReady && cues.length === 0;
      button.disabled = needsSubtitles && cues.length === 0;
    });
    cues.forEach(function (cue) {
      cue.outsideVideo = videoReady && subtitleFileModel.cueIsOutsideVideo(
        cue,
        videoDurationMs,
        offsetMs
      );
    });
    updateCaptureWorkspace();
    updateBatchButton();
  }

  function startCoverage(targetCues) {
    coverageRevision += 1;
    const revision = coverageRevision;
    coverageState = playerCoverageModel.emptyState();
    coverageState.total = targetCues.length;
    targetCues.forEach(function (cue) {
      cue.classification = "pending";
      cue.unknowns = [];
      cue.words = [];
      cue.coverageBlock = undefined;
    });
    updateCoverageUI();
    renderTranscript();
    renderOverlay();
    let batches;
    try {
      batches = subtitleFileModel.createCoverageBatches(targetCues);
    } catch (_error) {
      failCoverage(targetCues);
      return;
    }
    runCoverageBatches(revision, batches);
  }

  function failCoverage(targetCues) {
    targetCues.forEach(function (cue) {
      if (cue.classification !== "ready") {
        cue.classification = "unavailable";
      }
    });
    coverageState.running = false;
    coverageState.failed = true;
    updateCoverageUI();
    renderTranscript();
    renderOverlay();
    updateCaptureWorkspace();
    updateBatchButton();
  }

  async function runCoverageBatches(revision, batches) {
    coverageState.running = true;
    coverageState.failed = false;
    updateCoverageUI();
    for (let index = 0; index < batches.length; index += 1) {
      if (revision !== coverageRevision || shuttingDown) {
        return;
      }
      const batch = batches[index];
      const response = await message("goi.player.coverage", { blocks: batch });
      if (revision !== coverageRevision || shuttingDown) {
        return;
      }
      if (!response.ok || !playerCoverageModel.validBatchResponse(batch, response.result)) {
        const remainingIDs = new Set(batches.slice(index).flat().map(function (block) {
          return block.id;
        }));
        failCoverage(cues.filter(function (cue) {
          return remainingIDs.has(cue.id);
        }));
        return;
      }
      applyCoverageResult(batch, response.result);
    }
    if (revision !== coverageRevision) {
      return;
    }
    coverageState.running = false;
    coverageState.failed = false;
    updateCoverageUI();
  }

  function applyCoverageResult(batch, result) {
    const blocks = new Map(result.blocks.map(function (block) { return [block.id, block]; }));
    batch.forEach(function (requestBlock) {
      const cue = cueByID.get(requestBlock.id);
      const block = blocks.get(requestBlock.id) || { id: requestBlock.id, tokens: [] };
      if (!cue || cue.subtitleGeneration !== subtitleGeneration) {
        return;
      }
      cue.coverageBlock = block;
      cue.unknowns = subtitleModel.unknownWords(cue.text, block);
      cue.words = subtitleModel.words(cue.text, block);
      cue.classification = "ready";
    });
    coverageState = playerCoverageModel.addResult(coverageState, result);
    updateCoverageUI();
    renderTranscript();
    renderOverlay();
    updateCaptureWorkspace();
    updateBatchButton();
  }

  function retryCoverage() {
    const remaining = cues.filter(function (cue) { return cue.classification !== "ready"; });
    if (!remaining.length || coverageState.running) {
      return;
    }
    coverageRevision += 1;
    remaining.forEach(function (cue) { cue.classification = "pending"; });
    coverageState.failed = false;
    let batches;
    try {
      batches = subtitleFileModel.createCoverageBatches(remaining);
    } catch (_error) {
      failCoverage(remaining);
      return;
    }
    renderTranscript();
    renderOverlay();
    runCoverageBatches(coverageRevision, batches);
  }

  function uniqueUnknownCount() {
    const unknown = new Set();
    cues.forEach(function (cue) {
      if (cue.classification !== "ready") {
        return;
      }
      cue.unknowns.forEach(function (word) {
        unknown.add(word.expression || word.surface);
      });
    });
    return unknown.size;
  }

  function updateCoverageUI() {
    elements.coverageProgress.max = Math.max(1, coverageState.total);
    elements.coverageProgress.value = coverageState.completed;
    elements.coverageRetry.hidden = !coverageState.failed || coverageState.running;
    elements.coverageRetry.disabled = coverageState.running;
    elements.coverageSummary.textContent = playerCoverageModel.summaryText(
      coverageState,
      uniqueUnknownCount()
    );
  }

  function effectiveTimes(cue) {
    return subtitleFileModel.effectiveCueTimes(cue, offsetMs);
  }

  function currentTimeMs() {
    return Number.isFinite(elements.video.currentTime)
      ? Math.max(0, elements.video.currentTime * 1000)
      : 0;
  }

  function captureEligible(cue) {
    return Boolean(videoReady && cue && !cue.outsideVideo && effectiveTimes(cue));
  }

  function cueDisplayTime(cue) {
    const effective = effectiveTimes(cue);
    return subtitleModel.formatTimestamp(effective ? Math.max(0, effective.startMs) : 0);
  }

  function selectedCue() {
    return cueByID.get(selectedCueID) || null;
  }

  function clearSelectionWorkspace() {
    selectedCueID = undefined;
    workspaceRuntime.initialTarget = "";
    workspaceRuntime.draftDirty = false;
    workspaceRuntime.suggestedEntrySequence = undefined;
    elements.captureTarget.value = "";
    clearTimeout(lookupRuntime.timer);
    clearLookup();
    elements.captureSentence.value = "";
    elements.selectedTime.textContent = "No line selected";
    elements.captureTarget.disabled = true;
    elements.captureSentence.disabled = true;
    elements.captureSubmit.disabled = true;
    elements.jumpSelected.disabled = true;
    showStatus(elements.captureStatus, "", false);
    updateSelectedLineStyles();
  }

  function updateCaptureWorkspace() {
    const cue = selectedCue();
    if (!cue) {
      elements.captureSubmit.disabled = true;
      elements.jumpSelected.disabled = true;
      return;
    }
    const eligible = captureEligible(cue);
    elements.captureSentence.disabled = false;
    elements.captureSentence.value = cue.text;
    elements.captureTarget.disabled = !eligible;
    elements.jumpSelected.disabled = !eligible;
    elements.selectedTime.textContent = cue.outsideVideo
      ? "Outside video"
      : "At " + cueDisplayTime(cue);
    const target = subtitleModel.captureTarget(cue, elements.captureTarget.value);
    elements.captureSubmit.disabled = workspaceRuntime.captureBusy || !connectionReady || !eligible || !target;
  }

  function selectCue(cue, seek, surface, suggestedEntrySequence) {
    if (!cue) {
      return;
    }
    selectedCueID = cue.id;
    workspaceRuntime.captureRevision += 1;
    elements.captureSentence.disabled = false;
    elements.captureSentence.value = cue.text;
    elements.captureTarget.value = surface || cue.unknowns[0]?.surface || "";
    workspaceRuntime.initialTarget = elements.captureTarget.value;
    workspaceRuntime.draftDirty = false;
    workspaceRuntime.suggestedEntrySequence = Number.isSafeInteger(suggestedEntrySequence)
      ? suggestedEntrySequence
      : undefined;
    updateCaptureWorkspace();
    updateSelectedLineStyles();
    const target = subtitleModel.captureTarget(cue, elements.captureTarget.value);
    if (target) {
      lookupWord(target.expression);
    } else {
      clearLookup();
    }
    if (seek) {
      seekCue(cue);
    }
    if (settings.pauseBehavior === "on_selection" && !elements.video.paused) {
      elements.video.pause();
    }
    showPanel("capture");
    if (!elements.captureTarget.disabled) {
      elements.captureTarget.focus();
    }
  }

  function seekCue(cue) {
    if (!captureEligible(cue)) {
      return;
    }
    const effective = effectiveTimes(cue);
    const maximum = Math.max(0, (videoDurationMs || 0) - 1);
    elements.video.currentTime = Math.min(maximum, Math.max(0, effective.startMs)) / 1000;
  }

  function selectWord(cue, word, suggestedEntrySequence) {
    selectCue(cue, false, word.surface, suggestedEntrySequence);
    elements.captureTarget.focus();
  }

  function closeWordLookup(resumePlayback = true) {
    cancelWordLookupHover();
    cancelWordLookupClose();
    lookupRuntime.wordRevision += 1;
    lookupRuntime.selection = undefined;
    lookupRuntime.suggestedEntrySequence = undefined;
    lookupRuntime.pinned = false;
    elements.wordLookup.hidden = true;
    elements.wordLookupContent.textContent = "";
    if (resumePlayback) {
      resumeAfterSubtitleHover();
    }
  }

  function positionWordLookup(anchor) {
    const stage = elements.videoStage.getBoundingClientRect();
    const target = anchor.getBoundingClientRect();
    const width = elements.wordLookup.offsetWidth || 360;
    const height = elements.wordLookup.offsetHeight || 260;
    const center = target.left - stage.left + target.width / 2;
    const left = Math.min(stage.width - width - 12, Math.max(12, center - width / 2));
    let top = target.top - stage.top - height - 10;
    if (top < 12) {
      top = target.bottom - stage.top + 10;
    }
    elements.wordLookup.style.left = Math.max(12, left) + "px";
    elements.wordLookup.style.top = Math.max(12, Math.min(stage.height - height - 12, top)) + "px";
  }

  async function showWordLookup(cue, word, anchor, pinned) {
    const expression = word.expression || word.surface;
    cancelWordLookupHover();
    cancelWordLookupClose();
    lookupRuntime.pinned = Boolean(pinned);
    lockSubtitleHoverPause();
    lookupRuntime.selection = { cue, word };
    lookupRuntime.suggestedEntrySequence = undefined;
    const revision = ++lookupRuntime.wordRevision;
    elements.wordLookupTitle.textContent = word.surface;
    elements.wordLookupContent.textContent = "Looking up “" + expression + "”…";
    elements.wordLookupKnown.hidden = word.status !== "unknown";
    elements.wordLookupKnown.disabled = false;
    elements.wordLookupMine.disabled = !captureEligible(cue);
    elements.wordLookup.hidden = false;
    positionWordLookup(anchor);
    const response = await dictionaryClient.lookup(chrome.runtime, expression);
    if (revision !== lookupRuntime.wordRevision || !lookupRuntime.selection) {
      return;
    }
    dictionaryRenderer.render(elements.wordLookupContent, subtitleModel.dictionaryView(response), {
      selectedEntrySequence: lookupRuntime.suggestedEntrySequence,
      onSelect: function (candidate) {
        lookupRuntime.suggestedEntrySequence = candidate.entrySequence;
      }
    });
    positionWordLookup(anchor);
  }

  function scheduleWordLookup(cue, word, anchor) {
    cancelWordLookupHover();
    cancelWordLookupClose();
    if (!settings.hoverLookupEnabled || lookupRuntime.pinned) {
      return;
    }
    lookupRuntime.hoverTimer = setTimeout(function () {
      lookupRuntime.hoverTimer = undefined;
      if (anchor && anchor.isConnected === false && !anchor.parentElement) {
        return;
      }
      showWordLookup(cue, word, anchor, false);
    }, HOVER_LOOKUP_DELAY_MS);
  }

  function cancelWordLookupHover() {
    clearTimeout(lookupRuntime.hoverTimer);
    lookupRuntime.hoverTimer = undefined;
  }

  function cancelWordLookupClose() {
    clearTimeout(lookupRuntime.closeTimer);
    lookupRuntime.closeTimer = undefined;
  }

  function scheduleWordLookupClose() {
    cancelWordLookupHover();
    cancelWordLookupClose();
    if (lookupRuntime.pinned) {
      return;
    }
    lookupRuntime.closeTimer = setTimeout(function () {
      lookupRuntime.closeTimer = undefined;
      if (!lookupRuntime.pinned) {
        closeWordLookup(true);
      }
    }, 180);
  }

  function clearLookup() {
    lookupRuntime.revision += 1;
    elements.dictionaryLookup.hidden = true;
    elements.dictionaryLookup.textContent = "";
  }

  async function lookupWord(expression) {
    const query = String(expression || "").trim();
    if (!query) {
      clearLookup();
      return;
    }
    const revision = ++lookupRuntime.revision;
    elements.dictionaryLookup.hidden = false;
    elements.dictionaryLookup.textContent = "Looking up “" + query + "”…";
    const response = await dictionaryClient.lookup(chrome.runtime, query);
    if (revision !== lookupRuntime.revision) {
      return;
    }
    dictionaryRenderer.render(elements.dictionaryLookup, subtitleModel.dictionaryView(response), {
      selectedEntrySequence: workspaceRuntime.suggestedEntrySequence,
      onSelect: function (candidate) {
        workspaceRuntime.suggestedEntrySequence = candidate.entrySequence;
      }
    });
  }

  function visibleCues() {
    const visible = playerTranscriptModel.visibleCues(cues, elements.unknownOnly.checked);
    const query = elements.transcriptSearch.value.trim().toLocaleLowerCase();
    return query
      ? visible.filter(function (cue) { return cue.text.toLocaleLowerCase().includes(query); })
      : visible;
  }

  function updateJumpCurrent() {
    elements.jumpCurrent.disabled = !playerTranscriptModel.hasVisibleCurrentCue(
      currentCueIDs,
      cueByID,
      elements.unknownOnly.checked
    );
  }

  function createTranscriptLine(cue) {
    const item = document.createElement("li");
    item.className = "transcript-line";
    const time = document.createElement("button");
    time.type = "button";
    time.className = "line-time";
    item.appendChild(time);
    const body = document.createElement("div");
    body.className = "line-body";
    const text = document.createElement("div");
    text.className = "line-text";
    text.tabIndex = 0;
    text.setAttribute("role", "button");
    body.appendChild(text);
    const meta = document.createElement("div");
    meta.className = "line-meta";
    body.appendChild(meta);
    const translation = document.createElement("p");
    translation.className = "line-translation";
    translation.hidden = true;
    body.appendChild(translation);
    item.appendChild(body);
    const entry = { item, time, text, meta, translation, cue };
    time.addEventListener("click", function () {
      selectCue(entry.cue, true);
    });
    text.addEventListener("click", function () {
      const selection = globalThis.getSelection?.();
      if (selection && !selection.isCollapsed && selection.toString().trim()) {
        return;
      }
      selectCue(entry.cue, true);
    });
    text.addEventListener("keydown", function (event) {
      if (event.key !== "Enter" && event.key !== " ") {
        return;
      }
      event.preventDefault();
      selectCue(entry.cue, true);
    });
    return entry;
  }

  function updateTranscriptLine(entry, cue) {
    entry.cue = cue;
    const renderKey = [
      settings.furiganaEnabled,
      cueDisplayTime(cue),
      cue.text,
      cue.classification,
      cue.outsideVideo,
      captureEligible(cue),
      (Array.isArray(cue.words) ? cue.words : cue.unknowns).map(function (word) {
        return [word.start, word.end, word.surface, word.expression, word.reading, word.status].join(":");
      }).join("|")
    ].join("\u0000");
    if (entry.renderKey === renderKey) {
      return;
    }
    entry.renderKey = renderKey;
    entry.item.dataset.cueId = String(cue.id);
    entry.time.textContent = cueDisplayTime(cue);
    entry.time.disabled = !captureEligible(cue);
    entry.time.setAttribute(
      "aria-label",
      cue.outsideVideo ? "Subtitle outside video" : "Jump to " + entry.time.textContent
    );
    entry.text.replaceChildren();
    appendCueText(entry.text, cue, false);
    entry.text.dataset.translationText = cue.text;
    entry.text.tabIndex = captureEligible(cue) ? 0 : -1;
    entry.text.setAttribute("aria-disabled", String(!captureEligible(cue)));
    if (entry.translation.dataset.sourceText && entry.translation.dataset.sourceText !== cue.text) {
      entry.translation.hidden = true;
      entry.translation.textContent = "";
      delete entry.translation.dataset.sourceText;
    }
    entry.meta.replaceChildren();
    const classification = document.createElement("span");
    classification.textContent = playerTranscriptModel.classificationText(cue);
    entry.meta.appendChild(classification);
    if (cue.outsideVideo) {
      const outside = document.createElement("span");
      outside.className = "outside-label";
      outside.textContent = "Outside video";
      entry.meta.appendChild(outside);
    }
    const seen = new Set();
    cue.unknowns.forEach(function (word) {
      const key = word.expression || word.surface;
      if (!key || seen.has(key)) {
        return;
      }
      seen.add(key);
      const button = document.createElement("button");
      button.type = "button";
      button.className = "unknown-target";
      button.textContent = word.surface;
      button.disabled = !captureEligible(cue);
      button.title = word.expression === word.surface
        ? "Use as the mining target"
        : "Use " + word.expression + " as the dictionary form";
      button.addEventListener("click", function () {
        selectWord(cue, word);
      });
      entry.meta.appendChild(button);
    });
    const translate = document.createElement("button");
    translate.type = "button";
    translate.className = "line-translate";
    translate.textContent = "Translate";
    translate.dataset.defaultLabel = "Translate";
    translate.addEventListener("click", function () {
      translationModel.translateInto(translator, cue.text, entry.translation, translate);
    });
    entry.meta.appendChild(translate);
  }

  function clearRenderedTranscript() {
    renderedLines.clear();
    elements.subtitleLines.replaceChildren();
  }

  function renderTranscript() {
    const visible = visibleCues();
    const shown = visible.slice(0, renderedLimit);
    const shownIDs = new Set(shown.map(function (cue) { return cue.id; }));
    renderedLines.forEach(function (entry, cueID) {
      if (!shownIDs.has(cueID)) {
        entry.item.remove();
        renderedLines.delete(cueID);
      }
    });
    shown.forEach(function (cue) {
      let entry = renderedLines.get(cue.id);
      if (!entry) {
        entry = createTranscriptLine(cue);
        renderedLines.set(cue.id, entry);
      }
      updateTranscriptLine(entry, cue);
      elements.subtitleLines.appendChild(entry.item);
    });
    updateSelectedLineStyles();
    elements.transcriptEmpty.hidden = shown.length > 0;
    const coveragePending = cues.some(function (cue) { return cue.classification === "pending"; });
    elements.coverageReadiness.hidden = !elements.unknownOnly.checked || !coveragePending;
    if (!cues.length) {
      elements.transcriptEmptyTitle.textContent = "No subtitles yet";
      elements.transcriptEmptyDetail.textContent = "Choose an SRT, VTT, ASS, or SSA file.";
      elements.lineCount.textContent = "No subtitle file";
    } else if (!visible.length) {
      const query = elements.transcriptSearch.value.trim();
      elements.transcriptEmptyTitle.textContent = query ? "No matching subtitles" : "No unknown dialogue";
      elements.transcriptEmptyDetail.textContent = query ? "Try another search." : "Every checked line is known.";
      elements.lineCount.textContent = cues.length + " subtitles · none match this filter";
    } else {
      elements.lineCount.textContent = elements.unknownOnly.checked
        ? visible.length + " of " + cues.length + " subtitles shown"
        : cues.length + (cues.length === 1 ? " subtitle" : " subtitles");
    }
    elements.loadMore.hidden = shown.length >= visible.length;
    elements.loadMore.textContent = elements.loadMore.hidden
      ? "Show more lines"
      : "Show " + Math.min(TRANSCRIPT_PAGE_SIZE, visible.length - shown.length) + " more lines";
    updateJumpCurrent();
  }

  function updateSelectedLineStyles() {
    renderedLines.forEach(function (entry, cueID) {
      const selected = cueID === selectedCueID;
      const current = currentCueIDs.has(cueID);
      entry.item.classList.toggle("selected", selected);
      entry.item.classList.toggle("current", current);
      if (current) {
        entry.item.setAttribute("aria-current", "true");
      } else {
        entry.item.removeAttribute("aria-current");
      }
    });
  }

  function overlayCueIDs() {
    return playerTranscriptModel.overlayCueIDs({
      cueByID,
      currentCueIDs,
      displayMode: settings.displayMode,
      pauseRevealCueIDs,
      paused: elements.video.paused
    });
  }

  function appendCueText(container, cue, interactive = true) {
    let offset = 0;
    const classified = Array.isArray(cue.words) ? cue.words : cue.unknowns;
    const words = subtitleModel.lookupWords(cue.text, classified);
    words.slice().sort(function (left, right) {
      return left.start - right.start || left.end - right.end;
    }).forEach(function (word) {
      if (!Number.isSafeInteger(word.start) || !Number.isSafeInteger(word.end) ||
          word.start < offset || word.end <= word.start || word.end > cue.text.length) {
        return;
      }
      if (word.start > offset) {
        container.appendChild(document.createTextNode(cue.text.slice(offset, word.start)));
      }
      const surface = cue.text.slice(word.start, word.end);
      const wordNode = document.createElement(interactive ? "button" : "span");
      if (interactive) {
        wordNode.type = "button";
      }
      wordNode.className = "subtitle-word subtitle-word--" + (word.status || "unknown");
      subtitleView.renderWord(wordNode, surface, word, {
        furiganaEnabled: settings.furiganaEnabled,
      });
      if (interactive) {
        wordNode.setAttribute("aria-label", "Look up " + surface);
        wordNode.addEventListener("pointerenter", function () {
          dictionaryClient.lookup(chrome.runtime, word.expression || word.surface).catch(ignoreDictionaryPrefetchFailure);
          scheduleWordLookup(cue, word, wordNode);
        });
        wordNode.addEventListener("pointerleave", scheduleWordLookupClose);
        wordNode.addEventListener("focus", function () {
          dictionaryClient.lookup(chrome.runtime, word.expression || word.surface).catch(ignoreDictionaryPrefetchFailure);
        });
        wordNode.addEventListener("click", function () {
          lockSubtitleHoverPause();
          showWordLookup(cue, word, wordNode, true);
        });
      }
      container.appendChild(wordNode);
      offset = word.end;
    });
    if (offset < cue.text.length) {
      container.appendChild(document.createTextNode(cue.text.slice(offset)));
    }
  }

  function renderOverlay() {
    const ids = overlayCueIDs();
    const renderKey = ids.map(function (cueID) {
      const cue = cueByID.get(cueID);
      return cue ? settings.furiganaEnabled + ":" + cue.id + ":" + cue.classification + ":" + cue.text + ":" +
        (Array.isArray(cue.words) ? cue.words : cue.unknowns).map(function (word) {
          return [word.start, word.end, word.surface, word.expression, word.reading, word.status].join(",");
        }).join("|") : "";
    }).join("\u0000");
    if (renderKey === overlayRenderKey) {
      return;
    }
    overlayRenderKey = renderKey;
    const fragment = document.createDocumentFragment();
    ids.forEach(function (cueID) {
      const cue = cueByID.get(cueID);
      if (!cue) {
        return;
      }
      const line = document.createElement("div");
      line.className = "subtitle-cue";
      line.lang = "ja";
      line.addEventListener("pointerenter", pauseForSubtitleHover);
      line.addEventListener("pointerleave", function (event) {
        if (elements.subtitleOverlay.contains(event.relatedTarget) || lookupRuntime.hoverPauseLocked) {
          return;
        }
        resumeAfterSubtitleHover();
      });
      appendCueText(line, cue);
      const translate = document.createElement("button");
      translate.type = "button";
      translate.className = "subtitle-translate";
      translate.textContent = "EN";
      translate.dataset.defaultLabel = "EN";
      translate.title = "Translate this subtitle";
      translate.setAttribute("aria-label", "Translate this subtitle");
      const translation = document.createElement("span");
      translation.className = "subtitle-translation";
      translation.hidden = true;
      translate.addEventListener("click", function (event) {
        event.stopPropagation();
        lockSubtitleHoverPause();
        translationModel.translateInto(translator, cue.text, translation, translate);
      });
      line.appendChild(translate);
      line.appendChild(translation);
      fragment.appendChild(line);
    });
    elements.subtitleOverlay.replaceChildren(fragment);
  }

  function updatePlayback(forceDiscontinuity) {
    if (!videoReady || !cues.length) {
      currentCueIDs = new Set();
      updateJumpCurrent();
      renderOverlay();
      updateSelectedLineStyles();
      return;
    }
    const currentMs = currentTimeMs();
    const active = subtitleFileModel.activeTimelineCuesAt(cueTimeline, currentMs, offsetMs).filter(function (cue) {
      return !cue.outsideVideo;
    });
    const discontinuity = Boolean(forceDiscontinuity || playbackDiscontinuity);
    const nextCueIDs = new Set(active.map(function (cue) { return cue.id; }));
    const activeChanged = nextCueIDs.size !== currentCueIDs.size ||
      Array.from(nextCueIDs).some(function (cueID) { return !currentCueIDs.has(cueID); });
    currentCueIDs = nextCueIDs;
    if (active.length) {
      lastDialogueCueIDs = active.map(function (cue) { return cue.id; });
    }
    previousVideoMs = currentMs;
    playbackDiscontinuity = false;
    if (activeChanged) {
      renderOverlay();
      updateSelectedLineStyles();
      updateJumpCurrent();
    }
  }

  function schedulePlaybackFrame() {
    cancelPlaybackFrame();
    if (elements.video.paused || elements.video.ended || !videoReady) {
      return;
    }
    if (typeof elements.video.requestVideoFrameCallback === "function") {
      frameCallbackID = elements.video.requestVideoFrameCallback(function () {
        frameCallbackID = undefined;
        updatePlayback(false);
        schedulePlaybackFrame();
      });
      return;
    }
    animationFrameID = requestAnimationFrame(function () {
      animationFrameID = undefined;
      updatePlayback(false);
      schedulePlaybackFrame();
    });
  }

  function cancelPlaybackFrame() {
    if (frameCallbackID !== undefined && typeof elements.video.cancelVideoFrameCallback === "function") {
      elements.video.cancelVideoFrameCallback(frameCallbackID);
    }
    if (animationFrameID !== undefined) {
      cancelAnimationFrame(animationFrameID);
    }
    frameCallbackID = undefined;
    animationFrameID = undefined;
  }

  function oneTargetCues() {
    if (!videoReady) {
      return [];
    }
    return subtitleModel.oneTargetLines(cues, submittedCueIDs).filter(captureEligible);
  }

  function updateBatchButton() {
    const count = oneTargetCues().length;
    const sendCount = Math.min(count, MAX_BATCH_CAPTURES);
    elements.batchOneTarget.disabled = sendingBatch || workspaceRuntime.captureBusy || !connectionReady || sendCount === 0;
    elements.batchOneTarget.textContent = sendCount
      ? "Send " + sendCount + " " + (sendCount === 1 ? "line" : "lines") + " to mining"
      : videoReady
        ? "No matching lines to send"
        : "Choose a video to find lines";
  }

  function hideBatchConfirmation() {
    elements.batchConfirmation.hidden = true;
  }

  function showBatchConfirmation() {
    const count = Math.min(oneTargetCues().length, MAX_BATCH_CAPTURES);
    if (!count) {
      return;
    }
    elements.batchConfirmationText.textContent = "Send " + count +
      (count === 1 ? " line" : " lines") +
      " to mining? Lines you have not played will be text-only.";
    elements.batchConfirmation.hidden = false;
    elements.batchConfirm.focus();
  }

  function captureInput(cue, target) {
    const effective = effectiveTimes(cue);
    return {
      rawText: target.surface,
      expression: target.expression,
      contextText: cue.text,
      sourceTitle: videoFileName,
      sourcePositionMs: Math.max(0, Math.round(effective.startMs)),
      suggestedEntrySequence: workspaceRuntime.suggestedEntrySequence
    };
  }

  async function captureCue(cue, target, options) {
    if (workspaceRuntime.captureBusy || (sendingBatch && !(options && options.batch))) {
      return { ok: false };
    }
    workspaceRuntime.captureBusy = true;
    const generation = sessionGeneration;
    updateCaptureWorkspace();
    updateBatchButton();
    try {
      const result = await performCapture(cue, target, options);
      if (result.ok && generation === sessionGeneration) {
        submittedCueIDs.add(cue.id);
        if (!(options && options.batch) && cue.id === selectedCueID) {
          workspaceRuntime.initialTarget = elements.captureTarget.value;
          workspaceRuntime.draftDirty = false;
        }
      }
      return result;
    } finally {
      workspaceRuntime.captureBusy = false;
      updateCaptureWorkspace();
      updateBatchButton();
    }
  }

  async function performCapture(cue, target, options) {
    const captureOptions = options || {};
    if (!captureEligible(cue) || !target || !connectionReady) {
      return { ok: false };
    }
    const generation = sessionGeneration;
    const workspaceRevision = workspaceRuntime.captureRevision;
    if (!captureOptions.quiet) {
      showStatus(elements.captureStatus, "Sending to mining…", false);
      elements.captureSubmit.disabled = true;
    }
    const response = await message("goi.player.capture", {
      sessionID: currentSessionID(),
      capture: captureInput(cue, target)
    });
    if (!response.ok) {
      if (!captureOptions.quiet && workspaceRevision === workspaceRuntime.captureRevision) {
        showStatus(
          elements.captureStatus,
          errorText(response.errorCode, "Could not save this word."),
          true
        );
        updateCaptureWorkspace();
      }
      return { ok: false };
    }
    if (generation !== sessionGeneration) {
      showStatus(elements.pageStatus, "A capture from the previous player session was saved.", false);
      return { ok: true, queued: Boolean(response.queued) };
    }
    if (settings.pauseBehavior === "after_capture" && !elements.video.paused) {
      elements.video.pause();
    }
    if (!captureOptions.quiet && workspaceRevision === workspaceRuntime.captureRevision) {
      showStatus(
        elements.captureStatus,
        response.queued ? "Queued. Goi will retry when the server is reachable." : "Sent to mining.",
        false
      );
      updateCaptureWorkspace();
    }
    return { ok: true, queued: Boolean(response.queued) };
  }

  async function sendOneTargetBatch() {
    const lines = oneTargetCues().slice(0, MAX_BATCH_CAPTURES);
    if (!lines.length || sendingBatch) {
      return;
    }
    sendingBatch = true;
    const generation = sessionGeneration;
    const generationIsCurrent = function () { return generation === sessionGeneration; };
    hideBatchConfirmation();
    elements.captureStatus.tabIndex = -1;
    elements.captureStatus.focus();
    updateBatchButton();
    let sent = 0;
    let queued = 0;
    let failed = 0;
    for (const cue of lines) {
      if (!generationIsCurrent() || shuttingDown) {
        return;
      }
      if (!captureEligible(cue) || cue.subtitleGeneration !== subtitleGeneration) {
        failed += 1;
        continue;
      }
      const word = cue.unknowns[0];
      const target = subtitleModel.captureTarget(cue, word?.surface || word?.expression || "");
      const result = await captureCue(cue, target, { quiet: true, batch: true });
      if (!generationIsCurrent()) {
        return;
      }
      if (!result.ok) {
        failed += 1;
        continue;
      }
      if (result.queued) {
        queued += 1;
      } else {
        sent += 1;
      }
    }
    if (!generationIsCurrent()) {
      return;
    }
    sendingBatch = false;
    updateBatchButton();
    showStatus(
      elements.captureStatus,
      subtitleModel.batchSummary(sent, queued, failed),
      failed > 0
    );
    elements.batchOneTarget.focus();
  }

  function handleCaptureHotkey() {
    const active = Array.from(currentCueIDs).map(function (cueID) {
      return cueByID.get(cueID);
    }).filter(Boolean).sort(function (left, right) {
      return left.startMs - right.startMs || left.sourceOrder - right.sourceOrder;
    });
    let cue = selectedCue();
    let target = cue && subtitleModel.captureTarget(cue, elements.captureTarget.value);
    if (target && captureEligible(cue)) {
      captureCue(cue, target);
      return;
    }
    cue = active[0];
    if (cue) {
      const expressions = new Set(cue.unknowns.map(function (word) {
        return word.expression || word.surface;
      }).filter(Boolean));
      if (expressions.size === 1) {
        const word = cue.unknowns.find(function (candidate) {
          return (candidate.expression || candidate.surface) === expressions.values().next().value;
        });
        selectCue(cue, false, word.surface);
        target = subtitleModel.captureTarget(cue, word.surface);
        if (target && captureEligible(cue)) {
          captureCue(cue, target);
          return;
        }
      }
      selectCue(cue, false);
    }
    showStatus(elements.captureStatus, "Choose a target word, then press Alt+Shift+G again.", false);
    elements.captureTarget.focus();
  }

  async function toggleFullscreen() {
    try {
      if (document.fullscreenElement === elements.videoStage) {
        await document.exitFullscreen();
      } else {
        await elements.videoStage.requestFullscreen();
      }
    } catch (_error) {
      showStatus(elements.videoStatus, "Full screen is unavailable in this browser.", true);
    }
  }

  function jumpToCurrentLine() {
    const cueID = currentCueIDs.values().next().value;
    if (!cueID) {
      return;
    }
    const visible = visibleCues();
    const index = visible.findIndex(function (cue) { return cue.id === cueID; });
    if (index >= renderedLimit) {
      renderedLimit = Math.ceil((index + 1) / TRANSCRIPT_PAGE_SIZE) * TRANSCRIPT_PAGE_SIZE;
      renderTranscript();
    }
    renderedLines.get(cueID)?.item.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }

  function handleVideoMetadata() {
    videoReady = Number.isFinite(elements.video.duration) && elements.video.duration > 0;
    videoDurationMs = videoReady ? Math.round(elements.video.duration * 1000) : null;
    if (videoReady && !playbackRestored) {
      playbackRestored = true;
      const endMargin = Math.min(10, Math.max(0.25, elements.video.duration * 0.02));
      if (savedPlaybackSeconds > 0 && savedPlaybackSeconds < elements.video.duration - endMargin) {
        elements.video.currentTime = savedPlaybackSeconds;
        lastSavedPlaybackBucket = Math.floor(savedPlaybackSeconds / 5);
      }
    }
    elements.videoEmpty.hidden = true;
    showStatus(
      elements.videoStatus,
      videoReady ? "Video loaded." : "Chrome could not determine this video’s duration.",
      !videoReady
    );
    updateCueAvailability();
    renderTranscript();
    updatePlayback(true);
  }

  function handleVideoError() {
    videoReady = false;
    videoDurationMs = null;
    cancelPlaybackFrame();
    showStatus(
      elements.videoStatus,
      "Chrome cannot play this file or codec. Your subtitles remain available.",
      true
    );
    updateCueAvailability();
    renderTranscript();
  }

  function bindSettings() {
    PRESENTATION_KEYS.forEach(function (key) {
      const field = settingField(key);
      const controls = Array.isArray(field) ? field : [field];
      controls.forEach(function (control) {
        if (control.type === "range") {
          control.addEventListener("input", function () {
            const preview = settingsModel.applyPatch(settings, { [key]: fieldValue(field) });
            if (key === "fontSizePx") {
              elements.fontSizeValue.value = preview.fontSizePx + " px";
              elements.fontSizeValue.textContent = elements.fontSizeValue.value;
              elements.subtitleOverlay.style.setProperty("--subtitle-font-size", preview.fontSizePx + "px");
            } else if (key === "verticalPercent") {
              elements.verticalPercentValue.value = settingsModel.verticalPositionLabel(preview.verticalPercent);
              elements.verticalPercentValue.textContent = elements.verticalPercentValue.value;
              elements.subtitleOverlay.style.setProperty("--subtitle-top", preview.verticalPercent + "%");
              elements.subtitleOverlay.classList.toggle("dock-above", preview.verticalPercent >= 65);
            } else if (key === "backgroundOpacity") {
              elements.backgroundOpacityValue.value = Math.round(preview.backgroundOpacity * 100) + "%";
              elements.backgroundOpacityValue.textContent = elements.backgroundOpacityValue.value;
              elements.subtitleOverlay.style.setProperty(
                "--subtitle-background",
                "rgb(0 0 0 / " + preview.backgroundOpacity + ")"
              );
            }
          });
        }
        control.addEventListener("change", function () {
          if (control.type === "radio" && !control.checked) {
            return;
          }
          saveSetting(key, fieldValue(field));
        });
      });
    });
  }

  function bindPanelEvents() {
    panelButtons.forEach(function (button) {
      button.addEventListener("click", function () {
        togglePanel(button.dataset.panelTarget);
      });
    });
    elements.panelClose.addEventListener("click", function () {
      closePanel(true);
    });
    elements.wordLookupClose.addEventListener("click", function () {
      closeWordLookup(true);
    });
    elements.wordLookupMine.addEventListener("click", function () {
      const selection = lookupRuntime.selection;
      if (!selection) {
        return;
      }
      const suggestedEntrySequence = lookupRuntime.suggestedEntrySequence;
      closeWordLookup(false);
      selectWord(selection.cue, selection.word, suggestedEntrySequence);
    });
    elements.wordLookupKnown.addEventListener("click", async function () {
      const selection = lookupRuntime.selection;
      if (!selection) {
        return;
      }
      elements.wordLookupKnown.disabled = true;
      const response = await message("goi.vocabulary.known", {
        expression: selection.word.expression || selection.word.surface
      });
      if (!response.ok || !response.result) {
        elements.wordLookupKnown.disabled = false;
        showStatus(elements.videoStatus, errorText(response.errorCode, "Could not mark this word as known."), true);
        return;
      }
      if (response.result.state === "in_lessons") {
        elements.wordLookupKnown.disabled = false;
        showStatus(elements.videoStatus, "This word is already waiting in lessons.", false);
        return;
      }
      closeWordLookup(true);
      showStatus(
        elements.videoStatus,
        response.result.state === "already_known" ? "Already marked as known." : "Marked as known.",
        false
      );
      startCoverage(cues);
    });
    elements.wordLookup.addEventListener("pointerenter", cancelWordLookupClose);
    elements.wordLookup.addEventListener("pointerleave", scheduleWordLookupClose);
    document.addEventListener("pointerdown", function (event) {
      if (!elements.wordLookup.hidden && elements.wordLookup.contains(event.target)) {
        return;
      }
      if (!elements.wordLookup.hidden && !elements.wordLookup.contains(event.target) &&
          !elements.subtitleOverlay.contains(event.target)) {
        closeWordLookup(true);
        return;
      }
      if (!lookupRuntime.hoverPauseLocked || elements.subtitleOverlay.contains(event.target)) {
        return;
      }
      resumeAfterSubtitleHover();
    });
    document.addEventListener("keydown", function (event) {
      if (event.key === "Escape" && !elements.workspacePanel.hidden) {
        closePanel(false);
      }
    });
  }

  function removeActiveSubtitleTrack() {
    const activeIndex = subtitleTracks.findIndex(function (track) {
      return track.id === activeSubtitleTrackID;
    });
    if (activeIndex < 0) {
      return;
    }
    const removed = subtitleTracks[activeIndex];
    subtitleTracks.splice(activeIndex, 1);
    activeSubtitleTrackID = undefined;
    const replacement = subtitleTracks[Math.min(activeIndex, subtitleTracks.length - 1)];
    if (replacement) {
      activateSubtitleTrack(replacement.id, false);
    } else {
      clearSubtitleTrack();
    }
    showStatus(elements.pageStatus, removed.name + " removed.", false);
  }

  function bindFileEvents() {
    elements.videoFile.addEventListener("change", function () {
      const file = elements.videoFile.files && elements.videoFile.files[0];
      elements.videoFile.value = "";
      replaceVideo(file);
    });
    elements.emptyOpenVideo.addEventListener("click", function () {
      elements.videoFile.click();
    });
    elements.subtitleFile.addEventListener("change", function () {
      const files = Array.from(elements.subtitleFile.files || []);
      elements.subtitleFile.value = "";
      readSubtitleFiles(files);
    });
    elements.subtitleTrack.addEventListener("change", function () {
      activateSubtitleTrack(elements.subtitleTrack.value, true);
    });
    elements.subtitleTrackRemove.addEventListener("click", removeActiveSubtitleTrack);
    ["dragenter", "dragover"].forEach(function (type) {
      elements.videoStage.addEventListener(type, function (event) {
        event.preventDefault();
        elements.videoStage.classList.add("drag-active");
      });
    });
    ["dragleave", "drop"].forEach(function (type) {
      elements.videoStage.addEventListener(type, function (event) {
        event.preventDefault();
        elements.videoStage.classList.remove("drag-active");
      });
    });
    elements.videoStage.addEventListener("drop", function (event) {
      const subtitleFiles = [];
      Array.from(event.dataTransfer?.files || []).forEach(function (file) {
        const name = String(file.name || "").toLocaleLowerCase();
        if (file.type.startsWith("video/")) {
          replaceVideo(file);
        } else if (/\.(?:srt|vtt|ass|ssa)$/u.test(name)) {
          subtitleFiles.push(file);
        }
      });
      readSubtitleFiles(subtitleFiles);
    });
  }

  function bindTimingEvents() {
    elements.offsetEarlier.addEventListener("click", function () { requestOffset(offsetMs - 250); });
    elements.offsetLater.addEventListener("click", function () { requestOffset(offsetMs + 250); });
    elements.offsetReset.addEventListener("click", function () { requestOffset(0); });
    elements.offsetInput.addEventListener("change", function () {
      requestOffset(elements.offsetInput.value);
    });
  }

  function useSentenceSelection() {
    const start = elements.captureSentence.selectionStart;
    const end = elements.captureSentence.selectionEnd;
    if (!Number.isInteger(start) || !Number.isInteger(end) || end <= start) {
      return;
    }
    const selected = elements.captureSentence.value.slice(start, end).replace(/\s+/gu, " ").trim();
    if (!selected) {
      return;
    }
    elements.captureTarget.value = selected;
    workspaceRuntime.draftDirty = selected !== workspaceRuntime.initialTarget;
    updateCaptureWorkspace();
  }

  function bindCaptureEvents() {
    elements.captureTarget.addEventListener("input", function () {
      workspaceRuntime.draftDirty = Boolean(selectedCue()) && elements.captureTarget.value !== workspaceRuntime.initialTarget;
      workspaceRuntime.suggestedEntrySequence = undefined;
      updateCaptureWorkspace();
      clearTimeout(lookupRuntime.timer);
      lookupRuntime.timer = setTimeout(function () {
        const cue = selectedCue();
        const target = cue && subtitleModel.captureTarget(cue, elements.captureTarget.value);
        if (target) {
          lookupWord(target.expression);
        } else {
          clearLookup();
        }
      }, 250);
    });
    elements.captureSentence.addEventListener("mouseup", useSentenceSelection);
    elements.captureSentence.addEventListener("keyup", useSentenceSelection);
    elements.captureForm.addEventListener("submit", function (event) {
      event.preventDefault();
      const cue = selectedCue();
      const target = cue && subtitleModel.captureTarget(cue, elements.captureTarget.value);
      if (cue && target) {
        captureCue(cue, target);
      }
    });
    elements.jumpSelected.addEventListener("click", function () {
      const cue = selectedCue();
      if (cue) {
        seekCue(cue);
      }
    });
  }

  function bindTranscriptEvents() {
    elements.unknownOnly.addEventListener("change", function () {
      renderedLimit = TRANSCRIPT_PAGE_SIZE;
      clearRenderedTranscript();
      renderTranscript();
    });
    elements.transcriptSearch.addEventListener("input", function () {
      renderedLimit = TRANSCRIPT_PAGE_SIZE;
      clearRenderedTranscript();
      renderTranscript();
    });
    elements.loadMore.addEventListener("click", function () {
      renderedLimit += TRANSCRIPT_PAGE_SIZE;
      renderTranscript();
    });
    elements.jumpCurrent.addEventListener("click", jumpToCurrentLine);
    elements.coverageRetry.addEventListener("click", retryCoverage);
    elements.batchOneTarget.addEventListener("click", showBatchConfirmation);
    elements.batchConfirm.addEventListener("click", sendOneTargetBatch);
    elements.batchCancel.addEventListener("click", function () {
      hideBatchConfirmation();
      elements.batchOneTarget.focus();
    });
    elements.fullscreen.addEventListener("click", toggleFullscreen);
    document.addEventListener("fullscreenchange", function () {
      elements.fullscreen.textContent = document.fullscreenElement === elements.videoStage
        ? "Exit full screen"
        : "Full screen";
    });
    elements.connectionSetup.addEventListener("click", openConnectionSetup);
    document.addEventListener("selectionchange", updateSelectionTranslation);
    elements.translateSelection.addEventListener("click", function () {
      if (!translationRuntime.selectedText) {
        return;
      }
      elements.translationInput.value = translationRuntime.selectedText;
      schedulePastedTranslation(0);
    });
    elements.translatePasted.addEventListener("click", function () {
      schedulePastedTranslation(0);
    });
    elements.translationInput.addEventListener("input", function (event) {
      schedulePastedTranslation(event.inputType === "insertFromPaste" ? 0 : 500);
    });
    elements.translationInput.addEventListener("keydown", function (event) {
      if ((event.ctrlKey || event.metaKey) && event.key === "Enter") {
        event.preventDefault();
        schedulePastedTranslation(0);
      }
    });
  }

  function bindVideoEvents() {
    elements.video.addEventListener("loadedmetadata", handleVideoMetadata);
    elements.video.addEventListener("durationchange", function () {
      if (Number.isFinite(elements.video.duration) && elements.video.duration > 0) {
        handleVideoMetadata();
      }
    });
    elements.video.addEventListener("error", handleVideoError);
    elements.video.addEventListener("playing", function () {
      lookupRuntime.hoverPausedVideo = false;
      lookupRuntime.hoverPauseLocked = false;
      playbackDiscontinuity = previousVideoMs === null;
      updatePlayback(playbackDiscontinuity);
      schedulePlaybackFrame();
    });
    elements.video.addEventListener("pause", function () {
      cancelPlaybackFrame();
      savePlaybackPosition(true);
      pauseRevealCueIDs = currentCueIDs.size
        ? Array.from(currentCueIDs)
        : lastDialogueCueIDs.slice();
      updatePlayback(false);
      renderOverlay();
    });
    elements.video.addEventListener("timeupdate", function () {
      updatePlayback(false);
      savePlaybackPosition(false);
    });
    elements.video.addEventListener("seeking", function () {
      seeking = true;
      resetPlaybackTracking();
      renderOverlay();
    });
    elements.video.addEventListener("seeked", function () {
      seeking = false;
      updatePlayback(true);
    });
    elements.video.addEventListener("ended", function () {
      cancelPlaybackFrame();
      savePlaybackPosition(true);
      updatePlayback(false);
    });
    elements.video.addEventListener("emptied", function () {
      lookupRuntime.hoverPausedVideo = false;
      lookupRuntime.hoverPauseLocked = false;
      cancelPlaybackFrame();
      currentCueIDs = new Set();
      previousVideoMs = null;
      playbackDiscontinuity = true;
      renderOverlay();
    });
  }

  function bindExtensionEvents() {
    chrome.runtime.onMessage.addListener(function (runtimeMessage, sender) {
      if (!runtimeMessage || runtimeMessage.version !== 1 || sender.id !== chrome.runtime.id ||
          runtimeMessage.type !== "goi.player.capture-hotkey" ||
          runtimeMessage.tabId !== playerTabID) {
        return false;
      }
      handleCaptureHotkey();
      return false;
    });
    if (chrome.storage && chrome.storage.onChanged) {
      chrome.storage.onChanged.addListener(function (changes, areaName) {
        if (areaName === "sync" && changes[settingsModel.STORAGE_KEY]) {
          applySettings(changes[settingsModel.STORAGE_KEY].newValue);
        } else if (areaName === "local" && changes.connection) {
          refreshConnection();
        }
      });
    }
  }

  function bindLifecycleEvents() {
    globalThis.addEventListener("beforeunload", function (event) {
      if (!playerStateModel.shouldWarnBeforeUnload({
        captureDraftDirty: workspaceRuntime.draftDirty,
        captureBusy: workspaceRuntime.captureBusy,
        sendingBatch
      })) {
        return;
      }
      event.preventDefault();
      event.returnValue = "";
    });
    globalThis.addEventListener("pagehide", cleanup);
  }

  function bindEvents() {
    bindPanelEvents();
    bindFileEvents();
    bindTimingEvents();
    bindCaptureEvents();
    bindTranscriptEvents();
    bindVideoEvents();
    bindExtensionEvents();
    bindLifecycleEvents();
  }

  function cleanup(event) {
    if (event?.persisted || shuttingDown) {
      return;
    }
    shuttingDown = true;
    subtitleReadRevision += 1;
    coverageRevision += 1;
    clearTimeout(lookupRuntime.timer);
    cancelPlaybackFrame();
    savePlaybackPosition(true);
    if (videoURL) {
      URL.revokeObjectURL(videoURL);
      videoURL = "";
    }
  }

  async function start() {
    bindSettings();
    bindEvents();
    applySettings(settings);
    updateTimingControls();
    updateCoverageUI();
    renderTranscript();
    updateBatchButton();
    await Promise.all([refreshConnection(), loadSettings()]);
  }

  start().catch(function () {
    showStatus(elements.pageStatus, "Could not initialize the local player.", true);
  });
})();
