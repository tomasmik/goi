const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const settingsModel = require("../../shared/settings-model.js");
const captionModel = require("../../shared/caption-model.js");
const captureModel = require("../../shared/capture-model.js");
const subtitleModel = require("../../shared/subtitle-model.js");
const subtitleView = require("../../shared/subtitle-view.js");
const dictionaryView = require("../../shared/dictionary-view.js");
const dictionaryClient = require("../../shared/dictionary-client.js");
const subtitleFileModel = require("../../shared/subtitle-file-model.js");

function classNames(node) {
  return new Set(String(node.className || "").split(/\s+/u).filter(Boolean));
}

function fakeNode(tagName) {
  const listeners = new Map();
  const capturedPointers = new Set();
  const node = {
    tagName: tagName.toUpperCase(),
    children: [],
    className: "",
    dataset: {},
    hidden: false,
    isConnected: false,
    focused: false,
    style: {
      values: {},
      setProperty(name, value) {
        this.values[name] = value;
      },
    },
    classList: {
      add(...names) {
        const classes = classNames(node);
        names.forEach((name) => classes.add(name));
        node.className = Array.from(classes).join(" ");
      },
      remove(...names) {
        const classes = classNames(node);
        names.forEach((name) => classes.delete(name));
        node.className = Array.from(classes).join(" ");
      },
      toggle(name, enabled) {
        const classes = classNames(node);
        if (enabled) {
          classes.add(name);
        } else {
          classes.delete(name);
        }
        node.className = Array.from(classes).join(" ");
      },
    },
    matches(selector) {
      return String(selector).split(",").some(function (part) {
        const value = part.trim();
        return value.startsWith(".") && classNames(node).has(value.slice(1));
      });
    },
    setAttribute(name, value) {
      this[name] = String(value);
    },
    appendChild(child) {
      this.children.push(child);
      child.parentElement = this;
      child.isConnected = this.isConnected;
      return child;
    },
    replaceChildren(...children) {
      this.children = children;
      children.forEach((child) => {
        child.parentElement = this;
        child.isConnected = this.isConnected;
      });
    },
    remove() {
      this.isConnected = false;
      if (this.parentElement) {
        const index = this.parentElement.children.indexOf(this);
        if (index >= 0) {
          this.parentElement.children.splice(index, 1);
        }
      }
      this.parentElement = undefined;
    },
    contains(candidate) {
      let current = candidate;
      while (current) {
        if (current === this) {
          return true;
        }
        current = current.parentElement;
      }
      return false;
    },
    focus() {
      this.focused = true;
    },
    addEventListener(type, listener) {
      if (!listeners.has(type)) {
        listeners.set(type, new Set());
      }
      listeners.get(type).add(listener);
    },
    removeEventListener(type, listener) {
      listeners.get(type)?.delete(listener);
    },
    dispatch(type, event = {}) {
      const dispatched = {
        isTrusted: true,
        preventDefault() {},
        stopPropagation() {},
        ...event,
        currentTarget: this,
      };
      Array.from(listeners.get(type) || []).forEach((listener) => listener(dispatched));
    },
    setPointerCapture(pointerID) {
      capturedPointers.add(pointerID);
    },
    hasPointerCapture(pointerID) {
      return capturedPointers.has(pointerID);
    },
    releasePointerCapture(pointerID) {
      capturedPointers.delete(pointerID);
    },
    listenerCount(type) {
      return listeners.get(type)?.size || 0;
    },
  };
  return node;
}

function descendants(node) {
  return node.children.flatMap((child) => [child, ...descendants(child)]);
}

function createHarness(initialCaption = "", options = {}) {
  const player = fakeNode("div");
  player.isConnected = true;
  player.getBoundingClientRect = () => ({ top: 0, width: 960, height: 100 });
  const video = {
    currentTime: options.currentTime || 0,
    paused: Boolean(options.videoPaused),
    pauseCount: 0,
    playCount: 0,
    listeners: new Map(),
    addEventListener(type, listener) {
      const listeners = this.listeners.get(type) || new Set();
      listeners.add(listener);
      this.listeners.set(type, listeners);
    },
    removeEventListener(type, listener) {
      this.listeners.get(type)?.delete(listener);
    },
    dispatch(type) {
      Array.from(this.listeners.get(type) || []).forEach((listener) => listener());
    },
    pause() {
      this.paused = true;
      this.pauseCount += 1;
      this.dispatch("pause");
    },
    play() {
      if (options.playFailure) {
        return Promise.reject(new Error("playback blocked"));
      }
      this.paused = false;
      this.playCount += 1;
      this.dispatch("play");
      return Promise.resolve();
    },
  };
  video.getBoundingClientRect = () => options.videoBounds || player.getBoundingClientRect();
  player.querySelector = (selector) => selector === "video" ? video : null;
  const players = [player];
  let distractorPlayer;
  if (options.distractorPlayer) {
    distractorPlayer = fakeNode("div");
    distractorPlayer.isConnected = true;
    distractorPlayer.getBoundingClientRect = () => options.distractorPlayerBounds ||
      ({ top: 0, left: 0, right: 320, bottom: 180, width: 320, height: 180 });
    const distractorVideo = {
      paused: options.distractorVideoPaused !== false,
      ended: false,
    };
    distractorPlayer.querySelector = (selector) => selector === "video" ? distractorVideo : null;
    distractorPlayer.querySelectorAll = () => [];
    players.unshift(distractorPlayer);
  }

  const documentElement = fakeNode("html");
  documentElement.lang = "en";
  documentElement.isConnected = true;
  const documentListeners = new Map();
  let activeCaption = initialCaption;
  let selection = null;
  let captionReadCount = 0;
  let playerScanCount = 0;
  function createCaptionCue() {
    let segment = fakeNode("span");
    segment.isConnected = true;
    const container = fakeNode("div");
    container.className = "caption-window";
    container.isConnected = true;
    container.getClientRects = () => activeCaption ? [{}] : [];
    container.closest = (selector) => selector === ".html5-video-player" ? player : null;
    container.querySelectorAll = (selector) => {
      if (selector !== ".ytp-caption-segment" || !activeCaption) {
        return [];
      }
      segment.textContent = activeCaption;
      return [segment];
    };
    container.replaceSegment = function () {
      segment.isConnected = false;
      segment = fakeNode("span");
      segment.isConnected = true;
    };
    return container;
  }
  let captionContainer = createCaptionCue();
  const simultaneousCaptionContainers = (options.simultaneousCaptions || []).map(function (text) {
    const segment = fakeNode("span");
    segment.isConnected = true;
    segment.textContent = text;
    const container = fakeNode("div");
    container.isConnected = true;
    container.getClientRects = () => text ? [{}] : [];
    container.closest = (selector) => selector === ".html5-video-player" ? player : null;
    container.querySelectorAll = (selector) => selector === ".ytp-caption-segment" ? [segment] : [];
    return container;
  });
  function visibleCaptionContainers() {
    return [activeCaption ? captionContainer : null, ...simultaneousCaptionContainers].filter(Boolean);
  }
  player.querySelectorAll = (selector) => {
    if (selector === ".caption-window") {
      captionReadCount += 1;
    }
    if (selector === ".caption-window" || selector === ".ytp-caption-window-container") {
      return visibleCaptionContainers();
    }
    return [];
  };
  const document = {
    title: "Japanese lesson - YouTube",
    documentElement,
    createElement(tagName) {
      const node = fakeNode(tagName);
      node.ownerDocument = this;
      return node;
    },
    createDocumentFragment() {
      const node = fakeNode("fragment");
      node.ownerDocument = this;
      return node;
    },
    createTextNode(text) {
      const node = fakeNode("text");
      node.textContent = text;
      node.ownerDocument = this;
      return node;
    },
    querySelector(selector) {
      return selector === ".html5-video-player" ? players[0] : null;
    },
    querySelectorAll(selector) {
      if (selector === ".html5-video-player") {
        playerScanCount += 1;
        return players;
      }
      if (selector === ".caption-window") {
        captionReadCount += 1;
      }
      if (selector === ".caption-window") {
        return visibleCaptionContainers();
      }
      return [];
    },
    addEventListener(type, listener) {
      if (!documentListeners.has(type)) {
        documentListeners.set(type, new Set());
      }
      documentListeners.get(type).add(listener);
    },
    removeEventListener(type, listener) {
      documentListeners.get(type)?.delete(listener);
    },
    dispatch(type, event) {
      Array.from(documentListeners.get(type) || []).forEach((listener) => listener(event));
    },
    listenerCount(type) {
      return documentListeners.get(type)?.size || 0;
    },
  };

  const messages = [];
  let storedSettings = settingsModel.sanitize(options.persistedSettings);
  let runtimeListener;
  let storageListener;
  let mutationListener;
  const chrome = {
    runtime: {
      sendMessage(message) {
        messages.push(message);
        if (message.type === "goi.settings.get") {
          return Promise.resolve({ ok: true, settings: storedSettings });
        }
        if (message.type === "goi.settings.patch") {
          if (options.patchFailure === "reject") {
            return Promise.reject(new Error("storage unavailable"));
          }
          if (options.patchFailure === "response") {
            return Promise.resolve({ ok: false, error: "storage unavailable" });
          }
          storedSettings = settingsModel.applyPatch(storedSettings, message.patch);
          return Promise.resolve({ ok: true, settings: storedSettings });
        }
        if (message.type === "goi.youtube.transcript.get") {
          const response = typeof options.transcriptResponse === "function"
            ? options.transcriptResponse(message)
            : options.transcriptResponse;
          return Promise.resolve(response || {
            ok: true,
            state: "unavailable",
            reason: "no_japanese_track",
          });
        }
        if (message.type === "goi.capture.direct" && options.captureResponse) {
          return Promise.resolve(typeof options.captureResponse === "function"
            ? options.captureResponse(message)
            : options.captureResponse);
        }
        if (message.type === "goi.dictionary.lookup") {
          return Promise.resolve(options.dictionaryResponse || {
            ok: true,
            result: {
              query: message.expression,
              state: "ready",
              candidates: [{
                entry_sequence: 9001,
                written: message.expression,
                reading: "よみ",
                global_rank: 80,
                novel_rank: 180,
                meanings: ["meaning"],
                senses: [{ parts_of_speech: ["noun"], meanings: ["meaning"] }],
              }],
            },
          });
        }
        if (message.type === "goi.vocabulary.known") {
          return Promise.resolve(options.knownResponse || {
            ok: true,
            result: { state: "marked_known" },
          });
        }
        if (message.type === "goi.coverage.classify") {
          if (options.coverageFailure) {
            return Promise.reject(new Error("coverage unavailable"));
          }
          const result = typeof options.coverageResponse === "function"
            ? options.coverageResponse(message.blocks)
            : options.coverageResponse;
          return Promise.resolve(result).then((resolved) => resolved
            ? { ok: true, result: resolved }
            : { ok: false, errorCode: "not_connected" });
        }
        return Promise.resolve({ ok: true });
      },
      onMessage: {
        addListener(listener) {
          runtimeListener = listener;
        },
      },
    },
    storage: {
      onChanged: {
        addListener(listener) {
          storageListener = listener;
        },
      },
    },
  };
  const timers = [];
  let nextTimerID = 1;
  const context = {
    GoiExtension: {
      settingsModel,
      captionModel,
      captureModel,
      subtitleModel,
      subtitleView,
      subtitleFileModel,
      dictionaryClient,
      dictionaryView,
    },
    chrome,
    document,
    location: { href: "https://www.youtube.com/watch?v=test" },
    URL,
    navigator: { language: "en" },
    window: { getSelection: () => selection },
    MutationObserver: class {
      constructor(listener) {
        mutationListener = listener;
      }
      observe() {}
    },
    setTimeout(callback, delay) {
      const id = nextTimerID;
      nextTimerID += 1;
      timers.push({ callback, delay, id, cancelled: false });
      return id;
    },
    clearTimeout(id) {
      const timer = timers.find((candidate) => candidate.id === id);
      if (timer) {
        timer.cancelled = true;
      }
    },
    requestAnimationFrame(callback) {
      callback();
    },
  };
  const source = fs.readFileSync(path.join(__dirname, "../../youtube/overlay.js"), "utf8");
  vm.runInNewContext(source, context, { filename: "overlay.js" });

  function runTimer(delay) {
    const timerIndex = timers.findIndex((timer) => timer.delay === delay && !timer.cancelled);
    assert.notEqual(timerIndex, -1, `missing ${delay}ms timer`);
    timers.splice(timerIndex, 1)[0].callback();
  }

  return {
    document,
    messages,
    player,
    distractorPlayer,
    video,
    storageListener,
    async start() {
      await new Promise(setImmediate);
      if (timers.some((timer) => timer.delay === 16 && !timer.cancelled)) {
        runTimer(16);
      }
    },
    activeCaption() {
      return context.GoiYouTubeOverlay.getActiveCaption();
    },
    coverage() {
      return context.GoiYouTubeOverlay.getCoverage();
    },
    captionReadCount() {
      return captionReadCount;
    },
    playerScanCount() {
      return playerScanCount;
    },
    clearCaption() {
      activeCaption = "";
    },
    setCaption(nextCaption) {
      activeCaption = nextCaption;
    },
    replaceCaptionCue(nextCaption) {
      activeCaption = nextCaption;
      captionContainer = createCaptionCue();
    },
    replaceCaptionSegment(nextCaption) {
      activeCaption = nextCaption;
      captionContainer.replaceSegment();
    },
    removePlayerCandidate() {
      players.length = 0;
      player.isConnected = false;
    },
    restorePlayerCandidate() {
      player.isConnected = true;
      players.push(player);
    },
    selectCaption(surface) {
      const captionNode = descendants(player).find((node) =>
        classNames(node).has("goi-ext-caption-text")
      );
      const textNode = captionNode.children[0];
      captionNode.dispatch("pointerdown");
      selection = {
        anchorNode: textNode,
        focusNode: textNode,
        isCollapsed: false,
        rangeCount: 1,
        toString: () => surface,
      };
      captionNode.dispatch("pointerup");
    },
    selectOutsideCaption(surface) {
      const outsideNode = fakeNode("text");
      selection = {
        anchorNode: outsideNode,
        focusNode: outsideNode,
        isCollapsed: false,
        rangeCount: 1,
        toString: () => surface,
      };
      document.dispatch("selectionchange");
    },
    pointOutsideOverlay() {
      document.dispatch("pointerdown", { target: fakeNode("div") });
    },
    hoverCaption() {
      const captionNode = descendants(player).find((node) =>
        classNames(node).has("goi-ext-caption-text")
      );
      captionNode.dispatch("pointerenter");
    },
    leaveCaption() {
      const captionNode = descendants(player).find((node) =>
        classNames(node).has("goi-ext-caption-text")
      );
      captionNode.dispatch("pointerleave");
    },
    navigate(nextURL) {
      context.location.href = nextURL;
      document.dispatch("yt-navigate-finish");
    },
    readCaption() {
      mutationListener([{ target: captionContainer, addedNodes: [], removedNodes: [] }]);
      runTimer(16);
    },
    unrelatedMutation() {
      mutationListener([{ target: fakeNode("div"), addedNodes: [], removedNodes: [] }]);
    },
    async analyzeCaption() {
      runTimer(80);
      await new Promise(setImmediate);
    },
    sendRuntimeMessage(message) {
      runtimeListener(message, {}, function () {});
    },
    requestRuntimeMessage(message) {
      return new Promise(function (resolve) {
        let responded = false;
        const keepChannel = runtimeListener(message, {}, function (response) {
          responded = true;
          resolve(response);
        });
        if (!keepChannel && !responded) {
          resolve(undefined);
        }
      });
    },
    runTimer,
    advanceTime(milliseconds) {
      const due = timers.filter((timer) => timer.delay <= milliseconds && !timer.cancelled);
      due.forEach(function (timer) {
        const index = timers.indexOf(timer);
        if (index >= 0) {
          timers.splice(index, 1);
        }
        timer.callback();
      });
    },
    pendingTimerIDs(delay) {
      return timers
        .filter((timer) => timer.delay === delay && !timer.cancelled)
        .map((timer) => timer.id);
    },
  };
}

module.exports = {
  classNames,
  createHarness,
  descendants,
  settingsModel,
};
