const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const captureModel = require("../../shared/capture-model.js");
const settingsModel = require("../../shared/settings-model.js");
const runtimeRouter = require("../../background/runtime-router.js");
const connectionManager = require("../../background/connection-manager.js");
const captureOutbox = require("../../background/capture-outbox.js");
const captureDelivery = require("../../background/capture-delivery.js");
const dictionaryCache = require("../../background/dictionary-cache.js");
const youtubeTranscriptModel = require("../../shared/youtube-transcript-model.js");
const youtubeTranscriptLoader = require("../../background/youtube-transcript-loader.js");

function testEvent() {
  const listeners = [];
  return {
    addListener(listener) {
      listeners.push(listener);
    },
    async dispatch(...args) {
      await Promise.all(listeners.map(function (listener) {
        return listener(...args);
      }));
    },
  };
}

function companionMessageSender(targetTabID = 4, companionTabID = 90) {
  const url = `chrome-extension://goi-test/companion/companion.html?tab=${targetTabID}`;
  return {
    id: "goi-test",
    tab: { id: companionTabID, url },
    frameId: 0,
    url,
  };
}

function workerHarness(initialConnection, syncStorage, apiClient, sharedSessionStorage, options = {}) {
  const storage = initialConnection ? { connection: initialConnection } : {};
  const sessionStorage = sharedSessionStorage || {};
  const addListener = function () {};
  const alarms = new Map();
  const command = testEvent();
  const tabUpdated = testEvent();
  const tabActivated = testEvent();
  const tabRemoved = testEvent();
  const runtimeMessages = [];
  const chrome = {
    alarms: {
      async clear(name) {
        return alarms.delete(name);
      },
      create(name, schedule) {
        alarms.set(name, schedule);
      },
      onAlarm: { addListener },
    },
    commands: { onCommand: command },
    contextMenus: {
      create() {},
      onClicked: { addListener },
      removeAll() {},
    },
    permissions: {
      async contains() {
        return false;
      },
      async remove() {
        throw new Error("permission cleanup failed");
      },
    },
    runtime: {
      id: "goi-test",
      getURL(pathname) {
        return "chrome-extension://goi-test/" + pathname;
      },
      onInstalled: { addListener },
      onMessage: { addListener },
      onStartup: { addListener },
      async sendMessage(message) {
        runtimeMessages.push(message);
        return undefined;
      },
    },
    scripting: {},
    storage: {
      local: {
        async get(key) {
          return { [key]: storage[key] };
        },
        async remove(key) {
          delete storage[key];
        },
        async set(values) {
          Object.assign(storage, values);
        },
        async setAccessLevel() {},
      },
      session: {
        async get(key) {
          return { [key]: sessionStorage[key] };
        },
        async set(values) {
          Object.assign(sessionStorage, values);
        },
      },
      sync: syncStorage || {},
    },
    tabs: {
      onActivated: tabActivated,
      onRemoved: tabRemoved,
      onUpdated: tabUpdated,
    },
    windows: {},
  };
  const context = {
    AbortController,
    GoiExtension: {
      apiClient: apiClient || {},
      connectionManager,
      captureDelivery,
      captureOutbox,
      captureModel,
      dictionaryCache,
      runtimeRouter,
      settingsModel,
      youtubeTranscriptModel,
      youtubeTranscriptLoader,
    },
    chrome,
    crypto,
    clearTimeout: options.clearTimeout || clearTimeout,
    fetch: options.fetch || fetch,
    globalThis: null,
    importScripts() {},
    setTimeout: options.setTimeout || setTimeout,
    TextEncoder,
    URL,
  };
  context.globalThis = context;
  const backgroundDirectory = path.join(__dirname, "../../background");
  const source = [
    "companion-session.js",
    "automatic-analysis.js",
    "service-worker.js",
  ].map(function (name) {
    return fs.readFileSync(path.join(backgroundDirectory, name), "utf8");
  }).join("\n") + `
globalThis.__workerTest = {
  getConnection,
  saveConnection,
  verifyConnection,
  disconnectConnection,
  updateSettings,
  getSiteAutoStatus,
  setSiteAutoEnabled,
  listSiteAutoOrigins,
  removeSiteAutoOrigin,
  runAutomaticSiteAnalysis,
  scheduleAutomaticAnalysis,
  selectCollectedCapture,
  performCaptureFromTab,
  captureDirect,
  classifyCoverage,
  requestCoverage,
  analyzeTab,
  enqueueCapture,
  retryCaptureOutbox,
  runCaptureOutboxRetry,
  captureOutboxStatus,
  discardCaptureOutbox,
  getGlobalIgnoredWords,
  addGlobalIgnoredWord,
  removeGlobalIgnoredWord,
  isRetryableCaptureError,
  openSubtitleBrowser,
  loadCompanionTargets,
  associateCompanionTab,
  releaseCompanionTab,
  getCompanionSession,
  forwardCompanionLineAction,
  forwardCompanionTranscriptRetry,
  sanitizeSubtitleSession,
  captureCurrentYouTubeSelection,
  selectYouTubePlayerData,
  getYouTubeTranscript,
  handleRuntimeMessage,
  popupSender,
  companionSender,
  companionPageTargetID,
  playerSender,
  openLocalPlayer,
  getPlayerConnection,
  requestPlayerCoverage,
  captureFromPlayer,
};`;
  vm.runInNewContext(source, context, { filename: "service-worker.js" });
  return {
    alarms,
    chrome,
    events: { command, tabActivated, tabRemoved, tabUpdated },
    operations: context.__workerTest,
    runtimeMessages,
    sessionStorage,
    storage,
  };
}

module.exports = {
  captureModel,
  companionMessageSender,
  workerHarness,
};
