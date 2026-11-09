const test = require("node:test");
const assert = require("node:assert/strict");
const {
  classNames,
  createHarness,
  descendants,
} = require("./helpers/overlay-harness.js");

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
