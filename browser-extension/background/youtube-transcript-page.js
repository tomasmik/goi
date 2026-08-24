(function (root, factory) {
  const api = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }

  root.GoiYouTubeTranscriptPage = api;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  function responseObject(value) {
    if (!value) {
      return null;
    }
    if (typeof value === "string") {
      try {
        return JSON.parse(value);
      } catch (_error) {
        return null;
      }
    }
    return typeof value === "object" ? value : null;
  }

  function assignedObject(name) {
    const scripts = Array.from(document.scripts || []);
    for (const script of scripts) {
      const source = String(script && script.textContent || "");
      const nameIndex = source.indexOf(name);
      if (nameIndex < 0) {
        continue;
      }
      const equalsIndex = source.indexOf("=", nameIndex + name.length);
      const start = source.indexOf("{", equalsIndex + 1);
      if (equalsIndex < 0 || start < 0) {
        continue;
      }
      let depth = 0;
      let quoted = false;
      let escaped = false;
      for (let index = start; index < source.length; index += 1) {
        const character = source[index];
        if (quoted) {
          if (escaped) {
            escaped = false;
          } else if (character === "\\") {
            escaped = true;
          } else if (character === "\"") {
            quoted = false;
          }
          continue;
        }
        if (character === "\"") {
          quoted = true;
        } else if (character === "{") {
          depth += 1;
        } else if (character === "}") {
          depth -= 1;
          if (depth === 0) {
            const parsed = responseObject(source.slice(start, index + 1));
            if (parsed) {
              return parsed;
            }
            break;
          }
        }
      }
    }
    return null;
  }

  function findTranscriptParams(value) {
    const pending = [{ value, depth: 0 }];
    let visited = 0;
    for (let index = 0; index < pending.length; index += 1) {
      const item = pending[index];
      const node = item.value;
      if (!node || typeof node !== "object" || item.depth > 30 || visited > 50000) {
        continue;
      }
      visited += 1;
      if (node.getTranscriptEndpoint && typeof node.getTranscriptEndpoint.params === "string") {
        return node.getTranscriptEndpoint.params;
      }
      for (const child of Object.values(node)) {
        if (child && typeof child === "object") {
          pending.push({ value: child, depth: item.depth + 1 });
        }
      }
    }
    return "";
  }

  function displayText(value) {
    if (typeof value === "string") {
      return value;
    }
    if (!value || typeof value !== "object") {
      return "";
    }
    if (typeof value.simpleText === "string") {
      return value.simpleText;
    }
    return Array.isArray(value.runs)
      ? value.runs.map(function (run) { return String(run && run.text || ""); }).join("")
      : "";
  }

  function nestedContinuation(value) {
    const pending = [{ value, depth: 0 }];
    for (let index = 0; index < pending.length; index += 1) {
      const item = pending[index];
      const node = item.value;
      if (!node || typeof node !== "object" || item.depth > 8) {
        continue;
      }
      if (typeof node.continuation === "string") {
        return node.continuation;
      }
      for (const child of Object.values(node)) {
        if (child && typeof child === "object") {
          pending.push({ value: child, depth: item.depth + 1 });
        }
      }
    }
    return "";
  }

  function japaneseContinuation(value, trackTitles) {
    const wantedTitles = [];
    const wanted = new Set();
    (Array.isArray(trackTitles) ? trackTitles : []).forEach(function (title) {
      const normalized = String(title || "").trim().toLowerCase();
      if (normalized && !wanted.has(normalized)) {
        wanted.add(normalized);
        wantedTitles.push(normalized);
      }
    });
    const pending = [{ value, depth: 0 }];
    const matches = new Map();
    let languageCodeMatch = "";
    let labelMatch = "";
    let visited = 0;
    for (let index = 0; index < pending.length; index += 1) {
      const item = pending[index];
      const node = item.value;
      if (!node || typeof node !== "object" || item.depth > 30 || visited > 50000) {
        continue;
      }
      visited += 1;
      const title = displayText(node.title);
      const normalizedTitle = title.trim().toLowerCase();
      if (normalizedTitle || /^ja(?:-|$)/iu.test(String(node.languageCode || ""))) {
        const continuation = nestedContinuation(node);
        if (continuation) {
          if (wanted.has(normalizedTitle) && !matches.has(normalizedTitle)) {
            matches.set(normalizedTitle, continuation);
          }
          if (!languageCodeMatch && /^ja(?:-|$)/iu.test(String(node.languageCode || ""))) {
            languageCodeMatch = continuation;
          }
          if (!labelMatch && /日本語|Japanese/iu.test(title)) {
            labelMatch = continuation;
          }
        }
      }
      for (const child of Object.values(node)) {
        if (child && typeof child === "object") {
          pending.push({ value: child, depth: item.depth + 1 });
        }
      }
    }
    for (const title of wantedTitles) {
      if (matches.has(title)) {
        return matches.get(title);
      }
    }
    return languageCodeMatch || labelMatch;
  }

  function configValue(name) {
    if (globalThis.ytcfg && typeof globalThis.ytcfg.get === "function") {
      const value = globalThis.ytcfg.get(name);
      if (value != null) {
        return value;
      }
    }
    return globalThis.ytcfg && globalThis.ytcfg.data_
      ? globalThis.ytcfg.data_[name]
      : null;
  }

  async function requestTranscript(body) {
    const apiKey = String(configValue("INNERTUBE_API_KEY") || "");
    const context = configValue("INNERTUBE_CONTEXT");
    if (!apiKey || !context || !body) {
      return "";
    }
    const controller = new AbortController();
    const timeout = setTimeout(function () { controller.abort(); }, 10000);
    try {
      const client = context.client || {};
      const headers = {
        "content-type": "application/json",
        "x-youtube-client-name": String(configValue("INNERTUBE_CONTEXT_CLIENT_NAME") || 1),
        "x-youtube-client-version": String(client.clientVersion || configValue("INNERTUBE_CONTEXT_CLIENT_VERSION") || "")
      };
      const visitorData = String(configValue("VISITOR_DATA") || "");
      if (visitorData) {
        headers["x-goog-visitor-id"] = visitorData;
      }
      const response = await fetch("/youtubei/v1/get_transcript?key=" + encodeURIComponent(apiKey) + "&prettyPrint=false", {
        method: "POST",
        credentials: "include",
        headers,
        body: JSON.stringify(Object.assign({ context }, body)),
        signal: controller.signal
      });
      if (!response.ok) {
        return "";
      }
      const source = await response.text();
      return source.length <= 5 * 1024 * 1024 ? source : "";
    } catch (_error) {
      return "";
    } finally {
      clearTimeout(timeout);
    }
  }

  function publicPlayerResponse(response) {
    const renderer = response && response.captions &&
      response.captions.playerCaptionsTracklistRenderer;
    const tracks = renderer && Array.isArray(renderer.captionTracks)
      ? renderer.captionTracks
      : [];
    return {
      videoID: String(response && response.videoDetails && response.videoDetails.videoId || ""),
      tracks: tracks.slice(0, 100).map(function (track) {
        return {
          baseUrl: String(track && track.baseUrl || "").slice(0, 20000),
          languageCode: String(track && track.languageCode || "").slice(0, 100),
          kind: String(track && track.kind || "").slice(0, 100),
          name: String(track && track.name && (
            track.name.simpleText ||
            (Array.isArray(track.name.runs) ? track.name.runs.map(function (run) {
              return run && run.text || "";
            }).join("") : "")
          ) || "").slice(0, 500)
        };
      })
    };
  }

  async function load() {
    const responses = [];
    function addResponse(value) {
      const response = responseObject(value);
      if (response) {
        responses.push(publicPlayerResponse(response));
      }
    }

    const pagePlayer = document.getElementById("movie_player");
    let pagePlayerResponse = null;
    try {
      pagePlayerResponse = pagePlayer && typeof pagePlayer.getPlayerResponse === "function"
        ? pagePlayer.getPlayerResponse()
        : null;
    } catch (_error) {
      pagePlayerResponse = null;
    }
    addResponse(pagePlayerResponse);
    addResponse(globalThis.ytInitialPlayerResponse);
    addResponse(globalThis.ytplayer && globalThis.ytplayer.config &&
      globalThis.ytplayer.config.args && globalThis.ytplayer.config.args.raw_player_response);
    addResponse(globalThis.ytplayer && globalThis.ytplayer.config &&
      globalThis.ytplayer.config.args && globalThis.ytplayer.config.args.player_response);
    addResponse(assignedObject("ytInitialPlayerResponse"));

    const initialData = responseObject(globalThis.ytInitialData) || assignedObject("ytInitialData");
    const transcriptParams = findTranscriptParams(initialData);
    const japaneseTrackTitles = responses.flatMap(function (response) {
      return response.tracks.filter(function (track) {
        return /^ja(?:-|$)/iu.test(track.languageCode);
      });
    }).sort(function (left, right) {
      return Number(left.kind === "asr") - Number(right.kind === "asr");
    }).map(function (track) {
      return track.name;
    });
    let transcriptSource = transcriptParams
      ? await requestTranscript({ params: transcriptParams })
      : "";
    if (transcriptSource) {
      const transcriptResponse = responseObject(transcriptSource);
      const continuation = japaneseContinuation(transcriptResponse, japaneseTrackTitles);
      if (continuation) {
        const japaneseSource = await requestTranscript({ continuation });
        transcriptSource = japaneseSource || transcriptSource;
      }
    }
    return { responses, transcriptSource };
  }

  return { load };
});
