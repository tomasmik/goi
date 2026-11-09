const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");


const settingsModel = require("../shared/settings-model.js");
const {
  companionMessageSender,
  workerHarness,
} = require("./helpers/service-worker-harness.js");

test("limits connection and active-site operations to the popup or options page", function () {
  const harness = workerHarness(null);
  const contentSender = {
    id: "goi-test",
    tab: { id: 4, url: "https://reader.example/article" },
    frameId: 0,
    url: "https://reader.example/article",
  };
  const popupOnlyMessages = [
    "goi.player.open",
    "goi.connection.get",
    "goi.connection.verify",
    "goi.connection.save",
    "goi.connection.disconnect",
    "goi.connection.test",
    "goi.site-auto.get",
    "goi.site-auto.set",
    "goi.site-auto.list",
    "goi.site-auto.remove",
    "goi.capture.outbox-status",
    "goi.capture.outbox.retry",
    "goi.capture.outbox.discard",
    "goi.coverage.analyze-page",
  ];

  popupOnlyMessages.forEach(function (type) {
    let response;
    const keepChannelOpen = harness.handleRuntimeMessage(
      { type, version: 1 },
      contentSender,
      function (value) { response = value; },
    );
    assert.equal(keepChannelOpen, false, type);
    assert.deepEqual(JSON.parse(JSON.stringify(response)), {
      ok: false,
      errorCode: "unavailable_page",
    }, type);
  });

  assert.equal(harness.operations.popupSender({
    id: "goi-test",
    url: "chrome-extension://goi-test/popup/popup.html",
  }), true);
  assert.equal(harness.operations.popupSender({
    id: "goi-test",
    tab: { id: 7 },
    frameId: 0,
    url: "chrome-extension://goi-test/popup/popup.html",
  }), true);
  assert.equal(harness.operations.popupSender({
    id: "goi-test",
    tab: { id: 7 },
    frameId: 0,
    url: "chrome-extension://goi-test/options/options.html",
  }), true);
  assert.equal(harness.operations.popupSender({
    id: "goi-test",
    tab: { id: 7 },
    frameId: 2,
    url: "chrome-extension://goi-test/popup/popup.html",
  }), false);
});

test("ignores inherited object names as runtime message types", function () {
  const harness = workerHarness(null);
  let response;
  const keepChannelOpen = harness.handleRuntimeMessage(
    { type: "toString", version: 1 },
    { id: "goi-test", url: "chrome-extension://goi-test/popup/popup.html", frameId: 0 },
    function (value) { response = value; },
  );

  assert.equal(keepChannelOpen, false);
  assert.equal(response, undefined);
});

test("accepts only the exact top-level companion page and declared YouTube target", async function () {
  const harness = workerHarness(null);
  const sender = companionMessageSender(4, 90);

  assert.equal(harness.operations.companionPageTargetID(sender.url), 4);
  assert.equal(harness.operations.companionSender(sender), true);

  const invalidSenders = [
    { ...sender, id: "another-extension" },
    { ...sender, tab: undefined },
    { ...sender, frameId: 1 },
    { ...sender, url: sender.url + "&extra=1" },
    { ...sender, url: sender.url + "#fragment" },
    { ...sender, url: "chrome-extension://goi-test/companion/companion.html.evil?tab=4" },
    { ...sender, url: "chrome-extension://goi-test/companion/companion.html?tab=04" },
    { ...sender, tab: { ...sender.tab, url: sender.url.replace("tab=4", "tab=5") } },
  ];
  invalidSenders.forEach(function (candidate) {
    assert.equal(harness.operations.companionSender(candidate), false, candidate.url);
  });

  harness.chrome.tabs.get = async function (tabID) {
    return { id: tabID, url: "https://www.youtube.com/watch?v=abc" };
  };
  await assert.rejects(
    harness.operations.getCompanionSession(5, sender),
    (error) => error.status === 403,
  );
});

test("connection cleanup preserves a host permission used by automatic analysis", async function () {
  const harness = workerHarness({ baseUrl: "https://reader.example:8443", token: "token" });
  harness.storage.siteAutoOriginsV1 = ["https://reader.example"];
  let removals = 0;
  harness.chrome.permissions.remove = async function () {
    removals += 1;
    return true;
  };

  await harness.operations.disconnectConnection();

  assert.equal(harness.storage.connection, undefined);
  assert.equal(removals, 0);
});

test("serializes settings patches so concurrent updates cannot overwrite each other", async function () {
  const stored = { [settingsModel.STORAGE_KEY]: settingsModel.sanitize() };
  let getCalls = 0;
  let setCalls = 0;
  let releaseFirstSet;
  let firstSetStarted;
  const firstSetPending = new Promise(function (resolve) {
    firstSetStarted = resolve;
  });
  const syncStorage = {
    async get(key) {
      getCalls += 1;
      return { [key]: stored[key] };
    },
    async set(values) {
      setCalls += 1;
      if (setCalls === 1) {
        firstSetStarted();
        await new Promise(function (resolve) {
          releaseFirstSet = resolve;
        });
      }
      Object.assign(stored, values);
    },
  };
  const harness = workerHarness(null, syncStorage);

  const fontUpdate = harness.operations.updateSettings({ fontSizePx: 52 });
  await firstSetPending;
  const positionUpdate = harness.operations.updateSettings({ verticalPercent: 24 });
  await Promise.resolve();

  assert.equal(getCalls, 1);
  releaseFirstSet();
  await Promise.all([fontUpdate, positionUpdate]);
  assert.equal(getCalls, 2);
  assert.equal(stored[settingsModel.STORAGE_KEY].fontSizePx, 52);
  assert.equal(stored[settingsModel.STORAGE_KEY].verticalPercent, 24);
});

test("automatically analyzes each web navigation once and stops safely when disabled", async function () {
  const harness = workerHarness(null);
  const tab = { id: 7, url: "https://reader.example/chapter-1", status: "complete" };
  let analyses = 0;
  let stopped = 0;
  let permissionRemovals = 0;
  let coverageSource;
  const badgeUpdates = [];
  harness.chrome.tabs.query = async function () {
    return [tab];
  };
  harness.chrome.tabs.get = async function (tabId) {
    assert.equal(tabId, tab.id);
    return tab;
  };
  harness.chrome.permissions.contains = async function (permission) {
    assert.deepEqual(JSON.parse(JSON.stringify(permission)), {
      origins: ["https://reader.example/*"],
    });
    return true;
  };
  harness.chrome.permissions.remove = async function () {
    permissionRemovals += 1;
    return true;
  };
  harness.chrome.action = {
    async setBadgeText(value) {
      badgeUpdates.push(value);
    },
  };
  harness.chrome.scripting.executeScript = async function (input) {
    const argumentCount = Array.isArray(input.args) ? input.args.length : 0;
    if (argumentCount === 2) {
      stopped += 1;
      coverageSource = undefined;
      return [{ frameId: 0, result: true }];
    }
    if (argumentCount === 3) {
      if (coverageSource && coverageSource.url === input.args[1]) {
        return [{ frameId: 0, result: "existing" }];
      }
      coverageSource = { origin: input.args[2], url: input.args[1] };
      return [{ frameId: 0, result: "claimed" }];
    }
    if (!input.args) {
      return [{ frameId: 0, result: true }];
    }
    if (input.args.length === 1) {
      analyses += 1;
      return [{
        frameId: 0,
        result: {
          analysisID: input.args[0],
          url: tab.url,
          blocks: [],
        },
      }];
    }
    return [{ frameId: 0, result: true }];
  };

  const enabled = await harness.automaticAnalysis.setEnabled(true);
  assert.equal(enabled.enabled, true);
  assert.deepEqual(
    JSON.parse(JSON.stringify(harness.storage.siteAutoOriginsV1)),
    ["https://reader.example"],
  );
  assert.equal(analyses, 1);

  const reenabled = await harness.automaticAnalysis.setEnabled(true);
  assert.equal(reenabled.enabled, true);
  assert.equal(stopped, 1);
  assert.equal(analyses, 2);

  await harness.events.tabActivated.dispatch({ tabId: tab.id, windowId: 1 });
  await harness.events.tabUpdated.dispatch(tab.id, { url: tab.url }, tab);
  assert.equal(analyses, 2);

  tab.status = "loading";
  coverageSource = undefined;
  await harness.events.tabUpdated.dispatch(tab.id, { status: "loading" }, tab);
  tab.status = "complete";
  await harness.events.tabUpdated.dispatch(tab.id, { status: "complete" }, tab);
  assert.equal(analyses, 3);

  tab.url = "https://reader.example/chapter-2";
  await harness.events.tabUpdated.dispatch(tab.id, { url: tab.url }, tab);
  await harness.events.tabUpdated.dispatch(tab.id, { url: tab.url }, tab);
  assert.equal(analyses, 4);

  const disabled = await harness.automaticAnalysis.setEnabled(false);
  assert.equal(disabled.enabled, false);
  assert.equal(harness.storage.siteAutoOriginsV1, undefined);
  assert.equal(stopped, 2);
  assert.equal(permissionRemovals, 1);
  assert.deepEqual(
    JSON.parse(JSON.stringify(badgeUpdates.at(-1))),
    { tabId: tab.id, text: "" },
  );

  await harness.events.tabActivated.dispatch({ tabId: tab.id, windowId: 1 });
  assert.equal(analyses, 4);
});

test("bounds companion sessions and forwards only validated line actions", async function () {
  const harness = workerHarness(null);
  const sent = [];
  harness.chrome.tabs.get = async function (tabId) {
    assert.equal(tabId, 4);
    return { id: 4, url: "https://www.youtube.com/watch?v=abc", status: "complete" };
  };
  harness.chrome.scripting.executeScript = async function () {
    return [{ frameId: 0, result: true }];
  };
  harness.chrome.tabs.sendMessage = async function (tabId, message) {
    sent.push({ tabId, message });
    if (message.type === "goi.youtube.session.get") {
      return {
        ok: true,
        session: {
          revision: 7,
          sessionID: "session-" + "x".repeat(120),
          sourceTitle: "Japanese lesson",
          sourceURL: "https://www.youtube.com/watch?v=abc",
          observing: true,
          playbackPaused: true,
          currentLineID: 3,
          lines: [{
            id: 3,
            text: "猫を読む。",
            sourcePositionMs: 12000,
            classification: "ready",
            unknowns: [{ surface: "読む", expression: "読む", reading: "よむ", start: 2, end: 4 }],
          }, {
            id: 3,
            text: "duplicate",
            sourcePositionMs: -1,
            classification: "invented",
            unknowns: [],
          }],
        },
      };
    }
    return { ok: true, queued: false };
  };
  const sender = companionMessageSender();

  const session = await harness.operations.getCompanionSession(4, sender);
  assert.deepEqual(JSON.parse(JSON.stringify(session)), {
    ok: true,
    session: {
      revision: 7,
      sessionID: ("session-" + "x".repeat(120)).slice(0, 100),
      sourceTitle: "Japanese lesson",
      sourceURL: "https://www.youtube.com/watch?v=abc",
      observing: true,
      playbackPaused: true,
      currentLineID: 3,
      transcriptState: "unavailable",
      transcriptSource: "observed",
      transcriptReason: "",
      comprehension: null,
      lines: [{
        id: 3,
        text: "duplicate",
        sourcePositionMs: null,
        classification: "pending",
        unknowns: [],
      }],
    },
  });

  const captured = await harness.operations.forwardCompanionLineAction(
    "goi.youtube.line.capture",
    { tabId: 4, lineID: 3, surface: "読む", suggestedEntrySequence: 1579510 },
    sender,
  );
  assert.deepEqual(captured, { ok: true, queued: false });
  assert.deepEqual(JSON.parse(JSON.stringify(sent.at(-1))), {
    tabId: 4,
    message: {
      type: "goi.youtube.line.capture",
      version: 1,
      lineID: 3,
      surface: "読む",
      suggestedEntrySequence: 1579510,
    },
  });

  assert.deepEqual(
    await harness.operations.forwardCompanionTranscriptRetry(4, sender),
    { ok: true, queued: false },
  );
  assert.deepEqual(JSON.parse(JSON.stringify(sent.at(-1))), {
    tabId: 4,
    message: {
      type: "goi.youtube.transcript.retry",
      version: 1,
    },
  });

  await assert.rejects(
    harness.operations.getCompanionSession(4, { url: "https://www.youtube.com/watch?v=abc" }),
    (error) => error.status === 403,
  );
  await assert.rejects(
    harness.operations.forwardCompanionLineAction(
      "goi.youtube.line.capture",
      { tabId: 4, lineID: 3, surface: "x".repeat(201) },
      sender,
    ),
    (error) => error.status === 400,
  );
  await assert.rejects(
    harness.operations.forwardCompanionTranscriptRetry(4, { url: "https://www.youtube.com/watch?v=abc" }),
    (error) => error.status === 403,
  );
});

test("keeps all-sites access optional in the extension manifest", function () {
  const manifest = JSON.parse(fs.readFileSync(
    path.join(__dirname, "../manifest.json"),
    "utf8",
  ));

  assert.deepEqual(manifest.host_permissions, ["https://www.youtube.com/*"]);
  assert.deepEqual(manifest.optional_host_permissions, ["https://*/*", "http://*/*"]);
  assert.equal(manifest.permissions.includes("offscreen"), false);
  assert.equal(manifest.permissions.includes("tabCapture"), false);
});

test("downloads the complete Japanese YouTube transcript", async function () {
  let scriptCalls = 0;
  const harness = workerHarness(null);
  harness.chrome.scripting.executeScript = async function (input) {
    if (input.files) {
      assert.deepEqual(Array.from(input.files), ["background/youtube-transcript-page.js"]);
      return [{ result: undefined }];
    }
    scriptCalls += 1;
    assert.equal(input.world, "MAIN");
    return [{ result: {
      videoID: "test",
      transcriptSource: JSON.stringify({
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
      }),
      tracks: [{
        languageCode: "ja",
        baseUrl: "https://www.youtube.com/api/timedtext?v=test&lang=ja"
      }]
    } }];
  };

  const response = await harness.operations.getYouTubeTranscript({
    tab: { id: 4, url: "https://www.youtube.com/watch?v=test" },
    frameId: 0
  });

  assert.equal(response.ok, true);
  assert.equal(response.state, "ready");
  assert.equal(response.automatic, false);
  assert.equal(scriptCalls, 1);
  assert.deepEqual(JSON.parse(JSON.stringify(response.cues)), [
    { startMs: 1000, endMs: 2500, text: "最初の行" },
    { startMs: 4000, endMs: 5000, text: "最後の行" }
  ]);
});

test("falls back to timed text when the transcript panel is unavailable", async function () {
  let fetchedURL;
  const harness = workerHarness(null);
  harness.chrome.scripting.executeScript = async function (input) {
    assert.equal(input.world, "MAIN");
    if (input.args) {
      fetchedURL = new URL(input.args[0]);
      return [{ result: {
        ok: true,
        source: JSON.stringify({
          events: [
            { tStartMs: 1000, dDurationMs: 1500, segs: [{ utf8: "最初の行" }] }
          ]
        })
      } }];
    }
    return [{ result: {
      videoID: "test",
      transcriptSource: "",
      tracks: [{
        languageCode: "ja",
        baseUrl: "https://www.youtube.com/api/timedtext?v=test&lang=ja"
      }]
    } }];
  };

  const response = await harness.operations.getYouTubeTranscript({
    tab: { id: 4, url: "https://www.youtube.com/watch?v=test" },
    frameId: 0
  });

  assert.equal(response.state, "ready");
  assert.equal(fetchedURL.searchParams.get("fmt"), "json3");
  assert.deepEqual(JSON.parse(JSON.stringify(response.cues)), [
    { startMs: 1000, endMs: 2500, text: "最初の行" }
  ]);
});

test("falls back to an alternate player when web subtitles require a proof token", async function () {
  let scriptCalls = 0;
  const harness = workerHarness(null);
  harness.chrome.scripting.executeScript = async function (input) {
    if (input.files) {
      assert.deepEqual(Array.from(input.files), ["background/youtube-transcript-page.js"]);
      return [{ result: undefined }];
    }
    scriptCalls += 1;
    assert.equal(input.world, "MAIN");
    if (scriptCalls === 1) {
      assert.equal(input.args, undefined);
      return [{ result: {
        videoID: "test",
        transcriptSource: "",
        tracks: [{
          languageCode: "ja",
          baseUrl: "https://www.youtube.com/api/timedtext?v=test&lang=ja"
        }]
      } }];
    }
    if (scriptCalls === 2) {
      assert.match(input.args[0], /^https:\/\/www\.youtube\.com\/api\/timedtext/u);
      return [{ result: { ok: false } }];
    }
    assert.equal(input.args[0], "test");
    return [{ result: {
      ok: true,
      automatic: false,
      source: JSON.stringify({
        events: [
          { tStartMs: 1000, dDurationMs: 1500, segs: [{ utf8: "取得できた字幕" }] }
        ]
      })
    } }];
  };

  const response = await harness.operations.getYouTubeTranscript({
    tab: { id: 4, url: "https://www.youtube.com/watch?v=test" },
    frameId: 0
  });

  assert.equal(scriptCalls, 3);
  assert.equal(response.state, "ready");
  assert.equal(response.automatic, false);
  assert.deepEqual(JSON.parse(JSON.stringify(response.cues)), [
    { startMs: 1000, endMs: 2500, text: "取得できた字幕" }
  ]);
});

test("uses the main video's captions while a preroll response is active", async function () {
  const harness = workerHarness(null);
  harness.chrome.scripting.executeScript = async function (input) {
    if (input.args) {
      return [{ result: {
        ok: true,
        source: JSON.stringify({
          events: [
            { tStartMs: 1000, dDurationMs: 1500, segs: [{ utf8: "広告の後の字幕" }] }
          ]
        })
      } }];
    }
    return [{ result: {
      transcriptSource: "",
      responses: [
        {
          videoID: "preroll-ad",
          tracks: [{
            languageCode: "en",
            baseUrl: "https://www.youtube.com/api/timedtext?v=preroll-ad&lang=en"
          }]
        },
        {
          videoID: "test",
          tracks: [{
            languageCode: "ja",
            baseUrl: "https://www.youtube.com/api/timedtext?v=test&lang=ja"
          }]
        }
      ]
    } }];
  };

  const response = await harness.operations.getYouTubeTranscript({
    tab: { id: 4, url: "https://www.youtube.com/watch?v=test" },
    frameId: 0
  });

  assert.equal(response.state, "ready");
  assert.deepEqual(JSON.parse(JSON.stringify(response.cues)), [
    { startMs: 1000, endMs: 2500, text: "広告の後の字幕" }
  ]);
});

test("queues a failed YouTube capture and retries with the original nonce", async function () {
  const sent = [];
  const client = {
    create() {
      return {
        async capture(payload) {
          sent.push(JSON.parse(JSON.stringify(payload)));
          if (sent.length === 1) {
            throw new TypeError("network unavailable");
          }
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "top-secret" },
    undefined,
    client,
  );

  const delivery = await harness.operations.captureDirect({
    expression: "食べる",
    contextText: "夕食を食べる。",
    sourceKind: "video",
    sourceTitle: "Japanese lesson",
    sourceURL: "https://www.youtube.com/watch?v=example",
    sourcePositionMs: 1200,
  }, {
    tab: { id: 3 },
    frameId: 0,
    url: "https://www.youtube.com/watch?v=example",
  });

  assert.equal(delivery.queued, true);
  assert.equal(harness.storage.captureOutboxV2.length, 1);
  assert.equal(JSON.stringify(harness.storage.captureOutboxV2).includes("top-secret"), false);
  const queuedNonce = harness.storage.captureOutboxV2[0].payload.capture_nonce;
  assert.match(queuedNonce, /^[0-9a-f]{32}$/);
  assert.ok(harness.alarms.has("goi-capture-outbox"));

  await harness.delivery.retry(true);

  assert.equal(sent.length, 2);
  assert.equal(sent[0].capture_nonce, queuedNonce);
  assert.equal(sent[1].capture_nonce, queuedNonce);
  assert.equal(harness.storage.captureOutboxV2, undefined);
});

test("ordinary-page direct capture trusts only the top-frame sender metadata", async function () {
  let sent;
  const client = {
    create() {
      return {
        async capture(payload) {
          sent = JSON.parse(JSON.stringify(payload));
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "token" },
    undefined,
    client,
  );

  await harness.operations.captureDirect({
    expression: "読む",
    contextText: "本を読む。",
    sourceKind: "video",
    sourceTitle: "spoofed",
    sourceURL: "https://spoofed.example/",
    sourcePositionMs: 1000,
  }, {
    tab: { id: 4, title: "A book" },
    frameId: 0,
    url: "https://reader.example/chapter-1",
  });

  assert.equal(sent.source_kind, "web");
  assert.equal(sent.source_title, "A book");
  assert.equal(sent.source_url, "https://reader.example/chapter-1");
  assert.equal(sent.source_position_ms, null);

  await assert.rejects(
    harness.operations.captureDirect({
      expression: "読む",
      contextText: "本を読む。",
    }, {
      tab: { id: 4, title: "A book" },
      frameId: 2,
      url: "https://reader.example/frame",
    }),
    (error) => error.status === 400,
  );
});

test("local player requests require the exact top-level extension page", async function () {
  const client = {
    create() {
      return {
        async coverage(blocks) {
          return { summary: { total_occurrences: 1 }, blocks };
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "top-secret" },
    undefined,
    client,
  );
  const sender = {
    id: "goi-test",
    tab: { id: 12, url: "chrome-extension://goi-test/player/player.html" },
    frameId: 0,
    url: "chrome-extension://goi-test/player/player.html",
  };

  const connection = await harness.operations.getPlayerConnection(sender);
  assert.deepEqual(JSON.parse(JSON.stringify(connection)), {
    ok: true,
    tabId: 12,
    connection: { baseUrl: "https://goi.example", connected: true },
  });
  assert.equal(JSON.stringify(connection).includes("top-secret"), false);

  const coverage = await harness.operations.requestPlayerCoverage(
    [{ id: 1, text: "猫" }],
    sender,
  );
  assert.deepEqual(JSON.parse(JSON.stringify(coverage.blocks)), [{ id: 1, text: "猫" }]);
  await harness.operations.requestPlayerCoverage(
    [{ id: 2, text: "犬" }],
    { ...sender, url: sender.url + "#player-workspace" },
  );

  for (const untrusted of [
    { ...sender, frameId: 1 },
    { ...sender, url: "chrome-extension://goi-test/player/player.html?embedded=1" },
    { ...sender, id: "other-extension" },
    { ...sender, tab: undefined },
  ]) {
    await assert.rejects(
      harness.operations.requestPlayerCoverage([{ id: 1, text: "猫" }], untrusted),
      (error) => error.status === 403,
    );
  }
});

test("local capture forces video provenance", async function () {
  let sent;
  const client = {
    create() {
      return {
        async capture(payload) {
          sent = JSON.parse(JSON.stringify(payload));
          return { id: 42, revision: 3 };
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "top-secret" },
    undefined,
    client,
  );
  const sender = {
    id: "goi-test",
    tab: { id: 12, url: "chrome-extension://goi-test/player/player.html" },
    frameId: 0,
    url: "chrome-extension://goi-test/player/player.html",
  };

  const captured = await harness.operations.captureFromPlayer({
    sessionID: "player_session_123456",
    capture: {
      rawText: "食べる",
      expression: "食べる",
      contextText: "夕食を食べる。",
      sourceKind: "web",
      sourceTitle: "C:\\fakepath\\lesson.mp4",
      sourceURL: "file:///Users/example/lesson.mp4",
      source_url: "https://spoofed.example/video.mp4",
      sourcePositionMs: -12,
    },
  }, sender);

  assert.equal(sent.source_kind, "video");
  assert.equal(sent.source_title, "lesson.mp4");
  assert.equal(sent.source_url, "");
  assert.equal(sent.source_position_ms, 0);
  assert.equal(sent.context_text, "夕食を食べる。");
  assert.match(sent.capture_nonce, /^[0-9a-f]{32}$/u);
  assert.deepEqual(JSON.parse(JSON.stringify(captured)), { queued: false });
  assert.equal(JSON.stringify(harness.sessionStorage).includes("top-secret"), false);
});

test("coverage requests sanitize and bound page-provided text blocks", async function () {
  let sent;
  const client = {
    create() {
      return {
        async coverage(blocks) {
          sent = JSON.parse(JSON.stringify(blocks));
          return { summary: {}, blocks: [] };
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "token" },
    undefined,
    client,
  );

  await harness.operations.requestCoverage([{ id: 7, text: "日本語", ignored: "value" }]);
  assert.deepEqual(sent, [{ id: 7, text: "日本語" }]);

  await assert.rejects(
    harness.operations.requestCoverage([{ id: 1, text: "猫" }, { id: 1, text: "犬" }]),
    (error) => error.status === 422,
  );
  await assert.rejects(
    harness.operations.requestCoverage([{ id: 0, text: "猫" }]),
    (error) => error.status === 422,
  );
  const tooManyCharacters = Array.from({ length: 7 }, function (_value, index) {
    return { id: index + 1, text: "猫".repeat(index === 6 ? 1 : 20000) };
  });
  await assert.rejects(
    harness.operations.requestCoverage(tooManyCharacters),
    (error) => error.status === 422,
  );
  const oversizedJSON = Array.from({ length: 5 }, function (_value, index) {
    return { id: index + 1, text: "\u0000".repeat(18000) };
  });
  await assert.rejects(
    harness.operations.requestCoverage(oversizedJSON),
    (error) => error.status === 422,
  );
  await assert.rejects(
    harness.operations.classifyCoverage([{ id: 1, text: "猫" }], {
      tab: { id: 4 },
      frameId: 3,
      url: "https://reader.example/frame",
    }),
    (error) => error.status === 400,
  );
});

test("global ignored words are treated as known in coverage results", async function () {
  const client = {
    create() {
      return {
        async coverage() {
          return {
            summary: { known_occurrences: 0, total_occurrences: 2, unknown_unique: 2 },
            blocks: [{
              id: 1,
              tokens: [
                { surface: "東京", expression: "東京", status: "unknown" },
                { surface: "猫", expression: "猫", status: "unknown" },
              ],
            }],
          };
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "token" },
    undefined,
    client,
  );
  harness.storage.globalIgnoredWordsV1 = ["東京"];

  const result = await harness.operations.requestCoverage([{ id: 1, text: "東京の猫" }]);

  assert.equal(result.summary.known_occurrences, 1);
  assert.equal(result.summary.unknown_unique, 1);
  assert.deepEqual(result.blocks[0].tokens.map((token) => token.status), ["known", "unknown"]);
});

test("reader preference routes derive the origin and never expose trusted records", async function () {
  const worker = workerHarness({ baseUrl: "https://goi.example", token: "secret" });
  worker.storage.captureOutboxV2 = { sensitive: true };
  worker.storage["goiCoveragePanel:https://other.example"] = { collapsed: false, corner: "top-left" };
  const sender = { id: "goi-test", frameId: 0, url: "https://reader.example/chapter", tab: { id: 4, url: "https://reader.example/chapter" } };
  const get = { type: "goi.coverage.panel.get", version: 1 };
  assert.deepEqual(JSON.parse(JSON.stringify((await worker.dispatch(get, sender)).preference)), { collapsed: true, corner: "bottom-right" });
  const preference = { collapsed: false, corner: "top-right" };
  assert.equal((await worker.dispatch({ type: "goi.coverage.panel.update", version: 1, preference }, sender)).ok, true);
  assert.deepEqual(JSON.parse(JSON.stringify((await worker.dispatch(get, sender)).preference)), preference);
  for (const extra of [{ key: "connection" }, { key: "captureOutboxV2" }, { origin: "https://other.example" }]) {
    assert.equal((await worker.dispatch({ ...get, ...extra }, sender)).ok, false);
  }
  for (const value of [{ corner: "top-right" }, { collapsed: false, corner: "invalid" }, { ...preference, token: "bad" }]) {
    assert.equal((await worker.dispatch({ type: "goi.coverage.panel.update", version: 1, preference: value }, sender)).ok, false);
  }
  for (const denied of [
    { ...sender, frameId: 1 },
    { ...sender, url: "https://other.example" },
    { ...sender, url: "file:///tmp/page", tab: { id: 4, url: "file:///tmp/page" } },
    { id: "goi-test", url: "chrome-extension://goi-test/popup/popup.html" },
  ]) assert.equal((await worker.dispatch(get, denied)).errorCode, "unavailable_page");
  assert.equal(worker.storage.connection.token, "secret");
  assert.deepEqual(worker.storage.captureOutboxV2, { sensitive: true });
  assert.equal(worker.storage["goiCoveragePanel:https://other.example"].corner, "top-left");
});
