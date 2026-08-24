(function (root, factory) {
  const exports = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = exports;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.youtubeTranscriptModel = exports;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  const LIMITS = Object.freeze({
    sourceCharacters: 5 * 1024 * 1024,
    cues: 5000,
    cueCharacters: 2000,
    totalCharacters: 500000
  });

  function videoID(value) {
    try {
      const url = value instanceof URL ? value : new URL(value);
      if (url.protocol !== "https:" || url.hostname !== "www.youtube.com") {
        return "";
      }
      if (url.pathname === "/watch") {
        return String(url.searchParams.get("v") || "").trim();
      }
      const match = /^\/(?:embed|live|shorts)\/([^/]+)/u.exec(url.pathname);
      return match ? decodeURIComponent(match[1]).trim() : "";
    } catch (_error) {
      return "";
    }
  }

  function isVideoURL(value) {
    return Boolean(videoID(value));
  }

  function selectPlayerData(value, expectedVideoID) {
    if (!value || typeof value !== "object" || !expectedVideoID) {
      return null;
    }
    const candidates = Array.isArray(value.responses) ? value.responses : [value];
    const matching = candidates.filter(function (candidate) {
      return candidate && candidate.videoID === expectedVideoID;
    });
    if (!matching.length) {
      return null;
    }
    const tracks = [];
    const seen = new Set();
    matching.forEach(function (candidate) {
      if (!Array.isArray(candidate.tracks)) {
        return;
      }
      candidate.tracks.forEach(function (track) {
        if (!track || typeof track.baseUrl !== "string") {
          return;
        }
        const key = [track.baseUrl, track.languageCode, track.kind].join("\u0000");
        if (!seen.has(key)) {
          seen.add(key);
          tracks.push(track);
        }
      });
    });
    return {
      videoID: expectedVideoID,
      tracks,
      transcriptSource: typeof value.transcriptSource === "string" ? value.transcriptSource : ""
    };
  }

  function japaneseTrack(tracks) {
    if (!Array.isArray(tracks)) {
      return null;
    }
    const candidates = tracks.filter(function (track) {
      return track && /^ja(?:-|$)/iu.test(String(track.languageCode || ""));
    });
    candidates.sort(function (left, right) {
      const leftAutomatic = left.kind === "asr" ? 1 : 0;
      const rightAutomatic = right.kind === "asr" ? 1 : 0;
      return leftAutomatic - rightAutomatic;
    });
    return candidates[0] || null;
  }

  function timedTextURL(track) {
    if (!track || typeof track.baseUrl !== "string") {
      return "";
    }
    try {
      const url = new URL(track.baseUrl);
      const youtubeHost = url.hostname === "youtube.com" || url.hostname.endsWith(".youtube.com");
      if (url.protocol !== "https:" || !youtubeHost || url.pathname !== "/api/timedtext") {
        return "";
      }
      url.searchParams.set("fmt", "json3");
      return url.href;
    } catch (_error) {
      return "";
    }
  }

  function normalizeText(value) {
    return String(value == null ? "" : value)
      .replace(/[\u200b\u200c\u200d\ufeff]/gu, "")
      .replace(/[\t\f\v\u00a0 ]+/gu, " ")
      .replace(/ *\n+ */gu, " ")
      .trim();
  }

  function eventText(event) {
    if (!event || !Array.isArray(event.segs)) {
      return "";
    }
    return normalizeText(event.segs.map(function (segment) {
      return segment && typeof segment.utf8 === "string" ? segment.utf8 : "";
    }).join(""));
  }

  function formattedText(value) {
    if (!value || typeof value !== "object") {
      return "";
    }
    if (typeof value.simpleText === "string") {
      return normalizeText(value.simpleText);
    }
    if (!Array.isArray(value.runs)) {
      return "";
    }
    return normalizeText(value.runs.map(function (run) {
      return run && typeof run.text === "string" ? run.text : "";
    }).join(""));
  }

  function safeMilliseconds(value) {
    const milliseconds = typeof value === "string" && value.trim()
      ? Number(value)
      : value;
    return Number.isFinite(milliseconds) && milliseconds >= 0 && milliseconds <= Number.MAX_SAFE_INTEGER
      ? Math.round(milliseconds)
      : null;
  }

  function captionContinues(previous, cue) {
    const splitCompound = /\p{Script=Han}$/u.test(previous.text) &&
      /^\p{Script=Han}/u.test(cue.text);
    const trailingPunctuation = /^[.!?。！？…」』】）)]/u.test(cue.text);
    return cue.startMs <= previous.endMs + 1000 && (splitCompound || trailingPunctuation);
  }

  function joinCaptionText(left, right) {
    const separator = /[a-z0-9]$/iu.test(left) && /^[a-z0-9]/iu.test(right) ? " " : "";
    return normalizeText(left + separator + right);
  }

  function mergeRollingCue(cues, cue) {
    const previous = cues[cues.length - 1];
    if (!previous) {
      cues.push(cue);
      return;
    }
    if (previous.text === cue.text && cue.startMs <= previous.endMs + 250) {
      previous.endMs = Math.max(previous.endMs, cue.endMs);
      return;
    }
    if (cue.startMs <= previous.endMs && cue.text.startsWith(previous.text)) {
      previous.text = cue.text;
      previous.endMs = Math.max(previous.endMs, cue.endMs);
      return;
    }
    if (cue.startMs <= previous.endMs && previous.text.startsWith(cue.text)) {
      previous.endMs = Math.max(previous.endMs, cue.endMs);
      return;
    }
    if (captionContinues(previous, cue)) {
      previous.text = joinCaptionText(previous.text, cue.text);
      previous.endMs = Math.max(previous.endMs, cue.endMs);
      return;
    }
    cues.push(cue);
  }

  function parseTimedText(value) {
    const source = typeof value === "string" ? value : JSON.stringify(value);
    if (!source || source.length > LIMITS.sourceCharacters) {
      throw new Error("The YouTube transcript is empty or too large.");
    }
    let document;
    try {
      document = typeof value === "string" ? JSON.parse(value) : value;
    } catch (_error) {
      throw new Error("YouTube returned an unreadable transcript.");
    }
    const events = document && Array.isArray(document.events) ? document.events : [];
    const cues = [];
    let totalCharacters = 0;
    for (const event of events) {
      const text = eventText(event);
      const startMs = safeMilliseconds(event && event.tStartMs);
      const durationMs = safeMilliseconds(event && event.dDurationMs);
      if (!text || startMs === null || durationMs === null || durationMs <= 0) {
        continue;
      }
      if (Array.from(text).length > LIMITS.cueCharacters) {
        throw new Error("A YouTube subtitle line is too large.");
      }
      const cue = { startMs, endMs: startMs + durationMs, text };
      const before = cues.length ? cues[cues.length - 1].text.length : 0;
      mergeRollingCue(cues, cue);
      const after = cues.length ? cues[cues.length - 1].text.length : 0;
      totalCharacters += cues[cues.length - 1] === cue ? text.length : after - before;
      if (cues.length > LIMITS.cues || totalCharacters > LIMITS.totalCharacters) {
        throw new Error("The YouTube transcript is too large.");
      }
    }
    if (!cues.length) {
      throw new Error("YouTube did not return any subtitle lines.");
    }
    return cues;
  }

  function transcriptRendererCue(renderer) {
    if (!renderer || typeof renderer !== "object") {
      return null;
    }
    const startMs = safeMilliseconds(renderer.startMs ?? renderer.startOffsetMs);
    const endMs = safeMilliseconds(renderer.endMs);
    const durationMs = safeMilliseconds(renderer.durationMs);
    const text = formattedText(renderer.snippet) || formattedText(renderer.cue) ||
      formattedText(renderer.text);
    const resolvedEndMs = endMs === null && durationMs !== null && startMs !== null
      ? startMs + durationMs
      : endMs;
    return text && startMs !== null && resolvedEndMs !== null && resolvedEndMs > startMs
      ? { startMs, endMs: resolvedEndMs, text }
      : null;
  }

  function parseTranscriptResponse(value) {
    const source = typeof value === "string" ? value : JSON.stringify(value);
    if (!source || source.length > LIMITS.sourceCharacters) {
      throw new Error("The YouTube transcript is empty or too large.");
    }
    let document;
    try {
      document = typeof value === "string" ? JSON.parse(value) : value;
    } catch (_error) {
      throw new Error("YouTube returned an unreadable transcript.");
    }

    const foundCues = [];
    const seen = new Set();
    let visited = 0;
    function visit(node, depth) {
      if (!node || typeof node !== "object" || depth > 40) {
        return;
      }
      visited += 1;
      if (visited > 100000) {
        throw new Error("The YouTube transcript is too large.");
      }
      if (Array.isArray(node)) {
        node.forEach(function (item) { visit(item, depth + 1); });
        return;
      }
      const renderer = node.transcriptSegmentRenderer || node.transcriptCueRenderer;
      if (renderer) {
        const cue = transcriptRendererCue(renderer);
        if (cue) {
          const key = cue.startMs + "\u0000" + cue.endMs + "\u0000" + cue.text;
          if (!seen.has(key)) {
            seen.add(key);
            if (Array.from(cue.text).length > LIMITS.cueCharacters) {
              throw new Error("A YouTube subtitle line is too large.");
            }
            foundCues.push(cue);
            if (foundCues.length > LIMITS.cues) {
              throw new Error("The YouTube transcript is too large.");
            }
          }
        }
      }
      Object.values(node).forEach(function (item) { visit(item, depth + 1); });
    }
    visit(document, 0);
    foundCues.sort(function (left, right) {
      return left.startMs - right.startMs || left.endMs - right.endMs;
    });

    const cues = [];
    let totalCharacters = 0;
    for (const cue of foundCues) {
      const before = cues.length ? cues[cues.length - 1].text.length : 0;
      mergeRollingCue(cues, cue);
      const after = cues.length ? cues[cues.length - 1].text.length : 0;
      totalCharacters += cues[cues.length - 1] === cue ? cue.text.length : after - before;
      if (totalCharacters > LIMITS.totalCharacters) {
        throw new Error("The YouTube transcript is too large.");
      }
    }
    if (!cues.length) {
      throw new Error("YouTube did not return any subtitle lines.");
    }
    return cues;
  }

  return {
    LIMITS,
    isVideoURL,
    japaneseTrack,
    parseTranscriptResponse,
    parseTimedText,
    selectPlayerData,
    timedTextURL,
    videoID
  };
});
