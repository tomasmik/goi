(function () {
  "use strict";

  const settingsModel = globalThis.GoiExtension.settingsModel;
  const subtitleModel = globalThis.GoiExtension.subtitleModel;
  const subtitleView = globalThis.GoiExtension.subtitleView;
  const dictionaryClient = globalThis.GoiExtension.dictionaryClient;
  const dictionaryRenderer = globalThis.GoiExtension.dictionaryView;
  const translationModel = globalThis.GoiExtension.translation || {
    create: function () {
      return { translate: function () { return Promise.reject(new Error("Translation is unavailable.")); } };
    },
    selectedText: function (selection) { return selection.toString().trim(); }
  };
  const HOVER_LOOKUP_DELAY_MS = 120;
  const tabID = Number(new URLSearchParams(location.search).get("tab"));
  const displayModeInputs = Array.from(document.querySelectorAll('input[name="display-mode"]'));
  const pauseBehaviorInputs = Array.from(document.querySelectorAll('input[name="pause-behavior"]'));
  const elements = {
    videoTitle: document.getElementById("video-title"),
    transcriptHeading: document.getElementById("transcript-heading") || document.createElement("h2"),
    transcriptNote: document.getElementById("transcript-note") || document.createElement("p"),
    transcriptRetry: document.getElementById("transcript-retry") || document.createElement("button"),
    lineCount: document.getElementById("line-count"),
    unknownOnly: document.getElementById("unknown-only"),
    search: document.getElementById("subtitle-search") || document.createElement("input"),
    autoFollow: document.getElementById("auto-follow") || document.createElement("input"),
    lines: document.getElementById("subtitle-lines"),
    emptyState: document.getElementById("empty-state"),
    emptyTitle: document.getElementById("empty-title"),
    emptyDetail: document.getElementById("empty-detail"),
    coverageReadiness: document.getElementById("coverage-readiness"),
    captureWorkspace: document.getElementById("capture-workspace"),
    captureEmpty: document.getElementById("capture-empty"),
    batchOneTarget: document.getElementById("batch-one-target"),
    captureForm: document.getElementById("capture-form"),
    captureTarget: document.getElementById("capture-target"),
    dictionaryLookup: document.getElementById("dictionary-lookup") || document.createElement("div"),
    captureSentence: document.getElementById("capture-sentence"),
    captureTime: document.getElementById("capture-time"),
    captureSubmit: document.getElementById("capture-submit"),
    jumpSelected: document.getElementById("jump-selected"),
    captureStatus: document.getElementById("capture-status"),
    overlayEnabled: document.getElementById("overlay-enabled"),
    hideNativeCaptions: document.getElementById("hide-native"),
    furiganaEnabled: document.getElementById("furigana"),
    hoverLookupEnabled: document.getElementById("hover-lookup"),
    hideNativeDetail: document.getElementById("hide-native-detail"),
    fontSizePx: document.getElementById("font-size"),
    fontSizeValue: document.getElementById("font-size-value"),
    verticalPercent: document.getElementById("vertical-position"),
    verticalPercentValue: document.getElementById("vertical-position-value"),
    backgroundOpacity: document.getElementById("background-opacity"),
    backgroundOpacityValue: document.getElementById("background-opacity-value"),
    coverageDisplay: document.getElementById("coverage-display"),
    settingsStatus: document.getElementById("settings-status"),
    pageStatus: document.getElementById("page-status"),
    wordPreview: document.getElementById("word-preview"),
    translateSelection: document.getElementById("translate-selection"),
    translationToolsToggle: document.getElementById("translation-tools-toggle"),
    translationTools: document.getElementById("translation-tools"),
    translationInput: document.getElementById("translation-input"),
    translatePasted: document.getElementById("translate-pasted"),
    translationStatus: document.getElementById("translation-status"),
    translationResult: document.getElementById("translation-result"),
    batchToolsToggle: document.getElementById("batch-tools-toggle"),
    batchTools: document.getElementById("batch-tools")
  };
  if (!document.getElementById("auto-follow")) {
    elements.autoFollow.checked = true;
  }
  let session = { revision: -1, lines: [] };
  let selectedLineID;
  let lastSentCaptureKey = "";
  let lastSentTarget = "";
  let refreshTimer;
  let refreshPending = false;
  let settings = settingsModel.sanitize();
  let sessionSourceIdentity = "";
  let sessionInstanceID = "";
  let sendingBatch = false;
  let captureTargetEdited = false;
  let selectedEntrySequence = null;
  let dictionaryView = null;
  let captureWorkspaceRevision = 0;
  let lookupRevision = 0;
  let lookupTimer;
  let wordPreviewRevision = 0;
  let wordPreviewTimer;
  let wordPreviewCloseTimer;
  const submittedLineIDs = new Set();
  const renderedLineItems = new Map();
  const MAX_BATCH_LINES = 100;
  const SUBMITTED_STORAGE_KEY = "goiCompanionSubmittedV2:" + tabID;
  let selectedTranslationText = "";
  const translationRuntime = {
    timer: undefined,
    version: 0,
    schedule: setTimeout,
    cancel: clearTimeout
  };

  function message(type, extra) {
    return chrome.runtime.sendMessage({ type, version: 1, ...(extra || {}) }).catch(function () {
      return { ok: false, errorCode: "unavailable_page" };
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

  function showStatus(element, text, error) {
    element.textContent = text;
    element.classList.toggle("error", Boolean(error));
  }

  function showCaptureStatus(text, state) {
    showStatus(elements.captureStatus, text, state === "error");
    elements.captureStatus.classList.toggle("sent", state === "sent");
    elements.captureStatus.classList.toggle("queued", state === "queued");
    elements.captureStatus.classList.toggle("pending", state === "pending");
  }

  function updateText(element, text) {
    if (element.textContent !== text) {
      element.textContent = text;
    }
  }

  function selectedLine() {
    return session.lines.find(function (line) { return line.id === selectedLineID; });
  }

  function captureKey(line, target, entrySequence) {
    if (!line) {
      return "";
    }
    return [line.id, String(target || "").trim(), entrySequence || ""].join(":");
  }

  function updateCaptureSubmit(line) {
    const valid = Boolean(line && subtitleModel.captureTarget(line, elements.captureTarget.value));
    const sent = valid && captureKey(line, elements.captureTarget.value, selectedEntrySequence) === lastSentCaptureKey;
    elements.captureSubmit.textContent = sent ? "Sent to mining ✓" : "Send to mining";
    elements.captureSubmit.classList.toggle("sent", sent);
    elements.captureSubmit.disabled = sent || !valid;
  }

  function updateSelectedLineDetails(line) {
    elements.captureSentence.value = line.text;
    if (!captureTargetEdited && !elements.captureTarget.value && line.unknowns[0]) {
      elements.captureTarget.value = line.unknowns[0].surface;
    }
    elements.captureTime.textContent = "Caption at " + subtitleModel.formatTimestamp(line.sourcePositionMs);
    updateCaptureSubmit(line);
    elements.jumpSelected.disabled = !Number.isSafeInteger(line.sourcePositionMs);
  }

  function updateSelectedLineStyles() {
    renderedLineItems.forEach(function (entry, lineID) {
      const selected = lineID === selectedLineID;
      const current = lineID === session.currentLineID;
      entry.item.classList.toggle("selected", selected);
      entry.item.classList.toggle("current", current);
      entry.wordButtons.forEach(function (button) {
        const selectedWord = selected && (
          button.dataset.surface === elements.captureTarget.value ||
          button.dataset.expression === elements.captureTarget.value
        );
        button.classList.toggle("is-selected", selectedWord);
      });
      if (current) {
        entry.item.setAttribute("aria-current", "true");
      } else {
        entry.item.removeAttribute("aria-current");
      }
    });
  }

  function followCurrentLine() {
    if (!elements.autoFollow.checked || !Number.isSafeInteger(session.currentLineID)) {
      return;
    }
    const current = renderedLineItems.get(session.currentLineID);
    if (current && typeof current.item.scrollIntoView === "function") {
      current.item.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  }

  function selectLine(line, seek, surface, expression) {
    if (!line) {
      return;
    }
    selectedLineID = line.id;
    captureWorkspaceRevision += 1;
    captureTargetEdited = false;
    selectedEntrySequence = null;
    dictionaryView = null;
    elements.captureTarget.value = surface || line.unknowns[0]?.surface || "";
    showCaptureStatus("", "");
    updateSelectedLineDetails(line);
    if (elements.captureWorkspace) elements.captureWorkspace.hidden = false;
    if (elements.captureEmpty) elements.captureEmpty.hidden = true;
    updateSelectedLineStyles();
    if (elements.captureTarget.value) {
      lookupWord(expression || elements.captureTarget.value);
    } else {
      clearLookup();
    }
    if (seek) {
      seekLine(line);
    }
  }

  function clearLookup() {
    lookupRevision += 1;
    elements.dictionaryLookup.hidden = true;
    elements.dictionaryLookup.textContent = "";
    selectedEntrySequence = null;
    dictionaryView = null;
  }

  function renderDictionaryLookup() {
    dictionaryRenderer.render(elements.dictionaryLookup, dictionaryView, {
      selectedEntrySequence,
      onSelect(candidate) {
        selectedEntrySequence = candidate.entrySequence;
        showCaptureStatus("", "");
        updateCaptureSubmit(selectedLine());
      }
    });
  }

  async function lookupWord(expression) {
    const query = String(expression || "").trim();
    if (!query) {
      clearLookup();
      return;
    }
    const revision = ++lookupRevision;
    elements.dictionaryLookup.hidden = false;
    elements.dictionaryLookup.textContent = "Looking up “" + query + "”…";
    const response = await dictionaryClient.lookup(chrome.runtime, query);
    if (revision !== lookupRevision) {
      return;
    }
    dictionaryView = subtitleModel.dictionaryView(response);
    renderDictionaryLookup();
  }

  async function seekLine(line) {
    const response = await message("goi.companion.line.seek", {
      tabId: tabID,
      lineID: line.id
    });
    if (!response || !response.ok) {
      showStatus(elements.pageStatus, "Could not jump to that subtitle line.", true);
    }
  }

  function classificationText(line) {
    if (line.classification === "pending") {
      return "Checking vocabulary…";
    }
    if (line.classification === "unavailable") {
      return "Coverage unavailable. Retrying.";
    }
    if (line.unknowns.length === 0) {
      return "All known";
    }
    return line.unknowns.length + (line.unknowns.length === 1 ? " unknown word" : " unknown words");
  }

  function createLineItem(line) {
    const item = document.createElement("li");
    item.className = "subtitle-line";

    const time = document.createElement("button");
    time.type = "button";
    time.className = "subtitle-time";
    item.appendChild(time);

    const body = document.createElement("div");
    body.className = "subtitle-body";
    const text = document.createElement("div");
    text.className = "subtitle-text";
    text.lang = "ja";
    text.tabIndex = 0;
    body.appendChild(text);

    const meta = document.createElement("div");
    meta.className = "line-meta";
    const classification = document.createElement("span");
    meta.appendChild(classification);
    const translate = document.createElement("button");
    translate.type = "button";
    translate.className = "line-translate";
    translate.textContent = "Translate";
    meta.appendChild(translate);
    body.appendChild(meta);
    const translation = document.createElement("p");
    translation.className = "line-translation";
    translation.hidden = true;
    body.appendChild(translation);
    item.appendChild(body);

    const entry = { item, time, text, meta, classification, translate, translation, wordButtons: [], line };
    time.addEventListener("click", function () {
      selectLine(entry.line, true);
    });
    text.addEventListener("click", function () {
      const selection = window.getSelection();
      if (selection && !selection.isCollapsed && selection.toString().trim()) {
        return;
      }
      selectLine(entry.line, false);
    });
    text.addEventListener("keydown", function (event) {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        selectLine(entry.line, false);
      }
    });
    translate.addEventListener("click", function (event) {
      event.stopPropagation();
      translationModel.translateInto(translator, entry.line.text, entry.translation, entry.translate, entry.classification);
    });
    return entry;
  }

  function updateLineWords(entry) {
    entry.text.dataset.translationText = entry.line.text;
    const classified = Array.isArray(entry.line.words) ? entry.line.words : entry.line.unknowns;
    const words = subtitleModel.lookupWords(entry.line.text, classified);
    const nodes = [];
    entry.wordButtons = [];
    let offset = 0;
    words.forEach(function (word) {
      if (!word || !Number.isSafeInteger(word.start) || !Number.isSafeInteger(word.end) ||
          word.start < offset || word.end <= word.start || word.end > entry.line.text.length) {
        return;
      }
      if (word.start > offset) {
        const plain = document.createElement("span");
        plain.textContent = entry.line.text.slice(offset, word.start);
        nodes.push(plain);
      }
      const button = document.createElement("button");
      button.type = "button";
      const status = word.status || "unknown";
      button.className = "subtitle-word subtitle-word--" + status;
      const surface = entry.line.text.slice(word.start, word.end);
      button.dataset.surface = word.surface || surface;
      button.dataset.expression = word.expression || word.surface || surface;
      subtitleView.renderWord(button, surface, word, {
        furiganaEnabled: settings.furiganaEnabled,
        status: status,
      });
      button.title = status === "unknown"
        ? "Unknown in Goi — click to look up"
        : status === "suspended_leech"
          ? "Suspended leech — click to look up or mine again"
          : status === "leech" ? "Leech — click to look up" : "Click to look up";
      button.setAttribute("aria-label", "Look up " + surface);
      button.addEventListener("pointerenter", function () {
        scheduleWordPreview(word, button);
      });
      button.addEventListener("pointerleave", scheduleWordPreviewClose);
      button.addEventListener("click", function (event) {
        event.stopPropagation();
        closeWordPreview();
        selectLine(entry.line, false, word.surface, word.expression);
        elements.captureTarget.focus();
      });
      nodes.push(button);
      entry.wordButtons.push(button);
      offset = word.end;
    });
    if (offset < entry.line.text.length) {
      const plain = document.createElement("span");
      plain.textContent = entry.line.text.slice(offset);
      nodes.push(plain);
    }
    entry.text.replaceChildren(...nodes);
  }

  function scheduleWordPreview(word, anchor) {
    cancelWordPreviewTimers();
    if (!settings.hoverLookupEnabled) {
      return;
    }
    wordPreviewTimer = setTimeout(function () {
      wordPreviewTimer = undefined;
      if (anchor && anchor.isConnected === false && !anchor.parentElement) {
        return;
      }
      showWordPreview(word, anchor);
    }, HOVER_LOOKUP_DELAY_MS);
  }

  async function showWordPreview(word, anchor) {
    const expression = word.expression || word.surface;
    const revision = ++wordPreviewRevision;
    elements.wordPreview.textContent = "Looking up “" + expression + "”…";
    elements.wordPreview.hidden = false;
    positionWordPreview(anchor);
    const response = await dictionaryClient.lookup(chrome.runtime, expression);
    if (revision !== wordPreviewRevision || elements.wordPreview.hidden) {
      return;
    }
    dictionaryRenderer.render(elements.wordPreview, subtitleModel.dictionaryView(response));
    positionWordPreview(anchor);
  }

  function positionWordPreview(anchor) {
    if (!anchor || typeof anchor.getBoundingClientRect !== "function") {
      return;
    }
    const target = anchor.getBoundingClientRect();
    const width = elements.wordPreview.offsetWidth || 420;
    const height = elements.wordPreview.offsetHeight || 260;
    const left = Math.max(12, Math.min(window.innerWidth - width - 12, target.left));
    let top = target.bottom + 8;
    if (top + height > window.innerHeight - 12) {
      top = target.top - height - 8;
    }
    elements.wordPreview.style.left = left + "px";
    elements.wordPreview.style.top = Math.max(12, top) + "px";
  }

  function cancelWordPreviewTimers() {
    clearTimeout(wordPreviewTimer);
    clearTimeout(wordPreviewCloseTimer);
    wordPreviewTimer = undefined;
    wordPreviewCloseTimer = undefined;
  }

  function scheduleWordPreviewClose() {
    clearTimeout(wordPreviewTimer);
    clearTimeout(wordPreviewCloseTimer);
    wordPreviewTimer = undefined;
    wordPreviewCloseTimer = setTimeout(closeWordPreview, 180);
  }

  function closeWordPreview() {
    cancelWordPreviewTimers();
    wordPreviewRevision += 1;
    elements.wordPreview.hidden = true;
    elements.wordPreview.textContent = "";
  }

  function updateLineItem(entry, line) {
    entry.line = line;
    entry.item.dataset.lineId = String(line.id);
    entry.time.textContent = subtitleModel.formatTimestamp(line.sourcePositionMs);
    entry.time.setAttribute("aria-label", "Jump to " + entry.time.textContent);
    entry.time.disabled = !Number.isSafeInteger(line.sourcePositionMs);
    updateLineWords(entry);
    entry.classification.textContent = classificationText(line);
  }

  function clearRenderedLines() {
    renderedLineItems.clear();
    elements.lines.replaceChildren();
  }

  function updateSelectionTranslation() {
    const selection = window.getSelection();
    const insideTranscript = selection && !selection.isCollapsed && selection.rangeCount > 0 &&
      elements.lines.contains(selection.anchorNode) && elements.lines.contains(selection.focusNode);
    selectedTranslationText = insideTranscript
      ? translationModel.selectedText(selection, elements.lines, ".subtitle-text")
      : "";
    elements.translateSelection.hidden = !selectedTranslationText;
  }

  function setToolPanel(panel, toggle, open) {
    panel.hidden = !open;
    toggle.setAttribute("aria-expanded", String(open));
  }

  function toggleToolPanel(panel, toggle, otherPanel, otherToggle) {
    const open = panel.hidden;
    setToolPanel(otherPanel, otherToggle, false);
    setToolPanel(panel, toggle, open);
    return open;
  }

  function clearCaptureWorkspace() {
    captureWorkspaceRevision += 1;
    selectedLineID = undefined;
    captureTargetEdited = false;
    elements.captureTarget.value = "";
    elements.captureSentence.value = "";
    clearTimeout(lookupTimer);
    clearLookup();
    elements.captureTime.textContent = "";
    lastSentCaptureKey = "";
    lastSentTarget = "";
    updateCaptureSubmit(undefined);
    elements.jumpSelected.disabled = true;
    showCaptureStatus("", "");
    if (elements.captureWorkspace) elements.captureWorkspace.hidden = true;
    if (elements.captureEmpty) elements.captureEmpty.hidden = false;
  }

  function renderLines() {
    const nearBottom = elements.lines.scrollHeight - elements.lines.scrollTop -
      elements.lines.clientHeight < 80;
    const lineIDs = new Set(session.lines.map(function (line) { return line.id; }));
    renderedLineItems.forEach(function (entry, lineID) {
      if (!lineIDs.has(lineID)) {
        entry.item.remove();
        renderedLineItems.delete(lineID);
      }
    });

    let visibleCount = 0;
    session.lines.forEach(function (line) {
      let entry = renderedLineItems.get(line.id);
      if (!entry) {
        entry = createLineItem(line);
        renderedLineItems.set(line.id, entry);
        elements.lines.appendChild(entry.item);
      }
      updateLineItem(entry, line);
      const query = elements.search.value.trim().toLocaleLowerCase();
      entry.item.hidden = (elements.unknownOnly.checked &&
        (line.classification !== "ready" || line.unknowns.length === 0)) ||
        (query && !line.text.toLocaleLowerCase().includes(query));
      if (!entry.item.hidden) {
        visibleCount += 1;
      }
    });

    updateSelectedLineStyles();
    elements.emptyState.hidden = visibleCount > 0;
    if (elements.coverageReadiness) {
      const pending = session.lines.filter(function (line) {
        return line.classification === "pending";
      }).length;
      const unavailable = session.lines.filter(function (line) {
        return line.classification === "unavailable";
      }).length;
      elements.coverageReadiness.hidden = !elements.unknownOnly.checked || pending + unavailable === 0;
      elements.coverageReadiness.textContent = [
        pending ? pending + (pending === 1 ? " line is still being checked" : " lines are still being checked") : "",
        unavailable ? unavailable + (unavailable === 1 ? " line could not be checked" : " lines could not be checked") : ""
      ].filter(Boolean).join(" · ") + ". Unchecked lines stay hidden.";
    }
    if (elements.unknownOnly.checked && session.lines.length > 0 && visibleCount === 0) {
      elements.emptyTitle.textContent = "No unknown dialogue";
      elements.emptyDetail.textContent = "No unknown lines.";
    } else if (session.transcriptState === "loading" || session.transcriptState === "checking") {
      elements.emptyTitle.textContent = session.transcriptState === "loading"
        ? "Loading transcript"
        : "Checking vocabulary";
      elements.emptyDetail.textContent = "Reading the video’s Japanese subtitles.";
    } else if (session.transcriptState === "unavailable" && session.lines.length === 0) {
      elements.emptyTitle.textContent = session.transcriptReason === "no_japanese_track"
        ? "No Japanese transcript"
        : "Full transcript unavailable";
      elements.emptyDetail.textContent = "Live captions will still appear here as the video plays.";
    } else {
      elements.emptyTitle.textContent = "Waiting for captions";
      elements.emptyDetail.textContent = "Live captions will appear here as the video plays.";
    }
    let countText;
    if (session.transcriptState === "ready" && session.comprehension) {
      const summary = session.comprehension;
      const percent = summary.total_occurrences
        ? Math.round(summary.known_occurrences / summary.total_occurrences * 1000) / 10
        : 0;
      countText = session.lines.length + (session.lines.length === 1 ? " subtitle line" : " subtitle lines") +
        " · " + percent + "% known";
    } else if (session.transcriptState === "checking") {
      countText = "Checking " + session.lines.length +
        (session.lines.length === 1 ? " subtitle line…" : " subtitle lines…");
    } else if (session.lines.length) {
      const live = session.transcriptSource === "full" ? "" : " live";
      countText = session.lines.length + live +
        (session.lines.length === 1 ? " subtitle line" : " subtitle lines");
    } else {
      countText = session.transcriptState === "loading" ? "Loading transcript…" : "Waiting for captions";
    }
    updateText(elements.lineCount, countText);
    if (session.transcriptSource === "full") {
      followCurrentLine();
    } else if (nearBottom && elements.autoFollow.checked) {
      elements.lines.scrollTop = elements.lines.scrollHeight;
    }
    updateBatchButton();
  }

  function oneTargetLines() {
    return subtitleModel.oneTargetLines(session.lines, submittedLineIDs);
  }

  function updateBatchButton() {
    const count = oneTargetLines().length;
    const oneTargetCount = subtitleModel.oneTargetLines(session.lines, new Set()).length;
    elements.batchOneTarget.disabled = sendingBatch || count === 0;
    elements.batchOneTarget.textContent = count
      ? "Send " + Math.min(count, MAX_BATCH_LINES) + " " +
        (Math.min(count, MAX_BATCH_LINES) === 1 ? "line" : "lines") + " to mining"
      : oneTargetCount > 0
        ? "All matching lines sent"
        : "No matching lines";
  }

  async function restoreSubmittedLineIDs(identity, instanceID) {
    submittedLineIDs.clear();
    if (!identity || !instanceID) {
      return;
    }
    try {
      const stored = await chrome.storage.session.get(SUBMITTED_STORAGE_KEY);
      const record = stored && stored[SUBMITTED_STORAGE_KEY];
      if (!record || record.session !== identity || record.instance !== instanceID ||
          !Array.isArray(record.lineIDs)) {
        return;
      }
      record.lineIDs.forEach(function (lineID) {
        if (Number.isSafeInteger(lineID) && lineID > 0) {
          submittedLineIDs.add(lineID);
        }
      });
    } catch (_error) {
      return;
    }
  }

  async function rememberSubmittedLine(lineID) {
    submittedLineIDs.add(lineID);
    const retainedLineIDs = new Set(session.lines.map(function (line) { return line.id; }));
    submittedLineIDs.forEach(function (submittedLineID) {
      if (!retainedLineIDs.has(submittedLineID)) {
        submittedLineIDs.delete(submittedLineID);
      }
    });
    try {
      await chrome.storage.session.set({
        [SUBMITTED_STORAGE_KEY]: {
          session: sessionSourceIdentity,
          instance: sessionInstanceID,
          lineIDs: Array.from(submittedLineIDs).sort(function (left, right) { return left - right; })
        }
      });
      return true;
    } catch (_error) {
      return false;
    }
  }

  async function applySession(nextSession) {
    if (!nextSession || !Array.isArray(nextSession.lines)) {
      return;
    }
    const nextIdentity = subtitleModel.sessionIdentity(nextSession.sourceURL);
    const nextInstanceID = String(nextSession.sessionID || "");
    const sourceChanged = nextIdentity !== sessionSourceIdentity || nextInstanceID !== sessionInstanceID;
    if (sourceChanged) {
      sessionSourceIdentity = nextIdentity;
      sessionInstanceID = nextInstanceID;
      clearCaptureWorkspace();
      clearRenderedLines();
      await restoreSubmittedLineIDs(nextIdentity, nextInstanceID);
    }
    const changed = sourceChanged || nextSession.revision !== session.revision;
    session = nextSession;
    elements.videoTitle.textContent = session.sourceTitle || "YouTube video";
    const fullTranscript = session.transcriptSource === "full";
    elements.transcriptHeading.textContent = fullTranscript ? "Full transcript" : "Live subtitle history";
    elements.transcriptNote.textContent = fullTranscript
      ? "Comprehension uses the complete Japanese transcript."
      : "Only subtitles seen since Goi started watching this video are shown. A complete comprehension score is not available.";
    elements.transcriptRetry.hidden = session.transcriptState !== "unavailable" ||
      session.transcriptReason === "no_japanese_track";
    if (changed) {
      renderLines();
      const current = selectedLine();
      if (current) {
        updateSelectedLineDetails(current);
      } else if (session.lines.length && session.transcriptSource !== "full") {
        selectLine(session.lines[session.lines.length - 1], false);
      }
    }
    if (!session.observing) {
      showStatus(elements.pageStatus, "Caption collection is off.", false);
    } else if (elements.pageStatus.textContent === "Caption collection is off.") {
      showStatus(elements.pageStatus, "", false);
    }
  }

  function scheduleRefresh(delay) {
    clearTimeout(refreshTimer);
    if (document.hidden) {
      return;
    }
    refreshTimer = setTimeout(refreshSession, delay);
  }

  async function refreshSession() {
    if (refreshPending || !Number.isInteger(tabID) || tabID <= 0) {
      return;
    }
    refreshPending = true;
    try {
      const response = await message("goi.companion.session.get", {
        tabId: tabID,
        sessionID: String(session.sessionID || ""),
        sinceRevision: session.revision
      });
      if (!response || !response.ok || (!response.unchanged && !response.session)) {
        throw new Error("session unavailable");
      }
      if (!response.unchanged) {
        await applySession(response.session);
      }
    } catch (_error) {
      showStatus(elements.pageStatus, "The YouTube tab is unavailable. Open the transcript browser from the video again.", true);
    } finally {
      refreshPending = false;
      scheduleRefresh(750);
    }
  }

  function applySettings(nextSettings) {
    settings = settingsModel.sanitize(nextSettings);
    elements.overlayEnabled.checked = settings.overlayEnabled;
    elements.hideNativeCaptions.checked = settings.hideNativeCaptions;
    elements.hideNativeCaptions.disabled = settings.displayMode !== "always";
    elements.furiganaEnabled.checked = settings.furiganaEnabled;
    elements.hoverLookupEnabled.checked = settings.hoverLookupEnabled;
    document.body.classList.toggle("furigana-enabled", settings.furiganaEnabled);
    if (!settings.hoverLookupEnabled) {
      closeWordPreview();
    }
    elements.hideNativeDetail.hidden = settings.displayMode === "always";
    elements.fontSizePx.value = String(settings.fontSizePx);
    elements.fontSizeValue.value = settings.fontSizePx + " px";
    elements.fontSizeValue.textContent = settings.fontSizePx + " px";
    elements.verticalPercent.value = String(settings.verticalPercent);
    elements.verticalPercentValue.value = settingsModel.verticalPositionLabel(settings.verticalPercent);
    elements.verticalPercentValue.textContent = elements.verticalPercentValue.value;
    elements.backgroundOpacity.value = String(settings.backgroundOpacity);
    elements.backgroundOpacityValue.value = Math.round(settings.backgroundOpacity * 100) + "%";
    elements.backgroundOpacityValue.textContent = Math.round(settings.backgroundOpacity * 100) + "%";
    elements.coverageDisplay.value = settings.coverageDisplay;
    displayModeInputs.forEach(function (input) {
      input.checked = input.value === settings.displayMode;
    });
    pauseBehaviorInputs.forEach(function (input) {
      input.checked = input.value === settings.pauseBehavior;
    });
    renderLines();
  }

  async function saveSetting(patch) {
    const previous = settings;
    applySettings(settingsModel.applyPatch(settings, patch));
    const response = await message("goi.settings.patch", { patch });
    if (!response || !response.ok || !response.settings) {
      applySettings(previous);
      showStatus(elements.settingsStatus, "Could not save that setting.", true);
      return;
    }
    applySettings(response.settings);
    showStatus(elements.settingsStatus, "Settings saved.", false);
    if (Object.prototype.hasOwnProperty.call(patch, "overlayEnabled")) {
      scheduleRefresh(0);
    }
  }

  function bindTranscriptEvents() {
    elements.unknownOnly.addEventListener("change", renderLines);
    elements.search.addEventListener("input", renderLines);
    elements.autoFollow.addEventListener("change", followCurrentLine);
    elements.transcriptRetry.addEventListener("click", async function () {
      elements.transcriptRetry.disabled = true;
      showStatus(elements.pageStatus, "Retrying transcript…", false);
      const response = await message("goi.companion.transcript.retry", { tabId: tabID });
      elements.transcriptRetry.disabled = false;
      if (!response || !response.ok) {
        showStatus(elements.pageStatus, "Could not retry the transcript.", true);
        return;
      }
      showStatus(elements.pageStatus, "", false);
      scheduleRefresh(0);
    });
    elements.batchOneTarget.addEventListener("click", async function () {
      const candidates = oneTargetLines().slice(0, MAX_BATCH_LINES);
      if (!candidates.length || !confirm(
        "Send " + candidates.length + (candidates.length === 1
          ? " line with one unknown word"
          : " lines with one unknown word each") + " to the Goi mining inbox?"
      )) {
        return;
      }
      sendingBatch = true;
      updateBatchButton();
      let sent = 0;
      let queued = 0;
      let failed = 0;
      let unremembered = 0;
      for (let index = 0; index < candidates.length; index += 1) {
        const line = candidates[index];
        showStatus(elements.pageStatus, "Sending " + (index + 1) + " of " + candidates.length + "…", false);
        const response = await message("goi.companion.line.capture", {
          tabId: tabID,
          lineID: line.id,
          surface: line.unknowns[0].surface
        });
        if (!response || !response.ok) {
          failed += 1;
          continue;
        }
        if (!await rememberSubmittedLine(line.id)) {
          unremembered += 1;
        }
        if (response.queued) {
          queued += 1;
        } else {
          sent += 1;
        }
      }
      sendingBatch = false;
      updateBatchButton();
      showStatus(
        elements.pageStatus,
        subtitleModel.batchSummary(sent, queued, failed) +
          (unremembered ? " · " + unremembered + " could not be marked sent" : ""),
        failed > 0 || unremembered > 0
      );
    });
  }

  function bindCaptureEvents() {
    elements.captureTarget.addEventListener("input", function () {
      captureTargetEdited = true;
      const line = selectedLine();
      updateSelectedLineStyles();
      showCaptureStatus("", "");
      updateCaptureSubmit(line);
      clearTimeout(lookupTimer);
      lookupTimer = setTimeout(function () {
        lookupWord(elements.captureTarget.value);
      }, 250);
    });
    elements.jumpSelected.addEventListener("click", function () {
      const line = selectedLine();
      if (line) {
        seekLine(line);
      }
    });
    elements.captureForm.addEventListener("submit", async function (event) {
      event.preventDefault();
      const line = selectedLine();
      if (!line) {
        return;
      }
      const workspaceRevision = captureWorkspaceRevision;
      const submittedTarget = elements.captureTarget.value.trim();
      const submittedKey = captureKey(line, submittedTarget, selectedEntrySequence);
      elements.captureSubmit.disabled = true;
      elements.captureSubmit.textContent = "Sending…";
      elements.captureSubmit.classList.toggle("sent", false);
      showCaptureStatus("Sending…", "pending");
      const response = await message("goi.companion.line.capture", {
        tabId: tabID,
        lineID: line.id,
        surface: elements.captureTarget.value,
        suggestedEntrySequence: selectedEntrySequence
      });
      if (workspaceRevision !== captureWorkspaceRevision) {
        return;
      }
      const currentLine = selectedLine();
      if (!response || !response.ok) {
        updateCaptureSubmit(currentLine);
        showCaptureStatus("Could not send “" + submittedTarget + "” to mining. Try again.", "error");
        return;
      }
      lastSentCaptureKey = submittedKey;
      lastSentTarget = submittedTarget;
      updateCaptureSubmit(currentLine);
      const remembered = await rememberSubmittedLine(line.id);
      if (workspaceRevision !== captureWorkspaceRevision) {
        return;
      }
      updateBatchButton();
      showCaptureStatus(
        remembered
          ? captureDeliveryText(response, submittedTarget)
          : captureDeliveryText(response, submittedTarget) + " This line could not be marked as sent.",
        !remembered ? "error" : response.queued ? "queued" : "sent"
      );
    });
  }

  function captureDeliveryText(response, target) {
    const label = "“" + String(target || "This word").trim() + "”";
    return response.queued
      ? label + " queued for mining. Goi will retry automatically."
      : label + " sent to mining.";
  }

  function bindSettingsEvents() {
    elements.overlayEnabled.addEventListener("change", function () {
      saveSetting({ overlayEnabled: elements.overlayEnabled.checked });
    });
    elements.hideNativeCaptions.addEventListener("change", function () {
      saveSetting({ hideNativeCaptions: elements.hideNativeCaptions.checked });
    });
    elements.furiganaEnabled.addEventListener("change", function () {
      saveSetting({ furiganaEnabled: elements.furiganaEnabled.checked });
    });
    elements.hoverLookupEnabled.addEventListener("change", function () {
      saveSetting({ hoverLookupEnabled: elements.hoverLookupEnabled.checked });
    });
    elements.wordPreview.addEventListener("pointerenter", function () {
      clearTimeout(wordPreviewCloseTimer);
      wordPreviewCloseTimer = undefined;
    });
    elements.wordPreview.addEventListener("pointerleave", scheduleWordPreviewClose);
    elements.fontSizePx.addEventListener("input", function () {
      elements.fontSizeValue.value = elements.fontSizePx.value + " px";
      elements.fontSizeValue.textContent = elements.fontSizePx.value + " px";
    });
    elements.fontSizePx.addEventListener("change", function () {
      saveSetting({ fontSizePx: Number(elements.fontSizePx.value) });
    });
    elements.verticalPercent.addEventListener("input", function () {
      const label = settingsModel.verticalPositionLabel(elements.verticalPercent.value);
      elements.verticalPercentValue.value = label;
      elements.verticalPercentValue.textContent = label;
    });
    elements.verticalPercent.addEventListener("change", function () {
      saveSetting({ verticalPercent: Number(elements.verticalPercent.value) });
    });
    elements.backgroundOpacity.addEventListener("input", function () {
      const percent = Math.round(Number(elements.backgroundOpacity.value) * 100) + "%";
      elements.backgroundOpacityValue.value = percent;
      elements.backgroundOpacityValue.textContent = percent;
    });
    elements.backgroundOpacity.addEventListener("change", function () {
      saveSetting({ backgroundOpacity: Number(elements.backgroundOpacity.value) });
    });
    elements.coverageDisplay.addEventListener("change", function () {
      saveSetting({ coverageDisplay: elements.coverageDisplay.value });
    });
    displayModeInputs.forEach(function (input) {
      input.addEventListener("change", function () {
        if (input.checked) {
          saveSetting({ displayMode: input.value });
        }
      });
    });
    pauseBehaviorInputs.forEach(function (input) {
      input.addEventListener("change", function () {
        if (input.checked) {
          saveSetting({ pauseBehavior: input.value });
        }
      });
    });
  }

  function bindTranslationEvents() {
    document.addEventListener("selectionchange", updateSelectionTranslation);
    elements.translateSelection.addEventListener("click", function () {
      if (!selectedTranslationText) {
        return;
      }
      setToolPanel(elements.batchTools, elements.batchToolsToggle, false);
      setToolPanel(elements.translationTools, elements.translationToolsToggle, true);
      elements.translationInput.value = selectedTranslationText;
      schedulePastedTranslation(0);
    });
    elements.translationToolsToggle.addEventListener("click", function () {
      if (toggleToolPanel(
        elements.translationTools,
        elements.translationToolsToggle,
        elements.batchTools,
        elements.batchToolsToggle
      )) {
        elements.translationInput.focus();
      }
    });
    elements.batchToolsToggle.addEventListener("click", function () {
      toggleToolPanel(
        elements.batchTools,
        elements.batchToolsToggle,
        elements.translationTools,
        elements.translationToolsToggle
      );
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

  function bindLifecycleEvents() {
    chrome.storage.onChanged.addListener(function (changes, area) {
      if (area === "sync" && changes[settingsModel.STORAGE_KEY]) {
        applySettings(changes[settingsModel.STORAGE_KEY].newValue);
      }
    });
    document.addEventListener("visibilitychange", function () {
      if (document.hidden) {
        clearTimeout(refreshTimer);
      } else {
        scheduleRefresh(0);
      }
    });
    window.addEventListener("beforeunload", function () {
      clearTimeout(refreshTimer);
      clearTimeout(lookupTimer);
    });
  }

  bindTranscriptEvents();
  bindCaptureEvents();
  bindSettingsEvents();
  bindTranslationEvents();
  bindLifecycleEvents();

  if (!Number.isInteger(tabID) || tabID <= 0) {
    showStatus(elements.pageStatus, "Open this window from a YouTube video.", true);
    return;
  }

  Promise.all([
    message("goi.settings.get"),
    refreshSession()
  ]).then(function (responses) {
    const settingResponse = responses[0];
    if (settingResponse && settingResponse.ok) {
      applySettings(settingResponse.settings);
    } else {
      applySettings(settings);
      showStatus(elements.settingsStatus, "Could not load subtitle settings.", true);
    }
  }).catch(function () {
    applySettings(settings);
  });
})();
