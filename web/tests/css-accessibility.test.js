import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const css = ["app.css", "study.css", "pages.css", "responsive.css"]
  .map((name) => readFileSync(new URL(`../static/css/${name}`, import.meta.url), "utf8"))
  .join("\n");

function variablesFor(selector) {
  const match = css.match(new RegExp(`\\${selector}\\s*\\{([^}]*)\\}`));
  assert.ok(match, `${selector} variables were not found`);
  return Object.fromEntries(Array.from(match[1].matchAll(/--([\w-]+):\s*(#[0-9a-f]{3,6})\s*;/gi), ([, name, value]) => [name, value]));
}

function luminance(hex) {
  const value = hex.slice(1);
  const expanded = value.length === 3 ? Array.from(value, (digit) => digit + digit).join("") : value;
  const channels = expanded.match(/.{2}/g).map((channel) => Number.parseInt(channel, 16) / 255);
  const linear = channels.map((channel) => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4);
  return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
}

function contrast(first, second) {
  const [lighter, darker] = [luminance(first), luminance(second)].sort((a, b) => b - a);
  return (lighter + 0.05) / (darker + 0.05);
}

test("theme tokens preserve text and focus contrast", () => {
  for (const [name, tokens] of [
    ["light", variablesFor(":root")],
    ["dark", variablesFor(".theme-dark")]
  ]) {
    for (const background of [tokens.canvas, tokens["canvas-strong"], tokens.surface, tokens["surface-raised"]]) {
      assert.ok(contrast(tokens.muted, background) >= 4.5, `${name} muted text must meet 4.5:1`);
      assert.ok(contrast(tokens["focus-ring"], background) >= 3, `${name} focus ring must meet 3:1`);
    }
    assert.ok(contrast(tokens.ink, tokens["warning-soft"]) >= 4.5, `${name} marked text must meet 4.5:1`);
  }
});

test("mobile forms keep visual and keyboard action order aligned", () => {
  assert.doesNotMatch(css, /flex-direction:\s*column-reverse/);
});

test("the hidden attribute overrides component display rules", () => {
  assert.match(css, /\[hidden\]\s*\{[^}]*display:\s*none\s*!important/s);
});
