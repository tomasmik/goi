const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const css = fs.readFileSync(path.join(__dirname, "../youtube/overlay.css"), "utf8");

function rule(selector) {
  const start = css.indexOf(selector + " {");
  assert.notEqual(start, -1, "missing CSS rule for " + selector);
  const end = css.indexOf("}", start);
  return css.slice(start, end);
}

function property(selector, name) {
  const match = new RegExp("(?:^|\\n)\\s*" + name + ":\\s*([^;]+)", "iu").exec(rule(selector));
  assert.ok(match, "missing " + name + " in " + selector);
  return match[1].trim();
}

function normalizeHex(value) {
  const hex = value.toLowerCase();
  if (/^#[0-9a-f]{3}$/u.test(hex)) {
    return "#" + Array.from(hex.slice(1), (character) => character + character).join("");
  }
  assert.match(hex, /^#[0-9a-f]{6}$/u);
  return hex;
}

function luminance(hex) {
  const channels = normalizeHex(hex).slice(1).match(/../gu).map(function (channel) {
    const value = Number.parseInt(channel, 16) / 255;
    return value <= 0.04045
      ? value / 12.92
      : ((value + 0.055) / 1.055) ** 2.4;
  });
  return (0.2126 * channels[0]) + (0.7152 * channels[1]) + (0.0722 * channels[2]);
}

function contrast(foreground, background) {
  const light = Math.max(luminance(foreground), luminance(background));
  const dark = Math.min(luminance(foreground), luminance(background));
  return (light + 0.05) / (dark + 0.05);
}

test("caption actions and success status meet normal-text contrast", function () {
  assert.equal(property(".goi-ext-caption-selection", "color"), "#fff");
  assert.equal(property(".goi-ext-caption-status", "color"), "#fff");
  const pairs = [
    [".goi-ext-caption-selection", "save action"],
    [".goi-ext-caption-selection:hover", "save action hover"],
    [".goi-ext-caption-known", "known action"],
    [".goi-ext-caption-known:hover", "known action hover"],
    [".goi-ext-caption-status", "success status"],
    [".goi-ext-caption-status--error", "error status"],
  ];

  pairs.forEach(function ([selector, name]) {
    assert.ok(
      contrast("#fff", property(selector, "background")) >= 4.5,
      name + " must have at least 4.5:1 contrast",
    );
  });
});

test("the caption restore rail remains visible in listening mode", function () {
  assert.match(
    rule(".goi-ext-caption-text:empty,\n.goi-ext-caption-text[hidden]"),
    /display:\s*none/iu,
  );
  assert.match(
    rule(".goi-ext-overlay-rail--expanded,\n.goi-ext-overlay-rail:focus-within,\n.goi-ext-overlay--captions-hidden .goi-ext-overlay-rail"),
    /opacity:\s*1/iu,
  );
});
