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
  const html = fs.readFileSync(path.join(__dirname, "../companion/companion.html"), "utf8");
  const ids = Array.from(html.matchAll(/\bid="([^"]+)"/gu), (match) => match[1]);
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

async function settle() {
  await new Promise(setImmediate);
  await new Promise(setImmediate);
}

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
