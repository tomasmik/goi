(function (root, factory) {
  const exports = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = exports;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.subtitleModel = exports;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  const WORD_STATUSES = new Set(["known", "unknown", "leech", "suspended_leech"]);
  const HAN_CHARACTER = /^\p{Script=Han}$/u;
  const KANA_CHARACTER = /^[\p{Script=Hiragana}\p{Script=Katakana}ー]$/u;
  const KANA_READING = /^[\p{Script=Hiragana}\p{Script=Katakana}ー]+$/u;
  const JAPANESE_TEXT = /[\p{Script=Han}\p{Script=Hiragana}\p{Script=Katakana}々〆ヶー]/u;
  const WORD_SEGMENTER = typeof Intl === "object" && typeof Intl.Segmenter === "function"
    ? new Intl.Segmenter("ja", { granularity: "word" })
    : null;

  function toHiragana(value) {
    return Array.from(String(value || "")).map(function (character) {
      const codePoint = character.codePointAt(0);
      return codePoint >= 0x30a1 && codePoint <= 0x30f6
        ? String.fromCodePoint(codePoint - 0x60)
        : character;
    }).join("");
  }

  function characterKind(character) {
    if (HAN_CHARACTER.test(character)) {
      return "kanji";
    }
    if (KANA_CHARACTER.test(character)) {
      return "kana";
    }
    return "other";
  }

  function surfaceRuns(surface) {
    return Array.from(surface).reduce(function (runs, character) {
      const kind = characterKind(character);
      const previous = runs[runs.length - 1];
      if (previous && previous.kind === kind) {
        previous.text += character;
      } else {
        runs.push({ kind, text: character });
      }
      return runs;
    }, []);
  }

  function appendPlainPart(parts, text) {
    if (!text) {
      return;
    }
    const previous = parts[parts.length - 1];
    if (previous && !previous.reading) {
      previous.text += text;
      return;
    }
    parts.push({ text, reading: "" });
  }

  function furiganaParts(surfaceValue, readingValue) {
    const surface = String(surfaceValue || "");
    const suppliedReading = String(readingValue || "").trim();
    const reading = toHiragana(suppliedReading);
    if (!surface || !KANA_READING.test(suppliedReading) || !Array.from(surface).some(function (character) {
      return HAN_CHARACTER.test(character);
    })) {
      return [{ text: surface, reading: "" }];
    }

    const runs = surfaceRuns(surface);
    const fallback = runs.every(function (run) {
      return run.kind === "kanji";
    }) ? [{ text: surface, reading }] : [{ text: surface, reading: "" }];
    const parts = [];
    let readingOffset = 0;
    for (let index = 0; index < runs.length; index += 1) {
      const run = runs[index];
      if (run.kind === "kanji") {
        const nextKana = runs.slice(index + 1).find(function (candidate) {
          return candidate.kind === "kana";
        });
        if (!nextKana) {
          const remaining = reading.slice(readingOffset);
          if (!remaining) {
            return fallback;
          }
          parts.push({ text: run.text, reading: remaining });
          readingOffset = reading.length;
          continue;
        }
        const anchor = toHiragana(nextKana.text);
        const anchorOffset = reading.indexOf(anchor, readingOffset + 1);
        if (anchorOffset < 0) {
          return fallback;
        }
        parts.push({ text: run.text, reading: reading.slice(readingOffset, anchorOffset) });
        readingOffset = anchorOffset;
        continue;
      }

      const normalized = toHiragana(run.text);
      if (!reading.startsWith(normalized, readingOffset)) {
        return fallback;
      }
      appendPlainPart(parts, run.text);
      readingOffset += normalized.length;
    }

    return readingOffset === reading.length ? parts : fallback;
  }

  function words(text, block) {
    const caption = String(text || "");
    const tokens = block && Array.isArray(block.tokens) ? block.tokens : [];
    return tokens.filter(function (token) {
      return token && WORD_STATUSES.has(token.status) &&
        Number.isSafeInteger(token.start_utf16) && Number.isSafeInteger(token.end_utf16) &&
        token.start_utf16 >= 0 && token.end_utf16 > token.start_utf16 &&
        token.end_utf16 <= caption.length;
    }).sort(function (left, right) {
      return left.start_utf16 - right.start_utf16;
    }).map(function (token) {
      const word = {
        surface: caption.slice(token.start_utf16, token.end_utf16),
        expression: String(token.expression || token.surface || ""),
        start: token.start_utf16,
        end: token.end_utf16,
        status: token.status
      };
      if (typeof token.reading === "string" && token.reading) {
        word.reading = token.reading;
      }
      return word;
    });
  }

  function lookupWords(text, classifiedWords) {
    const caption = String(text || "");
    const result = [];
    let offset = 0;
    const candidates = (Array.isArray(classifiedWords) ? classifiedWords : []).slice().sort(function (left, right) {
      return Number(left && left.start) - Number(right && right.start) ||
        Number(left && left.end) - Number(right && right.end);
    });
    candidates.forEach(function (word) {
      if (!word || !Number.isSafeInteger(word.start) || !Number.isSafeInteger(word.end) ||
          word.start < offset || word.end <= word.start || word.end > caption.length) {
        return;
      }
      result.push(...segmentLookupWords(caption, offset, word.start));
      result.push({
        ...word,
        surface: caption.slice(word.start, word.end),
        expression: String(word.expression || word.surface || caption.slice(word.start, word.end))
      });
      offset = word.end;
    });
    result.push(...segmentLookupWords(caption, offset, caption.length));
    return result;
  }

  function segmentLookupWords(text, start, end) {
    if (!WORD_SEGMENTER || end <= start) {
      return [];
    }
    const words = [];
    const source = text.slice(start, end);
    for (const segment of WORD_SEGMENTER.segment(source)) {
      if (!segment.isWordLike || !JAPANESE_TEXT.test(segment.segment)) {
        continue;
      }
      const wordStart = start + segment.index;
      words.push({
        surface: segment.segment,
        expression: segment.segment,
        start: wordStart,
        end: wordStart + segment.segment.length,
        status: "unclassified"
      });
    }
    return words;
  }

  function unknownWords(text, block) {
    return words(text, block).filter(function (word) {
      return word.status === "unknown";
    }).map(function (word) {
      const target = {
        surface: word.surface,
        expression: word.expression,
        start: word.start,
        end: word.end
      };
      if (word.reading) {
        target.reading = word.reading;
      }
      return target;
    });
  }

  function captureTarget(line, input) {
    const text = line && typeof line.text === "string" ? line.text : "";
    const surface = String(input || "").replace(/\s+/gu, " ").trim();
    if (!surface) {
      return null;
    }
    const candidates = Array.isArray(line && line.words)
      ? line.words
      : Array.isArray(line && line.unknowns) ? line.unknowns : [];
    const knownTarget = candidates.find(function (word) {
      return word && word.surface === surface;
    }) || candidates.find(function (word) {
      return word && word.expression === surface;
    });
    if (!knownTarget && !text.includes(surface)) {
      return null;
    }
    return {
      surface: knownTarget ? knownTarget.surface : surface,
      expression: knownTarget ? knownTarget.expression : surface
    };
  }

  function dictionaryMessage(response) {
    const result = response && response.result;
    if (response && response.errorCode === "not_connected") {
      return "Connect Goi to look up words.";
    }
    if (response && response.errorCode === "unauthorized") {
      return "Reconnect Goi to look up words.";
    }
    if (response && response.errorCode === "dictionary_unavailable") {
      return "The dictionary is not ready. Check Goi Settings.";
    }
    if (response && response.errorCode === "dictionary_api_unavailable") {
      return "Restart Goi, then reload the extension.";
    }
    if (response && response.errorCode === "network") {
      return "Could not reach Goi.";
    }
    return result && result.state === "no_match"
      ? "No dictionary match."
      : "Dictionary lookup unavailable.";
  }

  function dictionaryView(response) {
    const result = response && response.ok && response.result;
    const candidates = result && Array.isArray(result.candidates) ? result.candidates : [];
    if (!candidates.length) {
      return {
        query: String(result && result.query || ""),
        candidates: [],
        message: dictionaryMessage(response)
      };
    }
    return {
      query: String(result.query || ""),
      candidates: candidates.map(function (candidate) {
        const meanings = Array.isArray(candidate.meanings)
          ? candidate.meanings.filter(function (meaning) { return typeof meaning === "string" && meaning; })
          : [];
        const senses = Array.isArray(candidate.senses) ? candidate.senses.map(function (sense) {
          return {
            partsOfSpeech: Array.isArray(sense && sense.parts_of_speech)
              ? sense.parts_of_speech.filter(function (part) { return typeof part === "string" && part; })
              : [],
            meanings: Array.isArray(sense && sense.meanings)
              ? sense.meanings.filter(function (meaning) { return typeof meaning === "string" && meaning; })
              : []
          };
        }).filter(function (sense) {
          return sense.meanings.length > 0;
        }) : [];
        return {
          entrySequence: Number.isSafeInteger(candidate.entry_sequence) && candidate.entry_sequence > 0
            ? candidate.entry_sequence
            : null,
          written: String(candidate.written || ""),
          reading: String(candidate.reading || ""),
          globalRank: frequencyRank(candidate.global_rank),
          novelRank: frequencyRank(candidate.novel_rank),
          meanings,
          senses: senses.length ? senses : [{ partsOfSpeech: [], meanings }]
        };
      }),
      message: ""
    };
  }

  function frequencyRank(rank) {
    return Number.isSafeInteger(rank) && rank >= 1 && rank <= 2147483647 ? rank : null;
  }

  function dictionaryText(response, meaningLimit) {
    const view = dictionaryView(response);
    const candidate = view.candidates[0];
    if (!candidate) {
      return view.message;
    }
    return [
      candidate.reading,
      candidate.meanings.slice(0, meaningLimit || 4).join("; ")
    ].filter(Boolean).join(" · ");
  }

  function sessionIdentity(sourceURL) {
    try {
      const pageURL = new URL(String(sourceURL || ""));
      if (pageURL.pathname === "/watch") {
        const videoID = pageURL.searchParams.get("v");
        return videoID ? pageURL.origin + "/watch?v=" + encodeURIComponent(videoID) : "";
      }
      if (/^\/(?:shorts|live|embed)\/[^/]+/u.test(pageURL.pathname)) {
        return pageURL.origin + pageURL.pathname;
      }
      pageURL.hash = "";
      return pageURL.href;
    } catch (_error) {
      return "";
    }
  }

  function oneTargetLines(lines, submittedLineIDs) {
    const submitted = submittedLineIDs instanceof Set ? submittedLineIDs : new Set();
    const seen = new Set();
    return (Array.isArray(lines) ? lines : []).filter(function (line) {
      if (!line || !Number.isSafeInteger(line.id) || seen.has(line.id)) {
        return false;
      }
      seen.add(line.id);
      const targets = new Set((Array.isArray(line.unknowns) ? line.unknowns : []).map(function (word) {
        return String(word && (word.expression || word.surface) || "");
      }).filter(Boolean));
      return line.classification === "ready" && targets.size === 1 && !submitted.has(line.id);
    });
  }

  function batchSummary(sent, queued, failed) {
    const parts = [];
    if (sent > 0) {
      parts.push(sent + " sent");
    }
    if (queued > 0) {
      parts.push(queued + " queued");
    }
    if (failed > 0) {
      parts.push(failed + " failed");
    }
    return parts.join(" · ") || "No lines were sent.";
  }

  function validCoverageBatch(batch, result) {
    if (!Array.isArray(batch) || !result || !Array.isArray(result.blocks) ||
        result.blocks.length !== batch.length) {
      return false;
    }
    const requested = new Map(batch.map(function (block) { return [block.id, block.text]; }));
    const seen = new Set();
    let known = 0;
    let total = 0;
    for (const block of result.blocks) {
      const text = requested.get(block.id);
      if (typeof text !== "string" || seen.has(block.id) || !Array.isArray(block.tokens)) {
        return false;
      }
      seen.add(block.id);
      for (const token of block.tokens) {
        if (!Number.isSafeInteger(token.start_utf16) || !Number.isSafeInteger(token.end_utf16) ||
            token.start_utf16 < 0 || token.end_utf16 <= token.start_utf16 ||
            token.end_utf16 > text.length || text.slice(token.start_utf16, token.end_utf16) !== token.surface ||
            !["known", "unknown", "leech", "suspended_leech"].includes(token.status)) {
          return false;
        }
        total += 1;
        known += Number(token.status !== "unknown");
      }
    }
    const summary = result.summary;
    return seen.size === requested.size && summary &&
      summary.known_occurrences === known && summary.total_occurrences === total &&
      Number.isSafeInteger(summary.excluded_names) && summary.excluded_names >= 0;
  }

  function formatTimestamp(milliseconds) {
    const totalSeconds = Math.max(0, Math.floor((Number(milliseconds) || 0) / 1000));
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;
    const minuteText = hours ? String(minutes).padStart(2, "0") : String(minutes);
    return (hours ? hours + ":" : "") + minuteText + ":" + String(seconds).padStart(2, "0");
  }

  return {
    batchSummary: batchSummary,
    captureTarget: captureTarget,
    dictionaryText: dictionaryText,
    dictionaryView: dictionaryView,
    formatTimestamp: formatTimestamp,
    furiganaParts: furiganaParts,
    lookupWords: lookupWords,
    oneTargetLines: oneTargetLines,
    sessionIdentity: sessionIdentity,
    unknownWords: unknownWords,
    validCoverageBatch: validCoverageBatch,
    words: words
  };
});
