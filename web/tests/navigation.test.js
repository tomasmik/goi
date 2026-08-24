import assert from "node:assert/strict";
import test from "node:test";

import { navigationSection } from "../static/js/navigation.js";

test("maps routes to their primary navigation section", () => {
  const cases = new Map([
    ["/", "study"],
    ["/reviews/session/12", "study"],
    ["/lessons", "study"],
    ["/vocabulary/4/edit", "vocabulary"],
    ["/imports/anki/2/mapping", "vocabulary"],
    ["/mining/captures/8", "mining"],
    ["/statistics", "study"],
    ["/settings/extension", "settings"],
    ["/login", ""],
    ["/settings-old", ""],
    ["/mining-tools", ""],
  ]);

  for (const [path, expected] of cases) {
    assert.equal(navigationSection(path), expected, path);
  }
});
