"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");

const transcriptPage = require("../background/youtube-transcript-page.js");

function withPageGlobals(values, operation) {
  const previous = new Map();
  for (const [name, value] of Object.entries(values)) {
    previous.set(name, globalThis[name]);
    globalThis[name] = value;
  }
  return Promise.resolve(operation()).finally(function () {
    for (const [name, value] of previous) {
      if (value === undefined) {
        delete globalThis[name];
      } else {
        globalThis[name] = value;
      }
    }
  });
}

function playerResponse() {
  return {
    videoDetails: { videoId: "video-1" },
    captions: {
      playerCaptionsTracklistRenderer: {
        captionTracks: [{
          baseUrl: "https://www.youtube.com/api/timedtext?v=video-1",
          languageCode: "ja",
          name: { simpleText: "Japanese" }
        }]
      }
    }
  };
}

test("reads and bounds the public player response", async function () {
  const result = await withPageGlobals({
    document: {
      scripts: [],
      getElementById: function () {
        return { getPlayerResponse: playerResponse };
      }
    }
  }, function () {
    return transcriptPage.load();
  });

  assert.deepEqual(result, {
    responses: [{
      videoID: "video-1",
      tracks: [{
        baseUrl: "https://www.youtube.com/api/timedtext?v=video-1",
        languageCode: "ja",
        kind: "",
        name: "Japanese"
      }]
    }],
    transcriptSource: ""
  });
});

test("requests the selected Japanese transcript continuation", async function () {
  const requests = [];
  const firstResponse = JSON.stringify({
    transcriptTrackSelectionMenuRenderer: {
      items: [{
        title: { simpleText: "Japanese" },
        continuation: "japanese-continuation"
      }]
    }
  });
  const japaneseResponse = JSON.stringify({ actions: [{ updateEngagementPanelAction: {} }] });

  const result = await withPageGlobals({
    document: {
      scripts: [],
      getElementById: function () {
        return { getPlayerResponse: playerResponse };
      }
    },
    ytInitialData: {
      engagementPanels: [{
        getTranscriptEndpoint: { params: "initial-params" }
      }]
    },
    ytcfg: {
      get: function (name) {
        return {
          INNERTUBE_API_KEY: "api-key",
          INNERTUBE_CONTEXT: { client: { clientVersion: "1.0" } },
          INNERTUBE_CONTEXT_CLIENT_NAME: 1
        }[name];
      }
    },
    fetch: async function (url, options) {
      requests.push({ url, body: JSON.parse(options.body) });
      return {
        ok: true,
        text: async function () {
          return requests.length === 1 ? firstResponse : japaneseResponse;
        }
      };
    }
  }, function () {
    return transcriptPage.load();
  });

  assert.equal(result.transcriptSource, japaneseResponse);
  assert.equal(requests.length, 2);
  assert.equal(requests[0].body.params, "initial-params");
  assert.equal(requests[1].body.continuation, "japanese-continuation");
});
