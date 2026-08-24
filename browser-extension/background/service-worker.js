importScripts(
  "../shared/capture-model.js",
  "../shared/settings-model.js",
  "../shared/youtube-transcript-model.js",
  "./runtime-router.js",
  "./api-client.js",
  "./connection-manager.js",
  "./capture-outbox.js",
  "./capture-delivery.js",
  "./dictionary-cache.js",
  "./youtube-transcript-loader.js",
  "./companion-session.js",
  "./automatic-analysis.js"
);

const MENU_ID = "goi-capture-selection";
const CONNECTION_KEY = "connection";
const CAPTURE_OUTBOX_KEY = "captureOutboxV2";
const CAPTURE_OUTBOX_ALARM = "goi-capture-outbox";
const COVERAGE_BLOCK_LIMIT = 1000;
const COVERAGE_BLOCK_CHARACTER_LIMIT = 20000;
const COVERAGE_CHARACTER_LIMIT = 120000;
const COVERAGE_REQUEST_BYTE_LIMIT = 480 * 1024;
const SITE_AUTO_STORAGE_KEY = "siteAutoOriginsV1";
const GLOBAL_IGNORE_STORAGE_KEY = "globalIgnoredWordsV1";
const COMPANION_TARGETS_STORAGE_KEY = "companionTargetsV1";
const COMPANION_TARGET_LIMIT = 16;
const COMPANION_WINDOW_WIDTH = 900;
const COMPANION_WINDOW_HEIGHT = 720;
const COVERAGE_SOURCE_KEY = "__goiCoverageSourceV1";
const PLAYER_DOCUMENT_PATH = "player/player.html";
const DICTIONARY_CACHE_TTL_MS = 10 * 60 * 1000;
const DICTIONARY_CACHE_LIMIT = 200;
const POPUP_ONLY_MESSAGES = new Set([
  "goi.player.open",
  "goi.connection.get",
  "goi.connection.verify",
  "goi.connection.save",
  "goi.connection.disconnect",
  "goi.connection.test",
  "goi.site-auto.get",
  "goi.site-auto.set",
  "goi.site-auto.list",
  "goi.site-auto.remove",
  "goi.companion.open",
  "goi.capture.outbox-status",
  "goi.capture.outbox.retry",
  "goi.capture.outbox.discard",
  "goi.coverage.analyze-page"
]);
const captureModel = globalThis.GoiExtension.captureModel;
const settingsModel = globalThis.GoiExtension.settingsModel;
const runtimeRouter = globalThis.GoiExtension.runtimeRouter;
const apiClient = globalThis.GoiExtension.apiClient;
const connectionManagerModel = globalThis.GoiExtension.connectionManager;
const captureOutbox = globalThis.GoiExtension.captureOutbox;
const captureDeliveryModel = globalThis.GoiExtension.captureDelivery;
const dictionaryCacheModel = globalThis.GoiExtension.dictionaryCache;
const youtubeTranscriptModel = globalThis.GoiExtension.youtubeTranscriptModel;
const youtubeTranscriptLoader = globalThis.GoiExtension.youtubeTranscriptLoader.create(
  chrome,
  youtubeTranscriptModel
);
const isYouTubeVideoURL = youtubeTranscriptModel.isVideoURL;
const selectYouTubePlayerData = youtubeTranscriptModel.selectPlayerData;
const pendingCaptureTabs = new Set();
const pendingCoverageTabs = new Set();
const automaticAnalysisQueues = new Map();
const automaticAttemptsByTab = new Map();
const automaticCoverageByTab = new Map();
const tabNavigationStates = new Map();
let settingsUpdateQueue = Promise.resolve();
let siteAutoUpdateQueue = Promise.resolve();
let companionTargetQueue = Promise.resolve();
let playerOpenQueue = Promise.resolve();
const companionTargetByTab = new Map();
const dictionaryCache = dictionaryCacheModel.create({
  ttlMs: DICTIONARY_CACHE_TTL_MS,
  limit: DICTIONARY_CACHE_LIMIT
});
let companionTargetsLoaded = false;

const connectionManager = connectionManagerModel.create({
  chrome,
  captureModel,
  apiClient,
  fetch,
  storageKey: CONNECTION_KEY,
  getSiteOrigins: getSiteAutoOrigins,
  cancelRetry: cancelCaptureOutboxRetry,
  wakeOutbox: function (baseUrl) {
    return wakeCaptureOutbox(baseUrl);
  },
  scheduleFallbackRetry: function () {
    return scheduleFallbackCaptureRetry();
  }
});
const protectLocalStorage = connectionManager.protectStorage;
const getConnection = connectionManager.get;
const getStoredConnection = connectionManager.getStored;
const verifyConnection = connectionManager.verify;
const saveConnection = connectionManager.save;
const disconnectConnection = connectionManager.disconnect;
const removePermissionSafely = connectionManager.removePermission;
const testConnection = connectionManager.test;

const captureDelivery = captureDeliveryModel.create({
  chrome,
  captureModel,
  captureOutbox,
  apiClient,
  fetch,
  getConnection,
  protectStorage: protectLocalStorage,
  storageKey: CAPTURE_OUTBOX_KEY,
  alarmName: CAPTURE_OUTBOX_ALARM
});

function recoverQueue(operation) {
  return operation.catch(function () {
    return undefined;
  });
}

function ignoreCompanionReleaseFailure() {}

function ignorePlayerNotificationFailure() {}

chrome.runtime.onInstalled.addListener(async function () {
  await protectLocalStorage();
  chrome.contextMenus.removeAll(function () {
    chrome.contextMenus.create({
      id: MENU_ID,
      title: "Add “%s” to Goi",
      contexts: ["selection"],
      documentUrlPatterns: ["http://*/*", "https://*/*"]
    });
  });
  await runCaptureOutboxRetry(true);
  await analyzeActiveAutomaticSite();
});

chrome.runtime.onStartup.addListener(async function () {
  await protectLocalStorage();
  await runCaptureOutboxRetry(true);
  await analyzeActiveAutomaticSite();
});

chrome.alarms.onAlarm.addListener(function (alarm) {
  if (alarm.name === CAPTURE_OUTBOX_ALARM) {
    runCaptureOutboxRetry(false);
  }
});

if (typeof globalThis.addEventListener === "function") {
  globalThis.addEventListener("online", function () {
    runCaptureOutboxRetry(true);
  });
}

chrome.contextMenus.onClicked.addListener(function (info, tab) {
  if (info.menuItemId !== MENU_ID || !tab || tab.id === undefined) {
    return;
  }
  captureFromTab(tab.id, {
    frameIds: [info.frameId || 0],
    fallbackSelection: info.selectionText || ""
  });
});

chrome.commands.onCommand.addListener(async function (command) {
  if (command === "analyze-active-page") {
    try {
      await analyzeActivePage();
    } catch {}
    return;
  }
  const tabs = await chrome.tabs.query({ active: true, currentWindow: true });
  const tab = tabs[0];
  if (!tab || tab.id === undefined) {
    return;
  }
  if (command === "open-subtitle-browser") {
    try {
      await openSubtitleBrowser({ tab, url: tab.url || "" });
    } catch {}
  } else if (command === "capture-selection") {
    if (isPlayerTab(tab)) {
      await notifyPlayerCaptureHotkey(tab.id);
      return;
    }
    if (isYouTubeVideoURL(tab.url) && await captureCurrentYouTubeSelection(tab)) {
      return;
    }
    await captureFromTab(tab.id, { allFrames: true, fallbackSelection: "" });
  } else if (command === "toggle-youtube-overlay") {
    const context = siteContextForTab(tab);
    if (!context || context.kind !== "youtube") {
      return;
    }
    try {
      const current = await getSettings();
      await updateYouTubeOverlay(context, !current.overlayEnabled);
    } catch {}
  }
});

if (chrome.tabs.onUpdated) {
  chrome.tabs.onUpdated.addListener(function (tabId, changeInfo, tab) {
    if ((changeInfo.status === "loading" || changeInfo.url) &&
        chrome.action && chrome.action.setBadgeText) {
      clearCoverageBadgeBestEffort(tabId);
    }
    if (changeInfo.url && companionPageTargetID(changeInfo.url) === null) {
      releaseCompanionTab(tabId).catch(ignoreCompanionReleaseFailure);
    }
    return handleAutomaticTabUpdate(tabId, changeInfo, tab);
  });
}

if (chrome.tabs.onActivated) {
  chrome.tabs.onActivated.addListener(function (activeInfo) {
    return scheduleAutomaticAnalysis(activeInfo.tabId);
  });
}

if (chrome.tabs.onRemoved) {
  chrome.tabs.onRemoved.addListener(async function (tabId) {
    try {
      await releaseCompanionTab(tabId);
    } catch {}
    try {
      await forgetCompanionsForTarget(tabId);
    } catch {}
    automaticAnalysisQueues.delete(tabId);
    automaticAttemptsByTab.delete(tabId);
    automaticCoverageByTab.delete(tabId);
    tabNavigationStates.delete(tabId);
  });
}

const TOP_FRAME_MESSAGES = new Set([
  "goi.coverage.refresh",
  "goi.coverage.closed",
  "goi.youtube.transcript.get"
]);

const MESSAGE_HANDLERS = {
  "goi.player.open": function () {
    return openLocalPlayer();
  },
  "goi.player.connection.get": function (_message, sender) {
    return getPlayerConnection(sender);
  },
  "goi.player.coverage": async function (message, sender) {
    return { ok: true, result: await requestPlayerCoverage(message.blocks, sender) };
  },
  "goi.player.capture": async function (message, sender) {
    return { ok: true, ...await captureFromPlayer(message, sender) };
  },
  "goi.connection.get": async function () {
    const connection = await getConnection();
    return {
      ok: true,
      connection: connection
        ? { baseUrl: connection.baseUrl, connected: Boolean(connection.token) }
        : { baseUrl: "", connected: false }
    };
  },
  "goi.connection.verify": async function (message) {
    const connection = await verifyConnection(message.baseUrl, message.token);
    return { ok: true, baseUrl: connection.baseUrl };
  },
  "goi.connection.save": async function (message) {
    const connection = await saveConnection(message.baseUrl, message.token);
    return { ok: true, baseUrl: connection.baseUrl };
  },
  "goi.connection.disconnect": async function () {
    await disconnectConnection();
    return { ok: true };
  },
  "goi.connection.test": async function () {
    await testConnection();
    return { ok: true };
  },
  "goi.settings.get": async function () {
    return { ok: true, settings: await getSettings() };
  },
  "goi.settings.patch": async function (message) {
    return { ok: true, settings: await updateSettings(message.patch) };
  },
  "goi.site-auto.get": function () {
    return getSiteAutoStatus();
  },
  "goi.site-auto.set": function (message) {
    return setSiteAutoEnabled(message.enabled);
  },
  "goi.site-auto.list": function () {
    return listSiteAutoOrigins();
  },
  "goi.site-auto.remove": function (message) {
    return removeSiteAutoOrigin(message.origin, message.revokePermission);
  },
  "goi.companion.open": function (_message, sender) {
    return openSubtitleBrowser(sender);
  },
  "goi.companion.session.get": function (message, sender) {
    return getCompanionSession(message.tabId, sender, message.sinceRevision, message.sessionID);
  },
  "goi.companion.transcript.retry": function (message, sender) {
    return forwardCompanionTranscriptRetry(message.tabId, sender);
  },
  "goi.companion.line.seek": function (message, sender) {
    return forwardCompanionLineAction("goi.youtube.line.seek", message, sender);
  },
  "goi.companion.line.capture": function (message, sender) {
    return forwardCompanionLineAction("goi.youtube.line.capture", message, sender);
  },
  "goi.youtube.transcript.get": function (_message, sender) {
    return getYouTubeTranscript(sender);
  },
  "goi.capture.direct": async function (message, sender) {
    return { ok: true, ...await captureDirect(message.capture, sender) };
  },
  "goi.capture.outbox-status": async function () {
    return { ok: true, ...await captureOutboxStatus() };
  },
  "goi.capture.outbox.retry": async function () {
    await runCaptureOutboxRetry(true);
    return { ok: true, ...await captureOutboxStatus() };
  },
  "goi.capture.outbox.discard": async function () {
    await discardCaptureOutbox();
    return { ok: true, ...await captureOutboxStatus() };
  },
  "goi.coverage.analyze-page": async function () {
    const result = await analyzeActivePage();
    return { ok: true, summary: result.summary };
  },
  "goi.coverage.refresh": async function (message, sender) {
    if (message.automatic !== true) {
      automaticCoverageByTab.delete(sender.tab.id);
      await markManualCoverageBestEffort(sender.tab.id);
    }
    const result = await analyzeTab(sender.tab.id);
    return { ok: true, summary: result.summary };
  },
  "goi.coverage.classify": async function (message, sender) {
    return { ok: true, result: await classifyCoverage(message.blocks, sender) };
  },
  "goi.dictionary.lookup": async function (message) {
    return { ok: true, result: await lookupDictionary(message.expression) };
  },
  "goi.vocabulary.known": async function (message) {
    return { ok: true, result: await markVocabularyKnown(message.expression) };
  },
  "goi.translation.remote": async function (message) {
    return { ok: true, result: await translateRemotely(message.text) };
  },
  "goi.coverage.ignore.list": async function () {
    return { ok: true, words: Array.from(await getGlobalIgnoredWords()).sort() };
  },
  "goi.coverage.ignore.add": async function (message) {
    return { ok: true, words: await addGlobalIgnoredWord(message.word) };
  },
  "goi.coverage.ignore.remove": async function (message) {
    return { ok: true, words: await removeGlobalIgnoredWord(message.word) };
  },
  "goi.coverage.closed": async function (_message, sender) {
    automaticCoverageByTab.delete(sender.tab.id);
    await clearCoverageBadge(sender.tab.id);
    return { ok: true };
  }
};

function runtimeMessageError(type, error) {
  if (type === "goi.connection.save" || type === "goi.connection.disconnect" ||
      type === "goi.settings.get" || type === "goi.settings.patch") {
    return { ok: false, error: error.message };
  }
  if (type === "goi.site-auto.get" || type === "goi.site-auto.set" ||
      type === "goi.site-auto.list" || type === "goi.site-auto.remove") {
    return siteAutoErrorResponse(error);
  }
  if (type === "goi.capture.outbox-status" || type === "goi.capture.outbox.retry" ||
      type === "goi.capture.outbox.discard") {
    return { ok: false, errorCode: "storage" };
  }
  if (type === "goi.coverage.closed") {
    return { ok: false, errorCode: "server" };
  }
  return { ok: false, errorCode: captureModel.classifyAPIError(error) };
}

const handleRuntimeMessage = runtimeRouter.create({
  runtimeID: chrome.runtime.id,
  handlers: MESSAGE_HANDLERS,
  popupOnly: POPUP_ONLY_MESSAGES,
  topFrameOnly: TOP_FRAME_MESSAGES,
  popupSender,
  errorResponse: runtimeMessageError
});

chrome.runtime.onMessage.addListener(handleRuntimeMessage);

function cancelCaptureOutboxRetry() {
  captureDelivery.cancel();
  dictionaryCache.clear();
}

async function removeSitePermissionSafely(pattern) {
  try {
    const origins = await getSiteAutoOrigins();
    for (const origin of origins) {
      if (captureModel.permissionPattern(origin) === pattern) {
        return;
      }
    }
    const connection = await getStoredConnection();
    if (connection && captureModel.permissionPattern(connection.baseUrl) === pattern) {
      return;
    }
  } catch (_error) {
    return;
  }
  await removePermissionSafely(pattern);
}

async function getSettings() {
  const stored = await chrome.storage.sync.get(settingsModel.STORAGE_KEY);
  return settingsModel.sanitize(stored[settingsModel.STORAGE_KEY]);
}

function updateSettings(patch) {
  const update = settingsUpdateQueue.then(async function () {
    const current = await getSettings();
    const settings = settingsModel.applyPatch(current, patch);
    await chrome.storage.sync.set({ [settingsModel.STORAGE_KEY]: settings });
    return settings;
  });
  settingsUpdateQueue = recoverQueue(update);
  return update;
}

function unavailableSiteStatus(ok, errorCode) {
  const status = {
    ok,
    available: false,
    enabled: false,
    kind: "unavailable",
    origin: "",
    permissionPattern: ""
  };
  if (errorCode) {
    status.errorCode = errorCode;
  }
  return status;
}

function siteAutoErrorResponse(error) {
  const errorCode = error && error.code === "permission_required"
    ? "permission_required"
    : captureModel.classifyAPIError(error);
  return unavailableSiteStatus(false, errorCode);
}

function siteContextForTab(tab) {
  if (!tab || !Number.isInteger(tab.id) || typeof tab.url !== "string") {
    return null;
  }
  let pageURL;
  try {
    pageURL = new URL(tab.url);
  } catch (_error) {
    return null;
  }
  if (pageURL.protocol !== "https:" && pageURL.protocol !== "http:") {
    return null;
  }
  const permissionPattern = pageURL.protocol + "//" + pageURL.hostname + "/*";
  return {
    tabId: tab.id,
    url: pageURL.href,
    origin: pageURL.origin,
    permissionPattern,
    kind: pageURL.protocol === "https:" && pageURL.hostname === "www.youtube.com"
      ? "youtube"
      : "web"
  };
}

async function getYouTubeTranscript(sender) {
  return youtubeTranscriptLoader.get(sender);
}

function publicSiteStatus(context, enabled) {
  const status = {
    ok: true,
    available: true,
    enabled: Boolean(enabled),
    kind: context.kind,
    origin: context.origin,
    permissionPattern: context.permissionPattern
  };
  if (context.kind === "youtube") {
    status.videoAvailable = isYouTubeVideoURL(context.url);
  }
  return status;
}

async function getActiveTab() {
  const tabs = await chrome.tabs.query({ active: true, currentWindow: true });
  return tabs[0];
}

function extensionPageURL(path) {
  return chrome.runtime.getURL
    ? chrome.runtime.getURL(path)
    : "chrome-extension://" + chrome.runtime.id + "/" + path;
}

function popupSender(sender) {
  if (!sender || sender.id !== chrome.runtime.id) {
    return false;
  }
  const allowed = new Set([
    extensionPageURL("popup/popup.html"),
    extensionPageURL("options/options.html")
  ]);
  if (!allowed.has(sender.url)) {
    return false;
  }
  return !sender.tab || sender.frameId === 0;
}

function playerPageURL(value) {
  if (typeof value !== "string") {
    return false;
  }
  const expected = extensionPageURL(PLAYER_DOCUMENT_PATH);
  return value === expected || value.startsWith(expected + "#");
}

function playerSender(sender) {
  return Boolean(sender && sender.id === chrome.runtime.id && sender.tab &&
    Number.isSafeInteger(sender.tab.id) && sender.tab.id > 0 && sender.frameId === 0 &&
    playerPageURL(sender.url));
}

function requirePlayerSender(sender) {
  if (playerSender(sender)) {
    return;
  }
  const error = new Error("Local player requests require the active player page");
  error.status = 403;
  throw error;
}

function isPlayerTab(tab) {
  const url = tab && (tab.url || tab.pendingUrl);
  return Boolean(tab && Number.isSafeInteger(tab.id) && tab.id > 0 &&
    playerPageURL(url));
}

function notifyPlayerCaptureHotkey(tabId) {
  return chrome.runtime.sendMessage({
    type: "goi.player.capture-hotkey",
    version: 1,
    tabId
  }).catch(ignorePlayerNotificationFailure);
}

function openLocalPlayer() {
  const operation = playerOpenQueue.then(openOrFocusLocalPlayer);
  playerOpenQueue = recoverQueue(operation);
  return operation;
}

async function openOrFocusLocalPlayer() {
  const playerURL = extensionPageURL(PLAYER_DOCUMENT_PATH);
  let matches = [];
  try {
    matches = await chrome.tabs.query({});
  } catch (_error) {
    matches = [];
  }
  const existing = matches.find(function (tab) {
    const reportedURL = tab && (tab.url || tab.pendingUrl);
    return tab && Number.isSafeInteger(tab.id) && tab.id > 0 &&
      playerPageURL(reportedURL);
  });
  if (existing) {
    await chrome.tabs.update(existing.id, { active: true });
    if (Number.isSafeInteger(existing.windowId) && chrome.windows && chrome.windows.update) {
      await chrome.windows.update(existing.windowId, { focused: true });
    }
    return { ok: true, tabId: existing.id };
  }
  const created = await chrome.tabs.create({ url: playerURL, active: true });
  const reportedURL = created && (created.url || created.pendingUrl);
  if (!created || !Number.isSafeInteger(created.id) || created.id <= 0 ||
      (reportedURL && reportedURL !== playerURL)) {
    const error = new Error("Could not open the local player");
    error.status = 500;
    throw error;
  }
  return { ok: true, tabId: created.id };
}

async function getPlayerConnection(sender) {
  requirePlayerSender(sender);
  const connection = await getConnection();
  return {
    ok: true,
    tabId: sender.tab.id,
    connection: connection
      ? { baseUrl: connection.baseUrl, connected: true }
      : { baseUrl: "", connected: false }
  };
}

async function requestPlayerCoverage(blocks, sender) {
  requirePlayerSender(sender);
  return requestCoverage(blocks);
}

function validatePlayerSessionID(value) {
  const id = typeof value === "string" ? value : "";
  if (!/^[A-Za-z0-9_-]{16,128}$/u.test(id)) {
    const error = new Error("The local player session is invalid");
    error.status = 422;
    throw error;
  }
}

function playerVideoTitle(value) {
  const title = String(value || "").replace(/\\/gu, "/").split("/").pop();
  return captureModel.normalizeWhitespace(title);
}

function playerSourcePosition(value) {
  const position = Number(value);
  const rounded = Math.max(0, Math.round(position));
  if (!Number.isFinite(position) || !Number.isSafeInteger(rounded)) {
    const error = new Error("The local video position is invalid");
    error.status = 422;
    throw error;
  }
  return rounded;
}

async function captureFromPlayer(message, sender) {
  requirePlayerSender(sender);
  validatePlayerSessionID(message && message.sessionID);
  const capture = message && message.capture && typeof message.capture === "object"
    ? message.capture
    : {};
  const sourceTitle = playerVideoTitle(capture.sourceTitle || capture.source_title);
  const contextText = captureModel.normalizeWhitespace(capture.contextText || capture.context_text);
  if (!sourceTitle || !contextText) {
    const error = new Error("A local video and subtitle sentence are required");
    error.status = 422;
    throw error;
  }
  const payload = captureModel.buildCapturePayload({
    ...capture,
    contextText,
    sourceKind: "video",
    sourceTitle,
    sourceURL: "",
    source_url: "",
    sourcePositionMs: playerSourcePosition(
      capture.sourcePositionMs == null
        ? capture.source_position_ms
        : capture.sourcePositionMs
    )
  }, makeNonce());
  if (!payload.expression || !payload.context_text) {
    const error = new Error("A word and subtitle sentence are required");
    error.status = 422;
    throw error;
  }

  const delivery = await deliverCapture(payload);
  return { queued: delivery.queued };
}

async function captureDirect(capture, sender) {
  if (!sender.tab || sender.frameId !== 0 || !sender.url) {
    const error = new Error("Direct captures must come from an injected page script");
    error.status = 400;
    throw error;
  }
  let source;
  try {
    source = new URL(sender.url);
  } catch (_error) {
    const error = new Error("Direct captures require a valid page URL");
    error.status = 400;
    throw error;
  }
  if (source.protocol !== "https:" && source.protocol !== "http:") {
    const error = new Error("Direct captures are limited to web pages");
    error.status = 403;
    throw error;
  }
  const isYouTubeVideo = isYouTubeVideoURL(source);
  const pageCapture = capture && typeof capture === "object" ? capture : {};
  const trustedCapture = {
    ...pageCapture,
    sourceKind: isYouTubeVideo ? "video" : "web",
    sourceTitle: sender.tab.title || "",
    sourceURL: source.href,
    sourcePositionMs: isYouTubeVideo ? pageCapture.sourcePositionMs : null
  };
  const payload = captureModel.buildCapturePayload(trustedCapture, makeNonce());
  if (!payload.expression || !payload.context_text) {
    const error = new Error("A word and sentence are required");
    error.status = 422;
    throw error;
  }
  const delivery = await deliverCapture(payload);
  return { queued: delivery.queued };
}

async function classifyCoverage(blocks, sender) {
  if (!sender.tab || sender.frameId !== 0 || !sender.url) {
    const error = new Error("Coverage must come from a page script");
    error.status = 400;
    throw error;
  }
  let source;
  try {
    source = new URL(sender.url);
  } catch (_error) {
    const error = new Error("Coverage requires a valid page URL");
    error.status = 400;
    throw error;
  }
  if (source.protocol !== "https:" && source.protocol !== "http:") {
    const error = new Error("Coverage is limited to web pages");
    error.status = 403;
    throw error;
  }
  return requestCoverage(blocks);
}

async function requestCoverage(blocks) {
  const safeBlocks = validateCoverageBlocks(blocks);
  const requestBytes = new TextEncoder().encode(JSON.stringify({ blocks: safeBlocks })).byteLength;
  if (requestBytes > COVERAGE_REQUEST_BYTE_LIMIT) {
    const error = new Error("Coverage text is too large");
    error.status = 422;
    throw error;
  }
  const connection = await getConnection();
  if (!connection) {
    const error = new Error("Goi is not connected");
    error.code = "not_connected";
    throw error;
  }
  const result = await apiClient.create(fetch, connection).coverage(safeBlocks);
  return applyGlobalIgnoredWords(result, await getGlobalIgnoredWords());
}

async function lookupDictionary(expression) {
  const connection = await getConnection();
  if (!connection) {
    const error = new Error("Connect Goi before looking up words.");
    error.code = "not_connected";
    throw error;
  }
  const query = String(expression || "").trim();
  const revision = captureDelivery.currentRevision();
  return dictionaryCache.lookup(
    query,
    function () { return fetchDictionary(connection, query); },
    function () { return captureDelivery.isCurrentRevision(revision); }
  );
}

async function fetchDictionary(connection, query) {
  try {
    return await apiClient.create(fetch, connection).dictionary(query);
  } catch (error) {
    if (error && error.status === 404) {
      error.code = "dictionary_api_unavailable";
    }
    throw error;
  }
}

async function markVocabularyKnown(expression) {
  const connection = await getConnection();
  if (!connection) {
    const error = new Error("Connect Goi before marking words as known.");
    error.code = "not_connected";
    throw error;
  }
  const result = await apiClient.create(fetch, connection).markKnown(expression);
  dictionaryCache.clear();
  return result;
}

async function translateRemotely(text) {
  const connection = await getConnection();
  if (!connection) {
    const error = new Error("Connect Goi before using remote translation.");
    error.code = "not_connected";
    throw error;
  }
  return apiClient.create(fetch, connection).translate(text);
}

async function getGlobalIgnoredWords() {
  const stored = await chrome.storage.local.get(GLOBAL_IGNORE_STORAGE_KEY);
  const values = stored[GLOBAL_IGNORE_STORAGE_KEY];
  return new Set(Array.isArray(values) ? values.filter(function (word) {
    return typeof word === "string" && word.length > 0 && word.length <= 200;
  }) : []);
}

async function addGlobalIgnoredWord(value) {
  const word = String(value || "").trim().slice(0, 200);
  if (!word) {
    const error = new Error("Ignored word is required");
    error.status = 422;
    throw error;
  }
  const words = await getGlobalIgnoredWords();
  if (words.size >= 1000 && !words.has(word)) {
    const error = new Error("Ignored word limit reached");
    error.status = 422;
    throw error;
  }
  words.add(word);
  const values = Array.from(words).sort();
  await chrome.storage.local.set({ [GLOBAL_IGNORE_STORAGE_KEY]: values });
  return values;
}

async function removeGlobalIgnoredWord(value) {
  const words = await getGlobalIgnoredWords();
  words.delete(String(value || "").trim());
  const values = Array.from(words).sort();
  if (values.length) {
    await chrome.storage.local.set({ [GLOBAL_IGNORE_STORAGE_KEY]: values });
  } else {
    await chrome.storage.local.remove(GLOBAL_IGNORE_STORAGE_KEY);
  }
  return values;
}

function applyGlobalIgnoredWords(result, ignored) {
  if (!result || !result.summary || !Array.isArray(result.blocks) || !ignored.size) {
    return result;
  }
  let ignoredOccurrences = 0;
  const unknownExpressions = new Set();
  result.blocks.forEach(function (block) {
    if (!Array.isArray(block.tokens)) return;
    block.tokens.forEach(function (token) {
      if (token.status !== "unknown") return;
      const key = token.expression || token.surface;
      if (ignored.has(key)) {
        token.status = "known";
        ignoredOccurrences += 1;
      } else if (key) {
        unknownExpressions.add(key);
      }
    });
  });
  result.summary.known_occurrences += ignoredOccurrences;
  result.summary.unknown_unique = unknownExpressions.size;
  return result;
}

async function clearCoverageBadge(tabId) {
  if (chrome.action && chrome.action.setBadgeText) {
    await chrome.action.setBadgeText({ tabId, text: "" });
  }
}

function clearCoverageBadgeBestEffort(tabId) {
  return clearCoverageBadge(tabId).catch(function ignoreUnavailableTab() {});
}

function validateCoverageBlocks(blocks) {
  if (!Array.isArray(blocks) || blocks.length === 0 || blocks.length > COVERAGE_BLOCK_LIMIT) {
    const error = new Error("Coverage requires a bounded list of text blocks");
    error.status = 422;
    throw error;
  }
  const seenIDs = new Set();
  let characters = 0;
  const safeBlocks = blocks.map(function (block) {
    if (!block || !Number.isSafeInteger(block.id) || block.id <= 0 || seenIDs.has(block.id) ||
        typeof block.text !== "string" || block.text.length > COVERAGE_BLOCK_CHARACTER_LIMIT) {
      const error = new Error("Coverage text blocks are invalid");
      error.status = 422;
      throw error;
    }
    seenIDs.add(block.id);
    characters += block.text.length;
    if (characters > COVERAGE_CHARACTER_LIMIT) {
      const error = new Error("Coverage text is too large");
      error.status = 422;
      throw error;
    }
    return { id: block.id, text: block.text };
  });
  return safeBlocks;
}

const isRetryableCaptureError = captureOutbox.isRetryableError;
const deliverCapture = captureDelivery.deliver;
const runCaptureOutboxRetry = captureDelivery.runRetry;
const scheduleFallbackCaptureRetry = captureDelivery.scheduleFallbackRetry;
const captureOutboxStatus = captureDelivery.status;
const discardCaptureOutbox = captureDelivery.discard;
const enqueueCapture = captureDelivery.enqueue;
const retryCaptureOutbox = captureDelivery.retry;
const wakeCaptureOutbox = captureDelivery.wake;

function makeNonce() {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, function (byte) {
    return byte.toString(16).padStart(2, "0");
  }).join("");
}

async function ensureCaptureScript(tabId, target) {
  const states = await chrome.scripting.executeScript({
    target,
    func: function () {
      return Boolean(globalThis.GoiCapture);
    }
  });
  const missingFrameIds = states
    .filter(function (state) {
      return !state.result;
    })
    .map(function (state) {
      return state.frameId;
    });
  if (missingFrameIds.length === 0) {
    return;
  }
  const missingTarget = { tabId, frameIds: missingFrameIds };
  await chrome.scripting.executeScript({
    target: missingTarget,
    files: ["shared/capture-model.js", "content/capture-content.js"]
  });
  await chrome.scripting.insertCSS({
    target: missingTarget,
    files: ["content/capture-content.css"]
  });
}

async function ensureCoverageScript(tabId) {
  const target = { tabId, frameIds: [0] };
  const states = await chrome.scripting.executeScript({
    target,
    func: function () {
      return Boolean(globalThis.GoiCoverage);
    }
  });
  if (states[0] && states[0].result) {
    return;
  }
  await chrome.scripting.executeScript({
    target,
    files: [
      "shared/capture-model.js",
      "shared/subtitle-model.js",
      "shared/dictionary-client.js",
      "shared/dictionary-view.js",
      "content/capture-content.js",
      "content/coverage-content.js"
    ]
  });
  await chrome.scripting.insertCSS({
    target,
    files: ["content/capture-content.css", "shared/dictionary-view.css", "content/coverage-content.css"]
  });
}

async function ensureYouTubeOverlayScript(tabId) {
  const target = { tabId, frameIds: [0] };
  const states = await chrome.scripting.executeScript({
    target,
    func: function () {
      return Boolean(globalThis.GoiYouTubeOverlay);
    }
  });
  if (states[0] && states[0].result) {
    return;
  }
  await chrome.scripting.insertCSS({
    target,
    files: ["content/capture-content.css", "shared/dictionary-view.css", "youtube/overlay.css"]
  });
  await chrome.scripting.executeScript({
    target,
    files: [
      "shared/capture-model.js",
      "shared/settings-model.js",
      "shared/caption-model.js",
      "shared/subtitle-model.js",
      "shared/dictionary-view.js",
      "shared/subtitle-file-model.js",
      "content/capture-content.js",
      "youtube/overlay.js"
    ]
  });
}

async function analyzeActivePage() {
  const tabs = await chrome.tabs.query({ active: true, currentWindow: true });
  const tab = tabs[0];
  if (!tab || tab.id === undefined) {
    const error = new Error("No active page is available");
    error.status = 400;
    throw error;
  }
  automaticCoverageByTab.delete(tab.id);
  await markManualCoverageBestEffort(tab.id);
  return analyzeTab(tab.id);
}

async function analyzeTab(tabId) {
  if (pendingCoverageTabs.has(tabId)) {
    const error = new Error("Coverage analysis is already running");
    error.status = 409;
    throw error;
  }
  pendingCoverageTabs.add(tabId);
  const analysisID = makeNonce();
  let pageState;
  try {
    await ensureCoverageScript(tabId);
    const target = { tabId, frameIds: [0] };
    const collected = await chrome.scripting.executeScript({
      target,
      func: function (id) {
        return globalThis.GoiCoverage.beginAnalysis(id);
      },
      args: [analysisID]
    });
    pageState = collected[0] && collected[0].result;
    if (!pageState || pageState.analysisID !== analysisID || typeof pageState.url !== "string" ||
        !Array.isArray(pageState.blocks)) {
      const error = new Error("The page returned invalid coverage state");
      error.status = 502;
      throw error;
    }
    const blocks = pageState.blocks;
    const result = blocks.length === 0
      ? {
          summary: {
            known_occurrences: 0,
            total_occurrences: 0,
            unknown_unique: 0,
            excluded_names: 0
          },
          blocks: []
        }
      : await requestCoverage(blocks);
    const rendered = await chrome.scripting.executeScript({
      target,
      func: function (id, url, coverage) {
        return Boolean(globalThis.GoiCoverage && globalThis.GoiCoverage.finishAnalysis(id, url, coverage));
      },
      args: [analysisID, pageState.url, result]
    });
    if (!rendered[0] || !rendered[0].result) {
      const error = new Error("The page changed before coverage finished");
      error.status = 409;
      throw error;
    }
    if (chrome.action && chrome.action.setBadgeText) {
      const total = result.summary.total_occurrences;
      const percent = total
        ? captureModel.coveragePercent(result.summary.known_occurrences, total) + "%"
        : "—";
      await chrome.action.setBadgeText({ tabId, text: percent });
      if (chrome.action.setBadgeBackgroundColor) {
        await chrome.action.setBadgeBackgroundColor({ tabId, color: "#177f83" });
      }
    }
    return result;
  } catch (error) {
    await clearCoverageBadgeBestEffort(tabId);
    try {
      await chrome.scripting.executeScript({
        target: { tabId, frameIds: [0] },
        func: function (id, url) {
          if (globalThis.GoiCoverage) {
            globalThis.GoiCoverage.failAnalysis(id, url);
          }
        },
        args: [analysisID, pageState && pageState.url]
      });
    } catch (_renderError) {
      // Keep the analysis error; the tab may already be gone.
    }
    throw error;
  } finally {
    pendingCoverageTabs.delete(tabId);
  }
}

async function collectCapture(tabId, target, fallbackSelection) {
  await ensureCaptureScript(tabId, target);
  const results = await chrome.scripting.executeScript({
    target,
    func: function (fallback) {
      return {
        capture: globalThis.GoiCapture.collect(fallback),
        focused: document.hasFocus() && document.activeElement?.tagName !== "IFRAME"
      };
    },
    args: [fallbackSelection]
  });
  return selectCollectedCapture(results);
}

function selectCollectedCapture(results) {
  const captures = results
    .map(function (result) {
      return {
        frameId: result.frameId,
        focused: Boolean(result.result && result.result.focused),
        value: result.result && result.result.capture
      };
    })
    .filter(function (result) {
      return result.value && result.value.ok;
    });
  return captures.find(function (capture) {
    return capture.focused;
  }) || captures[0] || null;
}

async function showToast(tabId, frameId, state, messageCode) {
  try {
    await chrome.scripting.executeScript({
      target: { tabId, frameIds: [frameId] },
      func: function (toastState, code) {
        globalThis.GoiCapture.showToast(toastState, code);
      },
      args: [state, messageCode]
    });
  } catch (_error) {
    // Restricted pages have nowhere to show a toast.
  }
}

async function captureFromTab(tabId, options) {
  if (pendingCaptureTabs.has(tabId)) {
    return;
  }
  pendingCaptureTabs.add(tabId);
  try {
    await performCaptureFromTab(tabId, options);
  } catch (_error) {
    await showToast(
      tabId,
      options.frameIds ? options.frameIds[0] : 0,
      "error",
      "server"
    );
  } finally {
    pendingCaptureTabs.delete(tabId);
  }
}

async function performCaptureFromTab(tabId, options) {
  const target = options.allFrames
    ? { tabId, allFrames: true }
    : { tabId, frameIds: options.frameIds };
  let collected;
  try {
    collected = await collectCapture(tabId, target, options.fallbackSelection);
  } catch (_error) {
    await showToast(
      tabId,
      0,
      "error",
      "unavailable_page"
    );
    return;
  }
  if (!collected) {
    await showToast(tabId, options.frameIds ? options.frameIds[0] : 0, "error", "no_selection");
    return;
  }

  const frameId = collected.frameId;
  const connection = await getConnection();
  if (!connection) {
    await showToast(tabId, frameId, "error", "not_connected");
    return;
  }

  const payload = captureModel.buildCapturePayload(collected.value.capture, makeNonce());
  await showToast(tabId, frameId, "saving", "saving");
  try {
    const delivery = await deliverCapture(payload);
    const state = delivery.queued ? "queued" : "saved";
    await showToast(tabId, frameId, state, state);
  } catch (error) {
    await showToast(tabId, frameId, "error", captureModel.classifyAPIError(error));
  }
}
