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
    if (tabList.matches(".filter-tabs")) {
      tabList.scrollLeft = Math.max(0, current.offsetLeft - (tabList.clientWidth - current.offsetWidth) / 2);
      continue;
    }
    const start = current.offsetLeft;
    const end = start + current.offsetWidth;
    if (start < tabList.scrollLeft || end > tabList.scrollLeft + tabList.clientWidth) {
      current.scrollIntoView?.({ block: "nearest", inline: "center" });
    }
  }
}
