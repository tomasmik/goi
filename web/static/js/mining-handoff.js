const limits = {
  expression: 256,
  context_text: 2000,
  source_title: 300,
};

function withinLimit(value, limit) {
  return typeof value === "string" && Array.from(value).length <= limit;
}

export function validPayload(payload, sourceOrigin) {
  if (!payload || payload.type !== "goi:mining-capture" || payload.version !== 1) {
    return false;
  }
  if (
    !withinLimit(payload.expression, limits.expression) ||
    payload.expression.trim() === "" ||
    !withinLimit(payload.context_text, limits.context_text) ||
    !withinLimit(payload.source_title, limits.source_title)
  ) {
    return false;
  }
  if (typeof payload.source_url !== "string" || new TextEncoder().encode(payload.source_url).length > 2048) {
    return false;
  }
  if (payload.source_kind !== "web" && payload.source_kind !== "video") {
    return false;
  }
  try {
    const sourceURL = new URL(payload.source_url);
    if ((sourceURL.protocol !== "http:" && sourceURL.protocol !== "https:") || sourceURL.origin !== sourceOrigin) {
      return false;
    }
  } catch {
    return false;
  }
  if (typeof payload.source_position_seconds !== "string") {
    return false;
  }
  const position = payload.source_position_seconds.trim();
  return position === "" || (Number.isFinite(Number(position)) && Number(position) >= 0);
}

export function initMiningCapture(root = document, browser = window) {
  const form = root.querySelector("[data-mining-capture-form]");
  if (!form || !browser.opener || form.dataset.miningCaptureBound === "true") {
    return null;
  }

  const fields = {
    expression: form.querySelector("[data-mining-expression]"),
    context: form.querySelector("[data-mining-context]"),
    title: form.querySelector("[data-mining-title]"),
    url: form.querySelector("[data-mining-url]"),
    sourceKind: form.querySelector("[data-mining-source-kind]"),
    position: form.querySelector("[data-mining-position]"),
  };
  if (Object.values(fields).some((field) => !field)) {
    return null;
  }

  form.dataset.miningCaptureBound = "true";
  const message = root.querySelector("[data-mining-message]");

  function stop() {
    browser.removeEventListener("message", receiveCapture);
    browser.removeEventListener("pagehide", stop);
  }

  function receiveCapture(event) {
    if (event.source !== browser.opener) {
      return;
    }
    if (!validPayload(event.data, event.origin)) {
      if (message) {
        message.textContent = "Capture data was rejected. Paste the word manually.";
      }
      return;
    }

    fields.expression.value = event.data.expression;
    fields.context.value = event.data.context_text;
    fields.title.value = event.data.source_title;
    fields.url.value = event.data.source_url;
    fields.sourceKind.value = event.data.source_kind;
    fields.position.value = event.data.source_position_seconds;
    if (message) {
      message.textContent = "Capture loaded. Review it, then save to your inbox.";
    }
    fields.expression.focus();
    stop();
  }

  browser.addEventListener("message", receiveCapture);
  browser.addEventListener("pagehide", stop, { once: true });
  browser.opener.postMessage({ type: "goi:mining-ready", version: 1 }, "*");
  return { stop };
}
