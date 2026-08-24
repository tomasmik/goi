(function (root, factory) {
  const exports = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = exports;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.captureModel = exports;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  const EXPRESSION_LIMIT = 256;
  const CONTEXT_LIMIT = 2000;
  const TITLE_LIMIT = 300;
  const URL_BYTE_LIMIT = 2048;

  function normalizeWhitespace(value) {
    return String(value == null ? "" : value).replace(/\s+/gu, " ").trim();
  }

  function truncateCodePoints(value, limit) {
    const text = String(value == null ? "" : value);
    if (!Number.isInteger(limit) || limit < 0) {
      throw new RangeError("limit must be a non-negative integer");
    }

    return Array.from(text).slice(0, limit).join("");
  }

  function coveragePercent(knownOccurrences, totalOccurrences) {
    if (!Number.isSafeInteger(knownOccurrences) || !Number.isSafeInteger(totalOccurrences) ||
        knownOccurrences < 0 || totalOccurrences <= 0 || knownOccurrences > totalOccurrences) {
      return "—";
    }
    if (knownOccurrences === totalOccurrences) {
      return "100";
    }
    const percent = Math.floor(knownOccurrences / totalOccurrences * 1000) / 10;
    return Number.isInteger(percent) ? String(percent) : percent.toFixed(1);
  }

  function utf8Length(value) {
    if (typeof TextEncoder !== "undefined") {
      return new TextEncoder().encode(value).length;
    }

    return unescape(encodeURIComponent(value)).length;
  }

  function boundedSourceURL(value, limit) {
    const text = String(value == null ? "" : value);
    if (utf8Length(text) <= limit) {
      return text;
    }

    let parsed;
    try {
      parsed = new URL(text);
    } catch (_) {
      return "";
    }

    parsed.hash = "";
    if (utf8Length(parsed.href) <= limit) {
      return parsed.href;
    }
    parsed.search = "";
    if (utf8Length(parsed.href) <= limit) {
      return parsed.href;
    }
    return utf8Length(parsed.origin) <= limit ? parsed.origin : "";
  }

  function sentenceRangesWithSegmenter(text, locale) {
    if (typeof Intl === "undefined" || typeof Intl.Segmenter !== "function") {
      return [];
    }

    try {
      const segmenter = new Intl.Segmenter(locale || undefined, { granularity: "sentence" });
      return Array.from(segmenter.segment(text), function (part) {
        return {
          start: part.index,
          end: part.index + part.segment.length,
          text: part.segment,
        };
      });
    } catch (_) {
      return [];
    }
  }

  function sentenceRangesWithPunctuation(text) {
    const ranges = [];
    const terminator = /[。！？.!?]/u;
    const closer = /["'”’）】」』]/u;
    let start = 0;
    let index = 0;

    while (index < text.length) {
      const character = text[index];
      index += character.length;
      if (!terminator.test(character)) {
        continue;
      }

      while (index < text.length) {
        const next = text[index];
        if (!terminator.test(next) && !closer.test(next)) {
          break;
        }
        index += next.length;
      }

      ranges.push({ start: start, end: index, text: text.slice(start, index) });
      start = index;
    }

    if (start < text.length) {
      ranges.push({ start: start, end: text.length, text: text.slice(start) });
    }
    return ranges;
  }

  function sentenceContext(text, selectionStart, selectionEnd, locale) {
    const source = String(text == null ? "" : text);
    const offsetsAreValid =
      Number.isInteger(selectionStart) &&
      Number.isInteger(selectionEnd) &&
      selectionStart >= 0 &&
      selectionEnd >= selectionStart &&
      selectionEnd <= source.length;

    if (!offsetsAreValid) {
      return truncateCodePoints(normalizeWhitespace(source), CONTEXT_LIMIT);
    }

    let ranges = sentenceRangesWithSegmenter(source, locale);
    if (ranges.length === 0) {
      ranges = sentenceRangesWithPunctuation(source);
    }

    const selected = ranges.filter(function (range) {
      if (selectionStart === selectionEnd) {
        return range.start <= selectionStart && selectionStart < range.end;
      }
      return range.start < selectionEnd && selectionStart < range.end;
    });

    if (selected.length === 0) {
      return truncateCodePoints(normalizeWhitespace(source), CONTEXT_LIMIT);
    }

    const context = source.slice(selected[0].start, selected[selected.length - 1].end);
    return truncateCodePoints(normalizeWhitespace(context), CONTEXT_LIMIT);
  }

  function normalizeOrigin(value) {
    const input = String(value == null ? "" : value).trim();
    let parsed;
    try {
      parsed = new URL(input);
    } catch (_) {
      throw new TypeError("Goi server must be a valid HTTP or HTTPS URL");
    }

    if (parsed.protocol !== "https:" && parsed.protocol !== "http:") {
      throw new TypeError("Goi server must use HTTP or HTTPS");
    }
    if (parsed.username || parsed.password) {
      throw new TypeError("Goi server URL must not contain credentials");
    }
    if (parsed.pathname !== "/" || parsed.search || parsed.hash) {
      throw new TypeError("Goi server URL must contain only an origin");
    }

    return parsed.origin;
  }

  function permissionPattern(value) {
    const parsed = new URL(normalizeOrigin(value));
    return parsed.protocol + "//" + parsed.hostname + "/*";
  }

  function positionMilliseconds(value) {
    if (value == null || value === "") {
      return null;
    }

    const number = Number(value);
    if (!Number.isFinite(number) || number < 0) {
      return null;
    }
    return Math.round(number);
  }

  function resolveCaptureAttribution(input) {
    const capture = input && typeof input === "object" ? input : {};
    const hostname = String(capture.hostname || "").toLowerCase();
    const pathname = String(capture.pathname || "");
    const isYouTubeVideoPage =
      hostname === "www.youtube.com" &&
      (pathname === "/watch" || /^\/(?:embed|live|shorts)(?:\/|$)/u.test(pathname));
    const selectionInCaption = Boolean(capture.selectionInCaption);
    const activeCaption = normalizeWhitespace(capture.activeCaption);
    const videoIsRelevant = Boolean(capture.hasVideo) && (selectionInCaption || isYouTubeVideoPage);

    return {
      contextText: selectionInCaption && activeCaption
        ? activeCaption
        : String(capture.contextText == null ? "" : capture.contextText),
      sourceKind: videoIsRelevant ? "video" : "web",
    };
  }

  function buildCapturePayload(input, captureNonce) {
    const capture = input && typeof input === "object" ? input : {};
    const expression = normalizeWhitespace(capture.expression || capture.rawText || capture.raw_text);
    const rawText = normalizeWhitespace(capture.rawText || capture.raw_text || expression);
    const sourceKind = capture.sourceKind || capture.source_kind;

    const entrySequence = Number(capture.suggestedEntrySequence || capture.suggested_entry_sequence);
    const payload = {
      capture_nonce: truncateCodePoints(
        captureNonce || capture.captureNonce || capture.capture_nonce,
        128,
      ),
      raw_text: truncateCodePoints(rawText, EXPRESSION_LIMIT),
      expression: truncateCodePoints(expression, EXPRESSION_LIMIT),
      context_text: truncateCodePoints(
        normalizeWhitespace(capture.contextText || capture.context_text || rawText),
        CONTEXT_LIMIT,
      ),
      source_kind: sourceKind === "video" ? "video" : "web",
      source_title: truncateCodePoints(
        normalizeWhitespace(capture.sourceTitle || capture.source_title),
        TITLE_LIMIT,
      ),
      source_url: boundedSourceURL(
        String(capture.sourceURL || capture.source_url || "").trim(),
        URL_BYTE_LIMIT,
      ),
      source_position_ms: positionMilliseconds(
        capture.sourcePositionMs == null
          ? capture.source_position_ms
          : capture.sourcePositionMs,
      ),
    };
    if (Number.isSafeInteger(entrySequence) && entrySequence > 0) {
      payload.suggested_entry_sequence = entrySequence;
    }
    return payload;
  }

  function classifyAPIError(errorOrStatus) {
    if (errorOrStatus && errorOrStatus.code === "secure_transport_required") {
      return "insecure_transport";
    }
    if (
      errorOrStatus &&
      (errorOrStatus.code === "not_connected" ||
        errorOrStatus.code === "insecure_transport" ||
        errorOrStatus.code === "queue_full" ||
        errorOrStatus.code === "dictionary_unavailable" ||
        errorOrStatus.code === "dictionary_api_unavailable" ||
        errorOrStatus.code === "translation_unavailable" ||
        errorOrStatus.code === "translation_failed" ||
        errorOrStatus.code === "unexpected_response")
    ) {
      return errorOrStatus.code;
    }
    const status =
      typeof errorOrStatus === "number"
        ? errorOrStatus
        : errorOrStatus && typeof errorOrStatus.status === "number"
          ? errorOrStatus.status
          : 0;

    if (status === 401 || status === 403) {
      return "unauthorized";
    }
    if (status === 409) {
      return "idempotency_conflict";
    }
    if (status === 400 || status === 413 || status === 422) {
      return "invalid_capture";
    }
    if (status === 429) {
      return "rate_limited";
    }
    if (status >= 500) {
      return "server";
    }
    if (status === 0) {
      return "network";
    }
    return "server";
  }

  return {
    buildCapturePayload: buildCapturePayload,
    classifyAPIError: classifyAPIError,
    coveragePercent: coveragePercent,
    normalizeOrigin: normalizeOrigin,
    normalizeWhitespace: normalizeWhitespace,
    permissionPattern: permissionPattern,
    resolveCaptureAttribution: resolveCaptureAttribution,
    sentenceContext: sentenceContext,
    truncateCodePoints: truncateCodePoints,
  };
});
