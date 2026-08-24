const test = require("node:test");
const assert = require("node:assert/strict");

const model = require("../shared/youtube-transcript-model.js");

test("recognizes only supported YouTube video URLs", function () {
  assert.equal(model.isVideoURL("https://www.youtube.com/watch?v=video"), true);
  assert.equal(model.isVideoURL("https://www.youtube.com/shorts/video"), true);
  assert.equal(model.isVideoURL("https://www.youtube.com/"), false);
  assert.equal(model.isVideoURL("https://example.com/watch?v=video"), false);
});

test("combines caption tracks from matching player responses", function () {
  assert.deepEqual(model.selectPlayerData({
    responses: [
      { videoID: "ad", tracks: [{ baseUrl: "https://www.youtube.com/api/timedtext?lang=en" }] },
      { videoID: "main", tracks: [{ baseUrl: "https://www.youtube.com/api/timedtext?lang=ja", languageCode: "ja" }] },
      { videoID: "main", tracks: [{ baseUrl: "https://www.youtube.com/api/timedtext?lang=ja", languageCode: "ja" }] },
    ],
    transcriptSource: "transcript",
  }, "main"), {
    videoID: "main",
    tracks: [{ baseUrl: "https://www.youtube.com/api/timedtext?lang=ja", languageCode: "ja" }],
    transcriptSource: "transcript",
  });
});

test("prefers human Japanese captions and builds a JSON transcript URL", function () {
  const track = model.japaneseTrack([
    { languageCode: "en", baseUrl: "https://www.youtube.com/api/timedtext?lang=en" },
    { languageCode: "ja", kind: "asr", baseUrl: "https://www.youtube.com/api/timedtext?lang=ja&kind=asr" },
    { languageCode: "ja-JP", baseUrl: "https://www.youtube.com/api/timedtext?lang=ja" }
  ]);

  assert.equal(track.kind, undefined);
  const url = new URL(model.timedTextURL(track));
  assert.equal(url.origin + url.pathname, "https://www.youtube.com/api/timedtext");
  assert.equal(url.searchParams.get("fmt"), "json3");
});

test("rejects caption URLs outside YouTube timed text", function () {
  assert.equal(model.timedTextURL({
    baseUrl: "https://example.com/api/timedtext?lang=ja"
  }), "");
  assert.equal(model.timedTextURL({
    baseUrl: "http://www.youtube.com/api/timedtext?lang=ja"
  }), "");
  assert.equal(model.timedTextURL({
    baseUrl: "https://www.youtube.com/watch?v=test"
  }), "");
});

test("parses the full JSON3 transcript and collapses rolling updates", function () {
  const cues = model.parseTimedText(JSON.stringify({
    events: [
      { tStartMs: 0, dDurationMs: 1500, segs: [{ utf8: "今日は" }] },
      { tStartMs: 900, dDurationMs: 1600, segs: [{ utf8: "今日は晴れです" }] },
      { tStartMs: 3000, dDurationMs: 1200, segs: [{ utf8: "猫です\n" }] },
      { tStartMs: 4300, dDurationMs: 1000, segs: [{ utf8: "猫です" }] },
      { tStartMs: 5500, dDurationMs: 1000 }
    ]
  }));

  assert.deepEqual(cues, [
    { startMs: 0, endMs: 2500, text: "今日は晴れです" },
    { startMs: 3000, endMs: 5300, text: "猫です" }
  ]);
});

test("rejoins generated caption fragments split across word boundaries", function () {
  const cues = model.parseTimedText({
    events: [
      { tStartMs: 0, dDurationMs: 1000, segs: [{ utf8: "九州" }] },
      { tStartMs: 900, dDurationMs: 1200, segs: [{ utf8: "大学です。" }] },
      { tStartMs: 3000, dDurationMs: 500, segs: [{ utf8: "都営三田" }] },
      { tStartMs: 4200, dDurationMs: 1000, segs: [{ utf8: "線です。" }] },
      { tStartMs: 5000, dDurationMs: 1000, segs: [{ utf8: "次の文です。" }] }
    ]
  });

  assert.deepEqual(cues, [
    { startMs: 0, endMs: 2100, text: "九州大学です。" },
    { startMs: 3000, endMs: 5200, text: "都営三田線です。" },
    { startMs: 5000, endMs: 6000, text: "次の文です。" }
  ]);
});

test("parses transcript panel responses", function () {
  const cues = model.parseTranscriptResponse(JSON.stringify({
    actions: [{
      updateEngagementPanelAction: {
        content: {
          transcriptRenderer: {
            content: {
              transcriptSearchPanelRenderer: {
                body: {
                  transcriptSegmentListRenderer: {
                    initialSegments: [
                      { transcriptSegmentRenderer: {
                        startMs: "1000",
                        endMs: "2500",
                        snippet: { runs: [{ text: "最初の行" }] }
                      } },
                      { transcriptSegmentRenderer: {
                        startMs: "4000",
                        endMs: "5000",
                        snippet: { runs: [{ text: "最後の行" }] }
                      } }
                    ]
                  }
                }
              }
            }
          }
        }
      }
    }]
  }));

  assert.deepEqual(cues, [
    { startMs: 1000, endMs: 2500, text: "最初の行" },
    { startMs: 4000, endMs: 5000, text: "最後の行" }
  ]);
});

test("parses older transcript cue renderers", function () {
  assert.deepEqual(model.parseTranscriptResponse({
    transcriptCueRenderer: {
      startOffsetMs: "4000",
      durationMs: "1000",
      cue: { simpleText: "最後の行" }
    }
  }), [
    { startMs: 4000, endMs: 5000, text: "最後の行" }
  ]);
});

test("rejects transcript panel responses without subtitle lines", function () {
  assert.throws(function () {
    model.parseTranscriptResponse({ actions: [] });
  }, /subtitle lines/u);
});

test("extracts video IDs from supported YouTube URLs", function () {
  assert.equal(model.videoID("https://www.youtube.com/watch?v=abc123"), "abc123");
  assert.equal(model.videoID("https://www.youtube.com/shorts/abc123"), "abc123");
  assert.equal(model.videoID("https://example.com/watch?v=abc123"), "");
});
