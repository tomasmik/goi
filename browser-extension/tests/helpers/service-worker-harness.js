const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const captureModel = require("../../shared/capture-model.js");

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
  const storage = options.localStorage || {};
  if (initialConnection) storage.connection = initialConnection;
  const sessionStorage = sharedSessionStorage || {};
  const addListener = function () {};
  const alarms = new Map();
  const command = testEvent();
  const tabUpdated = testEvent();
  const tabActivated = testEvent();
  const tabRemoved = testEvent();
  const runtimeMessages = [];
  let runtimeListener;
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
      getManifest() { return require("../../manifest.json"); },
      getURL(pathname) {
        return "chrome-extension://goi-test/" + pathname;
      },
      onInstalled: { addListener },
      onMessage: { addListener(listener) { runtimeListener = listener; } },
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
    chrome,
    crypto,
    clearTimeout: options.clearTimeout || clearTimeout,
    fetch: options.fetch || fetch,
    globalThis: null,
    setTimeout: options.setTimeout || setTimeout,
    TextEncoder,
    TextDecoder,
    URL,
  };
  context.globalThis = context;
  const backgroundDirectory = path.join(__dirname, "../../background");
  vm.createContext(context);
  context.importScripts = function (...files) {
    for (const file of files) {
      vm.runInContext(fs.readFileSync(path.join(backgroundDirectory, file), "utf8"), context, {
        filename: file,
      });
      if (file === "./api-client.js" && apiClient) context.GoiExtension.apiClient = apiClient;
    }
  };
  vm.runInContext(fs.readFileSync(path.join(backgroundDirectory, "service-worker.js"), "utf8"), context, {
    filename: "service-worker.js",
  });
  vm.runInContext(`
globalThis.__workerTest = {
  captureDelivery,
  automaticAnalysis,
  getConnection,
  saveConnection,
  verifyConnection,
  disconnectConnection,
  updateSettings,
  selectCollectedCapture,
  performCaptureFromTab,
  captureDirect,
  classifyCoverage,
  requestCoverage,
  analyzeTab,
  runCaptureOutboxRetry,
  captureOutboxStatus,
  discardCaptureOutbox,
  getGlobalIgnoredWords,
  addGlobalIgnoredWord,
  removeGlobalIgnoredWord,
  openSubtitleBrowser,
  getCompanionSession,
  forwardCompanionLineAction,
  forwardCompanionTranscriptRetry,
  sanitizeSubtitleSession,
  captureCurrentYouTubeSelection,
  getYouTubeTranscript,
  popupSender,
  companionSender,
  companionPageTargetID,
  playerSender,
  openLocalPlayer,
  getPlayerConnection,
  requestPlayerCoverage,
  captureFromPlayer,
};`, context);
  return {
    alarms,
    chrome,
    events: { command, tabActivated, tabRemoved, tabUpdated },
    operations: context.__workerTest,
    handleRuntimeMessage: runtimeListener,
    dispatch(message, sender) {
      return new Promise(function (resolve) {
        if (!runtimeListener(message, sender, resolve)) resolve(undefined);
      });
    },
    delivery: context.__workerTest.captureDelivery,
    automaticAnalysis: context.__workerTest.automaticAnalysis,
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
