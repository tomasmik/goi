const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const captureModel = require("../shared/capture-model.js");
const dictionaryClient = require("../shared/dictionary-client.js");
const dictionaryView = require("../shared/dictionary-view.js");
const subtitleModel = require("../shared/subtitle-model.js");

const { workerHarness } = require("./helpers/service-worker-harness.js");

const excludedTags = new Set([
  "SCRIPT", "STYLE", "NOSCRIPT", "TEMPLATE", "TEXTAREA", "INPUT", "SELECT",
  "OPTION", "BUTTON", "CODE", "PRE", "KBD", "SAMP", "RT", "RP", "SVG",
  "CANVAS", "NAV", "ASIDE", "FOOTER",
]);
const readingTags = new Set([
  "P", "LI", "BLOCKQUOTE", "H1", "H2", "H3", "H4", "H5", "H6", "DT",
  "DD", "TD", "TH", "CAPTION", "FIGCAPTION", "DIV", "SECTION", "ARTICLE",
]);
const interactiveTags = new Set([
  "A", "BUTTON", "INPUT", "TEXTAREA", "SELECT", "OPTION", "LABEL", "SUMMARY",
]);

function matches(node, selector) {
  if (selector.startsWith("script, style")) {
    return excludedTags.has(node.tagName) ||
      (node.contentEditable !== undefined && node.contentEditable !== "false") ||
      node.dataset.goiCoverageUi === "true";
  }
  if (selector.startsWith("p, li")) {
    return readingTags.has(node.tagName);
  }
  if (selector.startsWith("a, button")) {
    return interactiveTags.has(node.tagName);
  }
  if (selector === "[data-goi-coverage-ui]") {
    return node.dataset.goiCoverageUi === "true";
  }
  if (selector === "ruby") return node.tagName === "RUBY";
  return false;
}

function setConnected(node, connected) {
  node.isConnected = connected;
  (node.children || []).forEach((child) => setConnected(child, connected));
  if (node.shadowRoot) setConnected(node.shadowRoot, connected);
}

function element(tagName) {
  const listeners = new Map();
  const classes = new Set();
  const node = {
    tagName: tagName.toUpperCase(),
    children: [],
    dataset: {},
    hidden: false,
    isConnected: true,
    style: {},
    attachShadow() {
      this.shadowRoot = element("shadow-root");
      return this.shadowRoot;
    },
    classList: {
      add(...names) {
        names.forEach((name) => classes.add(name));
      },
      contains(name) {
        return classes.has(name);
      },
      remove(...names) {
        names.forEach((name) => classes.delete(name));
      },
      toggle(name, force) {
        const enabled = force === undefined ? !classes.has(name) : Boolean(force);
        if (enabled) {
          classes.add(name);
        } else {
          classes.delete(name);
        }
        return enabled;
      },
    },
    append(...children) {
      children.forEach((child) => this.appendChild(child));
    },
    appendChild(child) {
      this.children.push(child);
      child.parentElement = this;
      setConnected(child, this.isConnected);
      return child;
    },
    replaceChildren(...children) {
      this.children.forEach((child) => setConnected(child, false));
      this.children = [];
      this.append(...children);
    },
    addEventListener(type, listener) {
      if (!listeners.has(type)) {
        listeners.set(type, new Set());
      }
      listeners.get(type).add(listener);
    },
    dispatch(type, event = {}) {
      const dispatched = {
        isTrusted: true,
        preventDefault() {},
        stopPropagation() {},
        target: this,
        ...event,
        currentTarget: this,
      };
      Array.from(listeners.get(type) || []).forEach((listener) => listener(dispatched));
    },
    setAttribute(name, value) {
      this[name] = String(value);
    },
    closest(selector) {
      let candidate = this;
      while (candidate) {
        if (matches(candidate, selector)) {
          return candidate;
        }
        candidate = candidate.parentElement;
      }
      return null;
    },
    contains(candidate) {
      while (candidate) {
        if (candidate === this) {
          return true;
        }
        candidate = candidate.parentElement;
      }
      return false;
    },
    getClientRects() {
      return [{}];
    },
    getBoundingClientRect() {
      return this.bounds || { left: 0, top: 0, right: 390, bottom: 180, width: 390, height: 180 };
    },
    focus() {
      this.focused = true;
    },
    remove() {
      setConnected(this, false);
      if (this.parentElement) {
        const index = this.parentElement.children.indexOf(this);
        if (index >= 0) {
          this.parentElement.children.splice(index, 1);
        }
      }
      this.parentElement = undefined;
    },
  };
  return node;
}

function text(value) {
  return {
    nodeValue: value,
    textContent: value,
    isConnected: true,
  };
}

function descendantTextNodes(root) {
  const nodes = [];
  (root.children || []).forEach(function visit(child) {
    if (typeof child.nodeValue === "string") {
      nodes.push(child);
      return;
    }
    (child.children || []).forEach(visit);
  });
  return nodes;
}

function descendants(root) {
  return [...(root.children || []), ...(root.shadowRoot ? [root.shadowRoot] : [])]
    .flatMap((child) => [child, ...descendants(child)]);
}

function createHarness(content, options = {}) {
  const root = element("main");
  const documentElement = element("html");
  documentElement.lang = "ja";
  documentElement.appendChild(root);
  const references = {};
  if (typeof content === "function") {
    content({ root, element, text, references });
  } else {
    const paragraph = element("p");
    references.textNode = paragraph.appendChild(text(content));
    root.appendChild(paragraph);
  }

  const ranges = [];
  let rejectRanges = false;
  class FakeRange {
    constructor() {
      ranges.push(this);
    }
    setStart(node, offset) {
      if (rejectRanges) {
        throw new Error("stale text node");
      }
      this.startNode = node;
      this.startContainer = node;
      this.startOffset = offset;
    }
    setEnd(node, offset) {
      this.endNode = node;
      this.endOffset = offset;
    }
    getBoundingClientRect() {
      return this.bounds || { top: 0, right: 60, bottom: 40, left: 0 };
    }
    getClientRects() {
      return this.rects || [this.getBoundingClientRect()];
    }
  }

  const highlights = new Map();
  const documentListeners = new Map();
  let caret;
  let selection;
  const confirmations = [];
  let walkerReads = 0;
  const document = {
    body: root,
    documentElement,
    title: "Reading",
    querySelector(selector) {
      if (selector === "main, [role='main']") {
        return root;
      }
      return null;
    },
    querySelectorAll(selector) {
      if (selector === "main, [role='main']") {
        return references.rootCandidates || [root];
      }
      if (selector === "article") {
        return [];
      }
      return [];
    },
    createTreeWalker(walkRoot) {
      const nodes = descendantTextNodes(walkRoot);
      let index = 0;
      return {
        nextNode() {
          walkerReads += 1;
          return nodes[index++] || null;
        },
      };
    },
    createElement: element,
    addEventListener(type, listener) {
      if (!documentListeners.has(type)) {
        documentListeners.set(type, new Set());
      }
      documentListeners.get(type).add(listener);
    },
    dispatch(type, event = {}) {
      const dispatched = {
        isTrusted: true,
        preventDefault() {},
        stopPropagation() {},
        target: root,
        ...event,
      };
      Array.from(documentListeners.get(type) || []).forEach((listener) => listener(dispatched));
    },
    caretRangeFromPoint() {
      return caret && { startContainer: caret.node, startOffset: caret.offset };
    },
  };
  const messages = [];
  const storageData = options.storageData || {};
  let messageResponse = { ok: true, queued: false };
  const worker = workerHarness(null, null, null, null, { localStorage: storageData });
  const context = {
    chrome: {
      runtime: {
        sendMessage(message) {
          if (message.type.startsWith("goi.coverage.panel.")) {
            return new Promise(function (resolve) {
              worker.handleRuntimeMessage(message, {
                id: "goi-test", frameId: 0,
                url: "https://reader.example/chapter",
                tab: { id: 1, url: "https://reader.example/chapter" },
              }, resolve);
            });
          }
          messages.push(message);
          return Promise.resolve(
            typeof messageResponse === "function" ? messageResponse(message) : messageResponse,
          );
        },
      },
      storage: {
        local: {
          get(key) {
            throw new Error("Content scripts cannot access trusted local storage");
          },
          set(values) {
            throw new Error("Content scripts cannot access trusted local storage");
          },
        },
      },
    },
    CSS: { highlights },
    GoiExtension: {
      captureModel,
      dictionaryClient,
      dictionaryView,
      subtitleModel,
    },
    Highlight: class {
      constructor(...values) {
        this.values = values;
      }
    },
    Range: FakeRange,
    NodeFilter: { SHOW_TEXT: 4 },
    document,
    getComputedStyle(node) {
      return node.computedStyle || {
        display: "block",
        visibility: "visible",
        opacity: "1",
        contentVisibility: "visible",
      };
    },
    globalThis: null,
    addEventListener(type, listener) {
      document.addEventListener(type, listener);
    },
    innerHeight: 800,
    innerWidth: 1200,
    location: { href: "https://reader.example/chapter", origin: "https://reader.example" },
    navigator: { language: "ja" },
    confirm(prompt) {
      confirmations.push(prompt);
      return options.confirm !== false;
    },
    getSelection() {
      return selection;
    },
  };
  context.globalThis = context;
  const source = fs.readFileSync(
    path.join(__dirname, "../content/coverage-content.js"),
    "utf8",
  );
  vm.runInNewContext(source, context, { filename: "coverage-content.js" });

  return {
    coverage: context.GoiCoverage,
    document,
    documentElement,
    highlights,
    messages,
    ranges,
    references,
    root,
    storageData,
    confirmations,
    resize(width, height) {
      context.innerWidth = width;
      context.innerHeight = height;
      document.dispatch("resize");
    },
    walkerReads() {
      return walkerReads;
    },
    setCaret(node, offset) {
      caret = { node, offset };
    },
    setSelection(value) {
      selection = value;
    },
    setRangeFailure(value) {
      rejectRanges = value;
    },
    setMessageResponse(response) {
      messageResponse = response;
    },
    findByID(id) {
      return descendants(documentElement).find((node) => node.id === id && node.isConnected);
    },
    findByClass(className) {
      return descendants(documentElement).find((node) => node.className === className && node.isConnected);
    },
  };
}

function resultFor(blocks, tokens) {
  return {
    summary: {
      known_occurrences: 0,
      total_occurrences: tokens.length,
      unknown_unique: tokens.length,
      excluded_names: 0,
    },
    blocks: blocks.map((block, index) => ({
      id: block.id,
      tokens: index === 0 ? tokens : [],
    })),
  };
}

test("splits long text within API limits and maps token offsets back to the page", function () {
  const value = "猫".repeat(18005);
  const harness = createHarness(value);
  const blocks = harness.coverage.collectBlocks();

  assert.equal(blocks.length, 2);
  assert.equal(blocks[0].text.length, 18000);
  assert.equal(blocks[1].text.length, 5);
  assert.equal(blocks.map((block) => block.text).join(""), value);

  harness.coverage.render({
    summary: {
      known_occurrences: 0,
      total_occurrences: 1,
      unknown_unique: 1,
      excluded_names: 0,
    },
    blocks: [{ id: 1, tokens: [] }, {
      id: 2,
      tokens: [{
        surface: "猫",
        expression: "猫",
        start_utf16: 1,
        end_utf16: 2,
        status: "unknown",
      }],
    }],
  });

  assert.equal(harness.ranges.length, 1);
  assert.equal(harness.ranges[0].startNode, harness.references.textNode);
  assert.equal(harness.ranges[0].startOffset, 18001);
  assert.equal(harness.ranges[0].endOffset, 18002);
  assert.equal(harness.highlights.has("goi-unknown-words"), true);
});

test("does not split a Unicode character at the visible-sample boundary", function () {
  const harness = createHarness("猫".repeat(119999) + "😀犬");

  const collected = harness.coverage.collectBlocks().map((block) => block.text).join("");

  assert.equal(collected.length, 119999);
  assert.equal(collected.endsWith("猫"), true);
  assert.equal(/\p{Surrogate}$/u.test(collected), false);
});

test("excludes the YouTube caption overlay from manual page analysis", function () {
  const harness = createHarness(function ({ root, element: makeElement, text: makeText }) {
    const paragraph = makeElement("p");
    paragraph.appendChild(makeText("本文を読む。"));
    root.appendChild(paragraph);

    const overlay = makeElement("section");
    overlay.dataset.goiCoverageUi = "true";
    overlay.appendChild(makeText("字幕を読む。"));
    root.appendChild(overlay);
  });

  assert.deepEqual(
    Array.from(harness.coverage.collectBlocks(), (block) => block.text),
    ["本文を読む。"],
  );
});

test("excludes text hidden by an ancestor's visual styles", function () {
  const harness = createHarness(function ({ root, element: makeElement, text: makeText }) {
    const hidden = makeElement("div");
    hidden.computedStyle = {
      display: "block",
      visibility: "visible",
      opacity: "0",
      contentVisibility: "visible",
    };
    const hiddenParagraph = makeElement("p");
    hiddenParagraph.appendChild(makeText("隠れた文章。"));
    hidden.appendChild(hiddenParagraph);
    root.appendChild(hidden);

    const visibleParagraph = makeElement("p");
    visibleParagraph.appendChild(makeText("見える文章。"));
    root.appendChild(visibleParagraph);
  });

  assert.deepEqual(
    Array.from(harness.coverage.collectBlocks(), (block) => block.text),
    ["見える文章。"],
  );
});

test("excludes every enabled form of editable content", function () {
  const harness = createHarness(function ({ root, element: makeElement, text: makeText }) {
    const editable = makeElement("div");
    editable.contentEditable = "";
    editable.appendChild(makeText("編集中の日本語。"));
    root.appendChild(editable);

    const paragraph = makeElement("p");
    paragraph.appendChild(makeText("読む日本語。"));
    root.appendChild(paragraph);
  });

  assert.deepEqual(
    Array.from(harness.coverage.collectBlocks(), (block) => block.text),
    ["読む日本語。"],
  );
});

test("groups inline reading text, excludes ruby annotations, and captures its sentence", async function () {
  const harness = createHarness(function ({ root, element: makeElement, text: makeText, references }) {
    const paragraph = makeElement("p");
    const first = makeElement("span");
    references.firstText = first.appendChild(makeText("食"));
    const ruby = makeElement("ruby");
    references.rubyBase = ruby.appendChild(makeText("べ"));
    const annotation = makeElement("rt");
    references.annotation = annotation.appendChild(makeText("た"));
    ruby.appendChild(annotation);
    const last = makeElement("span");
    references.lastText = last.appendChild(makeText("る。今日は学校へ行く。"));
    paragraph.append(first, ruby, last);
    root.appendChild(paragraph);
    references.clickTarget = first;
  });
  const blocks = harness.coverage.collectBlocks();

  assert.deepEqual(Array.from(blocks, (block) => block.text), ["食べる。今日は学校へ行く。"]);
  harness.coverage.render(resultFor(blocks, [{
    surface: "食べる",
    expression: "食べる",
    start_utf16: 0,
    end_utf16: 3,
    status: "unknown",
  }]));

  assert.deepEqual(
    harness.ranges.map((range) => range.startNode),
    [harness.references.firstText, harness.references.rubyBase, harness.references.lastText],
  );
  assert.equal(harness.ranges.some((range) => range.startNode === harness.references.annotation), false);

  harness.setCaret(harness.references.firstText, 0);
  harness.document.dispatch("click", {
    target: harness.references.clickTarget,
    clientX: 10,
    clientY: 10,
  });
  await new Promise(setImmediate);
  const mine = harness.findByID("goi-ext-coverage-mine");
  assert.ok(mine);
  mine.dispatch("click");
  await new Promise(setImmediate);

  const capture = harness.messages.find((message) => message.type === "goi.capture.direct");
  assert.equal(capture.capture.expression, "食べる");
  assert.equal(capture.capture.contextText, "食べる。");
});

test("ignores an older capture result after a newer capture starts", async function () {
  const harness = createHarness("猫と犬を見る。");
  const blocks = harness.coverage.collectBlocks();
  harness.coverage.render(resultFor(blocks, [{
    surface: "猫",
    expression: "猫",
    start_utf16: 0,
    end_utf16: 1,
    status: "unknown",
  }, {
    surface: "犬",
    expression: "犬",
    start_utf16: 2,
    end_utf16: 3,
    status: "unknown",
  }]));
  const responses = [];
  harness.setMessageResponse(function (message) {
    if (message.type === "goi.dictionary.lookup") {
      return Promise.resolve({ ok: true, result: { state: "no_match", query: message.expression } });
    }
    return new Promise(function (resolve) {
      responses.push(resolve);
    });
  });
  const next = harness.findByID("goi-ext-coverage-next");

  next.dispatch("click");
  await new Promise(setImmediate);
  harness.findByID("goi-ext-coverage-mine").dispatch("click");
  next.dispatch("click");
  await new Promise(setImmediate);
  harness.findByID("goi-ext-coverage-mine").dispatch("click");
  assert.equal(responses.length, 2);

  responses[1]({ ok: true, queued: false });
  await new Promise(setImmediate);
  const panel = harness.findByID("goi-ext-coverage");
  assert.equal(harness.findByClass("goi-ext-coverage-detail").textContent, "Added 「犬」");

  responses[0]({ ok: true, queued: false });
  await new Promise(setImmediate);
  assert.equal(harness.findByClass("goi-ext-coverage-detail").textContent, "Added 「犬」");
});

test("requires trusted clicks and revalidates page text before capture", async function () {
  const harness = createHarness("猫を見る。");
  const blocks = harness.coverage.collectBlocks();
  harness.coverage.render(resultFor(blocks, [{
    surface: "猫",
    expression: "猫",
    start_utf16: 0,
    end_utf16: 1,
    status: "unknown",
  }]));
  harness.setCaret(harness.references.textNode, 0);

  harness.document.dispatch("click", { isTrusted: false, clientX: 10, clientY: 10 });
  assert.equal(harness.findByID("goi-ext-coverage-mine"), undefined);

  harness.document.dispatch("click", { clientX: 10, clientY: 10 });
  await new Promise(setImmediate);
  let mine = harness.findByID("goi-ext-coverage-mine");
  assert.ok(mine);
  mine.dispatch("click", { isTrusted: false });
  await new Promise(setImmediate);
  assert.equal(harness.messages.some((message) => message.type === "goi.capture.direct"), false);

  harness.references.textNode.nodeValue = "犬を見る。";
  mine.dispatch("click");
  await new Promise(setImmediate);
  assert.equal(harness.messages.some((message) => message.type === "goi.capture.direct"), false);
});

test("does not render a completed analysis after the analyzed text changes", function () {
  const harness = createHarness("猫を読む。");
  const started = harness.coverage.beginAnalysis("analysis-1");
  harness.references.textNode.nodeValue = "犬を見る。";

  const rendered = harness.coverage.finishAnalysis("analysis-1", started.url, {
    summary: {
      known_occurrences: 1,
      total_occurrences: 1,
      unknown_unique: 0,
      excluded_names: 0,
    },
    blocks: [{ id: 1, tokens: [] }],
  });

  assert.equal(rendered, false);
});

test("late dictionary results cannot replace a more recently selected word", async function () {
  const harness = createHarness("猫犬");
  const responses = [];
  harness.setMessageResponse(() => new Promise(resolve => { responses.push(resolve); }));
  harness.coverage.render(resultFor(harness.coverage.collectBlocks(), [
    { surface: "猫", start_utf16: 0, end_utf16: 1, status: "known" },
    { surface: "犬", start_utf16: 1, end_utf16: 2, status: "known" },
  ]));
  const next = harness.findByID("goi-ext-coverage-next");
  next.dispatch("click");
  next.dispatch("click");
  responses[1]({ ok: true, result: { state: "no_match", query: "犬" } });
  await new Promise(setImmediate);
  responses[0]({ ok: true, result: { state: "no_match", query: "猫" } });
  await new Promise(setImmediate);
  assert.equal(harness.findByID("goi-ext-coverage-word-menu")["aria-label"], "Dictionary entry for 犬");
  assert.equal(harness.findByID("goi-ext-coverage-mine")["aria-label"], "Save 犬 to mining");
});
