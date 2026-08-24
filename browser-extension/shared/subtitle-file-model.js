(function (root, factory) {
  const exports = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = exports;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.subtitleFileModel = exports;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  const LIMITS = Object.freeze({
    sourceBytes: 5 * 1024 * 1024,
    validCues: 5000,
    cueCharacters: 2000,
    totalCharacters: 500000,
    offsetMilliseconds: 60000,
    coverageCues: 400,
    coverageUTF16Units: 80000,
    coverageBlockUTF16Units: 20000,
    coverageJSONBytes: 400 * 1024,
  });

  const UTF8_BOM = [0xef, 0xbb, 0xbf];
  const COVERAGE_ENVELOPE_BYTES = utf8ByteLength('{"blocks":[]}');

  class SubtitleFileError extends Error {
    constructor(code, message, details) {
      super(message);
      this.name = "SubtitleFileError";
      this.code = code;
      if (details && typeof details === "object") {
        Object.assign(this, details);
      }
    }
  }

  function subtitleError(code, message, details) {
    return new SubtitleFileError(code, message, details);
  }

  function toBytes(input) {
    if (input instanceof ArrayBuffer) {
      return new Uint8Array(input);
    }
    if (ArrayBuffer.isView(input)) {
      return new Uint8Array(input.buffer, input.byteOffset, input.byteLength);
    }
    throw subtitleError("invalid_source", "Choose a subtitle file to import.");
  }

  function hasPrefix(bytes, prefix) {
    return prefix.every(function (value, index) {
      return bytes[index] === value;
    });
  }

  function decodeWith(label, bytes) {
    try {
      return new TextDecoder(label, { fatal: true }).decode(bytes);
    } catch (_error) {
      throw subtitleError(
        "unsupported_encoding",
        "Could not decode subtitles. Save the file as UTF-8 or BOM-marked UTF-16."
      );
    }
  }

  function decodeSubtitleBytes(input) {
    const bytes = toBytes(input);
    if (bytes.byteLength > LIMITS.sourceBytes) {
      throw subtitleError("source_too_large", "Subtitle files must be 5 MiB or smaller.");
    }
    if (bytes.byteLength === 0) {
      throw subtitleError("empty_source", "The subtitle file is empty.");
    }

    let text;
    if (hasPrefix(bytes, UTF8_BOM)) {
      text = decodeWith("utf-8", bytes.subarray(UTF8_BOM.length));
    } else if (hasPrefix(bytes, [0xff, 0xfe])) {
      text = decodeWith("utf-16le", bytes.subarray(2));
    } else if (hasPrefix(bytes, [0xfe, 0xff])) {
      text = decodeWith("utf-16be", bytes.subarray(2));
    } else {
      try {
        text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
      } catch (_error) {
        try {
          text = new TextDecoder("shift_jis", { fatal: true }).decode(bytes);
        } catch (_shiftJISError) {
          throw subtitleError(
            "unsupported_encoding",
            "Could not decode subtitles. Save the file as UTF-8, UTF-16, or Japanese Shift-JIS."
          );
        }
      }
    }

    if (text.charCodeAt(0) === 0xfeff) {
      text = text.slice(1);
    }
    if (text.includes("\u0000")) {
      throw subtitleError(
        "unsupported_encoding",
        "Could not decode subtitles. Save the file as UTF-8 or BOM-marked UTF-16."
      );
    }
    return text.replace(/\r\n?/gu, "\n");
  }

  function splitBlocks(lines, start) {
    const blocks = [];
    let block = [];
    for (let index = start; index < lines.length; index += 1) {
      if (/^\s*$/u.test(lines[index])) {
        if (block.length) {
          blocks.push(block);
          block = [];
        }
        continue;
      }
      block.push(lines[index]);
    }
    if (block.length) {
      blocks.push(block);
    }
    return blocks;
  }

  function webVTTContentStart(lines) {
    for (let index = 1; index < lines.length; index += 1) {
      if (/^\s*$/u.test(lines[index])) {
        return index + 1;
      }
    }
    return lines.length;
  }

  function isWebVTTHeader(line) {
    return /^WEBVTT(?:[ \t].*)?$/u.test(String(line || ""));
  }

  function milliseconds(hours, minutes, seconds, fraction) {
    const result = ((hours * 60 + minutes) * 60 + seconds) * 1000 + fraction;
    return Number.isSafeInteger(result) ? result : null;
  }

  function parseSRTTimestamp(value) {
    const match = /^(\d+):([0-5]\d):([0-5]\d)[,.](\d{3})$/u.exec(value);
    if (!match) {
      return null;
    }
    return milliseconds(Number(match[1]), Number(match[2]), Number(match[3]), Number(match[4]));
  }

  function parseWebVTTTimestamp(value) {
    let match = /^(\d{2,}):([0-5]\d):([0-5]\d)\.(\d{3})$/u.exec(value);
    if (match) {
      return milliseconds(Number(match[1]), Number(match[2]), Number(match[3]), Number(match[4]));
    }
    match = /^([0-5]\d):([0-5]\d)\.(\d{3})$/u.exec(value);
    if (!match) {
      return null;
    }
    return milliseconds(0, Number(match[1]), Number(match[2]), Number(match[3]));
  }

  function parseTimeline(line, timestampParser) {
    const parts = String(line || "").split("-->");
    if (parts.length !== 2) {
      return null;
    }
    const start = timestampParser(parts[0].trim());
    const endValue = parts[1].trim().split(/[ \t]+/u)[0];
    const end = timestampParser(endValue);
    if (start === null || end === null) {
      return null;
    }
    return { startMs: start, endMs: end };
  }

  function subtitlePlainText(value) {
    return value
      .replace(/<br\s*\/?\s*>/giu, "\n")
      .replace(/<\/?(?:b|i|u|ruby|rt)>/giu, "")
      .replace(/<c(?:\.[a-z0-9_-]+)*>|<\/c>/giu, "")
      .replace(/<v(?:[ \t]+[^>]*)?>|<\/v>/giu, "")
      .replace(/<lang(?:[ \t]+[^>]*)?>|<\/lang>/giu, "")
      .replace(/<font(?:[ \t]+[^>]*)?>|<\/font>/giu, "")
      .replace(/<(?:\d{2,}:)?[0-5]\d:[0-5]\d\.\d{3}>/gu, "")
      .replace(/&(amp|lt|gt|nbsp|lrm|rlm);/gu, function (_entity, name) {
        return {
          amp: "&",
          lt: "<",
          gt: ">",
          nbsp: " ",
          lrm: "\u200e",
          rlm: "\u200f",
        }[name];
      });
  }

  function normalizeCueText(lines) {
    const value = Array.isArray(lines) ? lines.join("\n") : String(lines == null ? "" : lines);
    const normalized = subtitlePlainText(value.replace(/\r\n?/gu, "\n")).split("\n").map(function (line) {
      return line.replace(/[ \t\f\v\u00a0]+/gu, " ").trim();
    });
    while (normalized.length && normalized[0] === "") {
      normalized.shift();
    }
    while (normalized.length && normalized[normalized.length - 1] === "") {
      normalized.pop();
    }
    return normalized.join("\n");
  }

  function unicodeLength(value) {
    let length = 0;
    for (const _character of value) {
      length += 1;
    }
    return length;
  }

  function srtCue(block) {
    let timelineIndex = 0;
    if (/^\d+$/u.test(block[0].trim()) && block.length > 1) {
      timelineIndex = 1;
    }
    if ((timelineIndex === 1 && !block[timelineIndex].includes("-->")) ||
        (timelineIndex === 0 && !block[0].includes("-->"))) {
      return null;
    }
    const timeline = parseTimeline(block[timelineIndex], parseSRTTimestamp);
    const text = normalizeCueText(block.slice(timelineIndex + 1));
    if (!timeline || timeline.endMs <= timeline.startMs || !text) {
      return null;
    }
    return { startMs: timeline.startMs, endMs: timeline.endMs, text: text };
  }

  function reservedWebVTTBlock(block) {
    const first = block[0].trim();
    return /^(?:STYLE|REGION)$/u.test(first) || /^NOTE(?:[ \t]|$)/u.test(first);
  }

  function webVTTCue(block) {
    let timelineIndex = 0;
    if (!block[0].includes("-->") && block.length > 1) {
      timelineIndex = 1;
    }
    if (!block[timelineIndex] || !block[timelineIndex].includes("-->")) {
      return null;
    }
    const timeline = parseTimeline(block[timelineIndex], parseWebVTTTimestamp);
    const text = normalizeCueText(block.slice(timelineIndex + 1));
    if (!timeline || timeline.endMs <= timeline.startMs || !text) {
      return null;
    }
    return { startMs: timeline.startMs, endMs: timeline.endMs, text: text };
  }

  function parsedCueData(text, format) {
    if (format === "ass") {
      return parsedASSData(text);
    }
    const lines = text.split("\n");
    const start = format === "webvtt" ? webVTTContentStart(lines) : 0;
    const blocks = splitBlocks(lines, start);
    const timestampParser = format === "webvtt" ? webVTTCue : srtCue;
    const seen = new Set();
    const cues = [];
    let duplicateCueCount = 0;
    let skippedCueCount = 0;
    let sourceCueCount = 0;
    let validCueCount = 0;
    let totalCharacters = 0;

    blocks.forEach(function (block) {
      if (format === "webvtt" && reservedWebVTTBlock(block)) {
        return;
      }
      const sourceOrder = sourceCueCount;
      sourceCueCount += 1;
      const cue = timestampParser(block);
      if (!cue) {
        skippedCueCount += 1;
        return;
      }

      validCueCount += 1;
      if (validCueCount > LIMITS.validCues) {
        throw subtitleError("too_many_cues", "Subtitle files may contain at most 5,000 valid cues.");
      }
      const cueCharacters = unicodeLength(cue.text);
      if (cueCharacters > LIMITS.cueCharacters) {
        throw subtitleError("cue_too_long", "Each subtitle cue may contain at most 2,000 characters.", {
          sourceOrder: sourceOrder,
        });
      }
      totalCharacters += cueCharacters;
      if (totalCharacters > LIMITS.totalCharacters) {
        throw subtitleError("transcript_too_large", "Subtitle dialogue may contain at most 500,000 characters.");
      }

      const duplicateKey = cue.startMs + "\u0000" + cue.endMs + "\u0000" + cue.text;
      if (seen.has(duplicateKey)) {
        duplicateCueCount += 1;
        return;
      }
      seen.add(duplicateKey);
      cues.push({
        sourceOrder: sourceOrder,
        startMs: cue.startMs,
        endMs: cue.endMs,
        text: cue.text,
      });
    });

    return {
      cues: cues,
      duplicateCueCount: duplicateCueCount,
      skippedCueCount: skippedCueCount,
      sourceCueCount: sourceCueCount,
      validCueCount: validCueCount,
      totalCharacters: totalCharacters,
    };
  }

  function parseASSTimestamp(value) {
    const match = /^(\d+):([0-5]\d):([0-5]\d)[.](\d{2})$/u.exec(String(value || "").trim());
    if (!match) {
      return null;
    }
    return milliseconds(Number(match[1]), Number(match[2]), Number(match[3]), Number(match[4]) * 10);
  }

  function parsedASSData(text) {
    const lines = text.split("\n");
    let inEvents = false;
    let fields = [];
    const source = [];
    lines.forEach(function (line) {
      const trimmed = line.trim();
      if (/^\[/u.test(trimmed)) {
        inEvents = /^\[Events\]$/iu.test(trimmed);
        return;
      }
      if (!inEvents) {
        return;
      }
      if (/^Format:/iu.test(trimmed)) {
        fields = trimmed.slice(trimmed.indexOf(":") + 1).split(",").map(function (field) {
          return field.trim().toLocaleLowerCase();
        });
        return;
      }
      if (/^Dialogue:/iu.test(trimmed) && fields.length) {
        const values = trimmed.slice(trimmed.indexOf(":") + 1).split(",");
        if (values.length > fields.length) {
          values.splice(
            fields.length - 1,
            values.length - fields.length + 1,
            values.slice(fields.length - 1).join(",")
          );
        }
        const record = Object.fromEntries(fields.map(function (field, index) {
          return [field, values[index] || ""];
        }));
        const startMs = parseASSTimestamp(record.start);
        const endMs = parseASSTimestamp(record.end);
        const cueText = normalizeCueText(String(record.text || "")
          .replace(/\{[^}]*\}/gu, "")
          .replace(/\\[Nn]/gu, "\n")
          .replace(/\\h/gu, " "));
        if (startMs !== null && endMs > startMs && cueText) {
          source.push({ startMs: startMs, endMs: endMs, text: cueText });
        } else {
          source.push(null);
        }
      }
    });
    const cues = [];
    const seen = new Set();
    let duplicateCueCount = 0;
    let skippedCueCount = 0;
    let validCueCount = 0;
    let totalCharacters = 0;
    source.forEach(function (cue, sourceOrder) {
      if (!cue) {
        skippedCueCount += 1;
        return;
      }
      validCueCount += 1;
      if (validCueCount > LIMITS.validCues) {
        throw subtitleError("too_many_cues", "Subtitle files may contain at most 5,000 valid cues.");
      }
      const cueCharacters = unicodeLength(cue.text);
      if (cueCharacters > LIMITS.cueCharacters) {
        throw subtitleError("cue_too_long", "Each subtitle cue may contain at most 2,000 characters.", {
          sourceOrder: sourceOrder,
        });
      }
      totalCharacters += cueCharacters;
      if (totalCharacters > LIMITS.totalCharacters) {
        throw subtitleError("transcript_too_large", "Subtitle dialogue may contain at most 500,000 characters.");
      }
      const duplicateKey = cue.startMs + "\u0000" + cue.endMs + "\u0000" + cue.text;
      if (seen.has(duplicateKey)) {
        duplicateCueCount += 1;
        return;
      }
      seen.add(duplicateKey);
      cues.push({ ...cue, sourceOrder: sourceOrder });
    });
    return {
      cues: cues,
      duplicateCueCount: duplicateCueCount,
      skippedCueCount: skippedCueCount,
      sourceCueCount: source.length,
      validCueCount: validCueCount,
      totalCharacters: totalCharacters,
    };
  }

  function nonnegativeSafeInteger(value, fallback, name) {
    if (value === undefined) {
      return fallback;
    }
    if (!Number.isSafeInteger(value) || value < 0) {
      throw subtitleError("invalid_options", name + " must be a nonnegative integer.");
    }
    return value;
  }

  function parseSubtitleFile(input, options) {
    const text = decodeSubtitleBytes(input);
    const lines = text.split("\n");
    const format = isWebVTTHeader(lines[0])
      ? "webvtt"
      : /^\s*\[Script Info\]/imu.test(text) ? "ass" : "srt";
    const parsed = parsedCueData(text, format);
    if (!parsed.cues.length) {
      if (format === "srt" && !text.includes("-->")) {
        throw subtitleError(
          "unsupported_format",
          "This file is not recognizable SRT, WebVTT, ASS, or SSA subtitles."
        );
      }
      throw subtitleError("no_valid_cues", "No valid subtitle cues were found.", {
        skippedCueCount: parsed.skippedCueCount,
      });
    }

    const settings = options && typeof options === "object" ? options : {};
    const firstCueID = nonnegativeSafeInteger(settings.firstCueID, 1, "First cue ID");
    if (firstCueID < 1) {
      throw subtitleError("invalid_options", "First cue ID must be positive.");
    }
    const subtitleGeneration = nonnegativeSafeInteger(
      settings.subtitleGeneration,
      0,
      "Subtitle generation"
    );
    if (!Number.isSafeInteger(firstCueID + parsed.cues.length)) {
      throw subtitleError("invalid_options", "Cue IDs are outside the supported range.");
    }

    const cues = parsed.cues.map(function (cue, index) {
      return {
        id: firstCueID + index,
        subtitleGeneration: subtitleGeneration,
        sourceOrder: cue.sourceOrder,
        startMs: cue.startMs,
        endMs: cue.endMs,
        text: cue.text,
        classification: "pending",
        unknowns: [],
      };
    }).sort(function (left, right) {
      return left.startMs - right.startMs ||
        left.endMs - right.endMs ||
        left.sourceOrder - right.sourceOrder;
    });

    return {
      format: format,
      subtitleGeneration: subtitleGeneration,
      cues: cues,
      nextCueID: firstCueID + cues.length,
      skippedCueCount: parsed.skippedCueCount,
      duplicateCueCount: parsed.duplicateCueCount,
      sourceCueCount: parsed.sourceCueCount,
      validCueCount: parsed.validCueCount,
      totalCharacters: parsed.totalCharacters,
    };
  }

  function clampOffsetMilliseconds(value) {
    const number = Number(value);
    if (!Number.isFinite(number)) {
      return 0;
    }
    const rounded = Math.round(number);
    const clamped = Math.max(-LIMITS.offsetMilliseconds, Math.min(LIMITS.offsetMilliseconds, rounded));
    return clamped === 0 ? 0 : clamped;
  }

  function describeOffsetMilliseconds(value) {
    const offset = clampOffsetMilliseconds(value);
    if (offset === 0) {
      return "On time";
    }
    return Math.abs(offset) + " ms " + (offset > 0 ? "later" : "earlier");
  }

  function effectiveCueTimes(cue, offsetMilliseconds) {
    if (!cue || !Number.isSafeInteger(cue.startMs) || !Number.isSafeInteger(cue.endMs) ||
        cue.endMs <= cue.startMs) {
      return null;
    }
    const offset = clampOffsetMilliseconds(offsetMilliseconds);
    return {
      startMs: cue.startMs + offset,
      endMs: cue.endMs + offset,
    };
  }

  function activeCuesAt(cues, currentTimeMilliseconds, offsetMilliseconds) {
    const currentTime = Number(currentTimeMilliseconds);
    if (!Array.isArray(cues) || !Number.isFinite(currentTime)) {
      return [];
    }
    return cues.filter(function (cue) {
      const effective = effectiveCueTimes(cue, offsetMilliseconds);
      return effective && effective.startMs <= currentTime && currentTime < effective.endMs;
    });
  }

  function createCueTimeline(cues) {
    if (!Array.isArray(cues)) {
      return { cues: [], maximumEndThrough: [] };
    }
    const ordered = cues.slice().sort(function (left, right) {
      const startOrder = left.startMs - right.startMs;
      if (startOrder !== 0) {
        return startOrder;
      }
      return (Number(left.sourceOrder) || 0) - (Number(right.sourceOrder) || 0);
    });
    const maximumEndThrough = new Array(ordered.length);
    let maximumEnd = -Infinity;
    ordered.forEach(function (cue, index) {
      maximumEnd = Math.max(maximumEnd, cue.endMs);
      maximumEndThrough[index] = maximumEnd;
    });
    return { cues: ordered, maximumEndThrough };
  }

  function activeTimelineCuesAt(timeline, currentTimeMilliseconds, offsetMilliseconds) {
    const currentTime = Number(currentTimeMilliseconds);
    if (!timeline || !Array.isArray(timeline.cues) ||
        !Array.isArray(timeline.maximumEndThrough) || !Number.isFinite(currentTime)) {
      return [];
    }
    const targetTime = currentTime - clampOffsetMilliseconds(offsetMilliseconds);
    const cues = timeline.cues;
    let low = 0;
    let high = cues.length;
    while (low < high) {
      const middle = low + Math.floor((high - low) / 2);
      if (cues[middle].startMs <= targetTime) {
        low = middle + 1;
      } else {
        high = middle;
      }
    }

    const active = [];
    for (let index = low - 1; index >= 0; index -= 1) {
      if (timeline.maximumEndThrough[index] <= targetTime) {
        break;
      }
      const cue = cues[index];
      if (targetTime < cue.endMs) {
        active.push(cue);
      }
    }
    return active.reverse();
  }

  function cueIsOutsideVideo(cue, videoDurationMilliseconds, offsetMilliseconds) {
    const duration = Number(videoDurationMilliseconds);
    const effective = effectiveCueTimes(cue, offsetMilliseconds);
    if (!effective || !Number.isFinite(duration) || duration < 0) {
      return false;
    }
    return effective.endMs <= 0 || effective.startMs >= duration;
  }

  function utf8ByteLength(value) {
    return new TextEncoder().encode(value).byteLength;
  }

  function coverageBlock(cue, seenIDs) {
    if (!cue || !Number.isSafeInteger(cue.id) || cue.id <= 0 || seenIDs.has(cue.id) ||
        typeof cue.text !== "string" || cue.text.length === 0) {
      throw subtitleError("invalid_coverage_cue", "Coverage cues must have unique positive IDs and text.");
    }
    if (cue.text.length > LIMITS.coverageBlockUTF16Units) {
      throw subtitleError("coverage_cue_too_large", "A coverage cue exceeds the 20,000-character API limit.");
    }
    seenIDs.add(cue.id);
    return { id: cue.id, text: cue.text };
  }

  function createCoverageBatches(cues) {
    if (!Array.isArray(cues)) {
      throw subtitleError("invalid_coverage_cues", "Coverage cues must be a list.");
    }
    const batches = [];
    const seenIDs = new Set();
    let batch = [];
    let batchUTF16Units = 0;
    let batchJSONBytes = COVERAGE_ENVELOPE_BYTES;

    cues.forEach(function (cue) {
      const block = coverageBlock(cue, seenIDs);
      const blockJSONBytes = utf8ByteLength(JSON.stringify(block));
      const separatorBytes = batch.length ? 1 : 0;
      const exceedsBatch = batch.length >= LIMITS.coverageCues ||
        batchUTF16Units + block.text.length > LIMITS.coverageUTF16Units ||
        batchJSONBytes + separatorBytes + blockJSONBytes >= LIMITS.coverageJSONBytes;

      if (exceedsBatch && batch.length) {
        batches.push(batch);
        batch = [];
        batchUTF16Units = 0;
        batchJSONBytes = COVERAGE_ENVELOPE_BYTES;
      }

      const nextJSONBytes = batchJSONBytes + (batch.length ? 1 : 0) + blockJSONBytes;
      if (block.text.length > LIMITS.coverageUTF16Units || nextJSONBytes >= LIMITS.coverageJSONBytes) {
        throw subtitleError("coverage_cue_too_large", "A subtitle cue cannot fit in one coverage request.");
      }
      batch.push(block);
      batchUTF16Units += block.text.length;
      batchJSONBytes = nextJSONBytes;
    });

    if (batch.length) {
      batches.push(batch);
    }
    return batches;
  }

  return {
    LIMITS: LIMITS,
    SubtitleFileError: SubtitleFileError,
    activeCuesAt: activeCuesAt,
    activeTimelineCuesAt: activeTimelineCuesAt,
    clampOffsetMilliseconds: clampOffsetMilliseconds,
    createCoverageBatches: createCoverageBatches,
    createCueTimeline: createCueTimeline,
    cueIsOutsideVideo: cueIsOutsideVideo,
    decodeSubtitleBytes: decodeSubtitleBytes,
    describeOffsetMilliseconds: describeOffsetMilliseconds,
    effectiveCueTimes: effectiveCueTimes,
    normalizeCueText: normalizeCueText,
    parseSubtitleFile: parseSubtitleFile,
  };
});
