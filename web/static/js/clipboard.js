export async function copyTarget(target, clipboard, legacyCopy) {
  const value = "value" in target ? target.value : target.textContent || "";

  try {
    if (clipboard?.writeText) {
      await clipboard.writeText(value);
      return true;
    }
  } catch {
    // Older and non-secure browsers use the selection fallback below.
  }

  target.focus();
  target.select?.();
  try {
    return Boolean(legacyCopy?.());
  } catch {
    return false;
  }
}

export function initCopyButtons(root = document) {
  root.querySelectorAll("[data-copy-target]").forEach((button) => {
    if (button.dataset.copyBound === "true") {
      return;
    }
    button.dataset.copyBound = "true";
    button.addEventListener("click", async () => {
      const target = root.querySelector(button.dataset.copyTarget);
      if (!target) {
        return;
      }
      const copied = await copyTarget(
        target,
        navigator.clipboard,
        () => document.execCommand("copy")
      );
      const message = copied ? "Copied" : "Selected — copy manually";
      button.textContent = message;
      const statusSelector = button.dataset.copyStatus;
      const status = statusSelector ? root.querySelector(statusSelector) : null;
      if (status) {
        status.textContent = message;
      }
    });
  });
}
