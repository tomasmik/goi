const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const settingsModel = require("../shared/settings-model.js");
const subtitleModel = require("../shared/subtitle-model.js");
const subtitleView = require("../shared/subtitle-view.js");
const dictionaryView = require("../shared/dictionary-view.js");
const dictionaryClient = require("../shared/dictionary-client.js");
const translationModel = require("../shared/translation.js");

function fakeNode(properties = {}) {
  const listeners = new Map();
  const attributes = new Map();
  const node = {
    children: [],
    className: "",
    dataset: {},
    hidden: false,
    scrollHeight: 100,
    scrollTop: 0,
    clientHeight: 100,
    textContent: "",
    value: "",
    style: {},
    ...properties,
    classList: {
      toggle(name, enabled) {
        const names = new Set(node.className.split(/\s+/u).filter(Boolean));
        if (enabled) {
          names.add(name);
        } else {
          names.delete(name);
        }
        node.className = Array.from(names).join(" ");
      },
    },
    setAttribute(name, value) {
      attributes.set(name, String(value));
    },
    removeAttribute(name) {
      attributes.delete(name);
    },
    appendChild(child) {
      if (child.parentElement) {
        const previousIndex = child.parentElement.children.indexOf(child);
        if (previousIndex >= 0) {
          child.parentElement.children.splice(previousIndex, 1);
        }
      }
      child.parentElement = this;
      this.children.push(child);
      return child;
    },
    replaceChildren(...children) {
      this.children.forEach(function (child) { child.parentElement = undefined; });
      this.children = [];
      children.forEach((child) => this.appendChild(child));
    },
    remove() {
      if (!this.parentElement) {
        return;
      }
      const index = this.parentElement.children.indexOf(this);
      if (index >= 0) {
        this.parentElement.children.splice(index, 1);
      }
      this.parentElement = undefined;
    },
    focus() {
      this.focused = true;
    },
    scrollIntoView(options) {
      this.scrolledIntoView = options;
    },
    addEventListener(type, listener) {
      const values = listeners.get(type) || [];
      values.push(listener);
      listeners.set(type, values);
    },
    async dispatch(type, event = {}) {
      for (const listener of listeners.get(type) || []) {
        await listener({ preventDefault() {}, stopPropagation() {}, ...event });
      }
    },
  };
  return node;
}

function subtitleLine(id, text, unknowns = [], classification = "ready") {
  return {
    id,
    text,
    sourcePositionMs: id * 1000,
    sourceTitle: "Japanese lesson",
    sourceURL: "https://www.youtube.com/watch?v=test",
    classification,
    unknowns,
  };
}

function descendants(node) {
  return node.children.flatMap((child) => [child, ...descendants(child)]);
}

function createHarness(options = {}) {
  const ids = [
    "video-title", "transcript-heading", "transcript-note", "transcript-retry",
    "line-count", "unknown-only", "auto-follow", "subtitle-lines", "empty-state",
    "empty-title", "empty-detail", "batch-one-target", "capture-form", "capture-target",
    "capture-sentence", "capture-time", "capture-submit", "jump-selected", "capture-status",
    "dictionary-lookup",
    "overlay-enabled",
    "hide-native", "furigana", "hover-lookup", "hide-native-detail", "font-size", "font-size-value", "vertical-position",
    "vertical-position-value", "background-opacity", "background-opacity-value",
    "coverage-display", "settings-status", "page-status", "word-preview",
    "translate-selection", "translation-tools-toggle", "translation-tools", "translation-input",
    "translate-pasted", "translation-status", "translation-result", "batch-tools-toggle", "batch-tools",
  ];
  const elements = Object.fromEntries(ids.map((id) => [id, fakeNode({ id })]));
  elements["auto-follow"].checked = true;
  elements["word-preview"].hidden = true;
  elements["translate-pasted"].hidden = true;
  elements["translate-pasted"].textContent = "Retry";
  elements["translation-tools"].hidden = true;
  elements["batch-tools"].hidden = true;
  const displayModes = ["always", "hidden", "unknown_only", "pause_reveal"].map((value) =>
    fakeNode({ value, checked: value === "always" })
  );
  const pauseBehaviors = ["never", "on_hover", "on_selection", "after_capture"].map((value) =>
    fakeNode({ value, checked: value === "after_capture" })
  );
  const storageData = options.storageData || {};
  const messages = [];
  const timers = [];
  let session = options.session || {
    sessionID: "page-one",
    revision: 1,
    sourceTitle: "Japanese lesson",
    sourceURL: "https://www.youtube.com/watch?v=test&t=10",
    observing: true,
    lines: [],
  };
  let settings = settingsModel.sanitize(options.settings);

  const context = {
    GoiExtension: { settingsModel, subtitleModel, subtitleView, dictionaryClient, dictionaryView, translation: translationModel },
    Set,
    URL,
    URLSearchParams,
    location: { search: "?tab=7" },
    confirm: () => true,
    document: {
      hidden: false,
      body: fakeNode({ tagName: "BODY" }),
      getElementById(id) {
        return elements[id];
      },
      querySelectorAll(selector) {
        return selector === 'input[name="display-mode"]' ? displayModes : pauseBehaviors;
      },
      createElement(tagName) {
        return fakeNode({ tagName: tagName.toUpperCase(), ownerDocument: this });
      },
      createTextNode(text) {
        return fakeNode({ nodeType: 3, textContent: text, ownerDocument: this });
      },
      addEventListener() {},
    },
    window: { addEventListener() {}, getSelection() { return null; }, innerWidth: 1200, innerHeight: 800 },
    setTimeout(callback, delay) {
      const timer = { callback, delay, cancelled: false };
      timers.push(timer);
      return timer;
    },
    clearTimeout(timer) {
      if (timer) {
        timer.cancelled = true;
      }
    },
    chrome: {
      runtime: {
        sendMessage(message) {
          messages.push(message);
          if (message.type === "goi.settings.get") {
            return Promise.resolve({ ok: true, settings });
          }
          if (message.type === "goi.settings.patch") {
            settings = settingsModel.applyPatch(settings, message.patch);
            return Promise.resolve({ ok: true, settings });
          }
          if (message.type === "goi.companion.session.get") {
            if (typeof options.sessionResponse === "function") {
              return Promise.resolve(options.sessionResponse(message, session));
            }
            return Promise.resolve({ ok: true, session });
          }
          if (message.type === "goi.companion.line.capture") {
            return Promise.resolve(options.captureResponse || { ok: true, queued: false });
          }
          if (message.type === "goi.dictionary.lookup") {
            return Promise.resolve(options.dictionaryResponse || {
              ok: true,
              result: {
                query: message.expression,
                state: "ready",
                candidates: [{
                  entry_sequence: 1579510,
                  written: message.expression,
                  reading: "よみ",
                  meanings: ["meaning"],
                }],
              },
            });
          }
          if (message.type === "goi.translation.remote") {
            return Promise.resolve(options.translationResponse || {
              ok: true,
              result: { translation: "English translation", provider: "goi" },
            });
          }
          return Promise.resolve({ ok: true });
        },
      },
      storage: {
        session: {
          async get(key) {
            return Object.prototype.hasOwnProperty.call(storageData, key)
              ? { [key]: storageData[key] }
              : {};
          },
          async set(values) {
            Object.assign(storageData, values);
          },
        },
        onChanged: { addListener() {} },
      },
    },
  };

  Object.values(elements).forEach(function (element) {
    element.ownerDocument = context.document;
  });

  const source = fs.readFileSync(path.join(__dirname, "../companion/companion.js"), "utf8");
  vm.runInNewContext(source, context, { filename: "companion.js" });

  return {
    elements,
    messages,
    displayModes,
    storageData,
    setSession(nextSession) {
      session = nextSession;
    },
    setMedia(nextMedia) {
      media = nextMedia;
    },
    async refresh() {
      const index = timers.findIndex((timer) => timer.delay === 750 && !timer.cancelled);
      assert.notEqual(index, -1, "missing companion refresh timer");
      const timer = timers.splice(index, 1)[0];
      timer.callback();
      await settle();
    },
    runTimer(delay) {
      const index = timers.findIndex((timer) => timer.delay === delay && !timer.cancelled);
      assert.notEqual(index, -1, "missing " + delay + "ms timer");
      return timers.splice(index, 1)[0].callback();
    },
  };
}

test("translates pasted text automatically and only shows retry after failure", async () => {
  const harness = createHarness();
  await settle();

  harness.elements["translation-input"].value = "猫です";
  await harness.elements["translation-input"].dispatch("input");
  await harness.runTimer(500);
  await settle();

  assert.equal(harness.elements["translation-result"].textContent, "English translation");
  assert.equal(harness.elements["translation-result"].hidden, false);
  assert.equal(harness.elements["translate-pasted"].hidden, true);
  assert.equal(harness.messages.some((message) =>
    message.type === "goi.translation.remote" && message.text === "猫です"
  ), true);

  const failed = createHarness({
    translationResponse: { ok: false, errorCode: "not_connected" },
  });
  await settle();
  failed.elements["translation-input"].value = "犬です";
  await failed.elements["translation-input"].dispatch("input");
  await failed.runTimer(500);
  await settle();

  assert.equal(failed.elements["translate-pasted"].hidden, false);
  assert.equal(failed.elements["translate-pasted"].textContent, "Retry");
  assert.match(failed.elements["translation-status"].textContent, /not available in this browser/u);
  assert.match(failed.elements["translation-status"].textContent, /not connected/u);
});

test("keeps translation and batch tools collapsed until requested", async () => {
  const harness = createHarness();
  await settle();

  assert.equal(harness.elements["translation-tools"].hidden, true);
  assert.equal(harness.elements["batch-tools"].hidden, true);

  await harness.elements["translation-tools-toggle"].dispatch("click");
  assert.equal(harness.elements["translation-tools"].hidden, false);
  assert.equal(harness.elements["batch-tools"].hidden, true);
  assert.equal(harness.elements["translation-input"].focused, true);

  await harness.elements["batch-tools-toggle"].dispatch("click");
  assert.equal(harness.elements["translation-tools"].hidden, true);
  assert.equal(harness.elements["batch-tools"].hidden, false);
});

test("looks up inline subtitle words without seeking the video", async () => {
  const harness = createHarness({
    session: {
      revision: 1,
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=test",
      observing: true,
      lines: [subtitleLine(1, "猫を読む", [{ surface: "読む", expression: "読む", start: 2, end: 4 }])],
    },
  });
  await settle();

  const word = descendants(harness.elements["subtitle-lines"]).find((node) =>
    String(node.className).includes("subtitle-word--unknown")
  );
  assert.ok(word);
  await word.dispatch("click");
  await settle();

  assert.equal(harness.elements["capture-target"].value, "読む");
  assert.match(word.className, /is-selected/u);
  const lookupNodes = descendants(harness.elements["dictionary-lookup"]);
  assert.equal(lookupNodes.some((node) => node.className === "goi-dictionary-reading" && node.textContent === "よみ"), true);
  assert.equal(lookupNodes.some((node) => node.tagName === "LI" && node.textContent === "meaning"), true);
  assert.equal(harness.messages.some((message) => message.type === "goi.companion.line.seek"), false);
  assert.equal(harness.messages.some((message) => message.type === "goi.dictionary.lookup"), true);

  const select = lookupNodes.find((node) => node.className === "goi-dictionary-select");
  await select.dispatch("click");
  await harness.elements["capture-form"].dispatch("submit");
  await settle();
  const capture = harness.messages.find((message) => message.type === "goi.companion.line.capture");
  assert.equal(capture.suggestedEntrySequence, 1579510);
});

test("shows furigana in the transcript and saves the display setting", async () => {
  const harness = createHarness({
    settings: { furiganaEnabled: true },
    session: {
      revision: 1,
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=test",
      observing: true,
      lines: [subtitleLine(1, "読む", [{
        surface: "読む",
        expression: "読む",
        reading: "よむ",
        start: 0,
        end: 2,
      }])],
    },
  });
  await settle();

  const reading = descendants(harness.elements["subtitle-lines"]).find((node) => node.tagName === "RT");
  assert.equal(reading.textContent, "よ");
  assert.equal(harness.elements.furigana.checked, true);

  harness.elements.furigana.checked = false;
  await harness.elements.furigana.dispatch("change");
  await settle();
  assert.equal(harness.messages.some((message) =>
    message.type === "goi.settings.patch" && message.patch.furiganaEnabled === false
  ), true);
});

test("shows a dictionary preview on hover when enabled", async () => {
  const harness = createHarness({
    settings: { hoverLookupEnabled: true },
    session: {
      revision: 1,
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=test",
      observing: true,
      lines: [subtitleLine(1, "猫", [{ surface: "猫", expression: "猫", start: 0, end: 1 }])],
    },
  });
  await settle();

  const word = descendants(harness.elements["subtitle-lines"]).find((node) =>
    String(node.className).includes("subtitle-word--unknown")
  );
  await word.dispatch("pointerenter");
  assert.equal(harness.elements["word-preview"].hidden, true);
  harness.runTimer(120);
  await settle();

  assert.equal(harness.elements["word-preview"].hidden, false);
  assert.equal(descendants(harness.elements["word-preview"]).some((node) =>
    node.className === "goi-dictionary-term" && node.textContent === "猫"
  ), true);

  await word.dispatch("pointerleave");
  harness.runTimer(180);
  assert.equal(harness.elements["word-preview"].hidden, true);
});

test("uses only the timestamp control to seek", async () => {
  const harness = createHarness({
    session: {
      revision: 1,
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=test",
      observing: true,
      lines: [subtitleLine(1, "猫", [{ surface: "猫", expression: "猫", start: 0, end: 1 }])],
    },
  });
  await settle();

  const line = harness.elements["subtitle-lines"].children[0];
  const time = line.children[0];
  await time.dispatch("click");

  assert.equal(harness.messages.filter((message) => message.type === "goi.companion.line.seek").length, 1);
});

test("shows full-transcript comprehension without selecting the last line", async () => {
  const harness = createHarness({
    session: {
      sessionID: "page-one",
      revision: 2,
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=test",
      observing: true,
      transcriptState: "ready",
      transcriptSource: "full",
      transcriptReason: "",
      comprehension: {
        known_occurrences: 3,
        total_occurrences: 4,
        unknown_unique: 1,
        excluded_names: 0,
        line_count: 2,
      },
      lines: [subtitleLine(1000000, "猫"), subtitleLine(1000001, "犬")],
    },
  });

  await settle();

  assert.equal(harness.elements["line-count"].textContent, "2 subtitle lines · 75% known");
  assert.equal(harness.elements["capture-target"].value, "");
  assert.equal(harness.elements["capture-submit"].disabled, true);
});

test("follows and marks the current line in a full transcript", async () => {
  const harness = createHarness({
    session: {
      sessionID: "page-one",
      revision: 3,
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=test",
      observing: true,
      transcriptState: "ready",
      transcriptSource: "full",
      currentLineID: 1000001,
      comprehension: {
        known_occurrences: 2,
        total_occurrences: 2,
        unknown_unique: 0,
        excluded_names: 0,
        line_count: 2,
      },
      lines: [subtitleLine(1000000, "猫"), subtitleLine(1000001, "犬")],
    },
  });

  await settle();

  const current = harness.elements["subtitle-lines"].children[1];
  assert.match(current.className, /current/u);
  assert.equal(current.scrolledIntoView.block, "nearest");
  assert.equal(current.scrolledIntoView.behavior, "smooth");
});

test("labels observed captions honestly and offers transcript retry", async () => {
  const harness = createHarness({
    session: {
      sessionID: "page-one",
      revision: 4,
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=test",
      observing: true,
      transcriptState: "unavailable",
      transcriptSource: "observed",
      transcriptReason: "coverage_unavailable",
      lines: [subtitleLine(1, "猫")],
    },
  });

  await settle();

  assert.equal(harness.elements["transcript-heading"].textContent, "Live subtitle history");
  assert.match(harness.elements["transcript-note"].textContent, /complete comprehension score is not available/u);
  assert.equal(harness.elements["transcript-retry"].hidden, false);
  await harness.elements["transcript-retry"].dispatch("click");
  assert.equal(harness.messages.some((message) => message.type === "goi.companion.transcript.retry"), true);
});

async function settle() {
  await new Promise(setImmediate);
  await new Promise(setImmediate);
}

test("updates revised subtitle lines without replacing their DOM nodes or target input", async () => {
  const harness = createHarness({
    session: {
      revision: 1,
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=test&t=10",
      observing: true,
      lines: [subtitleLine(1, "学校", [], "pending")],
    },
  });
  await settle();
  const item = harness.elements["subtitle-lines"].children[0];
  harness.elements["capture-target"].value = "学校";
  harness.elements["capture-target"].focus();
  await harness.elements["capture-target"].dispatch("input");

  harness.setSession({
    revision: 2,
    sourceTitle: "Japanese lesson",
    sourceURL: "https://www.youtube.com/watch?v=test&t=20",
    observing: true,
    lines: [subtitleLine(1, "学校へ行く", [{ surface: "行く", expression: "行く", start: 3, end: 5 }])],
  });
  await harness.refresh();

  assert.equal(harness.elements["subtitle-lines"].children[0], item);
  assert.equal(harness.elements["capture-target"].value, "学校");
  assert.equal(harness.elements["capture-target"].focused, true);
  assert.equal(harness.elements["capture-sentence"].value, "学校へ行く");
});

test("clears the capture workspace when YouTube changes videos or sessions", async () => {
  const line = subtitleLine(1, "猫", [{ surface: "猫", expression: "猫", start: 0, end: 1 }]);
  const harness = createHarness({
    session: {
      sessionID: "page-one",
      revision: 1,
      sourceTitle: "First lesson",
      sourceURL: "https://www.youtube.com/watch?v=first",
      observing: true,
      lines: [line],
    },
    captureResponse: {
      ok: true,
      queued: false,
      captureId: 42,
      revision: 3,
      captureNonce: "a".repeat(32),
      connectionOrigin: "https://goi.example",
    },
  });
  await settle();
  await harness.elements["capture-form"].dispatch("submit");
  await settle();
  assert.notEqual(harness.elements["capture-status"].textContent, "");

  harness.setSession({
    sessionID: "page-two",
    revision: 1,
    sourceTitle: "Second lesson",
    sourceURL: "https://www.youtube.com/watch?v=second",
    observing: true,
    lines: [],
  });
  await harness.refresh();

  assert.equal(harness.elements["capture-target"].value, "");
  assert.equal(harness.elements["capture-sentence"].value, "");
  assert.equal(harness.elements["capture-time"].textContent, "");
  assert.equal(harness.elements["capture-status"].textContent, "");
  assert.equal(harness.elements["capture-submit"].disabled, true);
  assert.equal(harness.elements["jump-selected"].disabled, true);
});

test("remembers submitted 1T lines across companion reopen", async () => {
  const identity = "https://www.youtube.com/watch?v=test";
  const key = "goiCompanionSubmittedV2:7";
  const storageData = { [key]: { session: identity, instance: "page-one", lineIDs: [1] } };
  const session = {
    sessionID: "page-one",
    revision: 1,
    sourceTitle: "Japanese lesson",
    sourceURL: identity,
    observing: true,
    lines: [
      subtitleLine(1, "猫", [{ surface: "猫", expression: "猫", start: 0, end: 1 }]),
      subtitleLine(2, "犬", [{ surface: "犬", expression: "犬", start: 0, end: 1 }]),
    ],
  };
  const first = createHarness({ session, storageData });
  await settle();
  assert.equal(first.elements["batch-one-target"].textContent, "Send 1 line to mining");

  await first.elements["batch-one-target"].dispatch("click");
  await settle();

  assert.deepEqual(JSON.parse(JSON.stringify(storageData[key].lineIDs)), [1, 2]);
  assert.equal(first.messages.filter((message) =>
    message.type === "goi.companion.line.capture"
  ).length, 1);

  const reopened = createHarness({ session, storageData });
  await settle();
  assert.equal(reopened.elements["batch-one-target"].textContent, "All matching lines sent");
  assert.equal(reopened.elements["batch-one-target"].disabled, true);
});

test("does not reuse submitted line IDs after the YouTube page reloads", async () => {
  const identity = "https://www.youtube.com/watch?v=test";
  const key = "goiCompanionSubmittedV2:7";
  const storageData = {
    [key]: { session: identity, instance: "old-page", lineIDs: [1] },
  };
  const harness = createHarness({
    storageData,
    session: {
      sessionID: "new-page",
      revision: 1,
      sourceTitle: "Japanese lesson",
      sourceURL: identity,
      observing: true,
      lines: [subtitleLine(1, "犬", [{ surface: "犬", expression: "犬", start: 0, end: 1 }])],
    },
  });
  await settle();

  assert.equal(harness.elements["batch-one-target"].textContent, "Send 1 line to mining");
});

test("polls by revision without rebuilding an unchanged transcript", async () => {
  const line = subtitleLine(1, "猫", [{ surface: "猫", expression: "猫", start: 0, end: 1 }]);
  const harness = createHarness({
    session: {
      sessionID: "page-one",
      revision: 4,
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=test",
      observing: true,
      lines: [line],
    },
    sessionResponse(message, session) {
      if (message.sessionID === session.sessionID && message.sinceRevision === session.revision) {
        return { ok: true, unchanged: true };
      }
      return { ok: true, session };
    },
  });
  await settle();
  const rendered = harness.elements["subtitle-lines"].children[0];

  await harness.refresh();

  const request = harness.messages.filter((message) =>
    message.type === "goi.companion.session.get"
  ).at(-1);
  assert.equal(request.sessionID, "page-one");
  assert.equal(request.sinceRevision, 4);
  assert.equal(harness.elements["subtitle-lines"].children[0], rendered);
});

test("makes a successful mining capture unmistakable until the target changes", async () => {
  const line = subtitleLine(1, "猫と犬", [
    { surface: "猫", expression: "猫", start: 0, end: 1 },
    { surface: "犬", expression: "犬", start: 2, end: 3 },
  ]);
  const harness = createHarness({
    session: {
      sessionID: "page-one",
      revision: 1,
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=test",
      observing: true,
      lines: [line],
    },
    captureResponse: {
      ok: true,
      queued: false,
    },
  });
  await settle();

  await harness.elements["capture-form"].dispatch("submit");
  await settle();

  assert.equal(harness.elements["capture-submit"].textContent, "Sent to mining ✓");
  assert.equal(harness.elements["capture-submit"].disabled, true);
  assert.match(harness.elements["capture-submit"].className, /\bsent\b/u);
  assert.match(harness.elements["capture-status"].className, /\bsent\b/u);
  assert.equal(harness.elements["capture-status"].textContent, "“猫” sent to mining.");

  harness.elements["capture-target"].value = "犬";
  await harness.elements["capture-target"].dispatch("input");

  assert.equal(harness.elements["capture-submit"].textContent, "Send to mining");
  assert.equal(harness.elements["capture-submit"].disabled, false);
  assert.equal(harness.elements["capture-status"].textContent, "");
});

test("shows and saves subtitle position, background, and coverage detail", async () => {
  const harness = createHarness({
    settings: { verticalPercent: 62, backgroundOpacity: 0.4, coverageDisplay: "compact" },
  });
  await settle();

  assert.equal(harness.elements["vertical-position"].value, "62");
  assert.equal(harness.elements["vertical-position-value"].textContent, "Lower");
  assert.equal(harness.elements["background-opacity"].value, "0.4");
  assert.equal(harness.elements["background-opacity-value"].textContent, "40%");
  assert.equal(harness.elements["coverage-display"].value, "compact");

  harness.elements["vertical-position"].value = "71";
  await harness.elements["vertical-position"].dispatch("input");
  assert.equal(harness.elements["vertical-position-value"].textContent, "Lower");
  await harness.elements["vertical-position"].dispatch("change");
  await settle();

  harness.elements["background-opacity"].value = "0.8";
  await harness.elements["background-opacity"].dispatch("input");
  assert.equal(harness.elements["background-opacity-value"].textContent, "80%");
  await harness.elements["background-opacity"].dispatch("change");
  await settle();

  harness.elements["coverage-display"].value = "hidden";
  await harness.elements["coverage-display"].dispatch("change");
  await settle();

  const patches = harness.messages
    .filter((message) => message.type === "goi.settings.patch")
    .map((message) => message.patch);
  assert.deepEqual(JSON.parse(JSON.stringify(patches)), [
    { verticalPercent: 71 },
    { backgroundOpacity: 0.8 },
    { coverageDisplay: "hidden" },
  ]);
});

test("disables the original-caption setting outside Always mode", async () => {
  const harness = createHarness({ settings: { displayMode: "unknown_only" } });
  await settle();

  assert.equal(harness.elements["hide-native"].disabled, true);
  assert.equal(harness.elements["hide-native-detail"].hidden, false);

  harness.displayModes.forEach((input) => {
    input.checked = input.value === "always";
  });
  await harness.displayModes.find((input) => input.checked).dispatch("change");
  await settle();

  assert.equal(harness.elements["hide-native"].disabled, false);
  assert.equal(harness.elements["hide-native-detail"].hidden, true);
});

test("marks changing companion information as live", function () {
  const html = fs.readFileSync(path.join(__dirname, "../companion/companion.html"), "utf8");

  assert.match(html, /id="line-count" role="status" aria-live="polite"/u);
  assert.match(html, /role="status" aria-live="polite" aria-atomic="true"/u);
});
