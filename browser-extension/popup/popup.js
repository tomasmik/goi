(function () {
  "use strict";

  const siteAutoControl = document.getElementById("site-auto-control");
  const siteAutoCheckbox = document.getElementById("site-auto");
  const siteAutoLabel = document.getElementById("site-auto-label");
  const siteAutoDetail = document.getElementById("site-auto-detail");
  const analyzeSection = document.getElementById("analyze-section");
  const localPlayerButton = document.getElementById("open-local-player");
  const subtitleBrowserButton = document.getElementById("open-subtitle-browser");
  const analyzeButton = document.getElementById("analyze-page");
  const status = document.getElementById("status");
  const outboxStatus = document.getElementById("outbox-status");
  const outboxDetails = document.getElementById("outbox-details");
  const outboxDetail = document.getElementById("outbox-detail");
  const outboxRetry = document.getElementById("outbox-retry");
  const outboxDiscard = document.getElementById("outbox-discard");
  const managedSites = document.getElementById("managed-sites");
  const managedSitesList = document.getElementById("managed-sites-list");
  const ignoredWords = document.getElementById("ignored-words");
  const ignoredWordsList = document.getElementById("ignored-words-list");
  const analyzeUnavailableReason = document.getElementById("analyze-unavailable-reason");
  const connectionSummary = document.getElementById("connection-summary");
  const connectionSettingsButton = document.getElementById("open-connection-settings");
  const youtubeSettings = document.getElementById("youtube-settings");
  const youtubeHoverLookup = document.getElementById("youtube-hover-lookup");
  const captureModel = globalThis.GoiExtension.captureModel;
  const popupModel = globalThis.GoiExtension.popupModel;
  let siteAutoState = {
    available: false,
    enabled: false,
    kind: "unavailable",
    origin: "",
    permissionPattern: "",
    videoAvailable: false
  };

  function message(type, extra) {
    return chrome.runtime.sendMessage({ type, version: 1, ...(extra || {}) });
  }

  function showStatus(text, error) {
    status.textContent = text;
    status.classList.toggle("error", Boolean(error));
  }

  function showOutboxStatus(response) {
    if (!response.ok) {
      outboxStatus.hidden = true;
      return;
    }
    const pending = Number.isSafeInteger(response.pending) ? response.pending : 0;
    outboxStatus.textContent = pending === 1
      ? "1 capture waiting to retry"
      : pending + " captures waiting to retry";
    outboxStatus.hidden = pending === 0;
    if (outboxDetails) {
      outboxDetails.hidden = pending === 0;
    }
    if (outboxDetail && pending > 0) {
      const destinations = Array.isArray(response.destinations) ? response.destinations.join(", ") : "the connected Goi server";
      const oldest = Number.isFinite(response.oldestAt)
        ? new Date(response.oldestAt).toLocaleString()
        : "unknown";
      outboxDetail.textContent = "Destination: " + destinations + " · oldest queued: " + oldest + ".";
    }
  }

  function renderManagedSites(response) {
    if (!managedSites || !managedSitesList || !response || !response.ok || !Array.isArray(response.origins)) {
      return;
    }
    managedSites.hidden = response.origins.length === 0;
    managedSitesList.replaceChildren();
    response.origins.forEach(function (origin) {
      const item = document.createElement("li");
      const label = document.createElement("span");
      label.textContent = origin;
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "quiet";
      remove.textContent = "Disable";
      remove.addEventListener("click", async function () {
        remove.disabled = true;
        const result = await message("goi.site-auto.remove", { origin: origin, revokePermission: true });
        if (!result || !result.ok) {
          remove.disabled = false;
          showStatus("Could not disable automatic analysis for " + origin + ".", true);
          return;
        }
        renderManagedSites(result);
      });
      item.append(label, remove);
      managedSitesList.appendChild(item);
    });
  }

  function renderIgnoredWords(response) {
    if (!ignoredWords || !ignoredWordsList || !response || !response.ok || !Array.isArray(response.words)) return;
    ignoredWords.hidden = response.words.length === 0;
    ignoredWordsList.replaceChildren();
    response.words.forEach(function (word) {
      const item = document.createElement("li");
      const label = document.createElement("span");
      label.textContent = word;
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "quiet";
      remove.textContent = "Remove";
      remove.addEventListener("click", async function () {
        const result = await message("goi.coverage.ignore.remove", { word: word });
        if (result?.ok) renderIgnoredWords(result);
      });
      item.append(label, remove);
      ignoredWordsList.appendChild(item);
    });
  }

  function prioritizeCurrentContext(kind) {
    const localSection = localPlayerButton && localPlayerButton.closest?.("section");
    const analyzeSection = analyzeButton && analyzeButton.closest?.("section");
    if (!localSection || !analyzeSection) return;
    if (kind === "youtube" && youtubeSettings?.parentNode) {
      youtubeSettings.parentNode.insertBefore(youtubeSettings, localSection);
    } else if (kind === "web" && analyzeSection.parentNode) {
      analyzeSection.parentNode.insertBefore(analyzeSection, localSection);
    }
  }

  function applySiteAutoResponse(response) {
    const available = Boolean(response && response.ok && response.available);
    siteAutoControl.hidden = !available;
    siteAutoCheckbox.disabled = !available;
    analyzeButton.disabled = !available;
    analyzeButton.textContent = available ? "Read with Goi" : "This page can’t be analyzed";
    analyzeButton.title = available ? "" : String(response?.reason || "This browser page does not allow extension analysis.");
    if (analyzeUnavailableReason) {
      analyzeUnavailableReason.hidden = available;
      analyzeUnavailableReason.textContent = available ? "" : analyzeButton.title;
    }
    subtitleBrowserButton.hidden = true;
    if (youtubeSettings) {
      youtubeSettings.hidden = true;
    }
    if (!available) {
      if (analyzeSection) analyzeSection.hidden = false;
      prioritizeCurrentContext("unavailable");
      return;
    }

    siteAutoState = {
      available: true,
      enabled: Boolean(response.enabled),
      kind: response.kind === "youtube" ? "youtube" : "web",
      videoAvailable: response.kind === "youtube" && response.videoAvailable !== false,
      origin: typeof response.origin === "string" ? response.origin : "",
      permissionPattern: typeof response.permissionPattern === "string"
        ? response.permissionPattern
        : ""
    };
    siteAutoCheckbox.checked = siteAutoState.enabled;
    if (siteAutoState.kind === "youtube") {
      if (youtubeSettings) {
        youtubeSettings.hidden = false;
      }
      subtitleBrowserButton.hidden = !siteAutoState.videoAvailable;
      siteAutoLabel.textContent = "Use Goi on YouTube";
      siteAutoDetail.textContent = "Collect captions for mining and coverage.";
      if (youtubeSettings && typeof youtubeSettings.appendChild === "function") {
        youtubeSettings.appendChild(siteAutoControl);
      }
      if (analyzeSection) analyzeSection.hidden = true;
      prioritizeCurrentContext("youtube");
      return;
    }
    if (analyzeSection) {
      analyzeSection.hidden = false;
      if (typeof analyzeSection.appendChild === "function") analyzeSection.appendChild(siteAutoControl);
    }
    siteAutoLabel.textContent = "Enable automatically on this site";
    siteAutoDetail.textContent = "Analyze this site when it loads.";
    prioritizeCurrentContext("web");
  }

  async function load() {
    const [connection, outboxResponse, siteAutoResponse, managedSitesResponse, ignoredWordsResponse, settingsResponse] = await Promise.all([
      popupModel.callSafely(function () {
        return message("goi.connection.get");
      }),
      popupModel.callSafely(function () {
        return message("goi.capture.outbox-status");
      }),
      popupModel.callSafely(function () {
        return message("goi.site-auto.get");
      }),
      popupModel.callSafely(function () {
        return message("goi.site-auto.list");
      }),
      popupModel.callSafely(function () {
        return message("goi.coverage.ignore.list");
      }),
      popupModel.callSafely(function () {
        return message("goi.settings.get");
      })
    ]);
    showOutboxStatus(outboxResponse);
    renderManagedSites(managedSitesResponse);
    renderIgnoredWords(ignoredWordsResponse);
    applySiteAutoResponse(siteAutoResponse);
    if (youtubeHoverLookup) {
      youtubeHoverLookup.checked = Boolean(settingsResponse.ok && settingsResponse.settings?.hoverLookupEnabled);
    }
    if (connection.ok && connection.connection) {
      if (connectionSummary) {
        connectionSummary.textContent = connection.connection.connected
          ? "Connected to " + connection.connection.baseUrl
          : "Not connected";
        connectionSummary.classList.toggle("error", !connection.connection.connected);
      }
      connectionSettingsButton.textContent = connection.connection.connected ? "Change" : "Set up";
    } else {
      if (connectionSummary) {
        connectionSummary.textContent = "Not connected";
        connectionSummary.classList.add("error");
      }
      connectionSettingsButton.textContent = "Set up";
    }
    if (!connection.ok || !siteAutoResponse.ok) {
      showStatus("Some extension settings could not be loaded. Try reopening the popup.", true);
    }
  }

  if (chrome.commands?.getAll) {
    chrome.commands.getAll().then(function (commands) {
      const capture = commands.find(function (command) { return command.name === "capture-selection"; });
      const shortcut = document.getElementById("capture-shortcut");
      if (shortcut && capture?.shortcut) shortcut.textContent = capture.shortcut;
    }).catch(function () {
      const shortcut = document.getElementById("capture-shortcut");
      if (shortcut) shortcut.textContent = "the configured shortcut";
    });
  }

  outboxRetry?.addEventListener("click", async function () {
    outboxRetry.disabled = true;
    const response = await message("goi.capture.outbox.retry");
    outboxRetry.disabled = false;
    showOutboxStatus(response);
  });
  outboxDiscard?.addEventListener("click", async function () {
    if (!confirm("Discard all captures waiting to retry?")) return;
    const response = await message("goi.capture.outbox.discard");
    showOutboxStatus(response);
  });

  connectionSettingsButton.addEventListener("click", async function () {
    connectionSettingsButton.disabled = true;
    try {
      await chrome.runtime.openOptionsPage();
    } catch (_error) {
      connectionSettingsButton.disabled = false;
      showStatus("Could not open connection settings.", true);
    }
  });

  localPlayerButton.addEventListener("click", async function () {
    localPlayerButton.disabled = true;
    const response = await popupModel.callSafely(function () {
      return message("goi.player.open");
    });
    localPlayerButton.disabled = false;
    if (!response.ok) {
      showStatus("Could not open the local video player. Try again.", true);
      return;
    }
    showStatus("Player opened.", false);
  });

  analyzeButton.addEventListener("click", async function () {
    const button = analyzeButton;
    button.disabled = true;
    showStatus("Analyzing…", false);
    const response = await popupModel.callSafely(function () {
      return message("goi.coverage.analyze-page");
    });
    button.disabled = false;
    if (!response.ok || !response.summary) {
      showStatus("Page analysis failed. Check the Goi connection.", true);
      return;
    }
    const summary = response.summary;
    if (!summary.total_occurrences) {
      showStatus("No Japanese text found.", false);
      return;
    }
    const percent = captureModel.coveragePercent(
      summary.known_occurrences,
      summary.total_occurrences
    );
    const detail = summary.unknown_unique
      ? "Unknown words highlighted."
      : "No unknown words.";
    showStatus(percent + "% known · " + detail, false);
  });

  subtitleBrowserButton.addEventListener("click", async function () {
    subtitleBrowserButton.disabled = true;
    const response = await popupModel.callSafely(function () {
      return message("goi.companion.open");
    });
    subtitleBrowserButton.disabled = false;
    if (!response.ok) {
      showStatus("Open a YouTube video before opening the transcript browser.", true);
      return;
    }
    showStatus("Transcript and mining opened.", false);
  });

  youtubeHoverLookup?.addEventListener("change", async function () {
    const enabled = youtubeHoverLookup.checked;
    youtubeHoverLookup.disabled = true;
    const response = await popupModel.callSafely(function () {
      return message("goi.settings.patch", { patch: { hoverLookupEnabled: enabled } });
    });
    youtubeHoverLookup.disabled = false;
    if (!response.ok || !response.settings) {
      youtubeHoverLookup.checked = !enabled;
      showStatus("Could not save hover lookup.", true);
      return;
    }
    youtubeHoverLookup.checked = Boolean(response.settings.hoverLookupEnabled);
    showStatus(youtubeHoverLookup.checked
      ? "Definitions will open after a short hover."
      : "Hover definitions are off.", false);
  });

  siteAutoCheckbox.addEventListener("change", async function () {
    const previousEnabled = siteAutoState.enabled;
    const enabled = siteAutoCheckbox.checked;
    if (!siteAutoState.available) {
      siteAutoCheckbox.checked = previousEnabled;
      return;
    }

    siteAutoCheckbox.disabled = true;
    let permission;
    let newlyGranted = false;
    if (enabled && siteAutoState.kind === "web") {
      permission = { origins: [siteAutoState.permissionPattern] };
      try {
        const alreadyGranted = await chrome.permissions.contains(permission);
        if (!alreadyGranted) {
          const granted = await chrome.permissions.request(permission);
          if (!granted) {
            siteAutoCheckbox.checked = previousEnabled;
            showStatus("Site access denied. Automatic analysis is off.", true);
            siteAutoCheckbox.disabled = false;
            return;
          }
          newlyGranted = true;
        }
      } catch (_) {
        siteAutoCheckbox.checked = previousEnabled;
        showStatus("Site access failed. Automatic analysis is off.", true);
        siteAutoCheckbox.disabled = false;
        return;
      }
    }

    const response = await popupModel.callSafely(function () {
      return message("goi.site-auto.set", { enabled: enabled });
    });
    if (!response.ok) {
      if (newlyGranted) {
        try {
          await chrome.permissions.remove(permission);
        } catch (_) {
          // Report the worker failure, not permission cleanup.
        }
      }
      siteAutoCheckbox.checked = previousEnabled;
      siteAutoCheckbox.disabled = false;
      showStatus("Could not save this setting. The previous value was restored.", true);
      return;
    }

    applySiteAutoResponse(response);
    siteAutoCheckbox.disabled = false;
    if (siteAutoState.kind === "youtube") {
      showStatus(siteAutoState.enabled
        ? "Enabled on YouTube."
        : "Disabled on YouTube.", false);
      return;
    }
    showStatus(siteAutoState.enabled
      ? "Enabled on this site."
      : "Disabled on this site.", false);
  });

  load().catch(function () {
    showStatus("Could not load extension settings.", true);
  });
})();
