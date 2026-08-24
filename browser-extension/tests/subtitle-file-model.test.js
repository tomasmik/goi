const test = require("node:test");
const assert = require("node:assert/strict");

const subtitleFileModel = require("../shared/subtitle-file-model.js");

const {
  LIMITS,
  activeCuesAt,
  activeTimelineCuesAt,
  clampOffsetMilliseconds,
  createCoverageBatches,
  createCueTimeline,
  cueIsOutsideVideo,
  decodeSubtitleBytes,
  describeOffsetMilliseconds,
  effectiveCueTimes,
  normalizeCueText,
  parseSubtitleFile,
} = subtitleFileModel;

function utf8(value) {
  return new TextEncoder().encode(value);
}

function utf16(value, littleEndian) {
  const bytes = new Uint8Array(2 + value.length * 2);
  bytes[0] = littleEndian ? 0xff : 0xfe;
  bytes[1] = littleEndian ? 0xfe : 0xff;
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    const offset = 2 + index * 2;
    bytes[offset] = littleEndian ? code & 0xff : code >> 8;
    bytes[offset + 1] = littleEndian ? code >> 8 : code & 0xff;
  }
  return bytes;
}

function expectCode(callback, code) {
  assert.throws(callback, function (error) {
    return error && error.name === "SubtitleFileError" && error.code === code;
  });
}

function repeatedSRTCues(count, textForIndex) {
  return Array.from({ length: count }, function (_unused, index) {
    return String(index + 1) + "\n00:00:00,000 --> 00:00:01,000\n" + textForIndex(index);
  }).join("\n\n");
}

function repeatedASSCues(count, textForIndex) {
  const lines = [
    "[Script Info]",
    "[Events]",
    "Format: Start, End, Text",
  ];
  for (let index = 0; index < count; index += 1) {
    lines.push("Dialogue: 0:00:00.00,0:00:01.00," + textForIndex(index));
  }
  return lines.join("\n");
}

test("parses, normalizes, reports, and sorts SRT cues", function () {
  const result = parseSubtitleFile(utf8([
    "9",
    "00:00:03,000 --> 00:00:04,000",
    " <i>日本語</i> ",
    "二   行",
    "",
    "00:00:01.000 --> 00:00:02.000",
    "最初",
    "",
    "not a cue",
    "",
    "4",
    "00:00:06,000 --> 00:00:05,000",
    "逆",
    "",
    "5",
    "00:00:07,000 --> 00:00:08,000",
    "<b> </b>",
  ].join("\r\n")), {
    firstCueID: 80,
    subtitleGeneration: 7,
  });

  assert.equal(result.format, "srt");
  assert.equal(result.skippedCueCount, 3);
  assert.equal(result.duplicateCueCount, 0);
  assert.equal(result.sourceCueCount, 5);
  assert.equal(result.validCueCount, 2);
  assert.equal(result.nextCueID, 82);
  assert.deepEqual(result.cues.map(function (cue) {
    return {
      id: cue.id,
      generation: cue.subtitleGeneration,
      sourceOrder: cue.sourceOrder,
      startMs: cue.startMs,
      endMs: cue.endMs,
      text: cue.text,
      classification: cue.classification,
      unknowns: cue.unknowns,
    };
  }), [{
    id: 81,
    generation: 7,
    sourceOrder: 1,
    startMs: 1000,
    endMs: 2000,
    text: "最初",
    classification: "pending",
    unknowns: [],
  }, {
    id: 80,
    generation: 7,
    sourceOrder: 0,
    startMs: 3000,
    endMs: 4000,
    text: "日本語\n二 行",
    classification: "pending",
    unknowns: [],
  }]);
  assert.notEqual(result.cues[0].unknowns, result.cues[1].unknowns);
});

test("removes only exact normalized duplicates and preserves repeats and overlaps", function () {
  const result = parseSubtitleFile(utf8([
    "00:00:00,000 --> 00:00:01,000",
    "<i>同じ</i>",
    "",
    "00:00:00,000 --> 00:00:01,000",
    "同じ",
    "",
    "00:00:02,000 --> 00:00:03,000",
    "同じ",
    "",
    "00:00:00,000 --> 00:00:01,000",
    "別の同時字幕",
    "",
    "00:00:00,500 --> 00:00:01,500",
    "重なる字幕",
  ].join("\n")), { firstCueID: 10 });

  assert.equal(result.validCueCount, 5);
  assert.equal(result.duplicateCueCount, 1);
  assert.deepEqual(result.cues.map(function (cue) {
    return [cue.id, cue.sourceOrder, cue.startMs, cue.endMs, cue.text];
  }), [
    [10, 0, 0, 1000, "同じ"],
    [12, 3, 0, 1000, "別の同時字幕"],
    [13, 4, 500, 1500, "重なる字幕"],
    [11, 2, 2000, 3000, "同じ"],
  ]);
});

test("parses WebVTT cue IDs and settings while skipping reserved blocks", function () {
  const result = parseSubtitleFile(utf8([
    "WEBVTT QA transcript",
    "Kind: captions",
    "",
    "NOTE this is not dialogue",
    "00:00.000 --> 00:01.000",
    "ignored note body",
    "",
    "STYLE",
    "::cue { color: lime; }",
    "",
    "REGION",
    "id:top",
    "",
    "speaker-one",
    "00:02.000 --> 00:04.000 align:start position:20%",
    "<v Alice><b>Hello &amp; welcome</b>",
    "<script>alert(1)</script>",
    "",
    "00:00:01.500 --> 00:00:02.500",
    "First",
    "",
    "bad-cue",
    "00:05.000 --> 00:04.000",
    "backwards",
  ].join("\n")), { subtitleGeneration: 2 });

  assert.equal(result.format, "webvtt");
  assert.equal(result.sourceCueCount, 3);
  assert.equal(result.skippedCueCount, 1);
  assert.deepEqual(result.cues.map(function (cue) {
    return [cue.startMs, cue.endMs, cue.text];
  }), [
    [1500, 2500, "First"],
    [2000, 4000, "Hello & welcome\n<script>alert(1)</script>"],
  ]);
});

test("decodes UTF-8 BOM and BOM-marked UTF-16 in either byte order", function () {
  const source = "WEBVTT\r\n\r\n00:00.000 --> 00:01.000\r\n日本語😀";
  const utf8WithBOM = new Uint8Array(3 + utf8(source).byteLength);
  utf8WithBOM.set([0xef, 0xbb, 0xbf]);
  utf8WithBOM.set(utf8(source), 3);

  const inputs = [utf8(source), utf8WithBOM, utf16(source, true), utf16(source, false)];
  inputs.forEach(function (input) {
    const result = parseSubtitleFile(input);
    assert.equal(result.format, "webvtt");
    assert.equal(result.cues[0].text, "日本語😀");
  });
  assert.equal(decodeSubtitleBytes(utf8("one\rtwo\r\nthree")), "one\ntwo\nthree");
});

test("decodes Japanese Shift-JIS and rejects UTF-16 without a byte-order mark", function () {
  assert.equal(decodeSubtitleBytes(Uint8Array.from([0x82, 0xa0, 0x82, 0xa2])), "あい");
  expectCode(function () {
    decodeSubtitleBytes(Uint8Array.from([0x31, 0x00, 0x0a, 0x00]));
  }, "unsupported_encoding");
});

test("parses ASS and SSA dialogue while removing presentation markup", function () {
  const source = `[Script Info]\nTitle: example\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\nDialogue: 0,0:00:01.20,0:00:03.40,Default,,0,0,0,,{\\i1}日本語{\\i0}\\Nsecond line`;
  const result = parseSubtitleFile(utf8(source));
  assert.equal(result.format, "ass");
  assert.equal(result.cues.length, 1);
  assert.equal(result.cues[0].startMs, 1200);
  assert.equal(result.cues[0].text, "日本語\nsecond line");
});

test("keeps unknown markup inert while normalizing recognized subtitle text", function () {
  assert.equal(
    normalizeCueText("  <b>Hello</b> &lt;unknown&gt;\n<00:01.500><v Speaker> two\t words </v>  "),
    "Hello <unknown>\ntwo words"
  );
  assert.equal(normalizeCueText("<script>do not run</script>"), "<script>do not run</script>");
});

test("reports unsupported files and files whose cues are all invalid", function () {
  expectCode(function () {
    parseSubtitleFile(utf8("ordinary text without timestamps"));
  }, "unsupported_format");

  assert.throws(function () {
    parseSubtitleFile(utf8("1\n00:00:02,000 --> 00:00:01,000\nbackwards"));
  }, function (error) {
    return error && error.code === "no_valid_cues" && error.skippedCueCount === 1;
  });

  expectCode(function () {
    parseSubtitleFile(utf8("WEBVTT\n\nNOTE no dialogue"));
  }, "no_valid_cues");
});

test("enforces source, cue, cue-count, and transcript hard limits without truncating", function () {
  assert.equal(
    decodeSubtitleBytes(new Uint8Array(LIMITS.sourceBytes).fill(0x20)).length,
    LIMITS.sourceBytes
  );
  expectCode(function () {
    decodeSubtitleBytes(new Uint8Array(LIMITS.sourceBytes + 1));
  }, "source_too_large");

  const maximumCue = "猫".repeat(LIMITS.cueCharacters);
  assert.equal(
    parseSubtitleFile(utf8("00:00:00,000 --> 00:00:01,000\n" + maximumCue)).totalCharacters,
    LIMITS.cueCharacters
  );
  expectCode(function () {
    parseSubtitleFile(utf8("00:00:00,000 --> 00:00:01,000\n" + maximumCue + "猫"));
  }, "cue_too_long");

  const maximumCount = repeatedSRTCues(LIMITS.validCues, function (index) {
    return "cue " + index;
  });
  assert.equal(parseSubtitleFile(utf8(maximumCount)).cues.length, LIMITS.validCues);
  expectCode(function () {
    parseSubtitleFile(utf8(maximumCount + "\n\n" + [
      LIMITS.validCues + 1,
      "00:00:00,000 --> 00:00:01,000",
      "one too many",
    ].join("\n")));
  }, "too_many_cues");

  const maximumTranscript = repeatedSRTCues(250, function (index) {
    return String(index).padStart(4, "0") + "語".repeat(1996);
  });
  assert.equal(parseSubtitleFile(utf8(maximumTranscript)).totalCharacters, LIMITS.totalCharacters);
  expectCode(function () {
    parseSubtitleFile(utf8(maximumTranscript + "\n\n" + [
      "251",
      "00:00:00,000 --> 00:00:01,000",
      "x",
    ].join("\n")));
  }, "transcript_too_large");

  expectCode(function () {
    parseSubtitleFile(utf8(repeatedASSCues(1, function () {
      return "猫".repeat(LIMITS.cueCharacters + 1);
    })));
  }, "cue_too_long");
  expectCode(function () {
    parseSubtitleFile(utf8(repeatedASSCues(LIMITS.validCues + 1, function () {
      return "duplicate";
    })));
  }, "too_many_cues");
});

test("carries monotonically allocated cue IDs across subtitle generations", function () {
  const first = parseSubtitleFile(utf8([
    "00:00:00,000 --> 00:00:01,000",
    "one",
    "",
    "00:00:01,000 --> 00:00:02,000",
    "two",
  ].join("\n")), { firstCueID: 40, subtitleGeneration: 5 });
  const second = parseSubtitleFile(
    utf8("00:00:00,000 --> 00:00:01,000\nreplacement"),
    { firstCueID: first.nextCueID, subtitleGeneration: 6 }
  );

  assert.deepEqual(first.cues.map(function (cue) { return cue.id; }), [40, 41]);
  assert.deepEqual(second.cues.map(function (cue) { return cue.id; }), [42]);
  assert.equal(second.cues[0].subtitleGeneration, 6);
});

test("applies clamped offsets to half-open cue intervals without changing source times", function () {
  const cues = [
    { id: 1, startMs: 1000, endMs: 2000 },
    { id: 2, startMs: 2000, endMs: 3000 },
    { id: 3, startMs: 1500, endMs: 2500 },
  ];
  const original = cues.map(function (cue) { return [cue.startMs, cue.endMs]; });

  assert.equal(clampOffsetMilliseconds(60001), 60000);
  assert.equal(clampOffsetMilliseconds(-90000), -60000);
  assert.equal(clampOffsetMilliseconds("249.6"), 250);
  assert.equal(clampOffsetMilliseconds("invalid"), 0);
  assert.equal(describeOffsetMilliseconds(250), "250 ms later");
  assert.equal(describeOffsetMilliseconds(-400), "400 ms earlier");
  assert.equal(describeOffsetMilliseconds(0), "On time");

  assert.deepEqual(activeCuesAt(cues, 1000, 0).map(function (cue) { return cue.id; }), [1]);
  assert.deepEqual(activeCuesAt(cues, 2000, 0).map(function (cue) { return cue.id; }), [2, 3]);
  assert.deepEqual(activeCuesAt(cues, 1250, 250).map(function (cue) { return cue.id; }), [1]);
  assert.deepEqual(effectiveCueTimes(cues[0], -250), { startMs: 750, endMs: 1750 });
  assert.deepEqual(cues.map(function (cue) { return [cue.startMs, cue.endMs]; }), original);

  assert.equal(cueIsOutsideVideo({ startMs: 10000, endMs: 11000 }, 10000, 0), true);
  assert.equal(cueIsOutsideVideo({ startMs: 9500, endMs: 10500 }, 10000, 0), false);
  assert.equal(cueIsOutsideVideo({ startMs: 100, endMs: 200 }, 10000, -1000), true);
});

test("indexes subtitle timelines without losing overlaps or offsets", function () {
  const timeline = createCueTimeline([
    { id: 3, sourceOrder: 2, startMs: 1500, endMs: 2500 },
    { id: 1, sourceOrder: 0, startMs: 0, endMs: 10000 },
    { id: 2, sourceOrder: 1, startMs: 1000, endMs: 2000 },
  ]);

  assert.deepEqual(
    activeTimelineCuesAt(timeline, 1750, 0).map(function (cue) { return cue.id; }),
    [1, 2, 3],
  );
  assert.deepEqual(
    activeTimelineCuesAt(timeline, 2250, 250).map(function (cue) { return cue.id; }),
    [1, 3],
  );
  assert.deepEqual(activeTimelineCuesAt(timeline, 10000, 0), []);
});

test("builds bounded coverage batches by count and UTF-16 size", function () {
  const byCount = createCoverageBatches(Array.from({ length: 401 }, function (_unused, index) {
    return { id: index + 1, text: "語" };
  }));
  assert.deepEqual(byCount.map(function (batch) { return batch.length; }), [400, 1]);

  const byText = createCoverageBatches(Array.from({ length: 41 }, function (_unused, index) {
    return { id: index + 1, text: "語".repeat(2000) };
  }));
  assert.deepEqual(byText.map(function (batch) { return batch.length; }), [40, 1]);

  byCount.concat(byText).forEach(function (batch) {
    assert.ok(batch.length <= LIMITS.coverageCues);
    assert.ok(batch.reduce(function (total, block) { return total + block.text.length; }, 0) <=
      LIMITS.coverageUTF16Units);
    assert.ok(Buffer.byteLength(JSON.stringify({ blocks: batch }), "utf8") < LIMITS.coverageJSONBytes);
  });
});

test("keeps every coverage request below the serialized JSON limit", function () {
  const batches = createCoverageBatches(Array.from({ length: 70 }, function (_unused, index) {
    return { id: index + 1, text: "\u0001".repeat(1000) };
  }));

  assert.ok(batches.length > 1);
  batches.forEach(function (batch) {
    assert.ok(Buffer.byteLength(JSON.stringify({ blocks: batch }), "utf8") < LIMITS.coverageJSONBytes);
  });
  expectCode(function () {
    createCoverageBatches([{ id: 1, text: "x".repeat(LIMITS.coverageBlockUTF16Units + 1) }]);
  }, "coverage_cue_too_large");
  expectCode(function () {
    createCoverageBatches([{ id: 1, text: "one" }, { id: 1, text: "duplicate" }]);
  }, "invalid_coverage_cue");
});
