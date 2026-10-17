const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const { spawn, spawnSync } = require("node:child_process");

test("Chrome installs the extension and runs local video and YouTube flows", {
  timeout: 30000,
}, async function (t) {
  const chromeExecutable = findChromeExecutable();
  assert.ok(chromeExecutable, "Chrome is required; set CHROME_BIN to its executable");

  const repositoryDirectory = path.resolve(__dirname, "../..");
  const goiFixture = await startGoiFixture();
  const smokeDirectory = fs.mkdtempSync(path.join(os.tmpdir(), "goi-chrome-smoke-"));
  const extensionDirectory = path.join(smokeDirectory, "extension");
  const profileDirectory = path.join(smokeDirectory, "profile");
  materializeEmbeddedExtension(repositoryDirectory, extensionDirectory, smokeDirectory);
  const manifest = JSON.parse(fs.readFileSync(path.join(extensionDirectory, "manifest.json"), "utf8"));
  // The smoke test cannot accept an optional-host permission prompt.
  manifest.host_permissions.push("http://127.0.0.1/*");
  fs.writeFileSync(
    path.join(extensionDirectory, "manifest.json"),
    JSON.stringify(manifest, null, 2) + "\n",
  );
  assert.equal(manifest.minimum_chrome_version, "116");
  assert.equal(manifest.permissions.includes("offscreen"), false);
  assert.equal(manifest.permissions.includes("tabCapture"), false);
  assert.equal(fs.existsSync(path.join(extensionDirectory, "offscreen/offscreen.html")), false);
  assert.equal(fs.existsSync(path.join(extensionDirectory, "offscreen/offscreen.js")), false);
  fs.mkdirSync(profileDirectory);
  const args = [
    "--no-first-run",
    "--no-default-browser-check",
    "--remote-debugging-pipe",
    "--enable-unsafe-extension-debugging",
    `--user-data-dir=${profileDirectory}`,
    "about:blank",
  ];
  if (process.env.GOI_CHROME_HEADED !== "1") {
    args.unshift("--headless=new");
  }
  if (typeof process.getuid === "function" && process.getuid() === 0) {
    args.unshift("--no-sandbox");
  }

  const chrome = spawn(chromeExecutable, args, {
    stdio: ["ignore", "ignore", "pipe", "pipe", "pipe"],
  });
  let chromeErrors = "";
  chrome.stderr.setEncoding("utf8");
  chrome.stderr.on("data", function (chunk) {
    chromeErrors += chunk;
  });
  const devTools = connectDevToolsPipe(chrome, function () {
    return chromeErrors;
  });

  t.after(async function () {
    try {
      await devTools.send("Browser.close");
    } catch (_error) {
    }
    devTools.close();
    try {
      await stopProcess(chrome);
    } finally {
      fs.rmSync(smokeDirectory, { recursive: true, force: true });
      await goiFixture.close();
    }
  });

  // This tab exists before installation, so only worker recovery can inject it.
  const youtubeFixture = await prepareYouTube(devTools);
  await devTools.send("Target.createTarget", { url: "about:blank" });
  const installed = await devTools.send("Extensions.loadUnpacked", {
    path: extensionDirectory,
  });
  assert.match(installed.id, /^[a-p]{32}$/);

  const extensionList = await devTools.send("Extensions.getExtensions");
  const extension = extensionList.extensions.find(function (candidate) {
    return candidate.id === installed.id;
  });
  assert.deepEqual(extension && {
    name: extension.name,
    version: extension.version,
    path: fs.realpathSync(extension.path),
    enabled: extension.enabled,
  }, {
    name: "Goi Capture",
    version: "0.2.5",
    path: fs.realpathSync(extensionDirectory),
    enabled: true,
  });

  const popupURL = `chrome-extension://${installed.id}/popup/popup.html`;
  const popupTarget = await devTools.send("Target.createTarget", { url: popupURL });
  const attached = await devTools.send("Target.attachToTarget", {
    targetId: popupTarget.targetId,
    flatten: true,
  });
  const popup = await waitForPopup(devTools, attached.sessionId);
  assert.deepEqual(popup, {
    ready: "complete",
    title: "Goi Capture",
    heading: "Goi Capture",
    extensionID: installed.id,
    manifestName: "Goi Capture",
    manifestVersion: "0.2.5",
    manifestIcon: "icons/icon128.png",
    analyzeDisabled: true,
    analyzeText: "This page can’t be analyzed",
    statusError: false,
  });

  const targets = await devTools.send("Target.getTargets");
  assert.equal(targets.targetInfos.some(function (target) {
    return target.type === "service_worker" &&
      target.url === `chrome-extension://${installed.id}/background/service-worker.js`;
  }), true, `Goi service worker was not running. Chrome output:\n${chromeErrors}`);

  await exerciseLocalPlayer(devTools, installed.id, attached.sessionId, goiFixture);
  await exerciseYouTube(devTools, installed.id, attached.sessionId, goiFixture, youtubeFixture);
  await exerciseReaderPage(devTools, installed.id, attached.sessionId, goiFixture);
});

async function exerciseLocalPlayer(devTools, extensionID, popupSessionID, goiFixture) {
  const openedPlayer = await devTools.send("Runtime.evaluate", {
    expression: `chrome.runtime.sendMessage({type: "goi.player.open", version: 1})`,
    awaitPromise: true,
    returnByValue: true,
  }, popupSessionID);
  assert.equal(openedPlayer.result.value.ok, true, JSON.stringify(openedPlayer.result.value));
  const playerTarget = await waitForPlayerTarget(devTools, extensionID);
  const playerSession = await devTools.send("Target.attachToTarget", {
    targetId: playerTarget.targetId,
    flatten: true,
  });
  const player = await waitForPlayer(devTools, playerSession.sessionId);
  assert.deepEqual(player, {
    ready: "complete",
    title: "Goi Local Player",
    heading: "Play a video from this computer",
    videoInput: "file",
    subtitleInput: "file",
    subtitleInputMultiple: true,
    trackControlsHidden: true,
    displayModes: 4,
    pauseModes: 4,
    parser: "object",
    connection: "Not connected",
  });
  await devTools.send("Emulation.setDeviceMetricsOverride", {
    width: 400,
    height: 800,
    deviceScaleFactor: 1,
    mobile: false,
  }, playerSession.sessionId);
  const narrowPlayer = await devTools.send("Runtime.evaluate", {
    expression: `(new Promise(function (resolve) {
      requestAnimationFrame(function () {
        resolve({
          horizontalOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
          videoVisible: document.getElementById("video-stage").getBoundingClientRect().width > 0,
          videoFillsViewport: document.getElementById("video-stage").getBoundingClientRect().height / innerHeight > 0.85,
          panelHidden: document.getElementById("workspace-panel").hidden
        });
      });
    }))`,
    awaitPromise: true,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(narrowPlayer.result.value, {
    horizontalOverflow: false,
    videoVisible: true,
    videoFillsViewport: true,
    panelHidden: true,
  });
  const panelInteraction = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const transcript = document.querySelector('[data-panel-target="transcript"]');
      transcript.click();
      const opened = {
        panelHidden: document.getElementById("workspace-panel").hidden,
        transcriptHidden: document.getElementById("transcript-panel").hidden,
        expanded: transcript.getAttribute("aria-expanded")
      };
      document.getElementById("panel-close").click();
      return {
        opened,
        closed: document.getElementById("workspace-panel").hidden,
        expandedAfterClose: transcript.getAttribute("aria-expanded")
      };
    })()`,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(panelInteraction.result.value, {
    opened: {
      panelHidden: false,
      transcriptHidden: false,
      expanded: "true",
    },
    closed: true,
    expandedAfterClose: "false",
  });
  await devTools.send("Emulation.clearDeviceMetricsOverride", undefined, playerSession.sessionId);

  await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const subtitles = new File([
        "1\\n00:00:00,000 --> 00:00:01,000\\n日本語を勉強する。\\n\\n" +
        "2\\n00:00:05,000 --> 00:00:06,000\\n猫が好きです。\\n"
      ], "local-smoke.srt", {type: "application/x-subrip"});
      const alternate = new File([
        "1\\n00:00:02,000 --> 00:00:03,000\\n別の字幕です。\\n"
      ], "alternate.srt", {type: "application/x-subrip"});
      const transfer = new DataTransfer();
      transfer.items.add(subtitles);
      transfer.items.add(alternate);
      const input = document.getElementById("subtitle-file");
      input.files = transfer.files;
      input.dispatchEvent(new Event("change", {bubbles: true}));
      return true;
    })()`,
    returnByValue: true,
  }, playerSession.sessionId);
  const transcript = await waitForPlayerTranscript(devTools, playerSession.sessionId);
  assert.deepEqual(transcript, {
    selectedTrack: "local-smoke.srt · 2 lines",
    trackCount: 2,
    trackControlsHidden: false,
    lineCount: 2,
    firstLine: "日本語を勉強する。",
    secondLine: "猫が好きです。",
    offsetEnabled: true,
    coverage: "Coverage unavailable",
  });
  await devTools.send("Emulation.setDeviceMetricsOverride", {
    width: 400,
    height: 800,
    deviceScaleFactor: 1,
    mobile: false,
  }, playerSession.sessionId);
  const narrowTrackPicker = await devTools.send("Runtime.evaluate", {
    expression: `(new Promise(function (resolve) {
      requestAnimationFrame(function () {
        resolve({
          horizontalOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
          pickerVisible: document.getElementById("subtitle-track").getBoundingClientRect().width > 0
        });
      });
    }))`,
    awaitPromise: true,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(narrowTrackPicker.result.value, {
    horizontalOverflow: false,
    pickerVisible: true,
  });
  await devTools.send("Emulation.clearDeviceMetricsOverride", undefined, playerSession.sessionId);
  const alternateTrack = await selectPlayerTrack(
    devTools,
    playerSession.sessionId,
    "alternate.srt",
    "別の字幕です。",
  );
  assert.deepEqual(alternateTrack, {
    selectedTrack: "alternate.srt · 1 line",
    lineCount: 1,
    firstLine: "別の字幕です。",
  });
  await selectPlayerTrack(
    devTools,
    playerSession.sessionId,
    "local-smoke.srt",
    "日本語を勉強する。",
  );
  const failOpenFilter = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const filter = document.getElementById("unknown-only");
      filter.click();
      return {
        checked: filter.checked,
        visibleLines: document.querySelectorAll(".transcript-line").length
      };
    })()`,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(failOpenFilter.result.value, {
    checked: true,
    visibleLines: 2,
  });

  const optionsTarget = await devTools.send("Target.createTarget", {
    url: `chrome-extension://${extensionID}/options/options.html`,
  });
  const optionsSession = await devTools.send("Target.attachToTarget", {
    targetId: optionsTarget.targetId,
    flatten: true,
  });
  const connected = await connectThroughOptions(
    devTools,
    optionsSession.sessionId,
    goiFixture.baseURL,
  );
  assert.deepEqual(connected, {
    heading: "Goi connection",
    summary: `Connected to ${goiFixture.baseURL}`,
    status: "Connected to Goi.",
    testButton: "Test connection",
  });
  await devTools.send("Target.activateTarget", { targetId: playerTarget.targetId });
  const coveredTranscript = await waitForPlayerCoverage(devTools, playerSession.sessionId);
  assert.deepEqual(coveredTranscript, {
    connection: `Connected to ${goiFixture.baseURL}`,
    summary: "50% known · 2 unknown · 2/2 checked",
    unknownTargets: ["勉強する", "猫"],
    batch: "Choose a video to find lines",
  });
  const localFurigana = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const input = document.getElementById("furigana");
      input.checked = true;
      input.dispatchEvent(new Event("change", {bubbles: true}));
      const result = {
        checked: input.checked,
        reading: document.querySelector(".transcript-line rt")?.textContent || "",
        saved: JSON.parse(localStorage.getItem("goiLocalPlayerDisplayV1") || "null")?.furiganaEnabled
      };
      return result;
    })()`,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(localFurigana.result.value, {
    checked: true,
    reading: "べんきょう",
    saved: true,
  });

  const generatedVideo = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      const canvas = document.createElement("canvas");
      canvas.width = 320;
      canvas.height = 180;
      const context = canvas.getContext("2d");
      const stream = canvas.captureStream(15);
      const audioContext = new AudioContext();
      const oscillator = audioContext.createOscillator();
      const gain = audioContext.createGain();
      const destination = audioContext.createMediaStreamDestination();
      gain.gain.value = 0.1;
      oscillator.connect(gain).connect(destination);
      oscillator.start();
      destination.stream.getAudioTracks().forEach(function (track) {
        stream.addTrack(track);
      });
      const mimeType = ["video/webm;codecs=vp8,opus", "video/webm"].find(function (type) {
        return MediaRecorder.isTypeSupported(type);
      });
      const recorder = mimeType ? new MediaRecorder(stream, {mimeType}) : new MediaRecorder(stream);
      const chunks = [];
      recorder.addEventListener("dataavailable", function (event) {
        if (event.data && event.data.size) chunks.push(event.data);
      });
      const stopped = new Promise(function (resolve) {
        recorder.addEventListener("stop", resolve, {once: true});
      });
      recorder.start(50);
      for (let frame = 0; frame < 30; frame += 1) {
        context.fillStyle = frame % 2 ? "#177f83" : "#b84639";
        context.fillRect(0, 0, canvas.width, canvas.height);
        context.fillStyle = "white";
        context.font = "32px sans-serif";
        context.fillText("Goi " + frame, 105, 100);
        await new Promise(function (resolve) { setTimeout(resolve, 55); });
      }
      recorder.stop();
      await stopped;
      oscillator.stop();
      stream.getTracks().forEach(function (track) { track.stop(); });
      await audioContext.close();
      const file = new File(chunks, "local-smoke.webm", {type: recorder.mimeType || "video/webm"});
      globalThis.goiSmokeVideoFile = file;
      const transfer = new DataTransfer();
      transfer.items.add(file);
      const input = document.getElementById("video-file");
      input.files = transfer.files;
      input.dispatchEvent(new Event("change", {bubbles: true}));
      return {size: file.size, type: file.type};
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.equal(generatedVideo.result.value.size > 0, true);
  assert.match(generatedVideo.result.value.type, /^video\/webm/u);
  const localVideo = await waitForPlayerVideo(devTools, playerSession.sessionId);
  assert.equal(localVideo.videoName, "local-smoke.webm");
  assert.equal(localVideo.blobSource, true);
  assert.equal(localVideo.ready, true);
  assert.equal(localVideo.batch, "Send 1 line to mining");

  const videoControls = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const video = document.getElementById("video");
      const rect = video.getBoundingClientRect();
      return {
        emptyOverlayVisible: document.getElementById("video-empty").getClientRects().length > 0,
        transportTarget: document.elementFromPoint(rect.left + 24, rect.bottom - 24)?.id
      };
    })()`,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(videoControls.result.value, {
    emptyOverlayVisible: false,
    transportTarget: "video",
  }, "A chosen video must expose its native controls instead of the empty-state overlay");

  const playButton = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const video = document.getElementById("video");
      video.muted = true;
      const rect = video.getBoundingClientRect();
      return {x: rect.left + 24, y: rect.bottom - 24};
    })()`,
    returnByValue: true,
  }, playerSession.sessionId);
  for (const type of ["mouseMoved", "mousePressed", "mouseReleased"]) {
    await devTools.send("Input.dispatchMouseEvent", {
      type, ...playButton.result.value, button: "left", clickCount: 1,
    }, playerSession.sessionId);
  }

  const playback = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      const video = document.getElementById("video");
      await new Promise(function (resolve) { setTimeout(resolve, 250); });
      const result = {
        advanced: video.currentTime > 0,
        wordCount: document.querySelectorAll(".subtitle-cue .subtitle-word").length,
        knownWord: document.querySelector(".subtitle-cue .subtitle-word--known")?.textContent || "",
        unknownLabel: document.querySelector(".subtitle-cue .subtitle-word--unknown")?.getAttribute("aria-label") || "",
        unclassifiedLabel: document.querySelector(".subtitle-cue .subtitle-word--unclassified")?.getAttribute("aria-label") || "",
        furigana: document.querySelector(".subtitle-cue .subtitle-word--unknown rt")?.textContent || "",
        sourceProtocol: new URL(video.src).protocol,
        furiganaGeometry: (function () {
          const word = document.querySelector(".subtitle-cue .subtitle-word--unknown");
          const ruby = word?.querySelector("ruby");
          const reading = ruby?.querySelector("rt");
          const cue = word?.closest(".subtitle-cue");
          const base = document.createRange();
          if (!word || !ruby || !reading || !cue || !ruby.firstChild) return null;
          base.selectNode(ruby.firstChild);
          const wordRect = word.getBoundingClientRect();
          const rubyRect = ruby.getBoundingClientRect();
          const readingRect = reading.getBoundingClientRect();
          const baseRect = base.getBoundingClientRect();
          const cueRect = cue.getBoundingClientRect();
          return {
            centered: Math.abs((readingRect.left + readingRect.right) - (baseRect.left + baseRect.right)) <= 2,
            aboveBase: readingRect.bottom <= baseRect.top + 1,
            insideBackground: readingRect.top >= cueRect.top - 1 && readingRect.bottom <= cueRect.bottom + 1,
            coversKanjiOnly: rubyRect.width > 0 && rubyRect.width < wordRect.width
          };
        })()
      };
      document.querySelector(".subtitle-cue .subtitle-word--unknown").click();
      result.lookupOpened = {
        hidden: document.getElementById("word-lookup").hidden,
        title: document.getElementById("word-lookup-title").textContent
      };
      await new Promise(function (resolve) { setTimeout(resolve, 900); });
      return result;
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(playback.result.value, {
    advanced: true,
    wordCount: 3,
    knownWord: "日本語",
    unknownLabel: "Look up 勉強する",
    unclassifiedLabel: "Look up を",
    furigana: "べんきょう",
    sourceProtocol: "blob:",
    furiganaGeometry: {
      centered: true,
      aboveBase: true,
      insideBackground: true,
      coversKanjiOnly: true,
    },
    lookupOpened: { hidden: false, title: "勉強する" },
  });
  assert.deepEqual(await waitForPlayerWordLookup(devTools, playerSession.sessionId), {
    visible: true,
    panelHidden: true,
    term: "勉強する",
    meaning: "to study",
    frequencies: ["G 090", "N 190"],
    knownLabel: "Mark as known",
    mineLabel: "Mine this word",
  });
  await saveSmokeScreenshot(devTools, playerSession.sessionId, "player-popup");
  await devTools.send("Runtime.evaluate", {
    expression: `document.getElementById("word-lookup-known").click()`,
  }, playerSession.sessionId);
  const knownDeadline = Date.now() + 5000;
  while (goiFixture.known.length === 0 && Date.now() < knownDeadline) {
    await delay(50);
  }
  assert.deepEqual(goiFixture.known, ["勉強する"]);
  await waitForPlayerCoverage(devTools, playerSession.sessionId);

  const miningPanel = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      document.querySelector(".unknown-target").click();
      return {
        panelHidden: document.getElementById("workspace-panel").hidden,
        captureHidden: document.getElementById("capture-panel").hidden,
        target: document.getElementById("capture-target").value
      };
    })()`,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(miningPanel.result.value, {
    panelHidden: false,
    captureHidden: false,
    target: "勉強する",
  });
  assert.deepEqual(await waitForDictionaryLookup(devTools, playerSession.sessionId), {
    term: "勉強する",
    reading: "べんきょうする",
    meaning: "to study",
    frequencies: ["G 090", "N 190"],
  });
  const zoomed = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      const tab = await chrome.tabs.getCurrent();
      await chrome.tabs.setZoom(tab.id, 2);
      return chrome.tabs.getZoom(tab.id);
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.equal(zoomed.result.value, 2);
  const badgeLayout = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      document.getElementById("dictionary-lookup").scrollIntoView({block: "center"});
      return Array.from(document.querySelectorAll("#dictionary-lookup .goi-dictionary-frequency"), (badge) => {
        const rect = badge.getBoundingClientRect();
        return rect.width > 0 && rect.left >= 0 && rect.right <= innerWidth &&
          rect.top >= 0 && rect.bottom <= innerHeight && badge.scrollWidth <= badge.clientWidth;
      });
    })()`,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(badgeLayout.result.value, [true, true]);
  await saveSmokeScreenshot(devTools, playerSession.sessionId, "player-200-percent");
  await devTools.send("Runtime.evaluate", {
    expression: `chrome.tabs.getCurrent().then((tab) => chrome.tabs.setZoom(tab.id, 1))`,
    awaitPromise: true,
  }, playerSession.sessionId);
  await devTools.send("Runtime.evaluate", {
    expression: `document.getElementById("capture-form").requestSubmit()`,
  }, playerSession.sessionId);
  const captured = await waitForPlayerCapture(devTools, playerSession.sessionId, goiFixture);
  assert.equal(captured.status, "Sent to mining.");
  assert.equal(captured.capture.expression, "勉強する");
  assert.equal(captured.capture.context_text, "日本語を勉強する。");
  assert.equal(captured.capture.source_kind, "video");
  assert.equal(captured.capture.source_title, "local-smoke.webm");
  assert.equal(captured.capture.source_url, "");
  assert.equal(captured.capture.source_position_ms, 0);

  const hoverLookup = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      const enabled = document.getElementById("hover-lookup");
      enabled.checked = true;
      enabled.dispatchEvent(new Event("change", {bubbles: true}));
      const video = document.getElementById("video");
      video.currentTime = 0.1;
      await video.play();
      await new Promise(function (resolve) { setTimeout(resolve, 100); });
      video.pause();
      const word = document.querySelector("#subtitle-overlay .subtitle-word--unknown");
      if (!word) {
        return {visible: false, term: "missing overlay word"};
      }
      word.dispatchEvent(new PointerEvent("pointerenter"));
      await new Promise(function (resolve) { setTimeout(resolve, 220); });
      const result = {
        visible: !document.getElementById("word-lookup").hidden,
        term: document.querySelector("#word-lookup-content .goi-dictionary-term")?.textContent || ""
      };
      word.dispatchEvent(new PointerEvent("pointerleave"));
      return result;
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(hoverLookup.result.value, {
    visible: true,
    term: "勉強する",
  });

  const hoverPause = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      const video = document.getElementById("video");
      const hoverSetting = document.querySelector('input[name="pause-behavior"][value="on_hover"]');
      hoverSetting.checked = true;
      hoverSetting.dispatchEvent(new Event("change", {bubbles: true}));
      video.currentTime = 0.1;
      await video.play();
      await new Promise(function (resolve) { setTimeout(resolve, 100); });
      const cue = document.querySelector(".subtitle-cue");

      cue.dispatchEvent(new PointerEvent("pointerenter"));
      const pausedOnHover = video.paused;
      cue.dispatchEvent(new PointerEvent("pointerleave"));
      await new Promise(function (resolve) { setTimeout(resolve, 50); });
      const resumedAfterLeave = !video.paused;

      cue.dispatchEvent(new PointerEvent("pointerenter"));
      document.querySelector(".subtitle-word--unknown").click();
      cue.dispatchEvent(new PointerEvent("pointerleave"));
      const stayedPausedForLookup = video.paused;
      document.getElementById("video-stage").dispatchEvent(new PointerEvent("pointerdown", {bubbles: true}));
      await new Promise(function (resolve) { setTimeout(resolve, 50); });
      const resumedAfterDismissal = !video.paused;
      video.pause();
      return {pausedOnHover, resumedAfterLeave, stayedPausedForLookup, resumedAfterDismissal};
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.equal(Boolean(hoverPause.exceptionDetails), false, JSON.stringify(hoverPause.exceptionDetails));
  assert.deepEqual(hoverPause.result.value, {
    pausedOnHover: true,
    resumedAfterLeave: true,
    stayedPausedForLookup: true,
    resumedAfterDismissal: true,
  });

  await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const subtitles = new File(["not a subtitle file"], "broken.txt", {type: "text/plain"});
      const transfer = new DataTransfer();
      transfer.items.add(subtitles);
      const input = document.getElementById("subtitle-file");
      input.files = transfer.files;
      input.dispatchEvent(new Event("change", {bubbles: true}));
    })()`,
  }, playerSession.sessionId);
  const preservedSubtitle = await waitForInvalidSubtitlePreservation(
    devTools,
    playerSession.sessionId,
  );
  assert.deepEqual(preservedSubtitle, {
    error: "This file is not recognizable SRT, WebVTT, ASS, or SSA subtitles.",
    selectedTrack: "local-smoke.srt · 2 lines",
    trackCount: 2,
    lineCount: 2,
    offset: "0",
  });

  const changedTiming = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      document.getElementById("offset-later").click();
      return {
        offset: document.getElementById("offset-input").value,
        description: document.getElementById("offset-description").textContent
      };
    })()`,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.deepEqual(changedTiming.result.value, {
    offset: "250",
    description: "250 ms later",
  });

  const savedPlayback = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const key = "goiLocalPlayerMediaStateV1";
      const state = JSON.parse(localStorage.getItem(key) || "{}");
      const identity = Object.keys(state).find(function (candidate) {
        return candidate.startsWith("local-smoke.webm:");
      });
      if (!identity) return null;
      state[identity].playbackSeconds = 0.25;
      localStorage.setItem(key, JSON.stringify(state));
      return {
        identity,
        offset: state[identity].offsetMs,
        hadPlayback: Number.isFinite(state[identity].playbackSeconds)
      };
    })()`,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.equal(savedPlayback.result.value.offset, 250);
  assert.equal(savedPlayback.result.value.hadPlayback, true);
  await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const transfer = new DataTransfer();
      transfer.items.add(globalThis.goiSmokeVideoFile);
      const input = document.getElementById("video-file");
      input.files = transfer.files;
      input.dispatchEvent(new Event("change", {bubbles: true}));
    })()`,
  }, playerSession.sessionId);
  await waitForPlayerVideo(devTools, playerSession.sessionId);
  const resumedPlayback = await devTools.send("Runtime.evaluate", {
    expression: `({
      currentTime: document.getElementById("video").currentTime,
      offset: document.getElementById("offset-input").value
    })`,
    returnByValue: true,
  }, playerSession.sessionId);
  assert.equal(Math.abs(resumedPlayback.result.value.currentTime - 0.25) < 0.12, true);
  assert.equal(resumedPlayback.result.value.offset, "250");

  await exercisePlayerReliability(devTools, playerSession.sessionId, goiFixture);

  const enabledYouTubeFurigana = await devTools.send("Runtime.evaluate", {
    expression: `chrome.runtime.sendMessage({
      type: "goi.settings.patch",
      version: 1,
      patch: {furiganaEnabled: true}
    })`,
    awaitPromise: true,
    returnByValue: true,
  }, popupSessionID);
  assert.equal(enabledYouTubeFurigana.result.value.settings.furiganaEnabled, true);
}

async function exercisePlayerReliability(devTools, sessionID, fixture) {
  const selected = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      const video = document.getElementById("video");
      document.getElementById("panel-close").click();
      document.getElementById("offset-reset").click();
      const pause = document.querySelector('[name="pause-behavior"][value="on_selection"]');
      pause.checked = true;
      pause.dispatchEvent(new Event("change"));
      video.currentTime = 0.1;
      await video.play();
      await new Promise(resolve => setTimeout(resolve, 80));
      document.querySelector("#subtitle-overlay .subtitle-word--unknown").click();
      return {paused: video.paused, lookupVisible: !document.getElementById("word-lookup").hidden};
    })()`,
    awaitPromise: true, returnByValue: true,
  }, sessionID);
  assert.deepEqual(selected.result.value, { paused: true, lookupVisible: true });
  for (const type of ["keyDown", "keyUp"]) {
    await devTools.send("Input.dispatchKeyEvent", {
      type, key: "Escape", code: "Escape", windowsVirtualKeyCode: 27,
    }, sessionID);
  }
  const dismissed = await devTools.send("Runtime.evaluate", {
    expression: `({
      hidden: document.getElementById("word-lookup").hidden,
      focusRestored: document.activeElement.matches(".subtitle-word--unknown"),
      paused: document.getElementById("video").paused
    })`, returnByValue: true,
  }, sessionID);
  assert.deepEqual(dismissed.result.value, { hidden: true, focusRestored: true, paused: true });

  await clickPageElement(devTools, sessionID, "#fullscreen");
  const fullscreenMining = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      for (let attempt = 0; attempt < 100 && !document.fullscreenElement; attempt += 1) {
        await new Promise(resolve => setTimeout(resolve, 20));
      }
      document.querySelector("#subtitle-overlay .subtitle-word--unknown").click();
      document.getElementById("word-lookup-mine").click();
      return {
        fullscreen: document.fullscreenElement?.id,
        panelHidden: document.getElementById("workspace-panel").hidden,
        sending: document.getElementById("page-status").textContent.startsWith("Adding 「")
      };
    })()`, awaitPromise: true, returnByValue: true,
  }, sessionID);
  assert.deepEqual(fullscreenMining.result.value, { fullscreen: "viewer-shell", panelHidden: true, sending: true });
  await clickPageElement(devTools, sessionID, "#fullscreen");

  await devTools.send("Runtime.evaluate", {
    expression: `document.querySelector("#subtitle-overlay .subtitle-word--known").click()`,
  }, sessionID);
  await devTools.send("Emulation.setDeviceMetricsOverride", {
    width: 500, height: 420, deviceScaleFactor: 1, mobile: false,
  }, sessionID);
  const dictionaryBounds = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      for (let attempt = 0; attempt < 100; attempt += 1) {
        if (document.querySelector("#word-lookup-content .goi-dictionary-term")?.textContent === "日本語") break;
        await new Promise(resolve => setTimeout(resolve, 20));
      }
      const stage = document.getElementById("video-stage").getBoundingClientRect();
      const popup = document.getElementById("word-lookup").getBoundingClientRect();
      const body = document.getElementById("word-lookup-content");
      return {
        fits: popup.left >= stage.left && popup.right <= stage.right && popup.top >= stage.top && popup.bottom <= stage.bottom,
        scrollable: body.scrollHeight > body.clientHeight && getComputedStyle(body).overflowY === "auto"
      };
    })()`, awaitPromise: true, returnByValue: true,
  }, sessionID);
  assert.deepEqual(dictionaryBounds.result.value, { fits: true, scrollable: true });
  await saveSmokeScreenshot(devTools, sessionID, "player-bounded-dictionary");
  await devTools.send("Emulation.clearDeviceMetricsOverride", undefined, sessionID);

  const captureCount = fixture.captures.length;
  const reliability = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      const video = document.getElementById("video");
      const byID = id => document.getElementById(id);
      async function waitFor(predicate, description) {
        for (let attempt = 0; attempt < 150; attempt += 1) {
          if (predicate()) return;
          await new Promise(resolve => setTimeout(resolve, 20));
        }
        throw new Error(description);
      }
      function chooseFile(id, file) {
        const transfer = new DataTransfer();
        transfer.items.add(file);
        byID(id).files = transfer.files;
        byID(id).dispatchEvent(new Event("change", {bubbles: true}));
      }
      byID("word-lookup-close").click();
      document.querySelector("#subtitle-overlay .subtitle-word--unknown").click();
      const sendMessage = chrome.runtime.sendMessage;
      let finishKnown;
      chrome.runtime.sendMessage = function (message, ...args) {
        if (message.type === "goi.vocabulary.known") return new Promise(resolve => { finishKnown = resolve; });
        return sendMessage.call(chrome.runtime, message, ...args);
      };
      byID("word-lookup-known").click();
      chrome.runtime.sendMessage = sendMessage;
      chooseFile("subtitle-file", new File([
        "1\\n00:00:00,000 --> 00:00:00,700\\n日本語を勉強する。\\n\\n" +
        "2\\n00:00:00,700 --> 00:00:01,500\\n猫が好きです。\\n"
      ], "reliability.srt", {type: "application/x-subrip"}));
      await waitFor(() => byID("subtitle-track").selectedOptions[0].textContent.startsWith("reliability.srt") &&
        byID("coverage-summary").textContent.includes("2/2 checked"), "new track coverage");
      const trackChangeClosedLookup = byID("word-lookup").hidden;
      document.querySelector("#subtitle-overlay .subtitle-word--unknown").click();
      finishKnown({ok: true, result: {state: "marked_known"}});
      await new Promise(resolve => setTimeout(resolve, 20));
      const lateKnownPreservedNewLookup = !byID("word-lookup").hidden;
      byID("word-lookup-close").click();
      Object.defineProperty(video, "duration", {configurable: true, value: Infinity});
      video.dispatchEvent(new Event("durationchange"));
      const unknownDurationReady = !document.querySelectorAll(".line-time")[1].disabled;
      document.querySelectorAll(".line-time")[1].click();
      await waitFor(() => !video.seeking, "unknown-duration seek");
      const unknownDurationSeek = Math.abs(video.currentTime - 0.7) < 0.05;
      delete video.duration;
      video.dispatchEvent(new Event("durationchange"));

      document.querySelector(".unknown-target").click();
      await waitFor(() => byID("dictionary-lookup").querySelector(".goi-dictionary-select"), "selected entry");
      byID("dictionary-lookup").querySelector(".goi-dictionary-select").click();
      byID("capture-sentence").setSelectionRange(0, 3);
      byID("capture-sentence").dispatchEvent(new MouseEvent("mouseup"));
      const changedTarget = byID("capture-target").value;
      byID("capture-form").requestSubmit();
      await waitFor(() => byID("capture-status").textContent === "Sent to mining.", "changed target capture");

      document.querySelector(".unknown-target").click();
      await waitFor(() => byID("dictionary-lookup").querySelector(".goi-dictionary-select"), "batch source entry");
      byID("dictionary-lookup").querySelector(".goi-dictionary-select").click();
      byID("batch-one-target").click();
      const batchCopy = byID("batch-confirmation-text").textContent;
      byID("batch-confirm").click();
      await waitFor(() => byID("capture-status").textContent === "1 sent", "batch capture");

      byID("panel-close").click();
      video.currentTime = 0.1;
      await waitFor(() => !video.seeking && document.querySelector("#subtitle-overlay .subtitle-word"), "subtitle before failure");
      document.querySelector("#subtitle-overlay .subtitle-word").click();
      chooseFile("video-file", new File(["not a playable video"], "broken.webm", {type: "video/webm"}));
      await waitFor(() => video.error && byID("video-status").textContent.includes("cannot play"), "unsupported video feedback");
      const failure = {
        lookupClosed: byID("word-lookup").hidden,
        subtitlesCleared: byID("subtitle-overlay").childElementCount === 0,
        tracksPreserved: byID("subtitle-track").options.length === 3,
        captureDisabled: byID("capture-submit").disabled,
        fullscreenDisabled: byID("fullscreen").disabled
      };
      const dropped = new DataTransfer();
      dropped.items.add(new File([globalThis.goiSmokeVideoFile], "dropped.webm", {type: ""}));
      byID("video-stage").dispatchEvent(new DragEvent("drop", {bubbles: true, dataTransfer: dropped}));
      await waitFor(() => video.readyState >= 2 && !video.error && !byID("fullscreen").disabled, "video recovery");
      video.currentTime = 0.1;
      await video.play();
      await new Promise(resolve => setTimeout(resolve, 80));
      const recovered = !video.paused && video.currentTime > 0.1 &&
        byID("video-name").textContent === "dropped.webm" && byID("video-empty").getClientRects().length === 0;
      video.pause();
      return {trackChangeClosedLookup, lateKnownPreservedNewLookup, unknownDurationReady, unknownDurationSeek, changedTarget, batchCopy, failure, recovered};
    })()`, awaitPromise: true, returnByValue: true,
  }, sessionID);
  assert.equal(Boolean(reliability.exceptionDetails), false, JSON.stringify(reliability.exceptionDetails));
  assert.deepEqual(reliability.result.value, {
    trackChangeClosedLookup: true,
    lateKnownPreservedNewLookup: true,
    unknownDurationReady: true,
    unknownDurationSeek: true,
    changedTarget: "日本語",
    batchCopy: "Send 1 line to mining? Saves text, filename, and timestamp; no audio or video clips.",
    failure: { lookupClosed: true, subtitlesCleared: true, tracksPreserved: true, captureDisabled: true, fullscreenDisabled: true },
    recovered: true,
  });
  const captures = fixture.captures.slice(captureCount);
  assert.deepEqual(captures.map(capture => capture.expression), ["日本語", "猫"]);
  assert.equal(captures.every(capture => capture.suggested_entry_sequence == null), true,
    "A new target or batch line must not inherit another word's selected dictionary entry");

  await devTools.send("Emulation.setEmulatedMedia", {
    features: [{ name: "prefers-color-scheme", value: "light" }],
  }, sessionID);
  const lightTheme = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      document.querySelector(".unknown-target").click();
      for (let attempt = 0; attempt < 100 && !document.querySelector("#dictionary-lookup .goi-dictionary-term"); attempt += 1) {
        await new Promise(resolve => setTimeout(resolve, 20));
      }
      function luminance(color) {
        const values = color.match(/[\\d.]+/g).slice(0, 3).map(Number).map(value => {
          const channel = value / 255;
          return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4;
        });
        return values[0] * 0.2126 + values[1] * 0.7152 + values[2] * 0.0722;
      }
      function readable(text, surface) {
        const foreground = luminance(getComputedStyle(text).color);
        const background = luminance(getComputedStyle(surface).backgroundColor);
        return (Math.max(foreground, background) + 0.05) / (Math.min(foreground, background) + 0.05) >= 4.5;
      }
      return {
        label: readable(document.querySelector(".capture-card label"), document.getElementById("capture-panel")),
        definition: readable(document.querySelector("#dictionary-lookup .goi-dictionary-term"), document.getElementById("dictionary-lookup")),
        transcript: readable(document.querySelector(".line-text"), document.querySelector(".transcript-line.selected"))
      };
    })()`, awaitPromise: true, returnByValue: true,
  }, sessionID);
  assert.deepEqual(lightTheme.result.value, { label: true, definition: true, transcript: true });
  await saveSmokeScreenshot(devTools, sessionID, "player-light-mining");
}

async function prepareYouTube(devTools) {
  const youtubeTarget = await devTools.send("Target.createTarget", { url: "about:blank" });
  const youtubeSession = await devTools.send("Target.attachToTarget", {
    targetId: youtubeTarget.targetId,
    flatten: true,
  });
  await devTools.send("Page.enable", undefined, youtubeSession.sessionId);
  await devTools.send("Runtime.enable", undefined, youtubeSession.sessionId);
  await devTools.send("Fetch.enable", {
    patterns: [
      { urlPattern: "https://www.youtube.com/watch*", requestStage: "Request" },
      { urlPattern: "https://www.youtube.com/youtubei/v1/get_transcript*", requestStage: "Request" },
      { urlPattern: "https://www.youtube.com/api/timedtext*", requestStage: "Request" },
    ],
  }, youtubeSession.sessionId);
  const fixtureBody = Buffer.from(`<!doctype html>
    <html><head><title>Goi YouTube smoke</title><style>
      .html5-video-player { position: relative; width: 800px; height: 450px; }
      .caption-window { width: 600px; height: 50px; }
    </style><script>
      globalThis.ytInitialPlayerResponse = {
        videoDetails: {videoId: "goi-extension-smoke"},
        captions: {playerCaptionsTracklistRenderer: {captionTracks: [{
          languageCode: "ja",
          name: {simpleText: "japonų"},
          baseUrl: "https://www.youtube.com/api/timedtext?v=goi-extension-smoke&lang=ja"
        }]}}
      };
      globalThis.ytInitialData = {
        engagementPanels: [{getTranscriptEndpoint: {params: "initial-transcript"}}]
      };
      globalThis.ytcfg = {get: function (name) {
        return {
          INNERTUBE_API_KEY: "fixture-key",
          INNERTUBE_CONTEXT: {client: {clientVersion: "fixture-version"}},
          INNERTUBE_CONTEXT_CLIENT_NAME: 1
        }[name];
      }};
    </script></head><body>
      <div class="html5-video-player" id="movie_player">
        <video></video>
        <div class="caption-window"><span class="ytp-caption-segment">日本語を勉強する。</span></div>
      </div>
      <script>
        document.getElementById("movie_player").getPlayerResponse = function () {
          return {
            videoDetails: {videoId: "goi-preroll-ad"},
            captions: {playerCaptionsTracklistRenderer: {captionTracks: [{
              languageCode: "en",
              name: {simpleText: "English"},
              baseUrl: "https://www.youtube.com/api/timedtext?v=goi-preroll-ad&lang=en"
            }]}}
          };
        };
      </script>
    </body></html>`).toString("base64");
  let transcriptPanelRequests = 0;
  let timedTextRequests = 0;
  const removeFixtureHandler = devTools.on("Fetch.requestPaused", function (params, sessionID) {
    if (sessionID !== youtubeSession.sessionId) {
      return;
    }
    const timedTextRequest = params.request.url.startsWith("https://www.youtube.com/api/timedtext");
    const transcriptPanelRequest = params.request.url.startsWith("https://www.youtube.com/youtubei/v1/get_transcript");
    if (timedTextRequest) {
      timedTextRequests += 1;
    }
    if (transcriptPanelRequest) {
      transcriptPanelRequests += 1;
    }
    const requestBody = String(params.request.postData || "");
    const responseBody = transcriptPanelRequest && requestBody.includes('"continuation":"japanese"')
      ? {actions: [{updateEngagementPanelAction: {content: {
        transcriptRenderer: {content: {transcriptSearchPanelRenderer: {body: {
          transcriptSegmentListRenderer: {initialSegments: [
            {transcriptSegmentRenderer: {
              startMs: "1000",
              endMs: "2500",
              snippet: {runs: [{text: "日本語を勉強する。"}]}
            }},
            {transcriptSegmentRenderer: {
              startMs: "5000",
              endMs: "6500",
              snippet: {runs: [{text: "猫が好きです。"}]}
            }}
          ]}
        }}}}
      }}}]}
      : transcriptPanelRequest
        ? {actions: [{updateEngagementPanelAction: {content: {
          transcriptRenderer: {content: {transcriptSearchPanelRenderer: {
            footer: {transcriptFooterRenderer: {languageMenu: {
              sortFilterSubMenuRenderer: {subMenuItems: [{
                title: "japonų",
                continuation: {reloadContinuationData: {continuation: "japanese"}}
              }]}
            }}}
          }}}
        }}}]}
        : {events: [
        {tStartMs: 1000, dDurationMs: 1500, segs: [{utf8: "日本語を勉強する。"}]},
        {tStartMs: 5000, dDurationMs: 1500, segs: [{utf8: "猫が好きです。"}]}
      ]};
    const body = transcriptPanelRequest || timedTextRequest
      ? Buffer.from(JSON.stringify(responseBody)).toString("base64")
      : fixtureBody;
    devTools.send("Fetch.fulfillRequest", {
      requestId: params.requestId,
      responseCode: 200,
      responseHeaders: [{
        name: "Content-Type",
        value: transcriptPanelRequest || timedTextRequest
          ? "application/json; charset=utf-8"
          : "text/html; charset=utf-8"
      }],
      body,
    }, sessionID).catch(function ignoreClosedFixtureRequest() {});
  });
  await devTools.send("Page.navigate", {
    url: "https://www.youtube.com/watch?v=goi-extension-smoke",
  }, youtubeSession.sessionId);
  await waitForDocumentReady(devTools, youtubeSession.sessionId);
  return {
    youtubeTarget, youtubeSession, removeFixtureHandler,
    get transcriptPanelRequests() { return transcriptPanelRequests; },
    get timedTextRequests() { return timedTextRequests; },
  };
}

async function exerciseYouTube(devTools, extensionID, popupSessionID, goiFixture, fixture) {
  const { youtubeTarget, youtubeSession, removeFixtureHandler } = fixture;
  const beforeRecovery = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      const tabs = await chrome.tabs.query({url: "https://www.youtube.com/watch?v=goi-extension-smoke"});
      const results = await chrome.scripting.executeScript({
        target: {tabId: tabs[0].id, frameIds: [0]},
        func: function () { return Boolean(globalThis.GoiYouTubeOverlay); }
      });
      return results[0].result;
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, popupSessionID);
  assert.equal(beforeRecovery.result.value, false, "The pre-install tab must exercise recovery injection");
  await devTools.send("Target.activateTarget", { targetId: youtubeTarget.targetId });
  const youtubeOverlay = await waitForYouTubeOverlay(devTools, youtubeSession.sessionId);
  removeFixtureHandler();
  assert.deepEqual(youtubeOverlay, {
    wordLabel: "Look up 勉強する",
    unclassifiedLabel: "Look up を",
    furigana: "べんきょう",
    overlayCount: 1,
    playerSelected: true,
    coverage: "Goi · 50% known · full transcript",
    furiganaGeometry: {
      centered: true,
      aboveBase: true,
      insideBackground: true,
      coversKanjiOnly: true,
    },
  });
  assert.equal(fixture.transcriptPanelRequests, 2);
  assert.equal(fixture.timedTextRequests, 0);

  const youtubeWord = await inspectAndCaptureYouTubeWord(devTools, youtubeSession.sessionId, goiFixture);
  assert.deepEqual(youtubeWord, {
    lookup: "べんきょうする · to study",
    frequencies: ["G 090", "N 190"],
    saved: true,
    rawText: "勉強する",
    contextText: "日本語を勉強する。",
  });

  await devTools.send("Target.activateTarget", { targetId: youtubeTarget.targetId });
  await devTools.send("Runtime.evaluate", {
    expression: `document.querySelector('[aria-label="Open transcript browser"]').click()`,
  }, youtubeSession.sessionId);
  const companionTarget = await waitForCompanionTarget(devTools, extensionID);
  const companionSession = await devTools.send("Target.attachToTarget", {
    targetId: companionTarget.targetId,
    flatten: true,
  });
  const companion = await waitForCompanion(devTools, companionSession.sessionId);
  assert.deepEqual(companion, {
    ready: "complete",
    heading: "Transcript and mining",
    caption: "日本語を勉強する。",
    secondCaption: "猫が好きです。",
    lineCount: "2 subtitle lines · 50% known",
    displayModes: 4,
    verticalPosition: "78",
    backgroundOpacity: "0.65",
    batchLabel: "Send 2 lines to mining",
    furiganaGeometry: {
      centered: true,
      aboveBase: true,
      coversKanjiOnly: true,
    },
  });
  await devTools.send("Runtime.evaluate", {
    expression: `document.querySelector(".subtitle-text").click()`,
  }, companionSession.sessionId);
  assert.deepEqual(await waitForDictionaryLookup(devTools, companionSession.sessionId), {
    term: "勉強する",
    reading: "べんきょうする",
    meaning: "to study",
    frequencies: ["G 090", "N 190"],
  });
  await saveSmokeScreenshot(devTools, companionSession.sessionId, "companion");
  await devTools.send("Target.closeTarget", { targetId: companionTarget.targetId });
}

async function exerciseReaderPage(devTools, extensionID, popupSessionID, goiFixture) {
  const readerTarget = await devTools.send("Target.createTarget", {
    url: goiFixture.baseURL + "/reader-smoke",
  });
  const readerSession = await devTools.send("Target.attachToTarget", {
    targetId: readerTarget.targetId,
    flatten: true,
  });
  await devTools.send("Page.enable", undefined, readerSession.sessionId);
  await devTools.send("Runtime.enable", undefined, readerSession.sessionId);
  await devTools.send("Emulation.setEmulatedMedia", {
    features: [{ name: "prefers-color-scheme", value: "light" }],
  }, readerSession.sessionId);
  await waitForDocumentReady(devTools, readerSession.sessionId);
  await devTools.send("Target.activateTarget", { targetId: readerTarget.targetId });
  const analyzed = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      const tabs = await chrome.tabs.query({url: ${JSON.stringify(goiFixture.baseURL + "/reader-smoke")}});
      await chrome.windows.update(tabs[0].windowId, {focused: true});
      await chrome.tabs.update(tabs[0].id, {active: true});
      return chrome.runtime.sendMessage({type: "goi.coverage.analyze-page", version: 1});
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, popupSessionID);
  assert.equal(analyzed.result.value.ok, true, JSON.stringify(analyzed.result.value));
  const readerCapture = await inspectAndCaptureAnalyzedPage(
    devTools,
    readerSession.sessionId,
    goiFixture,
  );
  assert.deepEqual(readerCapture, {
    summary: "2 / 4 words · 2 unique unknown words",
    term: "勉強する",
    reading: "べんきょうする",
    meaning: "to study",
    compact: true,
    frequencies: ["G 090", "N 190"],
    readablePopup: true,
    rawText: "勉強する",
    contextText: "日本語を勉強する。",
    sourceKind: "web",
    entrySequence: 12345,
  });
  await clickPageElement(devTools, readerSession.sessionId, '[aria-label="Show reading controls"]');
  await clickPageElement(devTools, readerSession.sessionId, '[aria-label="Move Goi coverage panel"]');
  const moved = await devTools.send("Runtime.evaluate", {
    expression: `document.getElementById("goi-ext-reader").shadowRoot.querySelector('[data-goi-corner]').dataset.goiCorner`,
    returnByValue: true,
  }, readerSession.sessionId);
  assert.equal(moved.result.value, "bottom-left");
  await exerciseReaderUsability(devTools, readerSession.sessionId, goiFixture);
  const storedPreference = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      const key = "goiCoveragePanel:" + ${JSON.stringify(goiFixture.baseURL)};
      for (let attempt = 0; attempt < 50; attempt += 1) {
        const value = (await chrome.storage.local.get(key))[key];
        if (value?.corner === "bottom-left") return value;
        await new Promise(resolve => setTimeout(resolve, 20));
      }
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, popupSessionID);
  assert.equal(storedPreference.result.value.corner, "bottom-left");
  await devTools.send("Page.reload", undefined, readerSession.sessionId);
  await waitForDocumentReady(devTools, readerSession.sessionId);
  const reopened = await devTools.send("Runtime.evaluate", {
    expression: `chrome.runtime.sendMessage({type: "goi.coverage.analyze-page", version: 1})`,
    awaitPromise: true,
    returnByValue: true,
  }, popupSessionID);
  assert.equal(reopened.result.value.ok, true);
  const restored = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      for (let attempt = 0; attempt < 50; attempt += 1) {
        const panel = document.getElementById("goi-ext-reader")?.shadowRoot.querySelector('[data-goi-corner]');
        if (panel?.dataset.goiCorner === "bottom-left") return true;
        await new Promise(resolve => setTimeout(resolve, 20));
      }
      return false;
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, readerSession.sessionId);
  assert.equal(restored.result.value, true, "Reader placement must survive page reload with trusted storage");

}

function findChromeExecutable() {
  const candidates = [
    process.env.CHROME_BIN,
    "/Applications/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
    "/usr/bin/google-chrome-for-testing",
    "/usr/bin/google-chrome",
    "/usr/bin/google-chrome-stable",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
  ];
  return candidates.find(function (candidate) {
    return candidate && fs.existsSync(candidate);
  });
}

async function saveSmokeScreenshot(devTools, sessionID, name) {
  const directory = process.env.GOI_SMOKE_SCREENSHOT_DIR;
  if (!directory) return;
  const screenshot = await devTools.send("Page.captureScreenshot", { format: "png" }, sessionID);
  fs.mkdirSync(directory, { recursive: true });
  fs.writeFileSync(path.join(directory, name + ".png"), Buffer.from(screenshot.data, "base64"));
}

function startGoiFixture() {
  const captures = [];
  const known = [];
  let nextCaptureID = 100;
  const server = http.createServer(function (request, response) {
    if (request.method === "GET" && request.url === "/reader-smoke") {
      response.writeHead(200, { "Content-Type": "text/html; charset=utf-8" });
      response.end(`<!doctype html><html lang="ja"><head><title>Goi reader smoke</title>
        <style>
          body { margin: 24px; font: 24px/1.8 sans-serif; }
          main { min-height: 1600px; }
          h1 { font-size: 24px; }
          button { width: 700px !important; font: 40px/3 serif !important; }
          section, article { position: fixed !important; width: 1200px !important; padding: 90px !important; }
          ol, li, strong { margin: 80px !important; font-size: 60px !important; }
          #edge-reading { position: fixed; right: 20px; bottom: 20px; margin: 0; }
        </style></head><body><main><h1>Japanese reading</h1>
          <p id="reading">日本語を勉強する。</p>
          <p id="adjacent-reading">日本語勉強する。</p>
          <p id="ruby-reading">日本語を<ruby><span id="ruby-base">勉強</span><rt>べんきょう</rt></ruby><span>する</span>。</p>
          <p id="edge-reading">日本語を勉強する。</p>
          <p id="offscreen-reading" style="margin-top: 1000px">日本語を勉強する。</p>
        </main></body></html>`);
      return;
    }
    if (request.headers.authorization !== "Bearer chrome-smoke-token") {
      writeJSON(response, 401, { code: "unauthorized" });
      return;
    }
    readRequestBody(request).then(function (body) {
      if (request.method === "GET" && request.url === "/api/extension/v1/status") {
        writeJSON(response, 200, { ok: true });
        return;
      }
      if (request.method === "GET" && request.url.startsWith("/api/extension/v1/dictionary?")) {
        const expression = new URL(request.url, "http://127.0.0.1").searchParams.get("expression") || "";
        if (expression === "日本語") {
          writeJSON(response, 200, {
            query: expression,
            state: "ready",
            candidates: [{
              written: "日本語",
              entry_sequence: 22222,
              reading: "にほんご",
              meanings: ["Japanese language"],
              senses: Array.from({ length: 16 }, (_, index) => ({
                parts_of_speech: ["noun"],
                meanings: [index === 0 ? "Japanese language" : "A longer dictionary explanation for sense " + (index + 1)],
              })),
            }],
          });
          return;
        }
        writeJSON(response, 200, {
          query: expression,
          state: "ready",
          candidates: [{
            written: "勉強する",
            entry_sequence: 12345,
            reading: "べんきょうする",
            global_rank: 90,
            novel_rank: 190,
            meanings: ["to study"],
            senses: [{ parts_of_speech: ["verb"], meanings: ["to study"] }],
          }],
        });
        return;
      }
      if (request.method === "POST" && request.url === "/api/extension/v1/coverage") {
        const submitted = JSON.parse(body.toString("utf8"));
        const blocks = submitted.blocks.map(function (block) {
          if (block.text === "日本語を勉強する。") {
            return {
              id: block.id,
              tokens: [
                token("日本語", "日本語", "にほんご", 0, 3, "known"),
                token("勉強する", "勉強する", "べんきょうする", 4, 8, "unknown"),
              ],
            };
          }
          if (block.text === "猫が好きです。") {
            return {
              id: block.id,
              tokens: [
                token("猫", "猫", "ねこ", 0, 1, "unknown"),
                token("好き", "好き", "すき", 2, 4, "known"),
              ],
            };
          }
          if (block.text === "日本語勉強する。") {
            return {
              id: block.id,
              tokens: [
                token("日本語", "日本語", "にほんご", 0, 3, "known"),
                token("勉強する", "勉強する", "べんきょうする", 3, 7, "unknown"),
              ],
            };
          }
          return { id: block.id, tokens: [] };
        });
        writeJSON(response, 200, {
          summary: {
            known_occurrences: 2,
            total_occurrences: 4,
            unknown_unique: 2,
            excluded_names: 0,
          },
          blocks,
        });
        return;
      }
      if (request.method === "POST" && request.url === "/api/extension/v1/known") {
        const submitted = JSON.parse(body.toString("utf8"));
        known.push(submitted.expression);
        writeJSON(response, 200, { state: "marked_known" });
        return;
      }
      if (request.method === "POST" && request.url === "/api/extension/v1/captures") {
        const capture = JSON.parse(body.toString("utf8"));
        captures.push(capture);
        const id = nextCaptureID++;
        writeJSON(response, 201, {
          id,
          revision: 1,
          status: "pending",
          replayed: false,
          review_url: `/mining/captures/${id}`,
        });
        return;
      }
      writeJSON(response, 404, { code: "not_found" });
    }).catch(function (error) {
      writeJSON(response, 500, { code: "fixture_error", message: error.message });
    });
  });
  return new Promise(function (resolve, reject) {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", function () {
      server.removeListener("error", reject);
      const address = server.address();
      resolve({
        baseURL: `http://127.0.0.1:${address.port}`,
        captures,
        known,
        close() {
          return new Promise(function (closeResolve, closeReject) {
            server.close(function (error) {
              if (error) closeReject(error);
              else closeResolve();
            });
          });
        },
      });
    });
  });
}

function token(surface, expression, reading, start, end, status) {
  return {
    surface,
    expression,
    reading,
    start_utf16: start,
    end_utf16: end,
    status,
  };
}

function readRequestBody(request) {
  return new Promise(function (resolve, reject) {
    const chunks = [];
    request.on("data", function (chunk) { chunks.push(chunk); });
    request.on("end", function () { resolve(Buffer.concat(chunks)); });
    request.on("error", reject);
  });
}

function writeJSON(response, status, body) {
  const encoded = JSON.stringify(body);
  response.writeHead(status, {
    "Content-Type": "application/json",
    "Content-Length": Buffer.byteLength(encoded),
  });
  response.end(encoded);
}

function materializeEmbeddedExtension(repositoryDirectory, outputDirectory, smokeDirectory) {
  const listed = spawnSync("go", ["list", "-json", "./browser-extension"], {
    cwd: repositoryDirectory,
    encoding: "utf8",
    env: {
      ...process.env,
      GOCACHE: path.join(smokeDirectory, "go-cache"),
    },
  });
  assert.equal(listed.status, 0, listed.stderr || listed.error?.message);
  const packageInfo = JSON.parse(listed.stdout);
  assert.ok(Array.isArray(packageInfo.EmbedFiles) && packageInfo.EmbedFiles.length > 0);
  packageInfo.EmbedFiles.forEach(function (relativePath) {
    const source = path.resolve(packageInfo.Dir, relativePath);
    const destination = path.resolve(outputDirectory, relativePath);
    assert.equal(destination.startsWith(outputDirectory + path.sep), true, "unsafe embedded path");
    fs.mkdirSync(path.dirname(destination), { recursive: true });
    fs.copyFileSync(source, destination);
  });
}

function connectDevToolsPipe(chrome, errorOutput) {
  const writer = chrome.stdio[3];
  const reader = chrome.stdio[4];
  const pending = new Map();
  const listeners = new Map();
  let nextID = 0;
  let buffer = "";
  let closed = false;

  reader.setEncoding("utf8");
  reader.on("data", function (chunk) {
    buffer += chunk;
    let separator = buffer.indexOf("\0");
    while (separator !== -1) {
      const rawMessage = buffer.slice(0, separator);
      buffer = buffer.slice(separator + 1);
      if (rawMessage) {
        receive(JSON.parse(rawMessage));
      }
      separator = buffer.indexOf("\0");
    }
  });
  reader.on("error", failAll);
  writer.on("error", failAll);
  chrome.on("error", failAll);
  chrome.on("exit", function (code, signal) {
    if (!closed) {
      failAll(new Error(
        `Chrome exited before the test completed (${code ?? signal}).\n${errorOutput()}`,
      ));
    }
  });

  function receive(message) {
    if (!message.id) {
      const handlers = listeners.get(message.method);
      if (handlers) {
        handlers.forEach(function (handler) {
          handler(message.params || {}, message.sessionId || "");
        });
      }
      return;
    }
    if (!pending.has(message.id)) {
      return;
    }
    const request = pending.get(message.id);
    pending.delete(message.id);
    clearTimeout(request.timer);
    if (message.error) {
      request.reject(new Error(`${request.method}: ${message.error.message}`));
      return;
    }
    request.resolve(message.result || {});
  }

  function failAll(error) {
    for (const request of pending.values()) {
      clearTimeout(request.timer);
      request.reject(error);
    }
    pending.clear();
  }

  return {
    send(method, params, sessionID) {
      if (closed) {
        return Promise.reject(new Error("DevTools pipe is closed"));
      }
      const id = ++nextID;
      const message = { id, method };
      if (params) {
        message.params = params;
      }
      if (sessionID) {
        message.sessionId = sessionID;
      }
      return new Promise(function (resolve, reject) {
        const timer = setTimeout(function () {
          pending.delete(id);
          const detail = method === "Runtime.evaluate" && params && typeof params.expression === "string"
            ? " (" + params.expression.replace(/\s+/gu, " ").slice(0, 120) + ")"
            : "";
          reject(new Error(`${method}: Chrome did not respond${detail}`));
        }, 10000);
        pending.set(id, { method, resolve, reject, timer });
        writer.write(`${JSON.stringify(message)}\0`);
      });
    },
    close() {
      closed = true;
      failAll(new Error("DevTools pipe closed"));
      if (!writer.destroyed) {
        writer.end();
      }
    },
    on(method, handler) {
      const handlers = listeners.get(method) || new Set();
      handlers.add(handler);
      listeners.set(method, handlers);
      return function () {
        handlers.delete(handler);
        if (handlers.size === 0) {
          listeners.delete(method);
        }
      };
    },
  };
}

async function waitForPopup(devTools, sessionID) {
  const deadline = Date.now() + 10000;
  const expression = `({
    ready: document.readyState,
    title: document.title,
    heading: document.querySelector("h1")?.textContent || "",
    extensionID: chrome.runtime.id,
    manifestName: chrome.runtime.getManifest().name,
    manifestVersion: chrome.runtime.getManifest().version,
    manifestIcon: chrome.runtime.getManifest().icons?.["128"] || "",
    analyzeDisabled: document.getElementById("analyze-page")?.disabled || false,
    analyzeText: document.getElementById("analyze-page")?.textContent || "",
    statusError: document.getElementById("status")?.classList.contains("error") || false
  })`;
  while (Date.now() < deadline) {
    try {
      const evaluated = await devTools.send("Runtime.evaluate", {
        expression,
        returnByValue: true,
      }, sessionID);
      const value = evaluated.result && evaluated.result.value;
      if (value &&
          value.ready === "complete" &&
          value.analyzeDisabled &&
          value.analyzeText === "This page can’t be analyzed") {
        return value;
      }
    } catch (_error) {
      // Opening the popup can replace its execution context.
    }
    await delay(50);
  }
  throw new Error("The installed extension popup did not load");
}

async function waitForYouTubeOverlay(devTools, sessionID) {
  const deadline = Date.now() + 10000;
  const expression = `(function () {
    const word = document.querySelector(".goi-ext-caption-word--unknown");
    const ruby = word?.querySelector("ruby");
    const reading = ruby?.querySelector("rt");
    const caption = word?.closest(".goi-ext-caption-text");
    let furiganaGeometry = null;
    if (word && ruby && reading && caption && ruby.firstChild) {
      const base = document.createRange();
      base.selectNode(ruby.firstChild);
      const wordRect = word.getBoundingClientRect();
      const rubyRect = ruby.getBoundingClientRect();
      const readingRect = reading.getBoundingClientRect();
      const baseRect = base.getBoundingClientRect();
      const captionRect = caption.getBoundingClientRect();
      furiganaGeometry = {
        centered: Math.abs((readingRect.left + readingRect.right) - (baseRect.left + baseRect.right)) <= 2,
        aboveBase: readingRect.bottom <= baseRect.top + 1,
        insideBackground: readingRect.top >= captionRect.top - 1 && readingRect.bottom <= captionRect.bottom + 1,
        coversKanjiOnly: rubyRect.width > 0 && rubyRect.width < wordRect.width
      };
    }
    return {
      wordLabel: word?.getAttribute("aria-label") || "",
      unclassifiedLabel: document.querySelector(".goi-ext-caption-word--unclassified")?.getAttribute("aria-label") || "",
      furigana: reading?.textContent || "",
      overlayCount: document.querySelectorAll(".goi-ext-overlay").length,
      playerSelected: Boolean(document.querySelector(".html5-video-player > .goi-ext-overlay")),
      coverage: document.querySelector(".goi-ext-caption-coverage")?.textContent || "",
      furiganaGeometry
    };
  })()`;
  while (Date.now() < deadline) {
    try {
      const evaluated = await devTools.send("Runtime.evaluate", {
        expression,
        returnByValue: true,
      }, sessionID);
      const value = evaluated.result && evaluated.result.value;
      if (value && value.wordLabel === "Look up 勉強する" && value.unclassifiedLabel === "Look up を" &&
          value.furigana === "べんきょう" &&
          value.coverage === "Goi · 50% known · full transcript") {
        return value;
      }
    } catch (_error) {
      // Navigation can briefly replace the page context.
    }
    await delay(50);
  }
  throw new Error("The installed extension did not run on the YouTube fixture");
}

async function waitForDictionaryLookup(devTools, sessionID) {
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    const evaluated = await devTools.send("Runtime.evaluate", {
      expression: `({
        term: document.querySelector("#dictionary-lookup .goi-dictionary-term")?.textContent || "",
        reading: document.querySelector("#dictionary-lookup .goi-dictionary-reading")?.textContent || "",
        meaning: document.querySelector("#dictionary-lookup .goi-dictionary-meanings li")?.textContent || "",
        frequencies: Array.from(document.querySelectorAll("#dictionary-lookup .goi-dictionary-frequency"), (node) => node.textContent)
      })`,
      returnByValue: true,
    }, sessionID);
    const value = evaluated.result && evaluated.result.value;
    if (value && value.term === "勉強する" && value.meaning === "to study") {
      return value;
    }
    await delay(50);
  }
  throw new Error("The local player did not show the dictionary result");
}

async function waitForPlayerWordLookup(devTools, sessionID) {
  const deadline = Date.now() + 5000;
  let lastValue;
  while (Date.now() < deadline) {
    const evaluated = await devTools.send("Runtime.evaluate", {
      expression: `({
        visible: !document.getElementById("word-lookup").hidden,
        panelHidden: document.getElementById("workspace-panel").hidden,
        term: document.querySelector("#word-lookup .goi-dictionary-term")?.textContent || "",
        meaning: document.querySelector("#word-lookup .goi-dictionary-meanings li")?.textContent || "",
        frequencies: Array.from(document.querySelectorAll("#word-lookup .goi-dictionary-frequency"), (node) => node.textContent),
        knownLabel: document.getElementById("word-lookup-known").textContent,
        mineLabel: document.getElementById("word-lookup-mine").textContent
      })`,
      returnByValue: true,
    }, sessionID);
    const value = evaluated.result && evaluated.result.value;
    lastValue = value;
    if (value && value.term === "勉強する" && value.meaning === "to study") {
      return value;
    }
    await delay(50);
  }
  throw new Error("The local player did not show the anchored dictionary lookup: " + JSON.stringify(lastValue));
}

async function inspectAndCaptureYouTubeWord(devTools, sessionID, fixture) {
  const capturesBefore = fixture.captures.length;
  const deadline = Date.now() + 10000;
  let clicked = false;
  let saved = false;
  let observedLookup = "";
  let observedFrequencies = [];
  let lastValue;
  while (Date.now() < deadline) {
    try {
      const evaluated = await devTools.send("Runtime.evaluate", {
        expression: `({
          wordLabel: document.querySelector(".goi-ext-caption-word--unknown")?.getAttribute("aria-label") || "",
          term: document.querySelector(".goi-dictionary-term")?.textContent || "",
          reading: document.querySelector(".goi-dictionary-reading")?.textContent || "",
          meaning: document.querySelector(".goi-dictionary-meanings li")?.textContent || "",
          frequencies: Array.from(document.querySelectorAll(".goi-dictionary-frequency"), (node) => node.textContent),
          select: document.querySelector(".goi-dictionary-select")?.textContent || ""
        })`,
        returnByValue: true,
      }, sessionID);
      const value = evaluated.result && evaluated.result.value;
      lastValue = value;
      if (value && value.wordLabel === "Look up 勉強する" && !clicked) {
        clicked = true;
        await clickPageElement(devTools, sessionID, ".goi-ext-caption-word--unknown");
      } else if (value && value.term === "勉強する" && value.reading === "べんきょうする" &&
          value.meaning === "to study" &&
          value.select === "Mine" && !saved) {
        observedLookup = value.reading + " · " + value.meaning;
        observedFrequencies = value.frequencies;
        await saveSmokeScreenshot(devTools, sessionID, "youtube-popup");
        saved = true;
        await clickPageElement(devTools, sessionID, ".goi-dictionary-select");
      }
      if (saved && fixture.captures.length > capturesBefore) {
        const capture = fixture.captures[capturesBefore];
        return {
          lookup: observedLookup,
          frequencies: observedFrequencies,
          saved: true,
          rawText: capture.raw_text,
          contextText: capture.context_text,
        };
      }
    } catch (_error) {}
    await delay(50);
  }
  throw new Error(
    "The installed extension did not look up and capture a YouTube subtitle word: " +
    JSON.stringify({clicked, saved, lastValue, captures: fixture.captures.length})
  );
}

async function waitForDocumentReady(devTools, sessionID) {
  const deadline = Date.now() + 5000;
  while (Date.now() < deadline) {
    const evaluated = await devTools.send("Runtime.evaluate", {
      expression: `document.readyState === "complete"`,
      returnByValue: true,
    }, sessionID);
    if (evaluated.result && evaluated.result.value) {
      return;
    }
    await delay(50);
  }
  throw new Error("The reader fixture did not load");
}

async function inspectAndCaptureAnalyzedPage(devTools, sessionID, fixture) {
  const before = fixture.captures.length;
  const deadline = Date.now() + 10000;
  let clicked = false;
  let selected = false;
  let lookup;
  let lastValue;
  while (Date.now() < deadline) {
    const evaluated = await devTools.send("Runtime.evaluate", {
      expression: `(function () {
        const reader = document.getElementById("goi-ext-reader")?.shadowRoot;
        if (!reader) return {};
        return {
        panel: Boolean(reader.getElementById("goi-ext-coverage")),
        summary: reader.querySelector("#goi-ext-coverage > div:first-child span")?.textContent || "",
        compact: reader.getElementById("goi-ext-coverage")?.classList.contains("goi-ext-coverage--collapsed") || false,
        term: reader.querySelector("#goi-ext-coverage-word-menu .goi-dictionary-term")?.textContent || "",
        reading: reader.querySelector("#goi-ext-coverage-word-menu .goi-dictionary-reading")?.textContent || "",
        meaning: reader.querySelector("#goi-ext-coverage-word-menu .goi-dictionary-meanings li")?.textContent || "",
        frequencies: Array.from(reader.querySelectorAll("#goi-ext-coverage-word-menu .goi-dictionary-frequency"), (node) => node.textContent),
        select: reader.querySelector("#goi-ext-coverage-word-menu .goi-dictionary-select")?.textContent || "",
        readablePopup: (function () {
          const menu = reader.getElementById("goi-ext-coverage-word-menu");
          if (!menu) return false;
          const rect = menu.getBoundingClientRect();
          return rect.width >= 300 && rect.left >= 0 && rect.top >= 0 &&
            rect.right <= innerWidth && rect.bottom <= innerHeight;
        })()
      }; })()`,
      returnByValue: true,
    }, sessionID);
    const value = evaluated.result && evaluated.result.value;
    lastValue = value;
    if (value && value.panel && !clicked) {
      clicked = true;
      await clickPageTextRange(devTools, sessionID, "#reading", 4, 8);
    } else if (value && value.term === "勉強する" && value.reading === "べんきょうする" &&
        value.meaning === "to study" && value.select === "Save to mining" && !selected) {
      lookup = value;
      await saveSmokeScreenshot(devTools, sessionID, "reader-popup");
      await devTools.send("Emulation.setEmulatedMedia", {
        features: [{ name: "prefers-color-scheme", value: "dark" }],
      }, sessionID);
      await saveSmokeScreenshot(devTools, sessionID, "reader-popup-dark");
      selected = true;
      await clickPageElement(devTools, sessionID, "#goi-ext-coverage-word-menu .goi-dictionary-select");
    }
    if (selected && fixture.captures.length > before) {
      const capture = fixture.captures.at(-1);
      return {
        summary: lookup.summary,
        term: lookup.term,
        reading: lookup.reading,
        meaning: lookup.meaning,
        frequencies: lookup.frequencies,
        compact: lookup.compact,
        readablePopup: lookup.readablePopup,
        rawText: capture.raw_text,
        contextText: capture.context_text,
        sourceKind: capture.source_kind,
        entrySequence: capture.suggested_entry_sequence,
      };
    }
    await delay(50);
  }
  throw new Error(
    "The installed extension did not provide dictionary-assisted page reading: " +
    JSON.stringify({clicked, selected, lastValue, captures: fixture.captures.length})
  );
}

async function exerciseReaderUsability(devTools, sessionID, fixture) {
  const capturesBefore = fixture.captures.length;
  await assertReaderFitsViewport(devTools, sessionID, false);
  await clickPageElement(devTools, sessionID, '[aria-label="Hide unknown-word highlights"]');
  await clickPageTextRange(devTools, sessionID, "#reading", 4, 8);
  await waitForReaderTerm(devTools, sessionID, "勉強する");
  const hiddenMarks = await devTools.send("Runtime.evaluate", {
    expression: `!CSS.highlights.has("goi-unknown-words")`,
    returnByValue: true,
  }, sessionID);
  assert.equal(hiddenMarks.result.value, true, "Hiding marks must leave dictionary lookup available");
  await clickPageElement(devTools, sessionID, "#goi-ext-coverage-word-close");

  // A caret on the right half of 語 is shared with the following unknown token.
  await clickPageTextRange(devTools, sessionID, "#adjacent-reading", 2, 3, 0.85);
  await waitForReaderTerm(devTools, sessionID, "日本語");
  await clickPageElement(devTools, sessionID, "#goi-ext-coverage-word-close");
  await clickPageTextRange(devTools, sessionID, "#ruby-base", 0, 2);
  await waitForReaderTerm(devTools, sessionID, "勉強する");
  await clickPageElement(devTools, sessionID, "#goi-ext-coverage-word-close");

  const whitespace = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const rect = document.getElementById("reading").getBoundingClientRect();
      return { x: rect.right - 5, y: rect.top + rect.height / 2 };
    })()`,
    returnByValue: true,
  }, sessionID);
  for (const type of ["mousePressed", "mouseReleased"]) {
    await devTools.send("Input.dispatchMouseEvent", {
      type, ...whitespace.result.value, button: "left", clickCount: 1,
    }, sessionID);
  }
  const blankClick = await devTools.send("Runtime.evaluate", {
    expression: `!document.getElementById("goi-ext-reader").shadowRoot.getElementById("goi-ext-coverage-word-menu")`,
    returnByValue: true,
  }, sessionID);
  assert.equal(blankClick.result.value, true, "Whitespace beside a line must not select its nearest word");

  await clickPageTextRange(devTools, sessionID, "#edge-reading", 0, 3);
  await waitForReaderTerm(devTools, sessionID, "日本語");
  await assertReaderFitsViewport(devTools, sessionID, true);
  const scrollPoint = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const lookup = document.getElementById("goi-ext-reader").shadowRoot.querySelector(".dictionary-lookup");
      const rect = lookup.getBoundingClientRect();
      return { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
    })()`,
    returnByValue: true,
  }, sessionID);
  await devTools.send("Input.dispatchMouseEvent", {
    type: "mouseWheel", ...scrollPoint.result.value, deltaX: 0, deltaY: 240,
  }, sessionID);
  const internallyScrolled = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      for (let attempt = 0; attempt < 30; attempt += 1) {
        const reader = document.getElementById("goi-ext-reader").shadowRoot;
        if (!reader.getElementById("goi-ext-coverage-word-menu")) return false;
        if (reader.querySelector(".dictionary-lookup").scrollTop > 0) return scrollY === 0;
        await new Promise(resolve => requestAnimationFrame(resolve));
      }
      return false;
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, sessionID);
  assert.equal(internallyScrolled.result.value, true, "Long definitions must scroll without dismissing or scrolling the page");
  await saveSmokeScreenshot(devTools, sessionID, "reader-long-edge");

  await devTools.send("Emulation.setDeviceMetricsOverride", {
    width: 360, height: 520, deviceScaleFactor: 1, mobile: false,
  }, sessionID);
  await assertReaderFitsViewport(devTools, sessionID, true);
  await saveSmokeScreenshot(devTools, sessionID, "reader-narrow-expanded");
  await clickPageElement(devTools, sessionID, "#goi-ext-coverage-word-close");
  await saveSmokeScreenshot(devTools, sessionID, "reader-narrow-controls");
  await clickPageElement(devTools, sessionID, '[aria-label="Hide reading controls"]');
  await assertReaderFitsViewport(devTools, sessionID, false);
  const compact = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const reader = document.getElementById("goi-ext-reader").shadowRoot;
      return reader.getElementById("goi-ext-coverage-next").getClientRects().length === 0 &&
        reader.querySelector('[aria-label="Close Goi reader"]').getClientRects().length > 0;
    })()`,
    returnByValue: true,
  }, sessionID);
  assert.equal(compact.result.value, true, "Compact reading controls must keep Close visible and word navigation out of the way");
  await clickPageTextRange(devTools, sessionID, "#reading", 0, 3);
  await waitForReaderTerm(devTools, sessionID, "日本語");
  const pageScrolled = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      scrollTo(0, 120);
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      return !document.getElementById("goi-ext-reader").shadowRoot.getElementById("goi-ext-coverage-word-menu");
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, sessionID);
  assert.equal(pageScrolled.result.value, true, "Scrolling the page must dismiss the detached dictionary");
  assert.equal(fixture.captures.length, capturesBefore, "Reading definitions must not save words automatically");
  await devTools.send("Emulation.clearDeviceMetricsOverride", undefined, sessionID);
  await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      scrollTo(0, 0);
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    })()`,
    awaitPromise: true,
  }, sessionID);
  await clickPageElement(devTools, sessionID, '[aria-label="Show reading controls"]');
  await devTools.send("Runtime.evaluate", {
    expression: `document.getElementById("goi-ext-reader").shadowRoot.getElementById("goi-ext-coverage-next").focus()`,
  }, sessionID);
  // The last clicked word was the first token; eight advances reach the off-screen paragraph.
  for (let word = 0; word < 8; word += 1) {
    await devTools.send("Input.dispatchKeyEvent", {
      type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13,
      text: "\r", unmodifiedText: "\r",
    }, sessionID);
    await devTools.send("Input.dispatchKeyEvent", {
      type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13,
    }, sessionID);
    const focusedDictionary = await devTools.send("Runtime.evaluate", {
      expression: `(function () {
        const reader = document.getElementById("goi-ext-reader").shadowRoot;
        return reader.activeElement?.id === "goi-ext-coverage-word-menu";
      })()`, returnByValue: true,
    }, sessionID);
    assert.equal(focusedDictionary.result.value, true, "Keyboard lookup must focus the dictionary");
    if (word < 7) {
      await devTools.send("Input.dispatchKeyEvent", {
        type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27,
      }, sessionID);
      await devTools.send("Input.dispatchKeyEvent", {
        type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27,
      }, sessionID);
    }
  }
  await waitForReaderTerm(devTools, sessionID, "日本語");
  const keyboardScroll = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
      const reader = document.getElementById("goi-ext-reader").shadowRoot;
      return scrollY > 0 && Boolean(reader.getElementById("goi-ext-coverage-word-menu"));
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, sessionID);
  assert.equal(keyboardScroll.result.value, true, "Keyboard navigation must keep the dictionary open after scrolling to a word");
}

async function waitForReaderTerm(devTools, sessionID, term) {
  const result = await devTools.send("Runtime.evaluate", {
    expression: `(async function () {
      let actual = "No dictionary popup";
      for (let attempt = 0; attempt < 100; attempt += 1) {
        const reader = document.getElementById("goi-ext-reader").shadowRoot;
        actual = reader.querySelector(".goi-dictionary-term")?.textContent ||
          reader.querySelector(".goi-dictionary-message")?.textContent || "No dictionary popup";
        if (actual === ${JSON.stringify(term)}) return actual;
        await new Promise(resolve => setTimeout(resolve, 20));
      }
      return actual;
    })()`,
    awaitPromise: true,
    returnByValue: true,
  }, sessionID);
  assert.equal(result.result.value, term, "Reader must look up " + term);
}

async function assertReaderFitsViewport(devTools, sessionID, withDictionary) {
  const result = await devTools.send("Runtime.evaluate", {
    expression: `(new Promise(function (resolve) {
      requestAnimationFrame(function () {
        const reader = document.getElementById("goi-ext-reader").shadowRoot;
        const panel = reader.getElementById("goi-ext-coverage");
        const menu = reader.getElementById("goi-ext-coverage-word-menu");
        function fits(node) {
          const rect = node.getBoundingClientRect();
          return rect.width > 0 && rect.left >= 0 && rect.top >= 0 &&
            rect.right <= innerWidth + 1 && rect.bottom <= innerHeight + 1 &&
            node.scrollWidth <= node.clientWidth + 1;
        }
        const summary = panel.querySelector("strong").getBoundingClientRect();
        resolve({
          panel: fits(panel),
          readableSummary: summary.width >= 100 && summary.height < 70,
          buttons: Array.from(panel.querySelectorAll("button"))
            .filter(button => button.getClientRects().length).every(fits),
          dictionary: ${withDictionary} ? Boolean(menu && fits(menu)) : true
        });
      });
    }))`,
    awaitPromise: true,
    returnByValue: true,
  }, sessionID);
  assert.deepEqual(result.result.value, {
    panel: true, readableSummary: true, buttons: true, dictionary: true,
  }, "Reader UI must remain legible and inside the viewport despite the page's styles");
}

async function clickPageTextRange(devTools, sessionID, selector, start, end, horizontalFraction = 0.5) {
  const evaluated = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const text = document.querySelector(${JSON.stringify(selector)}).firstChild;
      const range = document.createRange();
      range.setStart(text, ${start});
      range.setEnd(text, ${end});
      const rect = range.getBoundingClientRect();
      return {x: rect.left + rect.width * ${horizontalFraction}, y: rect.top + rect.height / 2};
    })()`,
    returnByValue: true,
  }, sessionID);
  const point = evaluated.result.value;
  await devTools.send("Input.dispatchMouseEvent", {
    type: "mousePressed", x: point.x, y: point.y, button: "left", clickCount: 1,
  }, sessionID);
  await devTools.send("Input.dispatchMouseEvent", {
    type: "mouseReleased", x: point.x, y: point.y, button: "left", clickCount: 1,
  }, sessionID);
}

async function clickPageElement(devTools, sessionID, selector) {
  const evaluated = await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const node = document.querySelector(${JSON.stringify(selector)}) ||
        document.getElementById("goi-ext-reader")?.shadowRoot.querySelector(${JSON.stringify(selector)});
      const rect = node.getBoundingClientRect();
      return {x: rect.left + rect.width / 2, y: rect.top + rect.height / 2};
    })()`,
    returnByValue: true,
  }, sessionID);
  const point = evaluated.result.value;
  await devTools.send("Input.dispatchMouseEvent", {
    type: "mousePressed", x: point.x, y: point.y, button: "left", clickCount: 1,
  }, sessionID);
  await devTools.send("Input.dispatchMouseEvent", {
    type: "mouseReleased", x: point.x, y: point.y, button: "left", clickCount: 1,
  }, sessionID);
}

async function waitForCompanionTarget(devTools, extensionID) {
  const deadline = Date.now() + 10000;
  const prefix = `chrome-extension://${extensionID}/companion/companion.html?tab=`;
  while (Date.now() < deadline) {
    const targets = await devTools.send("Target.getTargets");
    const target = targets.targetInfos.find(function (candidate) {
      return candidate.type === "page" && candidate.url.startsWith(prefix);
    });
    if (target) {
      return target;
    }
    await delay(50);
  }
  throw new Error("The installed extension did not open its companion window");
}

async function waitForPlayerTarget(devTools, extensionID) {
  const deadline = Date.now() + 10000;
  const url = `chrome-extension://${extensionID}/player/player.html`;
  while (Date.now() < deadline) {
    const targets = await devTools.send("Target.getTargets");
    const target = targets.targetInfos.find(function (candidate) {
      return candidate.type === "page" && candidate.url === url;
    });
    if (target) {
      return target;
    }
    await delay(50);
  }
  throw new Error("The installed extension did not open its local player");
}

async function connectThroughOptions(devTools, sessionID, baseURL) {
  const readyDeadline = Date.now() + 10000;
  while (Date.now() < readyDeadline) {
    const loaded = await devTools.send("Runtime.evaluate", {
      expression: `document.readyState === "complete" && !document.getElementById("save-connection").disabled`,
      returnByValue: true,
    }, sessionID);
    if (loaded.result && loaded.result.value) {
      break;
    }
    await delay(50);
  }

  await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      document.getElementById("base-url").value = ${JSON.stringify(baseURL)};
      document.getElementById("token").value = "chrome-smoke-token";
      document.getElementById("connection-form").requestSubmit();
    })()`,
  }, sessionID);
  await waitForOptionsStatus(devTools, sessionID, "Connected to Goi.");

  await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      document.getElementById("status").textContent = "Waiting for manual test";
      document.getElementById("test-connection").click();
    })()`,
  }, sessionID);
  await waitForOptionsStatus(devTools, sessionID, "Connected to Goi.");

  const evaluated = await devTools.send("Runtime.evaluate", {
    expression: `({
      heading: document.querySelector("h1")?.textContent || "",
      summary: document.getElementById("connection-summary")?.textContent || "",
      status: document.getElementById("status")?.textContent || "",
      testButton: document.getElementById("test-connection")?.textContent || ""
    })`,
    returnByValue: true,
  }, sessionID);
  return evaluated.result.value;
}

async function waitForOptionsStatus(devTools, sessionID, expected) {
  const deadline = Date.now() + 10000;
  let lastValue;
  while (Date.now() < deadline) {
    const evaluated = await devTools.send("Runtime.evaluate", {
      expression: `({
        status: document.getElementById("status")?.textContent || "",
        summary: document.getElementById("connection-summary")?.textContent || "",
        address: document.getElementById("base-url")?.value || "",
        testDisabled: document.getElementById("test-connection")?.disabled
      })`,
      returnByValue: true,
    }, sessionID);
    lastValue = evaluated.result && evaluated.result.value;
    if (lastValue && lastValue.status === expected) {
      return;
    }
    await delay(50);
  }
  throw new Error("The extension connection page did not report " + expected + ": " + JSON.stringify(lastValue));
}

async function waitForPlayer(devTools, sessionID) {
  const deadline = Date.now() + 10000;
  let lastValue;
  let lastError;
  const expression = `({
    ready: document.readyState,
    title: document.title,
    heading: document.querySelector("h1")?.textContent || "",
    videoInput: document.getElementById("video-file")?.type || "",
    subtitleInput: document.getElementById("subtitle-file")?.type || "",
    subtitleInputMultiple: document.getElementById("subtitle-file")?.multiple || false,
    trackControlsHidden: document.getElementById("subtitle-track-controls")?.hidden,
    displayModes: document.querySelectorAll('input[name="display-mode"]').length,
    pauseModes: document.querySelectorAll('input[name="pause-behavior"]').length,
    parser: typeof GoiExtension?.subtitleFileModel,
    connection: document.getElementById("connection-status")?.textContent || ""
  })`;
  while (Date.now() < deadline) {
    try {
      const evaluated = await devTools.send("Runtime.evaluate", {
        expression,
        returnByValue: true,
      }, sessionID);
      const value = evaluated.result && evaluated.result.value;
      lastValue = value;
      if (value && value.ready === "complete" && value.connection.startsWith("Not connected")) {
        return value;
      }
    } catch (error) {
      lastError = error;
    }
    await delay(50);
  }
  throw new Error("The installed extension local player did not load: " + JSON.stringify({
    lastValue,
    lastError: lastError && lastError.message,
  }));
}

async function waitForPlayerTranscript(devTools, sessionID) {
  const deadline = Date.now() + 10000;
  const expression = `({
    selectedTrack: document.getElementById("subtitle-track")?.selectedOptions[0]?.textContent || "",
    trackCount: document.getElementById("subtitle-track")?.options.length || 0,
    trackControlsHidden: document.getElementById("subtitle-track-controls")?.hidden,
    lineCount: document.querySelectorAll(".transcript-line").length,
    firstLine: document.querySelectorAll(".line-text")[0]?.textContent || "",
    secondLine: document.querySelectorAll(".line-text")[1]?.textContent || "",
    offsetEnabled: !document.getElementById("offset-input")?.disabled,
    coverage: document.getElementById("coverage-summary")?.textContent || ""
  })`;
  while (Date.now() < deadline) {
    try {
      const evaluated = await devTools.send("Runtime.evaluate", {
        expression,
        returnByValue: true,
      }, sessionID);
      const value = evaluated.result && evaluated.result.value;
      if (value && value.lineCount === 2 && value.coverage === "Coverage unavailable") {
        return value;
      }
    } catch (_error) {}
    await delay(50);
  }
  throw new Error("The installed extension player did not import local subtitles");
}

async function selectPlayerTrack(devTools, sessionID, name, expectedLine) {
  await devTools.send("Runtime.evaluate", {
    expression: `(function () {
      const select = document.getElementById("subtitle-track");
      const option = Array.from(select.options).find(function (candidate) {
        return candidate.textContent.startsWith(${JSON.stringify(name)});
      });
      select.value = option.value;
      select.dispatchEvent(new Event("change", {bubbles: true}));
    })()`,
  }, sessionID);
  const deadline = Date.now() + 10000;
  while (Date.now() < deadline) {
    const evaluated = await devTools.send("Runtime.evaluate", {
      expression: `({
        selectedTrack: document.getElementById("subtitle-track")?.selectedOptions[0]?.textContent || "",
        lineCount: document.querySelectorAll(".transcript-line").length,
        firstLine: document.querySelector(".line-text")?.textContent || ""
      })`,
      returnByValue: true,
    }, sessionID);
    const value = evaluated.result && evaluated.result.value;
    if (value && value.firstLine === expectedLine) {
      return value;
    }
    await delay(50);
  }
  throw new Error("The installed extension player did not switch subtitle tracks");
}

async function waitForPlayerCoverage(devTools, sessionID) {
  const deadline = Date.now() + 10000;
  const expression = `({
    connection: document.getElementById("connection-status")?.textContent || "",
    summary: document.getElementById("coverage-summary")?.textContent || "",
    unknownTargets: Array.from(document.querySelectorAll(".unknown-target"), function (target) {
      return target.textContent;
    }),
    batch: document.getElementById("batch-one-target")?.textContent || ""
  })`;
  let retried = false;
  while (Date.now() < deadline) {
    try {
      const evaluated = await devTools.send("Runtime.evaluate", {
        expression,
        returnByValue: true,
      }, sessionID);
      const value = evaluated.result && evaluated.result.value;
      if (value && value.connection.startsWith("Connected to ") && !retried) {
        retried = true;
        await devTools.send("Runtime.evaluate", {
          expression: `document.getElementById("coverage-retry").click()`,
        }, sessionID);
      }
      if (value && value.summary === "50% known · 2 unknown · 2/2 checked") {
        return value;
      }
    } catch (_error) {}
    await delay(50);
  }
  throw new Error("The installed extension player did not classify local subtitles");
}

async function waitForPlayerCapture(devTools, sessionID, fixture) {
  const deadline = Date.now() + 10000;
  let lastValue;
  const expression = `document.getElementById("capture-status")?.textContent || ""`;
  while (Date.now() < deadline) {
    try {
      const evaluated = await devTools.send("Runtime.evaluate", {
        expression,
        returnByValue: true,
      }, sessionID);
      const value = evaluated.result && evaluated.result.value;
      lastValue = value;
      if (value === "Sent to mining." && fixture.captures.length === 1) {
        return { status: value, capture: fixture.captures[0] };
      }
    } catch (_error) {}
    await delay(50);
  }
  throw new Error("The installed extension player did not save local text: " + JSON.stringify({
    status: lastValue,
    captures: fixture.captures.length,
  }));
}

async function waitForInvalidSubtitlePreservation(devTools, sessionID) {
  const deadline = Date.now() + 10000;
  const expression = `({
    error: document.getElementById("page-status")?.textContent || "",
    selectedTrack: document.getElementById("subtitle-track")?.selectedOptions[0]?.textContent || "",
    trackCount: document.getElementById("subtitle-track")?.options.length || 0,
    lineCount: document.querySelectorAll(".transcript-line").length,
    offset: document.getElementById("offset-input")?.value || ""
  })`;
  while (Date.now() < deadline) {
    const evaluated = await devTools.send("Runtime.evaluate", {
      expression,
      returnByValue: true,
    }, sessionID);
    const value = evaluated.result && evaluated.result.value;
    if (value && value.error === "This file is not recognizable SRT, WebVTT, ASS, or SSA subtitles.") {
      return value;
    }
    await delay(50);
  }
  throw new Error("An invalid subtitle replacement did not preserve the current session");
}

async function waitForPlayerVideo(devTools, sessionID) {
  const deadline = Date.now() + 10000;
  const expression = `({
    videoName: document.getElementById("video-name")?.textContent || "",
    blobSource: document.getElementById("video")?.src.startsWith("blob:") || false,
    ready: document.getElementById("video-status")?.textContent === "Video loaded.",
    batch: document.getElementById("batch-one-target")?.textContent || ""
  })`;
  while (Date.now() < deadline) {
    try {
      const evaluated = await devTools.send("Runtime.evaluate", {
        expression,
        returnByValue: true,
      }, sessionID);
      const value = evaluated.result && evaluated.result.value;
      if (value && value.videoName === "local-smoke.webm" && value.blobSource && value.ready) {
        return value;
      }
    } catch (_error) {}
    await delay(50);
  }
  throw new Error("The installed extension player did not load the generated local video");
}

async function waitForCompanion(devTools, sessionID) {
  const deadline = Date.now() + 10000;
  const expression = `(function () {
    function baseText(element) {
      if (!element) return "";
      const copy = element.cloneNode(true);
      copy.querySelectorAll("rt").forEach(function (reading) { reading.remove(); });
      return copy.textContent || "";
    }
    const word = document.querySelector(".subtitle-word--unknown");
    const ruby = word?.querySelector("ruby");
    const reading = ruby?.querySelector("rt");
    let furiganaGeometry = null;
    if (word && ruby && reading && ruby.firstChild) {
      const base = document.createRange();
      base.selectNode(ruby.firstChild);
      const wordRect = word.getBoundingClientRect();
      const rubyRect = ruby.getBoundingClientRect();
      const readingRect = reading.getBoundingClientRect();
      const baseRect = base.getBoundingClientRect();
      furiganaGeometry = {
        centered: Math.abs((readingRect.left + readingRect.right) - (baseRect.left + baseRect.right)) <= 2,
        aboveBase: readingRect.bottom <= baseRect.top + 1,
        coversKanjiOnly: rubyRect.width > 0 && rubyRect.width < wordRect.width
      };
    }
    return {
      ready: document.readyState,
      heading: document.querySelector("h1")?.textContent || "",
      caption: baseText(document.querySelector(".subtitle-text")),
      secondCaption: baseText(document.querySelectorAll(".subtitle-text")[1]),
      lineCount: document.getElementById("line-count")?.textContent || "",
      displayModes: document.querySelectorAll('input[name="display-mode"]').length,
      verticalPosition: document.getElementById("vertical-position")?.value || "",
      backgroundOpacity: document.getElementById("background-opacity")?.value || "",
      batchLabel: document.getElementById("batch-one-target")?.textContent || "",
      furiganaGeometry
    };
  })()`;
  while (Date.now() < deadline) {
    try {
      const evaluated = await devTools.send("Runtime.evaluate", {
        expression,
        returnByValue: true,
      }, sessionID);
      const value = evaluated.result && evaluated.result.value;
      if (value && value.ready === "complete" && value.caption && value.secondCaption &&
          value.lineCount === "2 subtitle lines · 50% known" &&
          value.batchLabel === "Send 2 lines to mining") {
        return value;
      }
    } catch (_error) {
      // Opening the companion can replace its execution context.
    }
    await delay(50);
  }
  throw new Error("The installed extension companion did not load the full transcript");
}

function delay(milliseconds) {
  return new Promise(function (resolve) {
    setTimeout(resolve, milliseconds);
  });
}

async function stopProcess(child) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return;
  }
  child.kill("SIGTERM");
  const exited = await waitForExit(child, 2000);
  if (!exited) {
    child.kill("SIGKILL");
    if (!await waitForExit(child, 2000)) {
      throw new Error("Chrome did not exit after SIGKILL");
    }
  }
}

function waitForExit(child, timeout) {
  if (child.exitCode !== null || child.signalCode !== null) {
    return Promise.resolve(true);
  }
  return new Promise(function (resolve) {
    const timer = setTimeout(function () {
      child.removeListener("exit", onExit);
      resolve(false);
    }, timeout);
    function onExit() {
      clearTimeout(timer);
      resolve(true);
    }
    child.once("exit", onExit);
    if (child.exitCode !== null || child.signalCode !== null) {
      child.removeListener("exit", onExit);
      clearTimeout(timer);
      resolve(true);
    }
  });
}
