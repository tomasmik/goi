import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const css = ["app.css", "study.css", "pages.css", "responsive.css"]
  .map((name) => readFileSync(new URL(`../static/css/${name}`, import.meta.url), "utf8"))
  .join("\n");

test("every web CSS variable reference has a definition", () => {
  const definitions = new Set(Array.from(css.matchAll(/--([\w-]+)\s*:/g), ([, name]) => name));
  const references = new Set(Array.from(css.matchAll(/var\(--([\w-]+)/g), ([, name]) => name));
  assert.deepEqual(Array.from(references).filter((name) => !definitions.has(name)).sort(), []);
});
