const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const css = fs.readFileSync(path.join(__dirname, "../popup/popup.css"), "utf8");
const sharedCSS = fs.readFileSync(path.join(__dirname, "../shared/ui.css"), "utf8");

function rule(selector, last) {
  const start = last
    ? css.lastIndexOf(selector + " {")
    : css.indexOf(selector + " {");
  assert.notEqual(start, -1, "missing CSS rule for " + selector);
  const end = css.indexOf("}", start);
  return css.slice(start, end);
}

function hexColor(selector, property, last) {
  const match = new RegExp("(?:^|\\n)\\s*" + property + ":\\s*(#[0-9a-f]{6}|white)", "iu").exec(rule(selector, last));
  assert.ok(match, "missing " + property + " in " + selector);
  return match[1].toLowerCase() === "white" ? "#ffffff" : match[1];
}

function luminance(hex) {
  const channels = hex.slice(1).match(/../gu).map(function (channel) {
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

function sharedVariables(last) {
  const start = last ? sharedCSS.lastIndexOf(":root {") : sharedCSS.indexOf(":root {");
  assert.notEqual(start, -1, "missing shared theme variables");
  const end = sharedCSS.indexOf("}", start);
  return Object.fromEntries(Array.from(
    sharedCSS.slice(start, end).matchAll(/--([\w-]+):\s*(#[0-9a-f]{6})\s*;/giu),
    ([, name, value]) => [name, value],
  ));
}

test("popup normal text colors meet WCAG AA contrast", function () {
  const bodyBackground = hexColor("body", "background");
  const surfaceBackground = hexColor("form,\nsection", "background");
  const shared = sharedVariables(false);
  const pairs = [
    [shared["goi-accent-fill-ink"], shared["goi-brand-fill"], "brand mark"],
    [shared["goi-accent-fill-ink"], shared["goi-brand-fill"], "primary button"],
    [shared["goi-reading-ink"], shared["goi-canvas"], "quiet action"],
    [hexColor("#status", "color"), bodyBackground, "status"],
    [hexColor("#status.error", "color"), bodyBackground, "error status"],
    [hexColor(".outbox-status", "color"), bodyBackground, "outbox status"],
    [hexColor(".analyze-section p", "color"), surfaceBackground, "coverage description"],
  ];

  pairs.forEach(function ([foreground, background, name]) {
    assert.ok(
      contrast(foreground, background) >= 4.5,
      name + " must have at least 4.5:1 contrast",
    );
  });
});

test("popup keyboard focus indicators use opaque high-contrast colors", function () {
  assert.match(
    sharedCSS,
    /\.goi-extension-ui button:focus-visible,[\s\S]*outline:\s*3px solid var\(--goi-focus\);[\s\S]*outline-offset:\s*2px/u,
  );
});

test("shared extension styles respect reduced motion", function () {
  assert.match(
    sharedCSS,
    /@media \(prefers-reduced-motion: reduce\)[\s\S]*transition-duration:\s*\.01ms !important;[\s\S]*animation-duration:\s*\.01ms !important;/u,
  );
});

test("popup dark-mode text colors meet WCAG AA contrast", function () {
  const bodyBackground = hexColor(":root,\n  body", "background");
  const surfaceBackground = hexColor("form,\n  section", "background", true);
  const muted = hexColor("label,\n  header p,\n  .analyze-section p,\n  .site-auto-detail,\n  .hint", "color");
  const shared = sharedVariables(true);
  const pairs = [
    [shared["goi-accent-fill-ink"], shared["goi-brand-fill"], "primary button"],
    [shared["goi-reading-ink"], shared["goi-canvas"], "quiet action"],
    [hexColor("#status", "color", true), bodyBackground, "status"],
    [hexColor("#status.error", "color", true), bodyBackground, "error status"],
    [hexColor(".outbox-status", "color", true), bodyBackground, "outbox status"],
    [muted, surfaceBackground, "coverage description"],
  ];

  pairs.forEach(function ([foreground, background, name]) {
    assert.ok(
      contrast(foreground, background) >= 4.5,
      "dark-mode " + name + " must have at least 4.5:1 contrast",
    );
  });
});
