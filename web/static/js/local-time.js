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
