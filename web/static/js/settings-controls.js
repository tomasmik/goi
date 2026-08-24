export function initBackupSettings(root = document, environment = globalThis) {
  for (const form of root.querySelectorAll("[data-backup-settings]")) {
    const toggle = form.querySelector("[data-backup-enabled]");
    const controls = [...form.querySelectorAll("[data-backup-dependent]")];
    if (!toggle || controls.length === 0) {
      continue;
    }
    const sync = () => {
      for (const control of controls) {
        control.disabled = !toggle.checked;
      }
    };
    toggle.addEventListener("change", sync);
    form.addEventListener("submit", () => {
      for (const control of controls) {
        control.disabled = false;
      }
    });
    sync();
  }
  for (const panel of root.querySelectorAll("[data-backup-live]")) {
    const status = panel.querySelector("[data-backup-status]");
    const button = panel.querySelector("[data-backup-run]");
    const url = panel.dataset?.backupStateUrl;
    if (!status || !button || !url || !button.disabled) {
      continue;
    }
    let timer;
    const poll = async () => {
      try {
        const response = await environment.fetch(url, { headers: { Accept: "application/json" } });
        if (!response.ok) {
          throw new Error("status request failed");
        }
        const result = await response.json();
        const state = result.state || {};
        if (result.busy || state.Status === "running" || state.status === "running") {
          status.textContent = "Backup is running…";
          timer = environment.setTimeout(poll, 1000);
          return;
        }
        const stateStatus = state.Status || state.status;
        if (stateStatus === "success") {
          environment.location.reload();
          return;
        }
        status.textContent = state.Error || state.error || "Backup did not complete.";
        button.disabled = false;
      } catch (_error) {
        status.textContent = "Could not refresh backup status. Reload this page to check it.";
        button.disabled = false;
      }
    };
    timer = environment.setTimeout(poll, 500);
    environment.addEventListener("pagehide", () => environment.clearTimeout(timer), { once: true });
  }
}

export function initLocalTimes(root = document, intl = Intl) {
  for (const element of root.querySelectorAll("time[data-local-time]")) {
    const date = new Date(element.dateTime);
    if (!Number.isNaN(date.getTime())) {
      element.textContent = new intl.DateTimeFormat(undefined, {
        dateStyle: "medium",
        timeStyle: "short",
        timeZone: element.dataset.timeZone || undefined,
      }).format(date);
    }
  }
}

export function initTimeZoneControls(root = document, intl = Intl) {
  for (const input of root.querySelectorAll("[data-time-zone]")) {
    const current = root.querySelector(`[data-time-zone-current="${input.id}"]`);
    const showCurrent = () => {
      if (current) {
        current.textContent = input.value;
      }
    };
    const listID = input.getAttribute("list");
    const list = listID ? root.getElementById(listID) : null;
    if (list && typeof intl.supportedValuesOf === "function") {
      const fragment = root.createDocumentFragment();
      for (const zone of intl.supportedValuesOf("timeZone")) {
        const option = root.createElement("option");
        option.value = zone;
        fragment.append(option);
      }
      list.replaceChildren(fragment);
    }
    const button = root.querySelector(`[data-time-zone-detect="${input.id}"]`);
    button?.addEventListener("click", () => {
      const zone = intl.DateTimeFormat().resolvedOptions().timeZone;
      if (zone) {
        input.value = zone;
        showCurrent();
        input.dispatchEvent?.(new Event("input", { bubbles: true }));
      }
    });
    input.addEventListener?.("input", showCurrent);
  }
}

export function initExampleProviderPresets(root = document) {
  for (const select of root.querySelectorAll("[data-example-provider-preset]")) {
    const form = select.closest("form");
    const baseURL = form?.querySelector("[data-example-base-url]");
    const model = form?.querySelector("[data-example-model]");
    if (!baseURL || !model) {
      continue;
    }
    const defaultModelPlaceholder = model.placeholder;
    const connectionDetails = baseURL.closest?.("details");
    const matchingPreset = Array.from(select.options || []).find((option) =>
      option.value !== "custom" && option.dataset.baseUrl === baseURL.value
    );
    if (matchingPreset) {
      select.value = matchingPreset.value;
    }
    select.addEventListener("change", () => {
      const option = select.selectedOptions[0];
      if (!option) {
        return;
      }
      if (connectionDetails) {
        connectionDetails.open = true;
      }
      if (option.value === "custom") {
        model.placeholder = defaultModelPlaceholder;
        baseURL.focus();
        return;
      }
      baseURL.value = option.dataset.baseUrl || "";
      model.value = option.dataset.model || "";
      model.placeholder = option.dataset.modelPlaceholder || defaultModelPlaceholder;
    });
  }
}

export function initTranslationProviderPanels(root = document) {
  for (const form of root.querySelectorAll("[data-translation-settings]")) {
    const providers = [...form.querySelectorAll('input[name="translation_provider"]')];
    const panels = [...form.querySelectorAll("[data-translation-provider-panel]")];
    if (providers.length === 0 || panels.length === 0) {
      continue;
    }
    const sync = () => {
      const selected = providers.find((provider) => provider.checked)?.value;
      for (const panel of panels) {
        panel.hidden = panel.dataset.translationProviderPanel !== selected;
      }
    };
    for (const provider of providers) {
      provider.addEventListener("change", sync);
    }
    sync();
  }
}

export function initCurrentOrigins(root = document, location = window.location) {
  for (const input of root.querySelectorAll("[data-current-origin]")) {
    input.value = location.origin;
  }
  for (const warning of root.querySelectorAll("[data-origin-warning]")) {
    try {
      const configured = new URL(warning.dataset.configuredUrl, location.href);
      warning.hidden = configured.origin === location.origin;
    } catch (_error) {
      warning.hidden = false;
    }
  }
}

function formValue(form) {
  return Array.from(form.elements || [])
    .filter((control) => control.name && !control.disabled && !["submit", "button", "reset"].includes(control.type))
    .map((control) => `${control.name}:${control.type === "checkbox" || control.type === "radio" ? control.checked : control.value}`)
    .join("\n");
}

export function initSettingsSaveBars(root = document) {
  for (const bar of root.querySelectorAll(".settings-save-bar")) {
    const form = bar.closest("form");
    if (!form) {
      continue;
    }
    const initialValue = formValue(form);
    const sync = () => {
      bar.hidden = formValue(form) === initialValue;
    };
    form.addEventListener("input", sync);
    form.addEventListener("change", sync);
    form.addEventListener("reset", () => queueMicrotask(sync));
    sync();
  }
}

export function initCurrentTabs(root = document) {
  for (const tabList of root.querySelectorAll(".settings-navigation, .filter-tabs")) {
    const current = tabList.querySelector('[aria-current="page"]');
    if (!current) {
      continue;
    }
    const start = current.offsetLeft;
    const end = start + current.offsetWidth;
    if (start < tabList.scrollLeft || end > tabList.scrollLeft + tabList.clientWidth) {
      current.scrollIntoView?.({ block: "nearest", inline: "center" });
    }
  }
}
