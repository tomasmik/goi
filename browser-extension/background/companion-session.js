function companionPageTargetID(value) {
  if (typeof value !== "string") {
    return null;
  }
  try {
    const pageURL = new URL(value);
    const expectedURL = new URL(extensionPageURL("companion/companion.html"));
    if (pageURL.protocol !== expectedURL.protocol || pageURL.host !== expectedURL.host ||
        pageURL.pathname !== expectedURL.pathname || pageURL.hash ||
        Array.from(pageURL.searchParams.keys()).some(function (key) { return key !== "tab"; })) {
      return null;
    }
    const values = pageURL.searchParams.getAll("tab");
    const tabID = values.length === 1 ? Number(values[0]) : NaN;
    return Number.isSafeInteger(tabID) && tabID > 0 && String(tabID) === values[0]
      ? tabID
      : null;
  } catch (_error) {
    return null;
  }
}

function companionSender(sender) {
  if (!sender || sender.id !== chrome.runtime.id || !sender.tab ||
      !Number.isSafeInteger(sender.tab.id) || sender.tab.id <= 0 || sender.frameId !== 0) {
    return false;
  }
  const targetTabID = companionPageTargetID(sender.url);
  if (targetTabID === null) {
    return false;
  }
  const tabURL = sender.tab.url || sender.tab.pendingUrl;
  return !tabURL || companionPageTargetID(tabURL) === targetTabID;
}

function normalizeCompanionTargetURL(value) {
  if (!isYouTubeVideoURL(value)) {
    return "";
  }
  const url = new URL(value);
  const normalizedURL = url.pathname === "/watch"
    ? url.origin + url.pathname + "?v=" + encodeURIComponent(url.searchParams.get("v"))
    : url.origin + url.pathname;
  return normalizedURL.length <= 2048 ? normalizedURL : "";
}

function normalizeCompanionTarget(companionTabID, targetTabID, targetURL, recovered) {
  const normalizedURL = normalizeCompanionTargetURL(targetURL);
  if (!Number.isSafeInteger(companionTabID) || companionTabID <= 0 ||
      !Number.isSafeInteger(targetTabID) || targetTabID <= 0 ||
      companionTabID === targetTabID || !normalizedURL) {
    return null;
  }
  return { targetTabID, targetURL: normalizedURL, recovered: recovered === true };
}

async function loadCompanionTargets() {
  if (companionTargetsLoaded) {
    return;
  }
  if (!chrome.storage.session || !chrome.storage.session.get) {
    throw companionPersistenceError();
  }
  let stored;
  try {
    stored = await chrome.storage.session.get(COMPANION_TARGETS_STORAGE_KEY);
  } catch (cause) {
    throw companionPersistenceError(cause);
  }
  if (!stored || typeof stored !== "object") {
    throw companionPersistenceError();
  }
  const storedValue = stored[COMPANION_TARGETS_STORAGE_KEY];
  const rawEntries = Array.isArray(storedValue) ? storedValue : [];
  const entries = rawEntries.slice(-COMPANION_TARGET_LIMIT);
  const loadedTargets = new Map();
  let rewrite = (storedValue !== undefined && !Array.isArray(storedValue)) ||
    rawEntries.length !== entries.length;
  entries.forEach(function (entry) {
    const normalized = normalizeCompanionTarget(
      entry && entry.companionTabID,
      entry && entry.targetTabID,
      entry && entry.targetURL,
      true
    );
    if (normalized) {
      rewrite = rewrite || loadedTargets.has(entry.companionTabID);
      loadedTargets.delete(entry.companionTabID);
      loadedTargets.set(entry.companionTabID, normalized);
    } else {
      rewrite = true;
    }
  });
  if (rewrite) {
    await writeCompanionTargets(loadedTargets);
  }
  replaceCompanionTargets(loadedTargets);
  companionTargetsLoaded = true;
}

async function writeCompanionTargets(targets) {
  if (!chrome.storage.session || !chrome.storage.session.set) {
    throw companionPersistenceError();
  }
  const entries = Array.from(targets || companionTargetByTab, function ([companionTabID, target]) {
    return {
      companionTabID,
      targetTabID: target.targetTabID,
      targetURL: target.targetURL
    };
  }).slice(-COMPANION_TARGET_LIMIT);
  try {
    await chrome.storage.session.set({ [COMPANION_TARGETS_STORAGE_KEY]: entries });
  } catch (cause) {
    throw companionPersistenceError(cause);
  }
}

function companionPersistenceError(cause) {
  const error = companionError("Could not save subtitle browser ownership", 503);
  error.code = "companion_storage_unavailable";
  error.cause = cause;
  return error;
}

function replaceCompanionTargets(targets) {
  companionTargetByTab.clear();
  targets.forEach(function (target, companionTabID) {
    companionTargetByTab.set(companionTabID, target);
  });
}

function serializeCompanionTargets(operation) {
  const update = companionTargetQueue.then(async function () {
    await loadCompanionTargets();
    return operation();
  });
  companionTargetQueue = recoverQueue(update);
  return update;
}

async function associateCompanionTab(companionTabID, targetTab) {
  const target = normalizeCompanionTarget(
    companionTabID,
    targetTab && targetTab.id,
    targetTab && targetTab.url,
    false
  );
  if (!target) {
    return null;
  }
  await serializeCompanionTargets(async function () {
    const nextTargets = new Map(companionTargetByTab);
    nextTargets.delete(companionTabID);
    nextTargets.set(companionTabID, target);
    while (nextTargets.size > COMPANION_TARGET_LIMIT) {
      nextTargets.delete(nextTargets.keys().next().value);
    }
    await writeCompanionTargets(nextTargets);
    replaceCompanionTargets(nextTargets);
  });
  return target.targetTabID;
}

function associateCompanionSender(sender, targetTab) {
  const declaredTargetTabID = companionSender(sender)
    ? companionPageTargetID(sender.url)
    : null;
  if (declaredTargetTabID === null || !targetTab || targetTab.id !== declaredTargetTabID) {
    return Promise.reject(companionError("Subtitle browser ownership is invalid", 403));
  }
  return associateCompanionTab(sender.tab.id, targetTab);
}

async function releaseCompanionTab(companionTabID) {
  const target = await serializeCompanionTargets(async function () {
    const existing = companionTargetByTab.get(companionTabID);
    if (!existing) {
      return null;
    }
    const nextTargets = new Map(companionTargetByTab);
    nextTargets.delete(companionTabID);
    await writeCompanionTargets(nextTargets);
    replaceCompanionTargets(nextTargets);
    return existing;
  });
  return target ? target.targetTabID : null;
}

function forgetCompanionsForTarget(targetTabID) {
  return serializeCompanionTargets(async function () {
    const nextTargets = new Map(companionTargetByTab);
    for (const [companionTabID, target] of nextTargets) {
      if (target.targetTabID === targetTabID) {
        nextTargets.delete(companionTabID);
      }
    }
    if (nextTargets.size !== companionTargetByTab.size) {
      await writeCompanionTargets(nextTargets);
      replaceCompanionTargets(nextTargets);
    }
  });
}

function companionError(message, status) {
  const error = new Error(message);
  error.status = status;
  return error;
}

async function youtubeTab(tabId) {
  if (!Number.isInteger(tabId)) {
    throw companionError("A YouTube tab is required", 400);
  }
  let tab;
  try {
    tab = await chrome.tabs.get(tabId);
  } catch (_error) {
    throw companionError("The YouTube tab is no longer available", 404);
  }
  if (!isYouTubeVideoURL(tab.url)) {
    throw companionError("Open a YouTube video to use the transcript browser", 400);
  }
  return tab;
}

async function restoreCompanionWindow(windowId) {
  if (!Number.isInteger(windowId) || !chrome.windows || !chrome.windows.update) {
    return;
  }
  await chrome.windows.update(windowId, { state: "normal" });
  await chrome.windows.update(windowId, {
    focused: true,
    width: COMPANION_WINDOW_WIDTH,
    height: COMPANION_WINDOW_HEIGHT
  });
}

async function openSubtitleBrowser(sender) {
  let tab = sender && sender.tab;
  if (!tab || !isYouTubeVideoURL(tab.url)) {
    tab = await getActiveTab();
  }
  const context = siteContextForTab(tab);
  if (!context || !isYouTubeVideoURL(context.url)) {
    throw companionError("Open a YouTube video to use the transcript browser", 400);
  }
  let associatedCompanionTabID;
  try {
    await ensureYouTubeOverlayScript(context.tabId);

    const baseURL = extensionPageURL("companion/companion.html");
    const pageURL = baseURL + "?tab=" + context.tabId;
    let existing = [];
    try {
      existing = await chrome.tabs.query({ url: baseURL + "*" });
    } catch (_error) {
      existing = [];
    }
    const existingTab = existing.find(function (candidate) {
      return Number.isInteger(candidate.id);
    });
    if (existingTab) {
      await chrome.tabs.update(existingTab.id, { active: true, url: pageURL });
      const associatedTargetID = await associateCompanionTab(existingTab.id, {
        id: context.tabId,
        url: context.url
      });
      if (!Number.isSafeInteger(associatedTargetID)) {
        throw companionError("Could not connect the transcript browser", 500);
      }
      associatedCompanionTabID = existingTab.id;
      await restoreCompanionWindow(existingTab.windowId);
      return { ok: true, tabId: context.tabId };
    }
    if (!chrome.windows || !chrome.windows.create) {
      const createdTab = await chrome.tabs.create({ url: pageURL, active: true });
      const associatedTargetID = await associateCompanionTab(createdTab && createdTab.id, {
        id: context.tabId,
        url: context.url
      });
      if (!Number.isSafeInteger(associatedTargetID)) {
        throw companionError("Could not connect the transcript browser", 500);
      }
      associatedCompanionTabID = createdTab.id;
      return { ok: true, tabId: context.tabId };
    }
    const createdWindow = await chrome.windows.create({
      url: pageURL,
      type: "popup",
      width: COMPANION_WINDOW_WIDTH,
      height: COMPANION_WINDOW_HEIGHT,
      focused: true
    });
    const createdTab = createdWindow && Array.isArray(createdWindow.tabs)
      ? createdWindow.tabs.find(function (candidate) { return Number.isSafeInteger(candidate.id); })
      : null;
    const associatedTargetID = await associateCompanionTab(createdTab && createdTab.id, {
      id: context.tabId,
      url: context.url
    });
    if (!Number.isSafeInteger(associatedTargetID)) {
      throw companionError("Could not connect the transcript browser", 500);
    }
    associatedCompanionTabID = createdTab.id;
    return { ok: true, tabId: context.tabId };
  } catch (error) {
    if (Number.isSafeInteger(associatedCompanionTabID)) {
      await releaseCompanionTab(associatedCompanionTabID).catch(ignoreCompanionReleaseFailure);
    }
    throw error;
  }
}

async function captureCurrentYouTubeSelection(tab) {
  if (!tab || !Number.isSafeInteger(tab.id) || !isYouTubeVideoURL(tab.url)) {
    return false;
  }
  try {
    await ensureYouTubeOverlayScript(tab.id);
    const response = await chrome.tabs.sendMessage(tab.id, {
      type: "goi.youtube.capture.current",
      version: 1
    });
    return Boolean(response && response.ok && response.handled);
  } catch (_error) {
    return false;
  }
}

async function getCompanionSession(tabId, sender, sinceRevision, sessionID) {
  if (!companionSender(sender)) {
    throw companionError("Subtitle sessions are available only to the companion window", 403);
  }
  const tab = await youtubeTab(tabId);
  await associateCompanionSender(sender, tab);
  await ensureYouTubeOverlayScript(tabId);
  const response = await chrome.tabs.sendMessage(tabId, {
    type: "goi.youtube.session.get",
    version: 1,
    sinceRevision: Number.isSafeInteger(sinceRevision) ? sinceRevision : -1,
    sessionID: typeof sessionID === "string" ? sessionID.slice(0, 100) : ""
  });
  if (response && response.ok && response.unchanged) {
    return { ok: true, unchanged: true };
  }
  return { ok: true, session: sanitizeSubtitleSession(response && response.session) };
}

async function forwardCompanionLineAction(type, message, sender) {
  if (!companionSender(sender)) {
    throw companionError("Subtitle actions are available only to the companion window", 403);
  }
  const tabId = message && message.tabId;
  const lineID = message && message.lineID;
  if (!Number.isSafeInteger(lineID) || lineID <= 0) {
    throw companionError("The subtitle line is invalid", 400);
  }
  const tab = await youtubeTab(tabId);
  await associateCompanionSender(sender, tab);
  const request = { type, version: 1, lineID };
  if (type === "goi.youtube.line.capture") {
    const surface = typeof message.surface === "string" ? message.surface.trim() : "";
    if (!surface || Array.from(surface).length > 200) {
      throw companionError("The capture word is invalid", 400);
    }
    request.surface = surface;
    if (Number.isSafeInteger(message.suggestedEntrySequence) && message.suggestedEntrySequence > 0) {
      request.suggestedEntrySequence = message.suggestedEntrySequence;
    }
  }
  const response = await chrome.tabs.sendMessage(tabId, request);
  return response && typeof response === "object"
    ? response
    : { ok: false, errorCode: "unavailable_page" };
}

async function forwardCompanionTranscriptRetry(tabId, sender) {
  if (!companionSender(sender)) {
    throw companionError("Transcript actions are available only to the companion window", 403);
  }
  const tab = await youtubeTab(tabId);
  await associateCompanionSender(sender, tab);
  const response = await chrome.tabs.sendMessage(tab.id, {
    type: "goi.youtube.transcript.retry",
    version: 1
  });
  return response && typeof response === "object"
    ? response
    : { ok: false, errorCode: "unavailable_page" };
}

function sanitizeSubtitleSession(value) {
  const session = value && typeof value === "object" ? value : {};
  const rawLines = Array.isArray(session.lines) ? session.lines.slice(-5000) : [];
  let characters = 0;
  const seen = new Set();
  const lines = [];
  for (let index = rawLines.length - 1; index >= 0; index -= 1) {
    const line = sanitizeSubtitleLine(rawLines[index]);
    if (!line || seen.has(line.id) || characters + line.text.length > 500000) {
      continue;
    }
    seen.add(line.id);
    characters += line.text.length;
    lines.unshift(line);
  }
  const result = {
    revision: Number.isSafeInteger(session.revision) ? session.revision : 0,
    sessionID: String(session.sessionID || "").slice(0, 100),
    sourceTitle: String(session.sourceTitle || "").slice(0, 500),
    sourceURL: String(session.sourceURL || "").slice(0, 4096),
    observing: Boolean(session.observing),
    playbackPaused: Boolean(session.playbackPaused),
    transcriptState: ["loading", "checking", "ready", "unavailable"].includes(session.transcriptState)
      ? session.transcriptState
      : "unavailable",
    transcriptSource: session.transcriptSource === "full" ? "full" : "observed",
    transcriptReason: String(session.transcriptReason || "").slice(0, 100),
    comprehension: sanitizeComprehension(session.comprehension),
    lines
  };
  if (Number.isSafeInteger(session.currentLineID) && session.currentLineID > 0) {
    result.currentLineID = session.currentLineID;
  }
  return result;
}

function sanitizeComprehension(value) {
  const summary = value && typeof value === "object" ? value : null;
  if (!summary) {
    return null;
  }
  const fields = [
    "known_occurrences",
    "total_occurrences",
    "unknown_unique",
    "excluded_names",
    "line_count"
  ];
  if (!fields.every(function (field) {
    return Number.isSafeInteger(summary[field]) && summary[field] >= 0;
  }) || summary.known_occurrences > summary.total_occurrences) {
    return null;
  }
  return Object.fromEntries(fields.map(function (field) { return [field, summary[field]]; }));
}

function sanitizeSubtitleLine(value) {
  const line = value && typeof value === "object" ? value : {};
  const text = typeof line.text === "string" ? line.text.trim() : "";
  if (!Number.isSafeInteger(line.id) || line.id <= 0 || !text || text.length > 18000) {
    return null;
  }
  const states = new Set(["pending", "ready", "unavailable"]);
  return {
    id: line.id,
    text,
    sourcePositionMs: Number.isSafeInteger(line.sourcePositionMs) && line.sourcePositionMs >= 0
      ? line.sourcePositionMs
      : null,
    classification: states.has(line.classification) ? line.classification : "pending",
    unknowns: sanitizeSubtitleUnknowns(line.unknowns, text)
  };
}

function sanitizeSubtitleUnknowns(values, text) {
  if (!Array.isArray(values)) {
    return [];
  }
  return values.slice(0, 200).map(function (value) {
    const word = value && typeof value === "object" ? value : {};
    const surface = String(word.surface || "").slice(0, 200);
    const expression = String(word.expression || "").slice(0, 200);
    if (!surface || !expression || !Number.isSafeInteger(word.start) ||
        !Number.isSafeInteger(word.end) || word.start < 0 || word.end <= word.start ||
        word.end > text.length || text.slice(word.start, word.end) !== surface) {
      return null;
    }
    const sanitized = { surface, expression, start: word.start, end: word.end };
    if (typeof word.reading === "string" && word.reading) {
      sanitized.reading = word.reading.slice(0, 200);
    }
    return sanitized;
  }).filter(Boolean);
}
