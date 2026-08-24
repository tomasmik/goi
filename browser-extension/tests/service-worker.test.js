const test = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");

const settingsModel = require("../shared/settings-model.js");
const {
  captureModel,
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
    "goi.companion.open",
    "goi.capture.outbox-status",
    "goi.capture.outbox.retry",
    "goi.capture.outbox.discard",
    "goi.coverage.analyze-page",
  ];

  popupOnlyMessages.forEach(function (type) {
    let response;
    const keepChannelOpen = harness.operations.handleRuntimeMessage(
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

test("lists and removes automatically enabled sites", async function () {
  const harness = workerHarness(null);
  harness.storage.siteAutoOriginsV1 = ["https://z.example", "https://a.example"];

  assert.deepEqual(
    JSON.parse(JSON.stringify(await harness.operations.listSiteAutoOrigins())),
    { ok: true, origins: ["https://a.example", "https://z.example"] },
  );
  await harness.operations.removeSiteAutoOrigin("https://a.example", false);
  assert.deepEqual(JSON.parse(JSON.stringify(harness.storage.siteAutoOriginsV1)), ["https://z.example"]);
});

test("stores a manageable global ignored-word list", async function () {
  const harness = workerHarness(null);

  await harness.operations.addGlobalIgnoredWord("東京");
  await harness.operations.addGlobalIgnoredWord("太郎");
  assert.deepEqual(JSON.parse(JSON.stringify(harness.storage.globalIgnoredWordsV1)), ["太郎", "東京"]);

  await harness.operations.removeGlobalIgnoredWord("太郎");
  assert.deepEqual(JSON.parse(JSON.stringify(harness.storage.globalIgnoredWordsV1)), ["東京"]);
});

test("routes subtitle dictionary lookups through the stored Goi connection", async function () {
  let requested;
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "secret" },
    undefined,
    {
      create(_fetch, connection) {
        assert.equal(connection.baseUrl, "https://goi.example");
        return {
          async dictionary(expression) {
            requested = expression;
            return { query: expression, state: "ready", candidates: [] };
          },
        };
      },
    },
  );
  let response;
  const keepChannelOpen = harness.operations.handleRuntimeMessage(
    { type: "goi.dictionary.lookup", version: 1, expression: "読む" },
    { id: "goi-test", tab: { id: 4, url: "https://www.youtube.com/watch?v=test" }, frameId: 0 },
    function (value) { response = value; },
  );
  assert.equal(keepChannelOpen, true);
  await new Promise(setImmediate);
  assert.equal(requested, "読む");
  assert.deepEqual(JSON.parse(JSON.stringify(response)), {
    ok: true,
    result: { query: "読む", state: "ready", candidates: [] },
  });
});

test("marks subtitle words as known through the stored Goi connection", async function () {
  let requested;
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "secret" },
    undefined,
    {
      create() {
        return {
          async markKnown(expression) {
            requested = expression;
            return { state: "marked_known" };
          },
        };
      },
    },
  );
  let response;
  harness.operations.handleRuntimeMessage(
    { type: "goi.vocabulary.known", version: 1, expression: "育てる" },
    { id: "goi-test", tab: { id: 4, url: "https://www.youtube.com/watch?v=test" }, frameId: 0 },
    function (value) { response = value; },
  );
  await new Promise(setImmediate);

  assert.equal(requested, "育てる");
  assert.deepEqual(JSON.parse(JSON.stringify(response)), {
    ok: true,
    result: { state: "marked_known" },
  });
});

test("explains when Goi does not provide the dictionary endpoint", async function () {
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "secret" },
    undefined,
    {
      create() {
        return {
          async dictionary() {
            const error = new Error("not found");
            error.status = 404;
            throw error;
          },
        };
      },
    },
  );
  let response;
  harness.operations.handleRuntimeMessage(
    { type: "goi.dictionary.lookup", version: 1, expression: "読む" },
    { id: "goi-test", tab: { id: 4, url: "https://www.youtube.com/watch?v=test" }, frameId: 0 },
    function (value) { response = value; },
  );

  await new Promise(setImmediate);

  assert.deepEqual(JSON.parse(JSON.stringify(response)), {
    ok: false,
    errorCode: "dictionary_api_unavailable",
  });
});

test("ignores inherited object names as runtime message types", function () {
  const harness = workerHarness(null);
  let response;
  const keepChannelOpen = harness.operations.handleRuntimeMessage(
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

test("connection changes survive failure to remove the previous host permission", async function () {
  const harness = workerHarness({ baseUrl: "https://old.example", token: "old-token" });

  const connection = await harness.operations.saveConnection(
    "https://new.example",
    "new-token",
  );

  assert.deepEqual(JSON.parse(JSON.stringify(connection)), {
    baseUrl: "https://new.example",
    token: "new-token",
  });
  assert.deepEqual(JSON.parse(JSON.stringify(harness.storage.connection)), {
    baseUrl: "https://new.example",
    token: "new-token",
  });
});

test("checks a candidate connection without replacing the saved connection", async function () {
  const checked = [];
  const harness = workerHarness(
    { baseUrl: "https://old.example", token: "old-token" },
    null,
    {
      create(_fetch, connection) {
        return {
          async status() {
            checked.push(connection);
            const error = new Error("unauthorized");
            error.status = 401;
            throw error;
          },
        };
      },
    },
  );

  await assert.rejects(
    harness.operations.verifyConnection("https://new.example", "wrong-token"),
    function (error) { return error.status === 401; },
  );
  assert.deepEqual(JSON.parse(JSON.stringify(checked)), [{
    baseUrl: "https://new.example",
    token: "wrong-token",
  }]);
  assert.deepEqual(JSON.parse(JSON.stringify(harness.storage.connection)), {
    baseUrl: "https://old.example",
    token: "old-token",
  });
});

test("disconnect succeeds after storage is cleared when permission cleanup fails", async function () {
  const harness = workerHarness({ baseUrl: "https://goi.example", token: "token" });

  await harness.operations.disconnectConnection();

  assert.equal(harness.storage.connection, undefined);
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

test("connection changes remove an unused previous host permission", async function () {
  const harness = workerHarness({ baseUrl: "https://old.example", token: "old-token" });
  harness.storage.siteAutoOriginsV1 = ["https://reader.example"];
  const removed = [];
  harness.chrome.permissions.remove = async function (permission) {
    removed.push(permission);
    return true;
  };

  await harness.operations.saveConnection("https://new.example", "new-token");

  assert.deepEqual(JSON.parse(JSON.stringify(removed)), [
    { origins: ["https://old.example/*"] },
  ]);
});

test("loads a stored HTTP connection for server-side transport validation", async function () {
  const harness = workerHarness({ baseUrl: "http://goi.example", token: "legacy-token" });

  assert.deepEqual(
    JSON.parse(JSON.stringify(await harness.operations.getConnection())),
    { baseUrl: "http://goi.example", token: "legacy-token" },
  );
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

test("keeps the settings queue usable after a failed write", async function () {
  const stored = { [settingsModel.STORAGE_KEY]: settingsModel.sanitize() };
  let setCalls = 0;
  const syncStorage = {
    async get(key) {
      return { [key]: stored[key] };
    },
    async set(values) {
      setCalls += 1;
      if (setCalls === 1) {
        throw new Error("storage unavailable");
      }
      Object.assign(stored, values);
    },
  };
  const harness = workerHarness(null, syncStorage);

  await assert.rejects(
    harness.operations.updateSettings({ fontSizePx: 52 }),
    /storage unavailable/u,
  );
  const settings = await harness.operations.updateSettings({ verticalPercent: 24 });

  assert.equal(settings.fontSizePx, settingsModel.DEFAULT_SETTINGS.fontSizePx);
  assert.equal(settings.verticalPercent, 24);
});

test("reports privacy-scoped automatic-site status for the active web origin", async function () {
  const harness = workerHarness(null);
  harness.chrome.tabs.query = async function (query) {
    assert.deepEqual(JSON.parse(JSON.stringify(query)), { active: true, currentWindow: true });
    return [{ id: 7, url: "https://reader.example:8443/chapter/1", status: "complete" }];
  };

  const status = await harness.operations.getSiteAutoStatus();

  assert.deepEqual(JSON.parse(JSON.stringify(status)), {
    ok: true,
    available: true,
    enabled: false,
    kind: "web",
    origin: "https://reader.example:8443",
    permissionPattern: "https://reader.example/*",
  });
});

test("requires the active origin permission before enabling automatic analysis", async function () {
  const harness = workerHarness(null);
  harness.chrome.tabs.query = async function () {
    return [{ id: 7, url: "http://reader.example/article", status: "complete" }];
  };

  const status = await harness.operations.setSiteAutoEnabled(true);

  assert.equal(status.ok, false);
  assert.equal(status.available, true);
  assert.equal(status.errorCode, "permission_required");
  assert.equal(status.permissionPattern, "http://reader.example/*");
  assert.equal(harness.storage.siteAutoOriginsV1, undefined);
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

  const enabled = await harness.operations.setSiteAutoEnabled(true);
  assert.equal(enabled.enabled, true);
  assert.deepEqual(
    JSON.parse(JSON.stringify(harness.storage.siteAutoOriginsV1)),
    ["https://reader.example"],
  );
  assert.equal(analyses, 1);

  const reenabled = await harness.operations.setSiteAutoEnabled(true);
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

  const disabled = await harness.operations.setSiteAutoEnabled(false);
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

test("disabling automatic analysis preserves a permission used by the Goi connection", async function () {
  const harness = workerHarness({
    baseUrl: "https://reader.example:8443",
    token: "token",
  });
  harness.storage.siteAutoOriginsV1 = ["https://reader.example"];
  harness.chrome.tabs.query = async function (query) {
    if (query.active) {
      return [{ id: 7, url: "https://reader.example/article", status: "complete" }];
    }
    return [];
  };
  harness.chrome.scripting.executeScript = async function () {
    return [{ frameId: 0, result: false }];
  };
  let removals = 0;
  harness.chrome.permissions.remove = async function () {
    removals += 1;
    return true;
  };

  const disabled = await harness.operations.setSiteAutoEnabled(false);

  assert.equal(disabled.enabled, false);
  assert.equal(removals, 0);
});

test("maps YouTube automatic mode to the existing overlay setting", async function () {
  const stored = { [settingsModel.STORAGE_KEY]: settingsModel.sanitize() };
  const syncStorage = {
    async get(key) {
      return { [key]: stored[key] };
    },
    async set(values) {
      Object.assign(stored, values);
    },
  };
  const harness = workerHarness(null, syncStorage);
  harness.chrome.tabs.query = async function () {
    return [{ id: 4, url: "https://www.youtube.com/watch?v=abc", status: "complete" }];
  };
  harness.chrome.permissions.contains = async function () {
    assert.fail("YouTube already has its manifest host permission");
  };

  const initial = await harness.operations.getSiteAutoStatus();
  assert.equal(initial.kind, "youtube");
  assert.equal(initial.enabled, true);
  assert.equal(initial.permissionPattern, "https://www.youtube.com/*");

  const disabled = await harness.operations.setSiteAutoEnabled(false);
  assert.equal(disabled.enabled, false);
  assert.equal(stored[settingsModel.STORAGE_KEY].overlayEnabled, false);
});

test("enabling YouTube captions injects the overlay into an already open tab", async function () {
  const stored = {
    [settingsModel.STORAGE_KEY]: settingsModel.sanitize({ overlayEnabled: false }),
  };
  const syncStorage = {
    async get(key) {
      return { [key]: stored[key] };
    },
    async set(values) {
      Object.assign(stored, values);
    },
  };
  const harness = workerHarness(null, syncStorage);
  harness.chrome.tabs.query = async function () {
    return [{ id: 4, url: "https://www.youtube.com/watch?v=abc", status: "complete" }];
  };
  const injections = [];
  harness.chrome.scripting.executeScript = async function (input) {
    injections.push(input);
    return input.func ? [{ frameId: 0, result: false }] : [{ frameId: 0 }];
  };
  harness.chrome.scripting.insertCSS = async function (input) {
    injections.push(input);
  };

  const enabled = await harness.operations.setSiteAutoEnabled(true);

  assert.equal(enabled.enabled, true);
  assert.equal(stored[settingsModel.STORAGE_KEY].overlayEnabled, true);
  assert.deepEqual(JSON.parse(JSON.stringify(injections[1].files)), [
    "content/capture-content.css",
    "shared/dictionary-view.css",
    "youtube/overlay.css",
  ]);
  assert.deepEqual(JSON.parse(JSON.stringify(injections[2].files)), [
    "shared/capture-model.js",
    "shared/settings-model.js",
    "shared/caption-model.js",
    "shared/subtitle-model.js",
    "shared/dictionary-view.js",
    "shared/subtitle-file-model.js",
    "content/capture-content.js",
    "youtube/overlay.js",
  ]);
});

test("restores the YouTube setting when current-tab injection fails", async function () {
  const stored = {
    [settingsModel.STORAGE_KEY]: settingsModel.sanitize({ overlayEnabled: false }),
  };
  const syncStorage = {
    async get(key) {
      return { [key]: stored[key] };
    },
    async set(values) {
      Object.assign(stored, values);
    },
  };
  const harness = workerHarness(null, syncStorage);
  harness.chrome.tabs.query = async function () {
    return [{ id: 4, url: "https://www.youtube.com/watch?v=abc", status: "complete" }];
  };
  harness.chrome.scripting.executeScript = async function () {
    throw new Error("tab closed");
  };

  await assert.rejects(harness.operations.setSiteAutoEnabled(true), /tab closed/);
  assert.equal(stored[settingsModel.STORAGE_KEY].overlayEnabled, false);
});

test("activating an existing YouTube tab restores its enabled overlay", async function () {
  const stored = {
    [settingsModel.STORAGE_KEY]: settingsModel.sanitize({ overlayEnabled: true }),
  };
  const syncStorage = {
    async get(key) {
      return { [key]: stored[key] };
    },
    async set(values) {
      Object.assign(stored, values);
    },
  };
  const harness = workerHarness(null, syncStorage);
  harness.chrome.tabs.get = async function () {
    return { id: 4, url: "https://www.youtube.com/watch?v=abc", status: "complete" };
  };
  const injections = [];
  harness.chrome.scripting.executeScript = async function (input) {
    injections.push(input);
    return input.func ? [{ frameId: 0, result: false }] : [{ frameId: 0 }];
  };
  harness.chrome.scripting.insertCSS = async function (input) {
    injections.push(input);
  };

  await harness.events.tabActivated.dispatch({ tabId: 4 });

  assert.equal(injections.length, 3);
  assert.deepEqual(JSON.parse(JSON.stringify(injections[2].files)), [
    "shared/capture-model.js",
    "shared/settings-model.js",
    "shared/caption-model.js",
    "shared/subtitle-model.js",
    "shared/dictionary-view.js",
    "shared/subtitle-file-model.js",
    "content/capture-content.js",
    "youtube/overlay.js",
  ]);
});

test("the YouTube shortcut persists and applies the enabled setting", async function () {
  const stored = {
    [settingsModel.STORAGE_KEY]: settingsModel.sanitize({ overlayEnabled: false }),
  };
  const syncStorage = {
    async get(key) {
      return { [key]: stored[key] };
    },
    async set(values) {
      Object.assign(stored, values);
    },
  };
  const harness = workerHarness(null, syncStorage);
  harness.chrome.tabs.query = async function () {
    return [{ id: 4, url: "https://www.youtube.com/watch?v=abc", status: "complete" }];
  };
  let scriptChecks = 0;
  harness.chrome.scripting.executeScript = async function (input) {
    if (input.func) {
      scriptChecks += 1;
      return [{ frameId: 0, result: true }];
    }
    return [{ frameId: 0 }];
  };

  await harness.events.command.dispatch("toggle-youtube-overlay");

  assert.equal(stored[settingsModel.STORAGE_KEY].overlayEnabled, true);
  assert.equal(scriptChecks, 1);
});

test("opens and focuses one exact local player tab", async function () {
  const harness = workerHarness(null);
  const playerURL = "chrome-extension://goi-test/player/player.html";
  const tabs = [];
  const updates = [];
  const focused = [];
  let queryCount = 0;
  harness.chrome.tabs.query = async function (query) {
    assert.deepEqual(JSON.parse(JSON.stringify(query)), {});
    queryCount += 1;
    if (queryCount === 2) {
      tabs[0].pendingUrl += "#player-workspace";
    }
    return tabs.slice();
  };
  harness.chrome.tabs.create = async function (options) {
    assert.deepEqual(JSON.parse(JSON.stringify(options)), { url: playerURL, active: true });
    const tab = { id: 12, windowId: 3, pendingUrl: options.url };
    tabs.push(tab);
    return tab;
  };
  harness.chrome.tabs.update = async function (tabId, options) {
    updates.push({ tabId, options });
  };
  harness.chrome.windows.update = async function (windowId, options) {
    focused.push({ windowId, options });
  };
  const results = await Promise.all([
    harness.operations.openLocalPlayer(),
    harness.operations.openLocalPlayer(),
  ]);

  assert.deepEqual(JSON.parse(JSON.stringify(results)), [
    { ok: true, tabId: 12 },
    { ok: true, tabId: 12 },
  ]);
  assert.equal(tabs.length, 1);
  assert.deepEqual(JSON.parse(JSON.stringify(updates)), [
    { tabId: 12, options: { active: true } },
  ]);
  assert.deepEqual(JSON.parse(JSON.stringify(focused)), [
    { windowId: 3, options: { focused: true } },
  ]);
});

test("routes the capture shortcut to the active local player", async function () {
  const harness = workerHarness(null);
  harness.chrome.tabs.query = async function () {
    return [{ id: 12, url: "chrome-extension://goi-test/player/player.html" }];
  };
  harness.chrome.scripting.executeScript = async function () {
    assert.fail("the local player shortcut must not use page injection");
  };

  await harness.events.command.dispatch("capture-selection");

  assert.deepEqual(JSON.parse(JSON.stringify(harness.runtimeMessages)), [{
    type: "goi.player.capture-hotkey",
    version: 1,
    tabId: 12,
  }]);
});

test("the capture shortcut uses the YouTube overlay capture", async function () {
  const harness = workerHarness(null);
  const tab = { id: 4, url: "https://www.youtube.com/watch?v=abc", status: "complete" };
  const tabMessages = [];
  let scriptChecks = 0;
  harness.chrome.tabs.query = async function () { return [tab]; };
  harness.chrome.scripting.executeScript = async function () {
    scriptChecks += 1;
    return [{ frameId: 0, result: true }];
  };
  harness.chrome.tabs.sendMessage = async function (tabId, message) {
    tabMessages.push({ tabId, message });
    return { ok: true, handled: true };
  };

  await harness.events.command.dispatch("capture-selection");

  assert.equal(scriptChecks, 1);
  assert.deepEqual(JSON.parse(JSON.stringify(tabMessages)), [{
    tabId: 4,
    message: { type: "goi.youtube.capture.current", version: 1 },
  }]);
});

test("the capture shortcut falls back when YouTube has no active occurrence selection", async function () {
  const client = {
    create() {
      return {
        async capture() {
          return { id: 1, revision: 1, status: "pending", replayed: false, review_url: "/1" };
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "token" },
    undefined,
    client,
  );
  const tab = { id: 4, url: "https://www.youtube.com/watch?v=abc", status: "complete" };
  let overlayRequests = 0;
  let ordinaryCollections = 0;
  harness.chrome.tabs.query = async function () { return [tab]; };
  harness.chrome.tabs.sendMessage = async function () {
    overlayRequests += 1;
    return { ok: true, handled: false };
  };
  harness.chrome.scripting.executeScript = async function (input) {
    if (Array.isArray(input.args) && input.args.length === 1) {
      ordinaryCollections += 1;
      return [{
        frameId: 0,
        result: {
          focused: true,
          capture: {
            ok: true,
            capture: { expression: "読む", contextText: "本を読む。" },
          },
        },
      }];
    }
    return [{ frameId: 0, result: true }];
  };

  await harness.events.command.dispatch("capture-selection");

  assert.equal(overlayRequests, 1);
  assert.equal(ordinaryCollections, 1);
});

test("restores and reuses one companion window for the active YouTube tab", async function () {
  const harness = workerHarness(null);
  const videoTab = { id: 4, url: "https://www.youtube.com/watch?v=abc", status: "complete" };
  let companionTabs = [];
  const created = [];
  const updated = [];
  const windowUpdates = [];
  harness.chrome.scripting.executeScript = async function () {
    return [{ frameId: 0, result: true }];
  };
  harness.chrome.tabs.query = async function (query) {
    assert.match(query.url, /companion\/companion\.html\*$/u);
    return companionTabs;
  };
  harness.chrome.tabs.update = async function (tabId, update) {
    updated.push({ tabId, update });
  };
  harness.chrome.windows.create = async function (options) {
    created.push(options);
    return { tabs: [{ id: 8, url: options.url }] };
  };
  harness.chrome.windows.update = async function (windowId, update) {
    windowUpdates.push({ windowId, update });
  };

  const opened = await harness.operations.openSubtitleBrowser({ tab: videoTab, url: videoTab.url });
  assert.deepEqual(JSON.parse(JSON.stringify(opened)), { ok: true, tabId: 4 });
  assert.deepEqual(JSON.parse(JSON.stringify(created)), [{
    url: "chrome-extension://goi-test/companion/companion.html?tab=4",
    type: "popup",
    width: 900,
    height: 720,
    focused: true,
  }]);

  companionTabs = [{ id: 8, windowId: 9 }];
  await harness.operations.openSubtitleBrowser({ tab: videoTab, url: videoTab.url });
  assert.equal(created.length, 1);
  assert.deepEqual(JSON.parse(JSON.stringify(updated)), [{
    tabId: 8,
    update: {
      active: true,
      url: "chrome-extension://goi-test/companion/companion.html?tab=4",
    },
  }]);
  assert.deepEqual(JSON.parse(JSON.stringify(windowUpdates)), [{
    windowId: 9,
    update: {
      state: "normal",
    },
  }, {
    windowId: 9,
    update: {
      focused: true,
      width: 900,
      height: 720,
    },
  }]);
});

test("opens the subtitle browser for YouTube Shorts", async function () {
  const harness = workerHarness(null);
  const videoTab = { id: 4, url: "https://www.youtube.com/shorts/abc", status: "complete" };
  harness.chrome.scripting.executeScript = async function () {
    return [{ frameId: 0, result: true }];
  };
  harness.chrome.tabs.query = async function () { return []; };
  harness.chrome.windows.create = async function (options) {
    return { tabs: [{ id: 8, url: options.url }] };
  };

  const opened = await harness.operations.openSubtitleBrowser({ tab: videoTab, url: videoTab.url });

  assert.deepEqual(JSON.parse(JSON.stringify(opened)), { ok: true, tabId: 4 });
});

test("retries companion ownership loading after transient session storage failure", async function () {
  const sessionStorage = {};
  const harness = workerHarness(null, null, null, sessionStorage);
  const videoTab = { id: 4, url: "https://www.youtube.com/watch?v=abc", status: "complete" };
  let reads = 0;
  let nextCompanionTabID = 8;
  harness.chrome.storage.session.get = async function (key) {
    reads += 1;
    if (reads === 1) {
      throw new Error("session storage unavailable");
    }
    return { [key]: sessionStorage[key] };
  };
  harness.chrome.scripting.executeScript = async function () {
    return [{ frameId: 0, result: true }];
  };
  harness.chrome.tabs.query = async function () { return []; };
  harness.chrome.tabs.sendMessage = async function () { return { ok: true }; };
  harness.chrome.windows.create = async function () {
    const tabID = nextCompanionTabID;
    nextCompanionTabID += 1;
    return { id: tabID + 1, tabs: [{ id: tabID }] };
  };

  await assert.rejects(
    harness.operations.openSubtitleBrowser({ tab: videoTab, url: videoTab.url }),
    (error) => error.code === "companion_storage_unavailable"
  );
  const opened = await harness.operations.openSubtitleBrowser({ tab: videoTab, url: videoTab.url });

  assert.deepEqual(JSON.parse(JSON.stringify(opened)), { ok: true, tabId: 4 });
  assert.equal(reads, 2);
  assert.deepEqual(JSON.parse(JSON.stringify(sessionStorage.companionTargetsV1)), [{
    companionTabID: 9,
    targetTabID: 4,
    targetURL: "https://www.youtube.com/watch?v=abc",
  }]);
});

test("rejects non-video YouTube pages before opening the companion", async function () {
  const harness = workerHarness(null);
  const home = { id: 4, url: "https://www.youtube.com/", status: "complete" };
  let windowsCreated = 0;
  harness.chrome.tabs.query = async function () { return [home]; };
  harness.chrome.windows.create = async function () { windowsCreated += 1; };

  await assert.rejects(
    harness.operations.openSubtitleBrowser({ tab: home, url: home.url }),
    (error) => error.status === 400,
  );
  await assert.rejects(
    harness.operations.openSubtitleBrowser({
      tab: { id: 4, url: "https://www.youtube.com/watch" },
      url: "https://www.youtube.com/watch",
    }),
    (error) => error.status === 400,
  );

  assert.equal(windowsCreated, 0);
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

test("keeps complete bounded transcripts instead of the old 300-line history window", function () {
  const harness = workerHarness(null);
  const lines = Array.from({ length: 400 }, function (_unused, index) {
    return {
      id: 1000000 + index,
      text: "字幕" + index,
      sourcePositionMs: index * 1000,
      classification: "ready",
      unknowns: []
    };
  });

  const session = harness.operations.sanitizeSubtitleSession({
    revision: 3,
    transcriptState: "ready",
    transcriptSource: "full",
    comprehension: {
      known_occurrences: 300,
      total_occurrences: 400,
      unknown_unique: 100,
      excluded_names: 0,
      line_count: 400
    },
    lines
  });

  assert.equal(session.lines.length, 400);
  assert.equal(session.lines[0].text, "字幕0");
  assert.equal(session.lines[399].text, "字幕399");
  assert.equal(session.comprehension.line_count, 400);
});

test("preserves bounded subtitle readings for the companion", function () {
  const harness = workerHarness(null);
  const session = harness.operations.sanitizeSubtitleSession({
    revision: 1,
    lines: [{
      id: 1,
      text: "読む",
      classification: "ready",
      unknowns: [{ surface: "読む", expression: "読む", reading: "よむ", start: 0, end: 2 }]
    }]
  });

  assert.equal(session.lines[0].unknowns[0].reading, "よむ");
});

test("keeps queued YouTube text successful", async function () {
  const client = {
    create() {
      return {
        async capture() { throw new TypeError("offline"); },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "secret" },
    undefined,
    client,
  );

  const delivery = await harness.operations.captureDirect({
    expression: "読む",
    contextText: "本を読む。",
    sourcePositionMs: 1000,
  }, {
    tab: { id: 5, title: "Lesson" },
    frameId: 0,
    url: "https://www.youtube.com/watch?v=abc",
  });

  assert.deepEqual(JSON.parse(JSON.stringify(delivery)), { queued: true });
  assert.equal(harness.storage.captureOutboxV2.length, 1);
  assert.match(harness.storage.captureOutboxV2[0].payload.capture_nonce, /^[0-9a-f]{32}$/u);
});

test("the analyze command runs the existing manual active-page analysis", async function () {
  const harness = workerHarness(null);
  const tab = { id: 9, url: "https://reader.example/article", status: "complete" };
  let manualMarkers = 0;
  let analyses = 0;
  harness.chrome.tabs.query = async function (query) {
    assert.deepEqual(JSON.parse(JSON.stringify(query)), { active: true, currentWindow: true });
    return [tab];
  };
  harness.chrome.scripting.executeScript = async function (input) {
    if (input.args && input.args[0] === "__goiCoverageSourceV1") {
      manualMarkers += 1;
      return [{ frameId: 0 }];
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

  await harness.events.command.dispatch("analyze-active-page");

  assert.equal(manualMarkers, 1);
  assert.equal(analyses, 1);
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
      assert.deepEqual(input.files, ["background/youtube-transcript-page.js"]);
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
      assert.deepEqual(input.files, ["background/youtube-transcript-page.js"]);
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

test("combines caption tracks from matching player responses", function () {
  const selected = workerHarness(null).operations.selectYouTubePlayerData({
    transcriptSource: "transcript",
    responses: [
      { videoID: "ad", tracks: [] },
      { videoID: "main", tracks: [{ baseUrl: "ja", languageCode: "ja" }] },
      { videoID: "main", tracks: [
        { baseUrl: "ja", languageCode: "ja" },
        { baseUrl: "en", languageCode: "en" }
      ] }
    ]
  }, "main");

  assert.deepEqual(JSON.parse(JSON.stringify(selected)), {
    videoID: "main",
    tracks: [
      { baseUrl: "ja", languageCode: "ja" },
      { baseUrl: "en", languageCode: "en" }
    ],
    transcriptSource: "transcript"
  });
});

test("reports when a YouTube video has no Japanese transcript", async function () {
  const harness = workerHarness(null);
  harness.chrome.scripting.executeScript = async function () {
    return [{ result: {
      videoID: "test",
      tracks: [{
        languageCode: "en",
        baseUrl: "https://www.youtube.com/api/timedtext?v=test&lang=en"
      }]
    } }];
  };

  const response = await harness.operations.getYouTubeTranscript({
    tab: { id: 4, url: "https://www.youtube.com/watch?v=test" },
    frameId: 0
  });

  assert.deepEqual(JSON.parse(JSON.stringify(response)), {
    ok: true,
    state: "unavailable",
    reason: "no_japanese_track"
  });
});

test("keyboard capture prefers the focused frame selection", function () {
  const harness = workerHarness(null);
  const capture = harness.operations.selectCollectedCapture([
    {
      frameId: 0,
      result: { focused: false, capture: { ok: true, capture: { expression: "stale" } } },
    },
    {
      frameId: 7,
      result: { focused: true, capture: { ok: true, capture: { expression: "focused" } } },
    },
  ]);

  assert.equal(capture.frameId, 7);
  assert.equal(capture.value.capture.expression, "focused");
});

test("reports rejected capture injection in the top frame", async function () {
  const harness = workerHarness(null);
  const calls = [];
  harness.chrome.scripting.executeScript = async function (input) {
    calls.push(input);
    if (calls.length === 1) {
      throw new Error("page rejects injection");
    }
    return [];
  };

  await harness.operations.performCaptureFromTab(7, {
    frameIds: [9],
    fallbackSelection: "",
  });

  assert.equal(calls.length, 2);
  assert.deepEqual(Array.from(calls[1].args), ["error", "unavailable_page"]);
  assert.deepEqual(JSON.parse(JSON.stringify(calls[1].target)), {
    tabId: 7,
    frameIds: [0],
  });
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

  await harness.operations.retryCaptureOutbox(true);

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

test("queued local capture keeps its text payload and origin", async function () {
  const client = {
    create() {
      return {
        async capture() {
          throw new TypeError("offline");
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
    tab: { id: 12 },
    frameId: 0,
    url: "chrome-extension://goi-test/player/player.html",
  };

  const captured = await harness.operations.captureFromPlayer({
    sessionID: "player_session_123456",
    capture: {
      expression: "読む",
      contextText: "本を読む。",
      sourceTitle: "lesson.webm",
      sourcePositionMs: 1200,
    },
  }, sender);

  assert.deepEqual(JSON.parse(JSON.stringify(captured)), { queued: true });
  assert.equal(harness.storage.captureOutboxV2[0].baseUrl, "https://goi.example");
  assert.match(harness.storage.captureOutboxV2[0].payload.capture_nonce, /^[0-9a-f]{32}$/u);
  assert.equal(harness.storage.captureOutboxV2[0].payload.expression, "読む");
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

test("a page without Japanese renders an empty coverage result without calling the API", async function () {
  let apiCalls = 0;
  const client = {
    create() {
      return {
        async coverage() {
          apiCalls += 1;
        },
      };
    },
  };
  const harness = workerHarness(
    { baseUrl: "https://goi.example", token: "token" },
    undefined,
    client,
  );
  const calls = [];
  harness.chrome.scripting.executeScript = async function (input) {
    calls.push(input);
    if (calls.length === 1) {
      return [{ frameId: 0, result: true }];
    }
    if (calls.length === 2) {
      return [{
        frameId: 0,
        result: {
          analysisID: input.args[0],
          url: "https://reader.example/",
          blocks: [],
        },
      }];
    }
    return [{ frameId: 0, result: true }];
  };

  const result = await harness.operations.analyzeTab(12);

  assert.equal(apiCalls, 0);
  assert.equal(result.summary.total_occurrences, 0);
  assert.equal(calls.length, 3);
  assert.equal(calls[2].args[2].summary.total_occurrences, 0);
});

test("clears the badge when a page changes before coverage can render", async function () {
  const client = {
    create() {
      return {
        async coverage(blocks) {
          return {
            summary: {
              known_occurrences: 1,
              total_occurrences: 1,
              unknown_unique: 0,
              excluded_names: 0,
            },
            blocks: [{ id: blocks[0].id, tokens: [] }],
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
  const badges = [];
  harness.chrome.action = {
    async setBadgeText(value) {
      badges.push(value);
    },
  };
  let callCount = 0;
  harness.chrome.scripting.executeScript = async function (input) {
    callCount += 1;
    if (callCount === 1) {
      return [{ frameId: 0, result: true }];
    }
    if (callCount === 2) {
      return [{
        frameId: 0,
        result: {
          analysisID: input.args[0],
          url: "https://reader.example/chapter-1",
          blocks: [{ id: 1, text: "猫" }],
        },
      }];
    }
    if (callCount === 3) {
      return [{ frameId: 0, result: false }];
    }
    return [{ frameId: 0, result: false }];
  };

  await assert.rejects(
    harness.operations.analyzeTab(12),
    (error) => error.status === 409,
  );

  assert.deepEqual(JSON.parse(JSON.stringify(badges)), [{ tabId: 12, text: "" }]);
});
