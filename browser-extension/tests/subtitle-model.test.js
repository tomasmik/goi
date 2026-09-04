const test = require("node:test");
const assert = require("node:assert/strict");

const subtitleModel = require("../shared/subtitle-model.js");

test("extracts bounded unknown targets using UTF-16 token offsets", function () {
  assert.deepEqual(subtitleModel.unknownWords("猫を勉強する", {
    tokens: [
      { surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" },
      { surface: "勉強", expression: "勉強する", start_utf16: 2, end_utf16: 4, status: "unknown" },
      { surface: "outside", expression: "outside", start_utf16: 20, end_utf16: 27, status: "unknown" },
    ],
  }), [{
    surface: "勉強",
    expression: "勉強する",
    start: 2,
    end: 4,
  }]);
});

test("exposes known and unknown subtitle words for dictionary lookup", function () {
  assert.deepEqual(subtitleModel.words("猫を読む", {
    tokens: [
      { surface: "猫", expression: "猫", reading: "ねこ", start_utf16: 0, end_utf16: 1, status: "known" },
      { surface: "読む", expression: "読む", reading: "よむ", start_utf16: 2, end_utf16: 4, status: "unknown" },
    ],
  }), [
    { surface: "猫", expression: "猫", reading: "ねこ", start: 0, end: 1, status: "known" },
    { surface: "読む", expression: "読む", reading: "よむ", start: 2, end: 4, status: "unknown" },
  ]);
});

test("makes Japanese words available for lookup before classification", function () {
  assert.deepEqual(subtitleModel.lookupWords("猫を見る。日本語です", []), [
    { surface: "猫", expression: "猫", start: 0, end: 1, status: "unclassified" },
    { surface: "を", expression: "を", start: 1, end: 2, status: "unclassified" },
    { surface: "見る", expression: "見る", start: 2, end: 4, status: "unclassified" },
    { surface: "日本語", expression: "日本語", start: 5, end: 8, status: "unclassified" },
    { surface: "です", expression: "です", start: 8, end: 10, status: "unclassified" },
  ]);
});

test("keeps classified words and fills lookup gaps", function () {
  const classified = subtitleModel.words("猫を見る", {
    tokens: [
      { surface: "猫", expression: "猫", start_utf16: 0, end_utf16: 1, status: "known" },
      { surface: "見る", expression: "見る", start_utf16: 2, end_utf16: 4, status: "unknown" },
    ],
  });
  assert.deepEqual(subtitleModel.lookupWords("猫を見る", classified), [
    { surface: "猫", expression: "猫", start: 0, end: 1, status: "known" },
    { surface: "を", expression: "を", start: 1, end: 2, status: "unclassified" },
    { surface: "見る", expression: "見る", start: 2, end: 4, status: "unknown" },
  ]);
});

test("keeps leeches selectable without treating them as unknown", function () {
  const block = {
    tokens: [
      { surface: "育て", expression: "育てる", start_utf16: 0, end_utf16: 2, status: "leech" },
      { surface: "難しい", expression: "難しい", start_utf16: 3, end_utf16: 6, status: "suspended_leech" },
    ],
  };

  assert.deepEqual(subtitleModel.words("育て 難しい", block), [
    { surface: "育て", expression: "育てる", start: 0, end: 2, status: "leech" },
    { surface: "難しい", expression: "難しい", start: 3, end: 6, status: "suspended_leech" },
  ]);
  assert.deepEqual(subtitleModel.unknownWords("育て 難しい", block), []);
});

test("aligns furigana with kanji while leaving visible kana alone", function () {
  assert.deepEqual(subtitleModel.furiganaParts("食べる", "タベル"), [
    { text: "食", reading: "た" },
    { text: "べる", reading: "" },
  ]);
  assert.deepEqual(subtitleModel.furiganaParts("申し込む", "モウシコム"), [
    { text: "申", reading: "もう" },
    { text: "し", reading: "" },
    { text: "込", reading: "こ" },
    { text: "む", reading: "" },
  ]);
  assert.deepEqual(subtitleModel.furiganaParts("日本語", "ニホンゴ"), [
    { text: "日本語", reading: "にほんご" },
  ]);
  assert.deepEqual(subtitleModel.furiganaParts("食べました", "タベマシタ"), [
    { text: "食", reading: "た" },
    { text: "べました", reading: "" },
  ]);
  assert.deepEqual(subtitleModel.furiganaParts("お祝い", "オイワイ"), [
    { text: "お", reading: "" },
    { text: "祝", reading: "いわ" },
    { text: "い", reading: "" },
  ]);
  assert.deepEqual(subtitleModel.furiganaParts("一か月", "イッカゲツ"), [
    { text: "一", reading: "いっ" },
    { text: "か", reading: "" },
    { text: "月", reading: "げつ" },
  ]);
  assert.deepEqual(subtitleModel.furiganaParts("聞き返す", "キキカエス"), [
    { text: "聞", reading: "き" },
    { text: "き", reading: "" },
    { text: "返", reading: "かえ" },
    { text: "す", reading: "" },
  ]);
});

test("omits furigana for kana-only and missing readings", function () {
  assert.deepEqual(subtitleModel.furiganaParts("カメラ", "カメラ"), [
    { text: "カメラ", reading: "" },
  ]);
  assert.deepEqual(subtitleModel.furiganaParts("読む", ""), [
    { text: "読む", reading: "" },
  ]);
});

test("omits uncertain furigana rather than annotating visible kana", function () {
  assert.deepEqual(subtitleModel.furiganaParts("食べる", "しょく"), [
    { text: "食べる", reading: "" },
  ]);
  assert.deepEqual(subtitleModel.furiganaParts("食べる", "*"), [
    { text: "食べる", reading: "" },
  ]);
  assert.deepEqual(subtitleModel.furiganaParts("日本語", "*"), [
    { text: "日本語", reading: "" },
  ]);
  assert.deepEqual(subtitleModel.furiganaParts("日本語", "Japanese"), [
    { text: "日本語", reading: "" },
  ]);
});

test("resolves known lemmas while allowing an exact custom target", function () {
  const line = {
    text: "日本語を勉強する。",
    unknowns: [{ surface: "勉強", expression: "勉強する" }],
  };

  assert.deepEqual(subtitleModel.captureTarget(line, "勉強"), {
    surface: "勉強",
    expression: "勉強する",
  });
  assert.deepEqual(subtitleModel.captureTarget(line, "日本語"), {
    surface: "日本語",
    expression: "日本語",
  });
  assert.deepEqual(subtitleModel.captureTarget(line, "勉強する"), {
    surface: "勉強",
    expression: "勉強する",
  });
  assert.equal(subtitleModel.captureTarget(line, "英語"), null);
});

test("keeps a known token's dictionary form when it is selected", function () {
  const line = {
    text: "読みます",
    words: [{ surface: "読み", expression: "読む", start: 0, end: 2, status: "known" }],
    unknowns: [],
  };
  assert.deepEqual(subtitleModel.captureTarget(line, "読み"), {
    surface: "読み",
    expression: "読む",
  });
});

test("formats dictionary results and connection failures consistently", function () {
  assert.equal(subtitleModel.dictionaryText({
    ok: true,
    result: { candidates: [{ reading: "よむ", meanings: ["to read", "to count"] }] },
  }, 1), "よむ · to read");
  assert.equal(
    subtitleModel.dictionaryText({ ok: false, errorCode: "not_connected" }),
    "Connect Goi to look up words.",
  );
  assert.equal(
    subtitleModel.dictionaryText({ ok: true, result: { state: "no_match", candidates: [] } }),
    "No dictionary match.",
  );
  assert.equal(
    subtitleModel.dictionaryText({ ok: false, errorCode: "dictionary_unavailable" }),
    "The dictionary is not ready. Check Goi Settings.",
  );
  assert.equal(
    subtitleModel.dictionaryText({ ok: false, errorCode: "dictionary_api_unavailable" }),
    "Restart Goi, then reload the extension.",
  );
});

test("keeps dictionary candidates and senses structured for rich popovers", function () {
  assert.deepEqual(subtitleModel.dictionaryView({
    ok: true,
    result: {
      query: "最初",
      candidates: [{
        entry_sequence: 1579510,
        written: "最初",
        reading: "さいしょ",
        global_rank: 90,
        novel_rank: 190,
        meanings: ["beginning", "first"],
        senses: [{ parts_of_speech: ["noun", "adverb"], meanings: ["beginning", "first"] }],
      }],
    },
  }), {
    query: "最初",
    candidates: [{
      entrySequence: 1579510,
      written: "最初",
      reading: "さいしょ",
      globalRank: 90,
      novelRank: 190,
      meanings: ["beginning", "first"],
      senses: [{ partsOfSpeech: ["noun", "adverb"], meanings: ["beginning", "first"] }],
    }],
    message: "",
  });

  assert.deepEqual(subtitleModel.dictionaryView({
    ok: true,
    result: {
      query: "読む",
      candidates: [{ written: "読む", reading: "よむ", meanings: ["to read"] }],
    },
  }).candidates[0].senses, [{ partsOfSpeech: [], meanings: ["to read"] }]);

  assert.equal(subtitleModel.dictionaryView({
    ok: true,
    result: {
      query: "旧",
      candidates: [{ written: "旧", reading: "きゅう", commonness: 4, meanings: ["old"] }],
    },
  }).candidates[0].globalRank, null);
});

test("prefers an exact surface over an earlier matching lemma", function () {
  const line = {
    text: "掛けるとかける。",
    unknowns: [
      { surface: "かける", expression: "掛ける" },
      { surface: "掛ける", expression: "掛ける" },
    ],
  };

  assert.deepEqual(subtitleModel.captureTarget(line, "掛ける"), {
    surface: "掛ける",
    expression: "掛ける",
  });
});

test("identifies a YouTube session without transient playback parameters", function () {
  assert.equal(
    subtitleModel.sessionIdentity("https://www.youtube.com/watch?v=abc123&t=42s&list=mine#chapter"),
    "https://www.youtube.com/watch?v=abc123",
  );
  assert.equal(
    subtitleModel.sessionIdentity("https://www.youtube.com/shorts/abc123?feature=share"),
    "https://www.youtube.com/shorts/abc123",
  );
  assert.equal(subtitleModel.sessionIdentity("not a URL"), "");
});

test("formats subtitle timestamps", function () {
  assert.equal(subtitleModel.formatTimestamp(62400), "1:02");
  assert.equal(subtitleModel.formatTimestamp(3723000), "1:02:03");
});

test("selects each unsent 1T line once and summarizes batch delivery", function () {
  const oneUnknown = [{ surface: "猫" }];
  const lines = [
    { id: 1, classification: "ready", unknowns: oneUnknown },
    { id: 1, classification: "ready", unknowns: oneUnknown },
    { id: 2, classification: "pending", unknowns: oneUnknown },
    { id: 3, classification: "ready", unknowns: [] },
    { id: 4, classification: "ready", unknowns: oneUnknown },
  ];

  assert.deepEqual(
    subtitleModel.oneTargetLines(lines, new Set([4])).map((line) => line.id),
    [1],
  );
  assert.equal(subtitleModel.batchSummary(3, 2, 1), "3 sent · 2 queued · 1 failed");
  assert.equal(subtitleModel.batchSummary(0, 0, 0), "No lines were sent.");
});

test("treats repeated occurrences of one unknown expression as one target", function () {
  const lines = [{
    id: 1,
    classification: "ready",
    unknowns: [
      { surface: "猫", expression: "猫" },
      { surface: "猫", expression: "猫" },
    ],
  }, {
    id: 2,
    classification: "ready",
    unknowns: [
      { surface: "猫", expression: "猫" },
      { surface: "犬", expression: "犬" },
    ],
  }];

  assert.deepEqual(subtitleModel.oneTargetLines(lines).map((line) => line.id), [1]);
});
