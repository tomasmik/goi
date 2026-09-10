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
