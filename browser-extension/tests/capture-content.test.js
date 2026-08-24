const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const captureModel = require("../shared/capture-model.js");

function collectCapture(options) {
  const selectedText = options.selectedText;
  const block = { textContent: options.blockText };
  const caption = options.selectionInCaption ? { textContent: options.captionText } : null;
  const selectedElement = {
    nodeType: 1,
    closest(selector) {
      return selector.startsWith("[data-goi-caption-text]") ? caption : block;
    },
  };
  const selectedAt = options.blockText.indexOf(selectedText);
  const range = {
    commonAncestorContainer: selectedElement,
    startContainer: selectedElement,
    startOffset: 0,
    toString: () => selectedText,
    cloneRange() {
      return {
        selectNodeContents() {},
        setEnd() {},
        toString: () => options.blockText.slice(0, selectedAt),
      };
    },
  };
  const selection = {
    rangeCount: 1,
    toString: () => selectedText,
    getRangeAt: () => range,
  };
  const video = { currentTime: 1.234 };
  const activeVideo = { currentTime: options.activeVideoTime ?? 1.234 };
  const context = {
    GoiExtension: { captureModel },
    GoiYouTubeOverlay: {
      getActiveCaption: () => options.activeCaption,
      getActiveVideo: () => activeVideo,
    },
    Node: { ELEMENT_NODE: 1 },
    document: {
      body: block,
      documentElement: { lang: "en" },
      title: "Source page",
      querySelector(selector) {
        return selector === ".html5-video-player video" || selector === "video" ? video : null;
      },
      querySelectorAll: () => [],
    },
    location: {
      hostname: options.hostname,
      pathname: options.pathname,
      href: `https://${options.hostname}${options.pathname}`,
    },
    navigator: { language: "en" },
    window: { getSelection: () => selection },
  };
  const source = fs.readFileSync(path.join(__dirname, "../content/capture-content.js"), "utf8");
  vm.runInNewContext(source, context, { filename: "capture-content.js" });
  return context.GoiCapture.collect("").capture;
}

test("keeps selected YouTube page text instead of replacing it with the active caption", () => {
  const capture = collectCapture({
    selectedText: "grammar",
    blockText: "This comment discusses grammar clearly.",
    captionText: "",
    activeCaption: "An unrelated subtitle is playing.",
    selectionInCaption: false,
    hostname: "www.youtube.com",
    pathname: "/watch",
  });

  assert.equal(capture.contextText, "This comment discusses grammar clearly.");
  assert.equal(capture.sourceKind, "video");
  assert.equal(capture.sourcePositionMs, 1234);
});

test("uses the complete active caption when the selection is inside captions", () => {
  const capture = collectCapture({
    selectedText: "文法",
    blockText: "文法",
    captionText: "文法",
    activeCaption: "この文法を勉強します。",
    selectionInCaption: true,
    hostname: "www.youtube.com",
    pathname: "/watch",
  });

  assert.equal(capture.contextText, "この文法を勉強します。");
  assert.equal(capture.sourceKind, "video");
});

test("uses the overlay's active player for a YouTube capture timestamp", () => {
  const capture = collectCapture({
    selectedText: "文法",
    blockText: "文法",
    captionText: "文法",
    activeCaption: "この文法を勉強します。",
    activeVideoTime: 9.876,
    selectionInCaption: true,
    hostname: "www.youtube.com",
    pathname: "/watch",
  });

  assert.equal(capture.sourcePositionMs, 9876);
});

test("does not attribute an unrelated embedded video to ordinary reading", () => {
  const capture = collectCapture({
    selectedText: "vocabulary",
    blockText: "The article explains this vocabulary in context.",
    captionText: "",
    activeCaption: "",
    selectionInCaption: false,
    hostname: "news.example",
    pathname: "/article",
  });

  assert.equal(capture.sourceKind, "web");
  assert.equal(capture.sourcePositionMs, null);
});
