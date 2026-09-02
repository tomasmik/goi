const test = require("node:test");
const assert = require("node:assert/strict");
const {
  classNames,
  createHarness,
  descendants,
  settingsModel,
} = require("./helpers/overlay-harness.js");

test("ignores unrelated YouTube mutations and keeps the selected player", async () => {
  const harness = createHarness("最初の字幕。");
  await harness.start();
  const reads = harness.captionReadCount();
  const playerScans = harness.playerScanCount();

  harness.unrelatedMutation();
  assert.deepEqual(harness.pendingTimerIDs(16), []);
  assert.equal(harness.captionReadCount(), reads);

  harness.setCaption("次の字幕。");
  harness.readCaption();
  assert.equal(harness.playerScanCount(), playerScans);
  assert.equal(harness.activeCaption(), "次の字幕。");
});

test("makes Japanese caption words clickable before coverage is ready", async () => {
  const caption = "昨日は映画を見ました。";
  const harness = createHarness(caption);
  await harness.start();

  const captionText = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-text")
  );
  const lookupWords = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-caption-word")
  );

  assert.equal(captionText.children.map((node) => node.textContent).join(""), caption);
  assert.deepEqual(lookupWords.map((word) => word.textContent), [
    "昨日", "は", "映画", "を", "見", "ま", "した"
  ]);
});

test("renders furigana only on unknown caption words when enabled", async () => {
  const caption = "猫を読む";
  const harness = createHarness(caption, {
    persistedSettings: { furiganaEnabled: true },
    coverageResponse(blocks) {
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 2,
          unknown_unique: 1,
          excluded_names: 0,
        },
        blocks: [{
          id: blocks[0].id,
          tokens: [
            { surface: "猫", expression: "猫", reading: "ねこ", start_utf16: 0, end_utf16: 1, status: "known" },
            { surface: "読む", expression: "読む", reading: "よむ", start_utf16: 2, end_utf16: 4, status: "unknown" },
          ],
        }],
      };
    },
  });

  await harness.start();
  await harness.analyzeCaption();

  const known = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word--known")
  );
  const unknown = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word--unknown")
  );
  assert.equal(descendants(known).some((node) => node.tagName === "RUBY"), false);
  assert.equal(descendants(unknown).some((node) => node.tagName === "RUBY"), true);
  assert.equal(descendants(unknown).find((node) => node.tagName === "RT").textContent, "よ");
  assert.equal(unknown["aria-label"], "Look up 読む");
});

test("repaints the current caption when furigana is enabled", async () => {
  const harness = createHarness("読む", {
    coverageResponse(blocks) {
      return {
        summary: { known_occurrences: 0, total_occurrences: 1, unknown_unique: 1, excluded_names: 0 },
        blocks: [{ id: blocks[0].id, tokens: [{
          surface: "読む",
          expression: "読む",
          reading: "よむ",
          start_utf16: 0,
          end_utf16: 2,
          status: "unknown",
        }] }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();
  assert.equal(descendants(harness.player).some((node) => node.tagName === "RT"), false);

  harness.storageListener({
    [settingsModel.STORAGE_KEY]: {
      newValue: settingsModel.sanitize({ furiganaEnabled: true }),
    },
  }, "sync");

  assert.equal(descendants(harness.player).find((node) => node.tagName === "RT").textContent, "よ");
});

test("combines simultaneous visible caption windows", async () => {
  const harness = createHarness("本編の字幕", {
    simultaneousCaptions: ["画面上の補足"],
    coverageResponse(blocks) {
      return {
        summary: {
          known_occurrences: 0,
          total_occurrences: 0,
          unknown_unique: 0,
          excluded_names: 0,
        },
        blocks: [{ id: blocks[0].id, tokens: [] }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  assert.equal(harness.activeCaption(), "本編の字幕\n画面上の補足");
  const request = harness.messages.find((message) => message.type === "goi.coverage.classify");
  assert.equal(request.blocks[0].text, "本編の字幕\n画面上の補足");
});

test("calculates comprehension from the complete YouTube transcript before playback", async () => {
  const harness = createHarness("", {
    currentTime: 1.5,
    transcriptResponse: {
      ok: true,
      state: "ready",
      automatic: false,
      cues: [
        { startMs: 1000, endMs: 2000, text: "猫" },
        { startMs: 5000, endMs: 6000, text: "犬" },
      ],
    },
    coverageResponse(blocks) {
      const resultBlocks = blocks.map(function (block) {
        const known = block.text === "猫";
        return {
          id: block.id,
          tokens: [{
            surface: block.text,
            expression: block.text,
            start_utf16: 0,
            end_utf16: block.text.length,
            status: known ? "known" : "unknown",
          }],
        };
      });
      return {
        summary: {
          known_occurrences: resultBlocks.filter(function (block) {
            return block.tokens[0].status === "known";
          }).length,
          total_occurrences: resultBlocks.length,
          unknown_unique: 1,
          excluded_names: 0,
        },
        blocks: resultBlocks,
      };
    },
  });

  await harness.start();
  await new Promise(setImmediate);
  await new Promise(setImmediate);

  const coverage = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-coverage")
  );
  assert.equal(coverage.textContent, "Goi · 50% known · full transcript");

  const response = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1,
  });
  assert.equal(response.session.transcriptSource, "full");
  assert.equal(response.session.transcriptState, "ready");
  assert.equal(response.session.lines.length, 2);
  assert.equal(response.session.lines[1].text, "犬");
  assert.equal(response.session.lines[1].unknowns[0].expression, "犬");
  assert.equal(response.session.currentLineID, 1000000);
  assert.deepEqual(JSON.parse(JSON.stringify(response.session.comprehension)), {
    known_occurrences: 1,
    total_occurrences: 2,
    unknown_unique: 1,
    excluded_names: 0,
    line_count: 2,
  });
});

test("retries full-transcript coverage after a temporary Goi outage", async () => {
  let coverageUnavailable = true;
  const options = {
    currentTime: 1.5,
    transcriptResponse: {
      ok: true,
      state: "ready",
      automatic: false,
      cues: [{ startMs: 1000, endMs: 2000, text: "猫" }],
    },
    coverageResponse(blocks) {
      return {
        summary: { known_occurrences: 1, total_occurrences: 1, unknown_unique: 0, excluded_names: 0 },
        blocks: [{
          id: blocks[0].id,
          tokens: [{ surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" }],
        }],
      };
    },
  };
  Object.defineProperty(options, "coverageFailure", { get() { return coverageUnavailable; } });
  const harness = createHarness("", options);

  await harness.start();
  await new Promise(setImmediate);
  coverageUnavailable = false;
  harness.runTimer(500);
  await new Promise(setImmediate);
  await new Promise(setImmediate);

  const response = await harness.requestRuntimeMessage({ type: "goi.youtube.session.get", version: 1 });
  assert.equal(response.session.transcriptState, "ready");
  assert.equal(response.session.comprehension.known_occurrences, 1);
});

test("keeps retrying while a long preroll hides the main video", async () => {
  let attempts = 0;
  const harness = createHarness("", {
    transcriptResponse() {
      attempts += 1;
      if (attempts < 5) {
        return { ok: true, state: "unavailable", reason: "player_unavailable" };
      }
      return {
        ok: true,
        state: "ready",
        automatic: false,
        cues: [{ startMs: 1000, endMs: 2000, text: "日本語" }]
      };
    },
    coverageResponse(blocks) {
      return {
        summary: { known_occurrences: 1, total_occurrences: 1, unknown_unique: 0, excluded_names: 0 },
        blocks: [{
          id: blocks[0].id,
          tokens: [{
            surface: "日本語",
            expression: "日本語",
            start_utf16: 0,
            end_utf16: 3,
            status: "known"
          }]
        }]
      };
    }
  });

  await harness.start();
  for (const delay of [500, 1500, 3000, 5000]) {
    harness.runTimer(delay);
    await new Promise(setImmediate);
  }
  await new Promise(setImmediate);

  const response = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1
  });
  assert.equal(attempts, 5);
  assert.equal(response.session.transcriptState, "ready");
  assert.equal(response.session.lines[0].text, "日本語");
});

test("reuses full-transcript tokens for the matching live caption", async () => {
  const harness = createHarness("猫", {
    transcriptResponse: {
      ok: true,
      state: "ready",
      automatic: false,
      cues: [{ startMs: 0, endMs: 2000, text: "猫" }],
    },
    coverageResponse(blocks) {
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 1,
          unknown_unique: 0,
          excluded_names: 0,
        },
        blocks: [{
          id: blocks[0].id,
          tokens: [{
            surface: "猫",
            expression: "猫",
            start_utf16: 0,
            end_utf16: 1,
            status: "known",
          }],
        }],
      };
    },
  });

  await harness.start();
  await new Promise(setImmediate);
  await harness.analyzeCaption();

  const requests = harness.messages.filter(function (message) {
    return message.type === "goi.coverage.classify";
  });
  assert.equal(requests.length, 1);
  const known = descendants(harness.player).find(function (node) {
    return classNames(node).has("goi-ext-caption-word--known");
  });
  assert.equal(known.textContent, "猫");
});

test("shows the complete automatic transcript cue instead of rolling words", async () => {
  const fullCaption = "こちらのホテルは高崎駅から歩いて3分くらいで着く";
  const nextCaption = "本当に便利なホテルです";
  const harness = createHarness("こちら", {
    currentTime: 1,
    transcriptResponse: {
      ok: true,
      state: "ready",
      automatic: true,
      cues: [
        { startMs: 0, endMs: 5000, text: fullCaption },
        { startMs: 2000, endMs: 7000, text: nextCaption },
      ],
    },
    coverageResponse(blocks) {
      return {
        summary: { known_occurrences: 0, total_occurrences: 0, unknown_unique: 0, excluded_names: 0 },
        blocks: blocks.map(function (block) { return { id: block.id, tokens: [] }; }),
      };
    },
  });

  await harness.start();
  await new Promise(setImmediate);
  assert.equal(harness.activeCaption(), fullCaption);

  harness.setCaption("こちらのホテルは");
  harness.readCaption();
  assert.equal(harness.activeCaption(), fullCaption);

  harness.video.currentTime = 2.5;
  harness.setCaption("本当に");
  harness.video.dispatch("timeupdate");
  harness.runTimer(16);
  assert.equal(harness.activeCaption(), nextCaption);
});

test("switches saved automatic caption modes while paused without native caption mutations", async () => {
  const fullCaption = "こちらのホテルは駅から歩いて3分で着く";
  const harness = createHarness("こちら", {
    currentTime: 1,
    videoPaused: true,
    persistedSettings: { automaticCaptionMode: "live" },
    transcriptResponse: {
      ok: true,
      state: "ready",
      automatic: true,
      cues: [{ startMs: 0, endMs: 5000, text: fullCaption }],
    },
    coverageResponse(blocks) {
      return {
        summary: { known_occurrences: 0, total_occurrences: 0, unknown_unique: 0, excluded_names: 0 },
        blocks: blocks.map(function (block) { return { id: block.id, tokens: [] }; }),
      };
    },
  });
  await harness.start();
  assert.equal(harness.activeCaption(), "こちら");
  harness.video.dispatch("timeupdate");
  assert.deepEqual(harness.pendingTimerIDs(16), []);

  const choices = descendants(harness.player).filter((node) => node.name === "goi-ext-automaticCaptionMode");
  const full = choices.find((node) => node.value === "full");
  full.checked = true;
  full.dispatch("change");
  await new Promise(setImmediate);
  harness.runTimer(16);
  assert.equal(harness.activeCaption(), fullCaption);
  assert.deepEqual(choices.filter((node) => node.checked).map((node) => node.value), ["full"]);

  const live = choices.find((node) => node.value === "live");
  live.checked = true;
  live.dispatch("change");
  await new Promise(setImmediate);
  harness.runTimer(16);
  assert.equal(harness.activeCaption(), "こちら");
  assert.deepEqual(choices.filter((node) => node.checked).map((node) => node.value), ["live"]);
  harness.setCaption("こちらのホテルは");
  harness.readCaption();
  assert.equal(harness.activeCaption(), "こちらのホテルは");
  assert.equal(harness.video.paused, true);

  const patches = harness.messages.filter((message) => message.type === "goi.settings.patch");
  assert.deepEqual(patches.map((message) => message.patch.automaticCaptionMode), ["full", "live"]);
});

test("uses live caption analysis for highlighting and mining", async () => {
  const coverageResponse = function (blocks) {
    const block = blocks[0];
    if (block.text === "猫を読みます。") {
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 2,
          unknown_unique: 1,
          excluded_names: 0,
        },
        blocks: [{
          id: block.id,
          tokens: [
            { surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" },
            { surface: "読み", expression: "読む", start_utf16: 2, end_utf16: 4, status: "unknown" },
          ],
        }],
      };
    }
    return {
      summary: {
        known_occurrences: 1,
        total_occurrences: 2,
        unknown_unique: 1,
        excluded_names: 0,
      },
      blocks: [{
        id: block.id,
        tokens: [
          { surface: "犬", expression: "犬", start_utf16: 0, end_utf16: 1, status: "unknown" },
          { surface: "見る", expression: "見る", start_utf16: 2, end_utf16: 4, status: "known" },
        ],
      }],
    };
  };
  const harness = createHarness("猫を読みます。", {
    currentTime: 12.4,
    captureResponse: { ok: true, queued: false },
    coverageResponse,
  });
  await harness.start();
  await harness.analyzeCaption();

  const coverage = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-coverage")
  );
  const unknown = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word--unknown")
  );
  assert.equal(coverage.textContent, "Goi · no Japanese transcript");
  assert.equal(unknown.textContent, "読み");
  assert.equal(unknown.tagName, "SPAN");

  const known = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word--known")
  );
  assert.equal(known.textContent, "猫");

  unknown.dispatch("click");
  await new Promise(setImmediate);
  const lookup = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-lookup")
  );
  assert.equal(lookup.hidden, false);
  assert.equal(lookup.style.values["--goi-ext-lookup-left"], "50%");
  assert.equal(
    descendants(lookup).find((node) => classNames(node).has("goi-dictionary-term")).textContent,
    "読む",
  );
  assert.equal(
    descendants(lookup).find((node) => classNames(node).has("goi-dictionary-reading")).textContent,
    "よみ",
  );
  assert.equal(
    descendants(lookup).find((node) => classNames(node).has("goi-dictionary-commonness")).textContent,
    "Commonness 80/100",
  );
  assert.equal(
    descendants(lookup).find((node) => node.tagName === "LI").textContent,
    "meaning",
  );
  let useEntry = descendants(lookup).find((node) =>
    classNames(node).has("goi-dictionary-select")
  );
  assert.equal(useEntry.textContent, "Mine");
  useEntry.dispatch("click");
  await new Promise(setImmediate);

  const capture = harness.messages.find((message) => message.type === "goi.capture.direct");
  assert.equal(capture.capture.rawText, "読み");
  assert.equal(capture.capture.expression, "読む");
  assert.equal(capture.capture.contextText, "猫を読みます。");
  assert.equal(capture.capture.sourcePositionMs, 12400);

  known.dispatch("click");
  await new Promise(setImmediate);
  useEntry = descendants(lookup).find((node) =>
    classNames(node).has("goi-dictionary-select")
  );
  assert.equal(useEntry.textContent, "Mine");
  useEntry.dispatch("click");
  await new Promise(setImmediate);
  const captures = harness.messages.filter((message) => message.type === "goi.capture.direct");
  assert.equal(captures.length, 2);
  assert.equal(captures[1].capture.rawText, "猫");
  assert.equal(captures[1].capture.expression, "猫");

  harness.setCaption("犬を見る。");
  harness.readCaption();
  await harness.analyzeCaption();
  assert.equal(coverage.textContent, "Goi · no Japanese transcript");
  assert.equal(harness.coverage().captionsAnalyzed, 2);

  const requestsBeforeRepeat = harness.messages.filter((message) =>
    message.type === "goi.coverage.classify"
  ).length;
  harness.setCaption("猫を読みます。");
  harness.readCaption();
  await harness.analyzeCaption();
  assert.equal(harness.messages.filter((message) =>
    message.type === "goi.coverage.classify"
  ).length, requestsBeforeRepeat);
  assert.equal(harness.coverage().captionsAnalyzed, 3);
  assert.equal(harness.coverage().summary.total_occurrences, 6);

  harness.navigate("https://www.youtube.com/watch?v=next");
  assert.equal(harness.coverage().captionsAnalyzed, 0);
  assert.equal(coverage.textContent, "Goi · loading full transcript…");
});

test("looks up visible Japanese before coverage analysis finishes", async () => {
  const harness = createHarness("猫を見る。");
  await harness.start();

  const words = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-caption-word--unclassified")
  );
  assert.deepEqual(words.map((word) => word.textContent), ["猫", "を", "見る"]);

  words[2].dispatch("click");
  await new Promise(setImmediate);
  const useEntry = descendants(harness.player).find((node) =>
    classNames(node).has("goi-dictionary-select")
  );
  assert.equal(useEntry.textContent, "Mine");
});

test("opens an unpinned dictionary preview after a short hover", async () => {
  const harness = createHarness("猫を見る。", {
    persistedSettings: { hoverLookupEnabled: true },
  });
  await harness.start();

  const word = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word--unclassified") && node.textContent === "見る"
  );
  const lookup = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-lookup")
  );

  word.dispatch("pointerenter");
  assert.equal(lookup.hidden, true);
  harness.runTimer(120);
  await new Promise(setImmediate);
  assert.equal(lookup.hidden, false);
  assert.equal(
    descendants(lookup).find((node) => classNames(node).has("goi-dictionary-term")).textContent,
    "見る",
  );

  word.dispatch("pointerleave");
  harness.runTimer(180);
  assert.equal(lookup.hidden, true);
});

test("does not replace a subtitle word during a pointer click", async () => {
  const harness = createHarness("猫を見る。", {
    coverageResponse: {
      summary: {
        known_occurrences: 1,
        total_occurrences: 2,
        unknown_unique: 1,
        excluded_names: 0,
      },
      blocks: [{
        id: 1,
        tokens: [
          { surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" },
          { surface: "見る", expression: "見る", start_utf16: 2, end_utf16: 4, status: "unknown" },
        ],
      }],
    },
  });
  await harness.start();

  const captionNode = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-text")
  );
  const word = descendants(captionNode).find((node) =>
    node.textContent === "見る" && classNames(node).has("goi-ext-caption-word--unclassified")
  );
  captionNode.dispatch("pointerdown");
  await harness.analyzeCaption();
  assert.equal(descendants(captionNode).includes(word), true);

  captionNode.dispatch("pointerup");
  word.dispatch("click");
  await new Promise(setImmediate);
  const useEntry = descendants(harness.player).find((node) =>
    classNames(node).has("goi-dictionary-select")
  );
  assert.equal(useEntry.textContent, "Mine");
});

test("shows ambiguous dictionary entries in one anchored popover", async () => {
  const harness = createHarness("最初", {
    dictionaryResponse: {
      ok: true,
      result: {
        query: "最初",
        state: "ambiguous",
        candidates: [{
          entry_sequence: 1001,
          written: "最初",
          reading: "さいしょ",
          commonness: 9,
          commonness_score: 90,
          meanings: ["beginning", "first"],
          senses: [{ parts_of_speech: ["noun", "adverb"], meanings: ["beginning", "first"] }],
        }, {
          entry_sequence: 1002,
          written: "最初",
          reading: "しょっぱな",
          commonness: 3,
          commonness_score: 30,
          meanings: ["very beginning"],
          senses: [{ parts_of_speech: ["noun"], meanings: ["very beginning"] }],
        }],
      },
    },
    coverageResponse(blocks) {
      return {
        summary: { known_occurrences: 0, total_occurrences: 1, unknown_unique: 1, excluded_names: 0 },
        blocks: [{
          id: blocks[0].id,
          tokens: [{ surface: "最初", expression: "最初", start_utf16: 0, end_utf16: 2, status: "unknown" }],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  const word = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word--unknown")
  );
  const overlay = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay")
  );
  overlay.getBoundingClientRect = () => ({ left: 100, top: 100, width: 800 });
  word.getBoundingClientRect = () => ({ left: 400, top: 500, bottom: 540, width: 80 });
  word.dispatch("click");
  await new Promise(setImmediate);

  const lookup = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-lookup")
  );
  assert.equal(lookup.style.values["--goi-ext-lookup-left"], "340px");
  assert.equal(lookup.style.values["--goi-ext-lookup-top"], "390px");
  assert.equal(
    descendants(lookup).find((node) => classNames(node).has("goi-dictionary-summary")).textContent,
    "2 matching entries",
  );
  assert.deepEqual(
    descendants(lookup)
      .filter((node) => classNames(node).has("goi-dictionary-reading"))
      .map((node) => node.textContent),
    ["さいしょ", "しょっぱな"],
  );
  assert.deepEqual(
    descendants(lookup).filter((node) => node.tagName === "LI").map((node) => node.textContent),
    ["beginning", "first", "very beginning"],
  );
  const choices = descendants(lookup).filter((node) =>
    classNames(node).has("goi-dictionary-select")
  );
  assert.equal(choices.length, 2);
  choices[1].dispatch("click");
  await new Promise(setImmediate);

  const capture = harness.messages.find((message) => message.type === "goi.capture.direct");
  assert.equal(capture.capture.suggestedEntrySequence, 1002);
});

test("keeps an explicit mining fallback when the dictionary has no entry", async () => {
  const harness = createHarness("未知語", {
    dictionaryResponse: {
      ok: true,
      result: {
        query: "未知語",
        state: "no_match",
        candidates: [],
      },
    },
    coverageResponse(blocks) {
      return {
        summary: { known_occurrences: 0, total_occurrences: 1, unknown_unique: 1, excluded_names: 0 },
        blocks: [{
          id: blocks[0].id,
          tokens: [{ surface: "未知語", expression: "未知語", start_utf16: 0, end_utf16: 3, status: "unknown" }],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  const word = descendants(harness.player).find((node) =>
    node.textContent === "未知語" && classNames(node).has("goi-ext-caption-word")
  );
  word.dispatch("click");
  await new Promise(setImmediate);

  const fallback = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-selection")
  );
  assert.equal(fallback.hidden, false);
  assert.equal(fallback.textContent, "Send to mining");
  assert.equal(harness.messages.some((message) => message.type === "goi.capture.direct"), false);

  fallback.dispatch("click");
  await new Promise(setImmediate);
  const capture = harness.messages.find((message) => message.type === "goi.capture.direct");
  assert.equal(capture.capture.rawText, "未知語");
});

test("marks an unknown subtitle word as known without mining it", async () => {
  const harness = createHarness("育てる", {
    coverageResponse: {
      summary: { known_occurrences: 0, total_occurrences: 1, unknown_unique: 1, excluded_names: 0 },
      blocks: [{
        id: 1,
        tokens: [{
          surface: "育てる",
          expression: "育てる",
          start_utf16: 0,
          end_utf16: 3,
          status: "unknown",
        }],
      }],
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  const unknown = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word--unknown")
  );
  unknown.dispatch("click");
  await new Promise(setImmediate);
  const markKnown = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-known")
  );
  assert.equal(markKnown.hidden, false);
  assert.equal(markKnown.textContent, "Mark as known");

  markKnown.dispatch("click");
  await new Promise(setImmediate);

  assert.deepEqual(
    JSON.parse(JSON.stringify(harness.messages.find((message) => message.type === "goi.vocabulary.known"))),
    { type: "goi.vocabulary.known", version: 1, expression: "育てる" },
  );
  assert.equal(harness.messages.some((message) => message.type === "goi.capture.direct"), false);
  assert.equal(markKnown.hidden, true);
});

test("does not round coverage up to 100 while an unknown word is highlighted", async () => {
  const caption = "猫".repeat(200);
  const tokens = Array.from(caption, function (surface, index) {
    return {
      surface,
      expression: surface,
      start_utf16: index,
      end_utf16: index + 1,
      status: index === 199 ? "unknown" : "known",
    };
  });
  const harness = createHarness(caption, {
    coverageResponse(blocks) {
      return {
        summary: {
          known_occurrences: 199,
          total_occurrences: 200,
          unknown_unique: 1,
          excluded_names: 0,
        },
        blocks: [{ id: blocks[0].id, tokens }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  const coverage = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-coverage")
  );
  const unknown = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word--unknown")
  );
  assert.equal(coverage.textContent, "Goi · no Japanese transcript");
  assert.equal(unknown.textContent, "猫");
});

test("exposes observed subtitle lines for seeking and companion capture", async () => {
  const harness = createHarness("猫を読みます。", {
    currentTime: 12.4,
    captureResponse: {
      ok: true,
      queued: false,
      captureId: 42,
      revision: 3,
      captureNonce: "a".repeat(32),
      connectionOrigin: "https://goi.example",
    },
    coverageResponse(blocks) {
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 2,
          unknown_unique: 1,
          excluded_names: 0,
        },
        blocks: [{
          id: blocks[0].id,
          tokens: [
            { surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" },
            { surface: "読み", expression: "読む", start_utf16: 2, end_utf16: 4, status: "unknown" },
          ],
        }],
      };
    },
  });
  await harness.start();

  const pendingSession = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1,
  });
  assert.equal(pendingSession.session.lines[0].classification, "pending");
  await harness.analyzeCaption();

  const sessionResponse = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1,
  });
  assert.equal(sessionResponse.ok, true);
  assert.ok(sessionResponse.session.revision > pendingSession.session.revision);
  assert.equal(sessionResponse.session.lines[0].classification, "ready");
  assert.equal(sessionResponse.session.lines.length, 1);
  assert.deepEqual(JSON.parse(JSON.stringify(sessionResponse.session.lines[0].unknowns)), [{
    surface: "読み",
    expression: "読む",
    start: 2,
    end: 4,
  }]);
  assert.equal(sessionResponse.session.lines[0].sourcePositionMs, 12400);

  harness.video.currentTime = 30;
  const seek = await harness.requestRuntimeMessage({
    type: "goi.youtube.line.seek",
    version: 1,
    lineID: sessionResponse.session.lines[0].id,
  });
  assert.deepEqual(JSON.parse(JSON.stringify(seek)), { ok: true });
  assert.equal(harness.video.currentTime, 12.4);

  const capture = await harness.requestRuntimeMessage({
    type: "goi.youtube.line.capture",
    version: 1,
    lineID: sessionResponse.session.lines[0].id,
    surface: "読み",
  });
  assert.deepEqual(JSON.parse(JSON.stringify(capture)), { ok: true, queued: false });
  const sent = harness.messages.filter((message) => message.type === "goi.capture.direct").at(-1);
  assert.equal(sent.capture.expression, "読む");
  assert.equal(sent.capture.contextText, "猫を読みます。");
  assert.equal(sent.capture.sourcePositionMs, 12400);
});

test("does not duplicate subtitle history when the browser seeks to a collected line", async () => {
  const harness = createHarness("猫を見る。", {
    currentTime: 12,
    coverageResponse(blocks) {
      return {
        summary: { known_occurrences: 1, total_occurrences: 1, unknown_unique: 0, excluded_names: 0 },
        blocks: [{ id: blocks[0].id, tokens: [
          { surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" },
        ] }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  const original = await harness.requestRuntimeMessage({ type: "goi.youtube.session.get", version: 1 });
  const lineID = original.session.lines[0].id;
  await harness.requestRuntimeMessage({ type: "goi.youtube.line.seek", version: 1, lineID });
  harness.video.dispatch("seeking");
  harness.replaceCaptionCue("猫を見る。");
  harness.readCaption();

  const replay = await harness.requestRuntimeMessage({ type: "goi.youtube.session.get", version: 1 });
  assert.equal(replay.session.lines.length, 1);
  assert.equal(replay.session.lines[0].id, lineID);

  harness.replaceCaptionCue("犬を見る。");
  harness.readCaption();
  harness.replaceCaptionCue("猫を見る。");
  harness.readCaption();
  const naturalRepeat = await harness.requestRuntimeMessage({ type: "goi.youtube.session.get", version: 1 });
  assert.equal(naturalRepeat.session.lines.length, 3);
  assert.deepEqual(JSON.parse(JSON.stringify(naturalRepeat.session.lines.map((line) => line.text))), [
    "猫を見る。", "犬を見る。", "猫を見る。",
  ]);
});

test("keeps pending unknown-only dialogue readable and shows it after an unknown result", async () => {
  const harness = createHarness("猫", {
    persistedSettings: { displayMode: "unknown_only" },
    coverageResponse(blocks) {
      return {
        summary: {
          known_occurrences: 0,
          total_occurrences: 1,
          unknown_unique: 1,
          excluded_names: 0,
        },
        blocks: [{
          id: blocks[0].id,
          tokens: [{ surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "unknown" }],
        }],
      };
    },
  });
  await harness.start();
  const captionText = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-text")
  );
  assert.equal(captionText.hidden, false);

  await harness.analyzeCaption();

  assert.equal(captionText.hidden, false);
  assert.equal(classNames(harness.player).has("goi-ext-hide-native-captions"), true);
});

test("hides known dialogue only after unknown-only coverage is ready", async () => {
  const harness = createHarness("猫", {
    persistedSettings: { displayMode: "unknown_only" },
    coverageResponse(blocks) {
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 1,
          unknown_unique: 0,
          excluded_names: 0,
        },
        blocks: [{
          id: blocks[0].id,
          tokens: [{ surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" }],
        }],
      };
    },
  });
  await harness.start();
  const captionText = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-text")
  );
  assert.equal(captionText.hidden, false);

  await harness.analyzeCaption();

  assert.equal(captionText.hidden, true);
});

test("fails open when unknown-only coverage is unavailable", async () => {
  const harness = createHarness("猫", {
    persistedSettings: { displayMode: "unknown_only" },
    coverageFailure: true,
  });
  await harness.start();
  await harness.analyzeCaption();
  const captionText = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-text")
  );

  assert.equal(captionText.hidden, false);
});

test("reveals the last observed line when playback pauses", async () => {
  const harness = createHarness("一度だけ見せる。", {
    persistedSettings: { displayMode: "pause_reveal" },
  });
  await harness.start();
  const captionText = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-text")
  );
  assert.equal(captionText.hidden, true);

  harness.clearCaption();
  harness.readCaption();
  harness.runTimer(500);
  assert.equal(captionText.children.length, 0);

  harness.video.pause();

  assert.equal(captionText.hidden, false);
  assert.equal(
    captionText.children.map((node) => node.textContent).join(""),
    "一度だけ見せる。"
  );
});

test("replaces a partial token when a rolling caption completes the word", async () => {
  const harness = createHarness("勉強す", {
    coverageResponse(blocks) {
      const block = blocks[0];
      const complete = block.text === "勉強する";
      const tokens = complete
        ? [{ surface: "勉強する", expression: "勉強する", start_utf16: 0, end_utf16: 4, status: "known" }]
        : [{ surface: "勉強", expression: "勉強", start_utf16: 0, end_utf16: 2, status: "unknown" }];
      return {
        summary: {
          known_occurrences: complete ? 1 : 0,
          total_occurrences: 1,
          unknown_unique: complete ? 0 : 1,
          excluded_names: 0,
        },
        blocks: [{ id: block.id, tokens }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.setCaption("勉強する");
  harness.readCaption();
  await harness.analyzeCaption();

  assert.equal(harness.coverage().captionsAnalyzed, 1);
  assert.equal(harness.coverage().summary.known_occurrences, 1);
  assert.equal(harness.coverage().summary.total_occurrences, 1);
  const coverage = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-coverage")
  );
  assert.equal(coverage.textContent, "Goi · no Japanese transcript");
});

test("keeps words that scroll out while a rolling caption adds new text", async () => {
  const harness = createHarness("私は学校へ", {
    coverageResponse(blocks) {
      const block = blocks[0];
      const complete = block.text === "私は学校へ行く";
      return {
        summary: {
          known_occurrences: complete ? 2 : 1,
          total_occurrences: complete ? 3 : 2,
          unknown_unique: 1,
          excluded_names: 0,
        },
        blocks: [{
          id: block.id,
          tokens: complete ? [
            { surface: "私", expression: "私", start_utf16: 0, end_utf16: 1, status: "known" },
            { surface: "学校", expression: "学校", start_utf16: 2, end_utf16: 4, status: "unknown" },
            { surface: "行く", expression: "行く", start_utf16: 5, end_utf16: 7, status: "known" },
          ] : [
            { surface: "私", expression: "私", start_utf16: 0, end_utf16: 1, status: "known" },
            { surface: "学校", expression: "学校", start_utf16: 2, end_utf16: 4, status: "unknown" },
          ],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.setCaption("学校へ行く");
  harness.readCaption();
  await harness.analyzeCaption();

  const requests = harness.messages.filter((message) => message.type === "goi.coverage.classify");
  assert.equal(requests.at(-1).blocks[0].text, "私は学校へ行く");
  assert.equal(harness.coverage().captionsAnalyzed, 1);
  assert.equal(harness.coverage().summary.known_occurrences, 2);
  assert.equal(harness.coverage().summary.total_occurrences, 3);
});

test("captures the complete rolling sentence through the instant-capture hotkey", async () => {
  const harness = createHarness("私は学校へ", {
    captureResponse: { ok: true, queued: false },
    coverageResponse(blocks) {
      return {
        summary: {
          known_occurrences: 2,
          total_occurrences: 3,
          unknown_unique: 1,
          excluded_names: 0,
        },
        blocks: [{
          id: blocks[0].id,
          tokens: [
            { surface: "私", expression: "私", start_utf16: 0, end_utf16: 1, status: "known" },
            { surface: "学校", expression: "学校", start_utf16: 2, end_utf16: 4, status: "known" },
            { surface: "行く", expression: "行く", start_utf16: 5, end_utf16: 7, status: "unknown" },
          ],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();
  harness.setCaption("学校へ行く");
  harness.readCaption();
  await harness.analyzeCaption();
  harness.selectCaption("行く");

  const response = await harness.requestRuntimeMessage({
    type: "goi.youtube.capture.current",
    version: 1,
  });

  assert.deepEqual(JSON.parse(JSON.stringify(response)), { ok: true, handled: true });
  const sent = harness.messages.filter((message) => message.type === "goi.capture.direct").at(-1);
  assert.equal(sent.lineID, 1);
  assert.equal(sent.capture.rawText, "行く");
  assert.equal(sent.capture.contextText, "私は学校へ行く");
});

test("leaves instant capture to selected-text fallback without an active target", async () => {
  const harness = createHarness("字幕です。");
  await harness.start();

  const response = await harness.requestRuntimeMessage({
    type: "goi.youtube.capture.current",
    version: 1,
  });

  assert.deepEqual(JSON.parse(JSON.stringify(response)), { ok: true, handled: false });
  assert.equal(harness.messages.some((message) => message.type === "goi.capture.direct"), false);
});

test("completes a word across a one-character rolling overlap", async () => {
  const harness = createHarness("食べ", {
    coverageResponse(blocks) {
      const block = blocks[0];
      const complete = block.text === "食べる";
      return {
        summary: {
          known_occurrences: complete ? 1 : 0,
          total_occurrences: 1,
          unknown_unique: complete ? 0 : 1,
          excluded_names: 0,
        },
        blocks: [{
          id: block.id,
          tokens: [{
            surface: block.text,
            expression: complete ? "食べる" : block.text,
            start_utf16: 0,
            end_utf16: block.text.length,
            status: complete ? "known" : "unknown",
          }],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.setCaption("べる");
  harness.readCaption();
  await harness.analyzeCaption();

  const requests = harness.messages.filter((message) => message.type === "goi.coverage.classify");
  assert.equal(requests.at(-1).blocks[0].text, "食べる");
  assert.equal(harness.coverage().captionsAnalyzed, 1);
  assert.equal(harness.coverage().summary.known_occurrences, 1);
  assert.equal(harness.coverage().summary.total_occurrences, 1);
});

test("keeps a one-character overlap between long rolling windows", async () => {
  const harness = createHarness("これは気をつ", {
    coverageResponse(blocks) {
      const block = blocks[0];
      const complete = block.text === "これは気をつけるべきです";
      return {
        summary: {
          known_occurrences: complete ? 1 : 0,
          total_occurrences: 1,
          unknown_unique: complete ? 0 : 1,
          excluded_names: 0,
        },
        blocks: [{
          id: block.id,
          tokens: [{
            surface: block.text,
            expression: complete ? "気をつける" : block.text,
            start_utf16: 0,
            end_utf16: block.text.length,
            status: complete ? "known" : "unknown",
          }],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.setCaption("つけるべきです");
  harness.readCaption();
  await harness.analyzeCaption();

  const requests = harness.messages.filter((message) => message.type === "goi.coverage.classify");
  assert.equal(requests.at(-1).blocks[0].text, "これは気をつけるべきです");
  assert.equal(harness.coverage().captionsAnalyzed, 1);
  assert.equal(harness.coverage().summary.known_occurrences, 1);
  assert.equal(harness.coverage().summary.total_occurrences, 1);
});

test("counts the same caption again after a real caption gap", async () => {
  const harness = createHarness("猫", {
    coverageResponse: {
      summary: {
        known_occurrences: 1,
        total_occurrences: 1,
        unknown_unique: 0,
        excluded_names: 0,
      },
      blocks: [{
        id: 1,
        tokens: [{ surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" }],
      }],
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.clearCaption();
  harness.readCaption();
  harness.setCaption("猫");
  harness.readCaption();
  await harness.analyzeCaption();

  assert.equal(harness.coverage().captionsAnalyzed, 2);
  assert.equal(harness.coverage().summary.known_occurrences, 2);
  assert.equal(harness.coverage().summary.total_occurrences, 2);
  assert.equal(harness.messages.filter((message) =>
    message.type === "goi.coverage.classify"
  ).length, 1);
});

test("counts a repeated caption when YouTube replaces the cue without a gap", async () => {
  const harness = createHarness("猫", {
    coverageResponse: {
      summary: {
        known_occurrences: 1,
        total_occurrences: 1,
        unknown_unique: 0,
        excluded_names: 0,
      },
      blocks: [{
        id: 1,
        tokens: [{ surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" }],
      }],
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.replaceCaptionCue("猫");
  harness.readCaption();
  await harness.analyzeCaption();

  assert.equal(harness.coverage().captionsAnalyzed, 2);
  assert.equal(harness.coverage().summary.known_occurrences, 2);
  assert.equal(harness.coverage().summary.total_occurrences, 2);
  assert.equal(harness.messages.filter((message) =>
    message.type === "goi.coverage.classify"
  ).length, 1);
});

test("counts a repeated caption when YouTube reuses the caption window", async () => {
  const harness = createHarness("猫", {
    coverageResponse: {
      summary: {
        known_occurrences: 1,
        total_occurrences: 1,
        unknown_unique: 0,
        excluded_names: 0,
      },
      blocks: [{
        id: 1,
        tokens: [{ surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" }],
      }],
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.replaceCaptionSegment("猫");
  harness.readCaption();
  await harness.analyzeCaption();

  assert.equal(harness.coverage().captionsAnalyzed, 2);
  assert.equal(harness.coverage().summary.known_occurrences, 2);
  assert.equal(harness.coverage().summary.total_occurrences, 2);
  assert.equal(harness.messages.filter((message) =>
    message.type === "goi.coverage.classify"
  ).length, 1);
});

test("counts a replaced prefix caption as a distinct occurrence", async () => {
  const harness = createHarness("はい", {
    coverageResponse(blocks) {
      const block = blocks[0];
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 1,
          unknown_unique: 0,
          excluded_names: 0,
        },
        blocks: [{
          id: block.id,
          tokens: [{
            surface: block.text,
            expression: block.text,
            start_utf16: 0,
            end_utf16: block.text.length,
            status: "known",
          }],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.replaceCaptionCue("はい、そうです");
  harness.readCaption();
  await harness.analyzeCaption();

  assert.equal(harness.coverage().captionsAnalyzed, 2);
  assert.equal(harness.coverage().summary.total_occurrences, 2);
  assert.equal(harness.messages.filter((message) =>
    message.type === "goi.coverage.classify"
  ).length, 2);
});

test("counts a replaced overlapping caption as a distinct occurrence", async () => {
  const harness = createHarness("猫を見る", {
    coverageResponse(blocks) {
      const block = blocks[0];
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 1,
          unknown_unique: 0,
          excluded_names: 0,
        },
        blocks: [{
          id: block.id,
          tokens: [{
            surface: block.text,
            expression: block.text,
            start_utf16: 0,
            end_utf16: block.text.length,
            status: "known",
          }],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.replaceCaptionCue("見るよ");
  harness.readCaption();
  await harness.analyzeCaption();

  assert.equal(harness.coverage().captionsAnalyzed, 2);
  assert.equal(harness.coverage().summary.total_occurrences, 2);
});

test("attaches the overlay to the active player and excludes it from page coverage", async () => {
  const harness = createHarness("日本語", { distractorPlayer: true, videoPaused: true });
  await harness.start();

  const overlay = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay")
  );
  assert.ok(overlay);
  assert.equal(overlay.dataset.goiCoverageUi, "true");
  assert.equal(descendants(harness.distractorPlayer).some((node) =>
    classNames(node).has("goi-ext-overlay")
  ), false);
  assert.equal(harness.activeCaption(), "日本語");
});

test("prefers a playing player over a larger paused player", async () => {
  const harness = createHarness("日本語", {
    distractorPlayer: true,
    distractorPlayerBounds: {
      top: 0,
      left: 0,
      right: 1280,
      bottom: 720,
      width: 1280,
      height: 720,
    },
    distractorVideoPaused: true,
    videoPaused: false,
  });
  await harness.start();

  assert.equal(descendants(harness.player).some((node) =>
    classNames(node).has("goi-ext-overlay")
  ), true);
  assert.equal(descendants(harness.distractorPlayer).some((node) =>
    classNames(node).has("goi-ext-overlay")
  ), false);
});

test("clears the mirror when YouTube no longer exposes a player", async () => {
  const harness = createHarness("日本語");
  await harness.start();

  assert.equal(harness.activeCaption(), "日本語");
  assert.equal(classNames(harness.player).has("goi-ext-hide-native-captions"), true);

  harness.removePlayerCandidate();
  harness.readCaption();

  assert.equal(harness.activeCaption(), "");
  assert.equal(classNames(harness.player).has("goi-ext-hide-native-captions"), false);
  assert.equal(descendants(harness.player).some((node) =>
    classNames(node).has("goi-ext-overlay")
  ), false);
  assert.equal(harness.coverage().captionsAnalyzed, 0);
});

test("retains observed subtitle history through a transient player replacement", async () => {
  const harness = createHarness("最初の字幕。");
  await harness.start();
  await harness.analyzeCaption();

  harness.removePlayerCandidate();
  harness.readCaption();
  let response = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1,
  });
  assert.equal(response.session.lines.length, 1);

  harness.restorePlayerCandidate();
  harness.replaceCaptionCue("次の字幕。");
  harness.readCaption();
  response = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1,
  });
  assert.equal(response.session.lines.some((line) => line.text === "最初の字幕。"), true);
});

test("refreshes companion playback metadata when no subtitle text changes", async () => {
  const harness = createHarness("字幕です。");
  await harness.start();
  const initial = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1,
  });

  harness.video.pause();
  const paused = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1,
    sessionID: initial.session.sessionID,
    sinceRevision: initial.session.revision,
  });

  assert.equal(paused.unchanged, undefined);
  assert.equal(paused.session.playbackPaused, true);
  assert.notEqual(paused.session.revision, initial.session.revision);
});

test("refreshes companion observing metadata when the overlay is disabled", async () => {
  const harness = createHarness("字幕です。");
  await harness.start();
  const initial = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1,
  });

  harness.storageListener({
    [settingsModel.STORAGE_KEY]: { newValue: { overlayEnabled: false } },
  }, "sync");
  const disabled = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1,
    sessionID: initial.session.sessionID,
    sinceRevision: initial.session.revision,
  });

  assert.equal(disabled.unchanged, undefined);
  assert.equal(disabled.session.observing, false);
  assert.notEqual(disabled.session.revision, initial.session.revision);
});

test("ignores synthetic clicks that would capture a caption word", async () => {
  const harness = createHarness("猫を見る。", {
    coverageResponse: {
      summary: {
        known_occurrences: 0,
        total_occurrences: 1,
        unknown_unique: 1,
        excluded_names: 0,
      },
      blocks: [{
        id: 1,
        tokens: [{
          surface: "猫",
          expression: "猫",
          start_utf16: 0,
          end_utf16: 1,
          status: "unknown",
        }],
      }],
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  const unknown = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word--unknown")
  );
  const lookup = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-lookup")
  );
  assert.equal(unknown.tabIndex, 0);
  assert.equal(unknown.role, "button");
  assert.equal(unknown["aria-label"], "Look up 猫");
  unknown.dispatch("click", { isTrusted: false });
  assert.equal(lookup.hidden, true);

  unknown.dispatch("keydown", { key: "Enter" });
  await new Promise(setImmediate);
  assert.equal(lookup.hidden, false);
  harness.pointOutsideOverlay();
  assert.equal(lookup.hidden, true);

  unknown.dispatch("click");
  await new Promise(setImmediate);
  assert.equal(lookup.hidden, false);
  descendants(lookup).find((node) =>
    classNames(node).has("goi-dictionary-select")
  ).dispatch("click", { isTrusted: false });
  await new Promise(setImmediate);

  assert.equal(harness.messages.some((message) => message.type === "goi.capture.direct"), false);
});

test("bounds retained caption analyses", async () => {
  const harness = createHarness("一。", {
    coverageResponse(blocks) {
      const block = blocks[0];
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 1,
          unknown_unique: 0,
          excluded_names: 0,
        },
        blocks: [{
          id: block.id,
          tokens: [{
            surface: block.text,
            expression: block.text,
            start_utf16: 0,
            end_utf16: block.text.length,
            status: "known",
          }],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  for (let index = 1; index < 305; index += 1) {
    harness.setCaption(String.fromCodePoint(0x4e00 + index) + "。");
    harness.readCaption();
    await harness.analyzeCaption();
  }

  assert.equal(harness.coverage().captionsAnalyzed, 305);
  assert.equal(harness.coverage().retainedCaptions, 100);
  const session = await harness.requestRuntimeMessage({
    type: "goi.youtube.session.get",
    version: 1,
  });
  assert.equal(session.session.lines.length, 300);
  assert.equal(session.session.lines.at(-1).text, String.fromCodePoint(0x4e00 + 304) + "。");
});

test("bounds pending caption analysis while a coverage request is stalled", async () => {
  const stalledCoverage = new Promise(() => {});
  const harness = createHarness("一。", {
    coverageResponse() {
      return stalledCoverage;
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  for (let index = 1; index <= 105; index += 1) {
    harness.setCaption(String.fromCodePoint(0x4e00 + index) + "。");
    harness.readCaption();
    harness.runTimer(80);
  }

  assert.equal(harness.coverage().sampled, true);
  assert.ok(harness.coverage().pendingAnalyses <= 101);
  const coverage = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-coverage")
  );
  assert.equal(coverage.textContent, "Goi · no Japanese transcript");
});

test("coalesces a queued rolling caption to its latest visible window", async () => {
  let resolveFirst;
  let requests = 0;
  const firstCoverage = new Promise(function (resolve) {
    resolveFirst = resolve;
  });
  const harness = createHarness("一二", {
    coverageResponse(blocks) {
      requests += 1;
      if (requests === 1) {
        return firstCoverage;
      }
      const block = blocks[0];
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 2,
          unknown_unique: 1,
          excluded_names: 0,
        },
        blocks: [{
          id: block.id,
          tokens: [
            { surface: "一二三", expression: "一二三", start_utf16: 0, end_utf16: 3, status: "known" },
            { surface: "四", expression: "四", start_utf16: 3, end_utf16: 4, status: "unknown" },
          ],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.setCaption("一二三");
  harness.readCaption();
  await harness.analyzeCaption();
  harness.setCaption("二三四");
  harness.readCaption();
  await harness.analyzeCaption();

  resolveFirst({
    summary: {
      known_occurrences: 1,
      total_occurrences: 1,
      unknown_unique: 0,
      excluded_names: 0,
    },
    blocks: [{
      id: 1,
      tokens: [{ surface: "一二", expression: "一二", start_utf16: 0, end_utf16: 2, status: "known" }],
    }],
  });
  await new Promise(setImmediate);
  await new Promise(setImmediate);

  const unknown = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word--unknown")
  );
  assert.equal(harness.messages.filter((message) =>
    message.type === "goi.coverage.classify"
  ).at(-1).blocks[0].text, "一二三四");
  assert.equal(unknown.textContent, "四");
});

test("marks stale caption coverage while retrying and clears it after recovery", async () => {
  let requests = 0;
  const harness = createHarness("猫", {
    coverageResponse(blocks) {
      requests += 1;
      if (requests === 2) {
        throw new Error("coverage unavailable");
      }
      const block = blocks[0];
      return {
        summary: {
          known_occurrences: 1,
          total_occurrences: 1,
          unknown_unique: 0,
          excluded_names: 0,
        },
        blocks: [{
          id: block.id,
          tokens: [{
            surface: block.text,
            expression: block.text,
            start_utf16: 0,
            end_utf16: block.text.length,
            status: "known",
          }],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  const coverage = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-coverage")
  );
  harness.setCaption("犬");
  harness.readCaption();
  await harness.analyzeCaption();

  assert.equal(coverage.textContent, "Goi · no Japanese transcript");

  harness.runTimer(30000);
  await new Promise(setImmediate);

  assert.equal(coverage.textContent, "Goi · no Japanese transcript");
  assert.equal(harness.coverage().summary.total_occurrences, 2);
});

test("saves the exact selection with its caption and selection-time timestamp", async () => {
  const originalCaption = "昨日は映画を見ました。";
  const harness = createHarness(originalCaption, { currentTime: 48.21 });
  await harness.start();

  harness.selectCaption("見ました");
  await new Promise(setImmediate);
  const useEntry = descendants(harness.player).find((node) =>
    classNames(node).has("goi-dictionary-select")
  );

  assert.equal(useEntry.tagName, "BUTTON");
  assert.equal(useEntry.type, "button");
  assert.equal(useEntry.textContent, "Mine");

  harness.setCaption("次の字幕です。");
  harness.video.currentTime = 51;
  harness.readCaption();
  useEntry.dispatch("click");
  await new Promise(setImmediate);

  const message = harness.messages.find((candidate) => candidate.type === "goi.capture.direct");
  assert.deepEqual(JSON.parse(JSON.stringify(message)), {
    type: "goi.capture.direct",
    version: 1,
    lineID: 1,
    capture: {
      rawText: "見ました",
      expression: "見ました",
      contextText: originalCaption,
      sourceKind: "video",
      sourceTitle: "Japanese lesson - YouTube",
      sourceURL: "https://www.youtube.com/watch?v=test",
      sourcePositionMs: 48210,
      suggestedEntrySequence: 9001,
    },
  });
  const lookup = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-lookup")
  );
  assert.equal(lookup.hidden, true);
});

test("reports a queued capture without treating it as delivered", async () => {
  const harness = createHarness("昨日は映画を見ました。", {
    persistedSettings: { pauseBehavior: "after_capture" },
    captureResponse: { ok: true, queued: true },
  });
  await harness.start();

  harness.selectCaption("見ました");
  await new Promise(setImmediate);
  const useEntry = descendants(harness.player).find((node) =>
    classNames(node).has("goi-dictionary-select")
  );
  useEntry.dispatch("click");
  await new Promise(setImmediate);

  const status = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-status")
  );
  assert.equal(status.textContent, "Queued — Goi will retry");
  assert.equal(harness.video.pauseCount, 0);
});

test("pauses on a real caption selection when configured", async () => {
  const harness = createHarness("日本語を勉強しています。", {
    persistedSettings: { pauseBehavior: "on_selection" },
  });
  await harness.start();

  harness.selectCaption("勉強しています");

  assert.equal(harness.video.pauseCount, 1);
});

test("temporarily pauses on subtitle hover and keeps a clicked lookup paused", async () => {
  const harness = createHarness("猫です。", {
    persistedSettings: { pauseBehavior: "on_hover" },
    coverageResponse(blocks) {
      return {
        summary: {
          known_occurrences: 0,
          total_occurrences: 1,
          unknown_unique: 1,
          excluded_names: 0,
        },
        blocks: [{
          id: blocks[0].id,
          tokens: [{
            surface: "猫",
            expression: "猫",
            start_utf16: 0,
            end_utf16: 1,
            status: "unknown",
          }],
        }],
      };
    },
  });
  await harness.start();
  await harness.analyzeCaption();

  harness.hoverCaption();
  assert.equal(harness.video.paused, true);
  assert.equal(harness.video.pauseCount, 1);

  harness.leaveCaption();
  assert.equal(harness.video.paused, false);
  assert.equal(harness.video.playCount, 1);

  harness.hoverCaption();
  const word = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-word")
  );
  word.dispatch("click");
  harness.leaveCaption();
  assert.equal(harness.video.paused, true);

  harness.pointOutsideOverlay();
  assert.equal(harness.video.paused, false);
  assert.equal(harness.video.playCount, 2);
});

test("does not resume a video that was already paused before subtitle hover", async () => {
  const harness = createHarness("字幕です。", {
    persistedSettings: { pauseBehavior: "on_hover" },
    videoPaused: true,
  });
  await harness.start();

  harness.hoverCaption();
  harness.leaveCaption();

  assert.equal(harness.video.pauseCount, 0);
  assert.equal(harness.video.playCount, 0);
  assert.equal(harness.video.paused, true);
});

test("dismisses the save action when selection moves outside the caption", async () => {
  const harness = createHarness("日本語を勉強しています。", {
    persistedSettings: { pauseBehavior: "on_selection" },
  });
  await harness.start();

  harness.selectCaption("勉強しています");
  await new Promise(setImmediate);
  const lookup = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-lookup")
  );
  assert.equal(lookup.hidden, false);

  harness.selectOutsideCaption("日本語");

  assert.equal(lookup.hidden, true);
  assert.equal(harness.video.pauseCount, 1);
});

test("dismisses a saved selection when the user points outside the overlay", async () => {
  const harness = createHarness("日本語を勉強しています。");
  await harness.start();

  harness.selectCaption("日本語");
  await new Promise(setImmediate);
  const lookup = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-lookup")
  );
  assert.equal(lookup.hidden, false);

  harness.pointOutsideOverlay();

  assert.equal(lookup.hidden, true);
});

test("clears the old caption immediately when YouTube navigates", async () => {
  const harness = createHarness("前の動画の字幕です。");
  await harness.start();
  harness.selectCaption("前の動画");
  harness.clearCaption();

  harness.navigate("https://www.youtube.com/watch?v=next");

  const captionText = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-text")
  );
  const save = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-selection")
  );
  assert.equal(harness.activeCaption(), "");
  assert.equal(captionText.children.length, 0);
  assert.equal(save.hidden, true);
});

test("synchronizes visible controls when settings change outside the overlay", async () => {
  const harness = createHarness();
  await harness.start();
  harness.storageListener({
    [settingsModel.STORAGE_KEY]: {
      newValue: {
        fontSizePx: 51,
        verticalPercent: 24,
        backgroundOpacity: 0.2,
        pauseBehavior: "after_capture",
      },
    },
  }, "sync");

  const ranges = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-overlay-range")
  );
  const pauseChoices = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-overlay-radio") && node.name === "goi-ext-pauseBehavior"
  );
  const values = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-overlay-value")
  );
  assert.deepEqual(ranges.map((control) => control.value), ["51", "24", "0.2"]);
  assert.deepEqual(values.map((output) => output.textContent), ["51 px", "Upper", "20%"]);
  assert.deepEqual(ranges.map((control) => control["aria-valuetext"]), ["51 px", "Upper", "20%"]);
  assert.equal(pauseChoices.find((control) => control.checked).value, "after_capture");
});

test("shows live values for caption sliders and associates each output", async () => {
  const harness = createHarness("", {
    persistedSettings: {
      fontSizePx: 36,
      verticalPercent: 72,
      backgroundOpacity: 0.4,
    },
  });
  await harness.start();
  const ranges = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-overlay-range")
  );
  const values = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-overlay-value")
  );
  const overlay = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay")
  );

  assert.deepEqual(values.map((output) => output.textContent), ["36 px", "Lower", "40%"]);
  assert.deepEqual(values.map((output) => output.for), ranges.map((range) => range.id));

  ranges[0].value = "41";
  ranges[0].dispatch("input");

  assert.equal(values[0].textContent, "41 px");
  assert.equal(ranges[0]["aria-valuetext"], "41 px");
  assert.equal(overlay.style.values["--goi-ext-font-size"], "41px");
  assert.equal(harness.messages.some((message) =>
    message.type === "goi.settings.patch" && message.patch.fontSizePx === 41
  ), false);
});

test("quick text-size buttons adjust by two pixels, persist, and clamp", async () => {
  const harness = createHarness("", { persistedSettings: { fontSizePx: 95 } });
  await harness.start();
  const increase = descendants(harness.player).find((node) =>
    node["aria-label"] === "Increase caption text size"
  );
  const decrease = descendants(harness.player).find((node) =>
    node["aria-label"] === "Decrease caption text size"
  );
  const fontSize = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay-range")
  );
  const value = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay-value")
  );

  assert.equal(increase.tagName, "BUTTON");
  assert.equal(decrease.tagName, "BUTTON");
  increase.dispatch("click");
  await new Promise(setImmediate);

  assert.equal(fontSize.value, "96");
  assert.equal(value.textContent, "96 px");
  assert.deepEqual(JSON.parse(JSON.stringify(harness.messages.at(-1))), {
    type: "goi.settings.patch",
    version: 1,
    patch: { fontSizePx: 96 },
  });

  const writesAtMaximum = harness.messages.filter((message) =>
    message.type === "goi.settings.patch"
  ).length;
  increase.dispatch("click");
  assert.equal(harness.messages.filter((message) =>
    message.type === "goi.settings.patch"
  ).length, writesAtMaximum);

  decrease.dispatch("click");
  await new Promise(setImmediate);
  assert.equal(fontSize.value, "94");
  assert.equal(value.textContent, "94 px");
  assert.equal(harness.messages.at(-1).patch.fontSizePx, 94);
});

test("replays the previous five seconds without changing playback state", async () => {
  const harness = createHarness("", { currentTime: 12, videoPaused: true });
  await harness.start();
  const replay = descendants(harness.player).find((node) =>
    node["aria-label"] === "Replay previous 5 seconds"
  );

  assert.equal(replay.tagName, "BUTTON");
  replay.dispatch("click");
  assert.equal(harness.video.currentTime, 7);
  assert.equal(harness.video.paused, true);
  replay.dispatch("click");
  replay.dispatch("click");
  assert.equal(harness.video.currentTime, 0);
});

test("reports when the browser blocks playback", async () => {
  const harness = createHarness("", { playFailure: true, videoPaused: true });
  await harness.start();
  const play = descendants(harness.player).find((node) =>
    node["aria-label"] === "Play or pause video"
  );

  play.dispatch("click");
  await new Promise(setImmediate);

  const status = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-caption-status")
  );
  assert.equal(status.textContent, "Playback blocked");
  assert.equal(classNames(status).has("goi-ext-caption-status--error"), true);
});

test("resets every caption setting to its default and persists the reset", async () => {
  const harness = createHarness("", {
    persistedSettings: {
      displayMode: "hidden",
      fontSizePx: 58,
      verticalPercent: 20,
      backgroundOpacity: 0.1,
      pauseBehavior: "after_capture",
      hideNativeCaptions: false,
    },
  });
  await harness.start();
  const reset = descendants(harness.player).find((node) =>
    classNames(node).has("goi-ext-overlay-reset")
  );
  const ranges = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-overlay-range")
  );
  const values = descendants(harness.player).filter((node) =>
    classNames(node).has("goi-ext-overlay-value")
  );

  assert.equal(reset.tagName, "BUTTON");
  assert.equal(reset.textContent, "Reset captions");
  assert.equal(reset["aria-label"], "Reset caption settings");
  reset.dispatch("click");
  await new Promise(setImmediate);

  assert.deepEqual(
    JSON.parse(JSON.stringify(harness.messages.at(-1).patch)),
    settingsModel.DEFAULT_SETTINGS,
  );
  assert.deepEqual(ranges.map((control) => control.value), ["34", "78", "0.65"]);
  assert.deepEqual(values.map((output) => output.textContent), ["34 px", "Lower", "65%"]);
});
