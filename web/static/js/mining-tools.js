function updateBulkSummary(form) {
  const selected = Array.from(form.querySelectorAll('input[name="capture_id"]:checked'));
  const summary = form.querySelector("[data-mining-selection-summary]");
  const toolbar = form.querySelector("[data-mining-bulk-toolbar]");
  const ready = selected.filter((input) => input.dataset.clearMatch === "true").length;
  toolbar?.classList.toggle("is-active", selected.length > 0);
  form.querySelectorAll("[data-mining-bulk-action]").forEach((button) => {
    button.disabled = button.hasAttribute("data-ready-only") ? ready === 0 : selected.length === 0;
  });
  if (!summary) {
    return;
  }
  if (!selected.length) {
    summary.textContent = "0 selected";
    return;
  }
  const review = selected.length - ready;
  summary.textContent = `${selected.length} selected · ${ready} ready · ${review} need review`;
}

export function initMiningTools(root = document, environment = window) {
  const bookmarkletOrigin = root.querySelector("[data-bookmarklet-origin]");
  const bookmarkletWarning = root.querySelector("[data-bookmarklet-origin-warning]");
  if (bookmarkletOrigin && bookmarkletWarning && bookmarkletOrigin.textContent.trim() !== environment.location.origin) {
    bookmarkletWarning.hidden = false;
  }

  const filters = root.querySelector("[data-mining-filters]");
  if (filters) {
    filters.querySelector("select")?.addEventListener("change", () => filters.requestSubmit());
  }

  const bulk = root.querySelector("[data-mining-bulk]");
  if (!bulk) {
    return;
  }
  bulk.querySelector("[data-mining-select-all]")?.addEventListener("click", () => {
    bulk.querySelectorAll('input[name="capture_id"]').forEach((input) => {
      input.checked = true;
    });
    updateBulkSummary(bulk);
  });
  bulk.querySelector("[data-mining-clear]")?.addEventListener("click", () => {
    bulk.querySelectorAll('input[name="capture_id"]:checked').forEach((input) => {
      input.checked = false;
    });
    updateBulkSummary(bulk);
  });
  bulk.addEventListener("change", () => updateBulkSummary(bulk));
  updateBulkSummary(bulk);
}
