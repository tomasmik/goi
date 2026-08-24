import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const reviewSession = readFileSync(new URL("../templates/review-session.html", import.meta.url), "utf8");

test("review answer input keeps its repeated prompt label visually hidden", () => {
  assert.match(reviewSession, /<label for="answer">\s*<span class="visually-hidden">/);
});
