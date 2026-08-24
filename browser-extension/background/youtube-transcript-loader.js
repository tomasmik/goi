(function (root, factory) {
  const api = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.youtubeTranscriptLoader = api;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  function create(chrome, youtubeTranscriptModel) {
    const selectYouTubePlayerData = youtubeTranscriptModel.selectPlayerData;

    async function getYouTubeTranscript(sender) {
      const tab = sender && sender.tab;
      const expectedVideoID = tab && youtubeTranscriptModel.videoID(tab.url);
      if (!tab || !Number.isSafeInteger(tab.id) || !expectedVideoID) {
        return { ok: false, errorCode: "unavailable_page" };
      }

      let results;
      try {
        await chrome.scripting.executeScript({
          target: { tabId: tab.id, frameIds: [0] },
          world: "MAIN",
          files: ["background/youtube-transcript-page.js"]
        });
        results = await chrome.scripting.executeScript({
          target: { tabId: tab.id, frameIds: [0] },
          world: "MAIN",
          func: function () {
            const loader = globalThis.GoiYouTubeTranscriptPage;
            return loader && typeof loader.load === "function"
              ? loader.load()
              : null;
          }
        });
      } catch (_error) {
        return { ok: true, state: "unavailable", reason: "player_unavailable" };
      }

      const playerData = selectYouTubePlayerData(
        results && results[0] && results[0].result,
        expectedVideoID
      );
      if (!playerData) {
        return { ok: true, state: "unavailable", reason: "player_unavailable" };
      }
      const track = youtubeTranscriptModel.japaneseTrack(playerData.tracks);
      const transcriptURL = youtubeTranscriptModel.timedTextURL(track);
      if (!track || !transcriptURL) {
        const alternate = await getAlternateYouTubeTranscript(tab.id, expectedVideoID);
        if (alternate) {
          return alternate;
        }
        return { ok: true, state: "unavailable", reason: "no_japanese_track" };
      }

      if (typeof playerData.transcriptSource === "string" && playerData.transcriptSource) {
        try {
          return {
            ok: true,
            state: "ready",
            automatic: track.kind === "asr",
            cues: youtubeTranscriptModel.parseTranscriptResponse(playerData.transcriptSource)
          };
        } catch (_error) {
          // Some videos expose the transcript panel without usable cue data. The
          // timed-text request below still works for older videos.
        }
      }

      let downloads;
      try {
        downloads = await chrome.scripting.executeScript({
          target: { tabId: tab.id, frameIds: [0] },
          world: "MAIN",
          args: [transcriptURL],
          func: async function (url) {
            const controller = new AbortController();
            const timeout = setTimeout(function () { controller.abort(); }, 10000);
            try {
              const response = await fetch(url, {
                credentials: "include",
                signal: controller.signal
              });
              if (!response.ok) {
                return { ok: false };
              }
              const source = await response.text();
              return source.length <= 5 * 1024 * 1024
                ? { ok: true, source }
                : { ok: false };
            } catch (_error) {
              return { ok: false };
            } finally {
              clearTimeout(timeout);
            }
          }
        });
        const download = downloads && downloads[0] && downloads[0].result;
        if (!download || !download.ok || typeof download.source !== "string") {
          const alternate = await getAlternateYouTubeTranscript(tab.id, expectedVideoID);
          if (alternate) {
            return alternate;
          }
          return { ok: true, state: "unavailable", reason: "download_failed" };
        }
        const cues = youtubeTranscriptModel.parseTimedText(download.source);
        return {
          ok: true,
          state: "ready",
          automatic: track.kind === "asr",
          cues
        };
      } catch (_error) {
        const alternate = await getAlternateYouTubeTranscript(tab.id, expectedVideoID);
        if (alternate) {
          return alternate;
        }
        return { ok: true, state: "unavailable", reason: "download_failed" };
      }
    }

    // YouTube's web subtitle URLs can require a per-video proof token that is not
    // exposed to extensions. The public Android player response supplies the same
    // caption tracks without that web-only token.
    async function getAlternateYouTubeTranscript(tabID, videoID) {
      let results;
      try {
        results = await chrome.scripting.executeScript({
          target: { tabId: tabID, frameIds: [0] },
          world: "MAIN",
          args: [videoID],
          func: async function (expectedVideoID) {
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

            async function fetchWithTimeout(url, options) {
              const controller = new AbortController();
              const timeout = setTimeout(function () { controller.abort(); }, 10000);
              try {
                return await fetch(url, Object.assign({}, options, { signal: controller.signal }));
              } finally {
                clearTimeout(timeout);
              }
            }

            const apiKey = String(configValue("INNERTUBE_API_KEY") || "");
            if (!apiKey) {
              return { ok: false };
            }

            const clientVersion = "21.26.364";
            const client = {
              clientName: "ANDROID",
              clientVersion,
              androidSdkVersion: 30,
              osName: "Android",
              osVersion: "11",
              hl: "ja",
              timeZone: "UTC",
              utcOffsetMinutes: 0
            };
            const headers = {
              "content-type": "application/json",
              "x-youtube-client-name": "3",
              "x-youtube-client-version": clientVersion
            };
            const visitorData = String(configValue("VISITOR_DATA") || "");
            if (visitorData) {
              headers["x-goog-visitor-id"] = visitorData;
            }

            try {
              const playerResponse = await fetchWithTimeout(
                "/youtubei/v1/player?key=" + encodeURIComponent(apiKey) + "&prettyPrint=false",
                {
                  method: "POST",
                  credentials: "omit",
                  headers,
                  body: JSON.stringify({
                    context: { client },
                    videoId: expectedVideoID,
                    contentCheckOk: true,
                    racyCheckOk: true
                  })
                }
              );
              if (!playerResponse.ok) {
                return { ok: false };
              }
              const player = await playerResponse.json();
              if (String(player && player.videoDetails && player.videoDetails.videoId || "") !== expectedVideoID) {
                return { ok: false };
              }
              const renderer = player && player.captions && player.captions.playerCaptionsTracklistRenderer;
              const tracks = renderer && Array.isArray(renderer.captionTracks)
                ? renderer.captionTracks
                : [];
              const japanese = tracks.filter(function (candidate) {
                return candidate && /^ja(?:-|$)/iu.test(String(candidate.languageCode || ""));
              }).sort(function (left, right) {
                return Number(left.kind === "asr") - Number(right.kind === "asr");
              });
              const track = japanese[0];
              if (!track || typeof track.baseUrl !== "string") {
                return { ok: false };
              }
              const transcriptURL = new URL(track.baseUrl);
              const youtubeHost = transcriptURL.hostname === "youtube.com" ||
                transcriptURL.hostname.endsWith(".youtube.com");
              if (transcriptURL.protocol !== "https:" || !youtubeHost ||
                  transcriptURL.pathname !== "/api/timedtext") {
                return { ok: false };
              }
              transcriptURL.searchParams.set("fmt", "json3");
              const transcriptResponse = await fetchWithTimeout(transcriptURL.href, {
                credentials: "omit"
              });
              if (!transcriptResponse.ok) {
                return { ok: false };
              }
              const source = await transcriptResponse.text();
              return source && source.length <= 5 * 1024 * 1024
                ? { ok: true, automatic: track.kind === "asr", source }
                : { ok: false };
            } catch (_error) {
              return { ok: false };
            }
          }
        });
      } catch (_error) {
        return null;
      }

      const download = results && results[0] && results[0].result;
      if (!download || !download.ok || typeof download.source !== "string") {
        return null;
      }
      try {
        return {
          ok: true,
          state: "ready",
          automatic: Boolean(download.automatic),
          cues: youtubeTranscriptModel.parseTimedText(download.source)
        };
      } catch (_error) {
        return null;
      }
    }

    return { get: getYouTubeTranscript };
  }

  return { create };
});
