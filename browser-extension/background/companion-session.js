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

async function requireCompanionTarget(tabId, sender) {
  if (!companionSender(sender) || companionPageTargetID(sender.url) !== tabId ||
      sender.tab.id === tabId) {
    throw companionError("Subtitle browser ownership is invalid", 403);
  }
  let tab;
  try {
    tab = await chrome.tabs.get(tabId);
  } catch (_error) {
    throw companionError("The YouTube tab is no longer available", 404);
  }
  if (tab.id !== tabId) {
    throw companionError("Subtitle browser ownership is invalid", 403);
  }
  if (!isYouTubeVideoURL(tab.url)) {
    throw companionError("Open a YouTube video to use the transcript browser", 400);
  }
  return tab;
}

function companionError(message, status) {
  const error = new Error(message);
  error.status = status;
  return error;
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
  const context = automaticAnalysisModel.siteContextForTab(tab);
  if (!context || !Number.isSafeInteger(context.tabId) || context.tabId <= 0 ||
      !isYouTubeVideoURL(context.url)) {
    throw companionError("Open a YouTube video to use the transcript browser", 400);
  }
  await ensureYouTubeOverlayScript(context.tabId);
  const baseURL = extensionPageURL("companion/companion.html");
  const pageURL = baseURL + "?tab=" + context.tabId;
  let existing = [];
  try {
    existing = await chrome.tabs.query({ url: baseURL + "*" });
  } catch (_error) {}
  const existingTab = existing.find(function (candidate) {
    return Number.isSafeInteger(candidate.id) && candidate.id > 0;
  });
  if (existingTab) {
    await chrome.tabs.update(existingTab.id, { active: true, url: pageURL });
    await restoreCompanionWindow(existingTab.windowId);
    return { ok: true, tabId: context.tabId };
  }
  let createdTab;
  if (!chrome.windows || !chrome.windows.create) {
    createdTab = await chrome.tabs.create({ url: pageURL, active: true });
  } else {
    const createdWindow = await chrome.windows.create({
      url: pageURL,
      type: "popup",
      width: COMPANION_WINDOW_WIDTH,
      height: COMPANION_WINDOW_HEIGHT,
      focused: true
    });
    createdTab = createdWindow && Array.isArray(createdWindow.tabs)
      ? createdWindow.tabs.find(function (candidate) { return Number.isSafeInteger(candidate.id); })
      : null;
  }
  if (!createdTab || !Number.isSafeInteger(createdTab.id) || createdTab.id <= 0) {
    throw companionError("Could not connect the transcript browser", 500);
  }
  return { ok: true, tabId: context.tabId };
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
  await requireCompanionTarget(tabId, sender);
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
  const tabId = message && message.tabId;
  await requireCompanionTarget(tabId, sender);
  const lineID = message && message.lineID;
  if (!Number.isSafeInteger(lineID) || lineID <= 0) {
    throw companionError("The subtitle line is invalid", 400);
  }
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
  const tab = await requireCompanionTarget(tabId, sender);
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
