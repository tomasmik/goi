import assert from "node:assert/strict";
import test from "node:test";

import { initLocalTimes } from "../static/js/local-time.js";

test("connected-browser times use Goi's configured timezone", () => {
  let options;
  const time = {
    dateTime: "2026-08-06T12:00:00Z",
    dataset: { timeZone: "Europe/Vilnius" },
    textContent: "",
  };
  const intl = {
    DateTimeFormat: function (_locale, nextOptions) {
      options = nextOptions;
      return { format() { return "Aug 6, 2026, 3:00 PM"; } };
    },
  };
  initLocalTimes({ querySelectorAll() { return [time]; } }, intl);
  assert.equal(options.timeZone, "Europe/Vilnius");
  assert.equal(time.textContent, "Aug 6, 2026, 3:00 PM");
});
