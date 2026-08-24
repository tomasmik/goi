import assert from "node:assert/strict";
import test from "node:test";

import {
  initBackupSettings,
  initCurrentOrigins,
  initExampleProviderPresets,
  initLocalTimes,
  initCurrentTabs,
  initSettingsSaveBars,
  initTimeZoneControls,
  initTranslationProviderPanels,
} from "../static/js/settings-controls.js";

test("settings save bars appear only after a form value changes", () => {
  const listeners = {};
  const input = { name: "reviews", type: "number", value: "20", checked: false, disabled: false };
  const form = {
    elements: [input],
    addEventListener(type, listener) { listeners[type] = listener; },
  };
  const bar = {
    hidden: false,
    closest() { return form; },
  };
  initSettingsSaveBars({ querySelectorAll() { return [bar]; } });
  assert.equal(bar.hidden, true);

  input.value = "30";
  listeners.input();
  assert.equal(bar.hidden, false);

  input.value = "20";
  listeners.change();
  assert.equal(bar.hidden, true);
});

test("current tabs are scrolled into view", () => {
  let options;
  const current = { offsetLeft: 500, offsetWidth: 100, scrollIntoView(next) { options = next; } };
  const navigation = { scrollLeft: 0, clientWidth: 390, querySelector() { return current; } };
  initCurrentTabs({ querySelectorAll() { return [navigation]; } });
  assert.deepEqual(options, { block: "nearest", inline: "center" });
});

test("connected-browser times use Goi's configured timezone", () => {
  let options;
  const time = {
    dateTime: "2026-08-06T12:00:00Z",
    dataset: { timeZone: "Europe/Vilnius" },
    textContent: "",
  };
  const intl = {
    DateTimeFormat: function (_locale, nextOptions) {
      options = nextOptions;
      return { format() { return "Aug 6, 2026, 3:00 PM"; } };
    },
  };
  initLocalTimes({ querySelectorAll() { return [time]; } }, intl);
  assert.equal(options.timeZone, "Europe/Vilnius");
  assert.equal(time.textContent, "Aug 6, 2026, 3:00 PM");
});

test("backup controls follow the daily backup toggle", () => {
  const listeners = {};
  const toggle = {
    checked: false,
    addEventListener(type, listener) { listeners[`toggle:${type}`] = listener; },
  };
  const controls = [{ disabled: false }, { disabled: false }];
  const form = {
    querySelector() { return toggle; },
    querySelectorAll() { return controls; },
    addEventListener(type, listener) { listeners[`form:${type}`] = listener; },
  };
  initBackupSettings({ querySelectorAll() { return [form]; } });
  assert.deepEqual(controls.map((control) => control.disabled), [true, true]);

  toggle.checked = true;
  listeners["toggle:change"]();
  assert.deepEqual(controls.map((control) => control.disabled), [false, false]);

  toggle.checked = false;
  listeners["toggle:change"]();
  listeners["form:submit"]();
  assert.deepEqual(controls.map((control) => control.disabled), [false, false]);
});

test("a completed backup refreshes the available backup list", async () => {
  let poll;
  let reloads = 0;
  const status = { textContent: "A backup is queued." };
  const button = { disabled: true };
  const panel = {
    dataset: { backupStateUrl: "/settings/backups/state" },
    querySelector(selector) { return selector.includes("status") ? status : button; },
  };
  const root = {
    querySelectorAll(selector) { return selector === "[data-backup-live]" ? [panel] : []; },
  };
  const environment = {
    async fetch() {
      return {
        ok: true,
        async json() { return { busy: false, state: { status: "success" } }; },
      };
    },
    setTimeout(callback) { poll = callback; return 1; },
    clearTimeout() {},
    addEventListener() {},
    location: { reload() { reloads++; } },
  };

  initBackupSettings(root, environment);
  await poll();
  assert.equal(reloads, 1);
});

test("timezone detection fills the browser timezone", () => {
  let click;
  let inputListener;
  const input = {
    id: "time-zone",
    value: "UTC",
    getAttribute() { return null; },
    addEventListener(type, listener) { if (type === "input") inputListener = listener; },
  };
  const current = { textContent: "UTC" };
  const button = {
    addEventListener(type, listener) { if (type === "click") click = listener; },
  };
  const root = {
    querySelectorAll() { return [input]; },
    querySelector(selector) { return selector.includes("current") ? current : button; },
  };
  const intl = {
    DateTimeFormat() {
      return { resolvedOptions() { return { timeZone: "Europe/Vilnius" }; } };
    },
  };
  initTimeZoneControls(root, intl);
  click();
  assert.equal(input.value, "Europe/Vilnius");
  assert.equal(current.textContent, "Europe/Vilnius");

  input.value = "Asia/Tokyo";
  inputListener();
  assert.equal(current.textContent, "Asia/Tokyo");
});

test("provider presets fill editable connection fields", () => {
  let change;
  const baseURL = { value: "", focus() {} };
  const model = { value: "" };
  const form = {
    querySelector(selector) { return selector.includes("base-url") ? baseURL : model; },
  };
  const select = {
    selectedOptions: [{ value: "ollama", dataset: { baseUrl: "http://127.0.0.1:11434/v1", model: "qwen3:4b" } }],
    closest() { return form; },
    addEventListener(type, listener) { if (type === "change") change = listener; },
  };
  initExampleProviderPresets({ querySelectorAll() { return [select]; } });
  change();
  assert.equal(baseURL.value, "http://127.0.0.1:11434/v1");
  assert.equal(model.value, "qwen3:4b");
});

test("OpenRouter preset uses its API base URL and model hint", () => {
  let change;
  const baseURL = { value: "", focus() {}, closest() { return null; } };
  const model = { value: "old-model", placeholder: "qwen3:4b" };
  const form = {
    querySelector(selector) { return selector.includes("base-url") ? baseURL : model; },
  };
  const select = {
    options: [],
    selectedOptions: [{
      value: "openrouter",
      dataset: {
        baseUrl: "https://openrouter.ai/api/v1",
        model: "",
        modelPlaceholder: "provider/model",
      },
    }],
    closest() { return form; },
    addEventListener(type, listener) { if (type === "change") change = listener; },
  };

  initExampleProviderPresets({ querySelectorAll() { return [select]; } });
  change();

  assert.equal(baseURL.value, "https://openrouter.ai/api/v1");
  assert.equal(model.value, "");
  assert.equal(model.placeholder, "provider/model");
});

test("provider presets recognize an existing local connection", () => {
  const baseURL = { value: "http://127.0.0.1:11434/v1", closest() { return null; } };
  const model = { value: "qwen3:4b" };
  const options = [
    { value: "custom", dataset: {} },
    { value: "ollama", dataset: { baseUrl: "http://127.0.0.1:11434/v1", model: "qwen3:4b" } },
  ];
  const form = {
    querySelector(selector) { return selector.includes("base-url") ? baseURL : model; },
  };
  const select = {
    options,
    value: "custom",
    closest() { return form; },
    addEventListener() {},
  };

  initExampleProviderPresets({ querySelectorAll() { return [select]; } });
  assert.equal(select.value, "ollama");
});

test("translation settings show only the selected provider panel", () => {
  let selectMicrosoft;
  const providers = [
    { checked: true, value: "none", addEventListener() {} },
    {
      checked: false,
      value: "microsoft",
      addEventListener(type, listener) { if (type === "change") selectMicrosoft = listener; },
    },
  ];
  const microsoft = {
    dataset: { translationProviderPanel: "microsoft" },
    hidden: false,
  };
  const form = {
    querySelectorAll(selector) {
      return selector.includes("translation_provider") ? providers : [microsoft];
    },
  };

  initTranslationProviderPanels({ querySelectorAll() { return [form]; } });
  assert.equal(microsoft.hidden, true);

  providers[0].checked = false;
  providers[1].checked = true;
  selectMicrosoft();
  assert.equal(microsoft.hidden, false);
});

test("current origin fields show the address open in the browser", () => {
  const input = { value: "" };
  initCurrentOrigins({ querySelectorAll() { return [input]; } }, { origin: "https://goi.example" });
  assert.equal(input.value, "https://goi.example");
});
