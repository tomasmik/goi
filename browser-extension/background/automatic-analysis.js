async function getSiteAutoStatus() {
  const tab = await getActiveTab();
  const reason = unavailablePageReason(tab);
  if (reason) {
    return { ...unavailableSiteStatus(true), reason };
  }
  const context = siteContextForTab(tab);
  if (!context) {
    return unavailableSiteStatus(true);
  }
  if (context.kind === "youtube") {
    const settings = await getSettings();
    return publicSiteStatus(context, settings.overlayEnabled);
  }
  const origins = await getSiteAutoOrigins();
  const enabled = origins.has(context.origin) && await hasSitePermission(context.permissionPattern);
  return publicSiteStatus(context, enabled);
}

function unavailablePageReason(tab) {
  if (!tab || typeof tab.url !== "string") {
    return "No active browser page is available.";
  }
  let pageURL;
  try {
    pageURL = new URL(tab.url);
  } catch (_error) {
    return "This page address cannot be inspected.";
  }
  if (pageURL.protocol === "file:") {
    return "Use Goi’s local video player for files from this computer.";
  }
  if (pageURL.protocol !== "http:" && pageURL.protocol !== "https:") {
    return "Browser settings and internal pages do not allow extension analysis.";
  }
  if (pageURL.pathname.toLocaleLowerCase().endsWith(".pdf")) {
    return "Chrome’s PDF viewer does not expose document text to Goi.";
  }
  return "";
}

async function setSiteAutoEnabled(enabled) {
  const tab = await getActiveTab();
  const context = siteContextForTab(tab);
  if (!context) {
    return unavailableSiteStatus(false, "unavailable_page");
  }
  if (typeof enabled !== "boolean") {
    return {
      ...publicSiteStatus(context, false),
      ok: false,
      errorCode: "invalid_request"
    };
  }
  if (context.kind === "youtube") {
    const settings = await updateYouTubeOverlay(context, enabled);
    return publicSiteStatus(context, settings.overlayEnabled);
  }
  if (enabled && !await hasSitePermission(context.permissionPattern)) {
    return {
      ...publicSiteStatus(context, false),
      ok: false,
      errorCode: "permission_required"
    };
  }

  const origins = await updateSiteAutoOrigin(context.origin, enabled);
  if (enabled) {
    clearAutomaticOriginRecords(context.origin, false);
    if (!origins.added) {
      await stopAutomaticCoverage(context.tabId, context.origin);
    }
    await scheduleAutomaticAnalysis(context.tabId);
  } else {
    await disableAutomaticOrigin(context);
  }
  return publicSiteStatus(context, enabled);
}

async function getSiteAutoOrigins() {
  const stored = await chrome.storage.local.get(SITE_AUTO_STORAGE_KEY);
  const values = stored[SITE_AUTO_STORAGE_KEY];
  if (!Array.isArray(values)) {
    return new Set();
  }
  return new Set(values.map(normalizeSiteOrigin).filter(Boolean));
}

async function listSiteAutoOrigins() {
  return { ok: true, origins: Array.from(await getSiteAutoOrigins()).sort() };
}

async function removeSiteAutoOrigin(value, revokePermission) {
  const origin = normalizeSiteOrigin(value);
  if (!origin) {
    const error = new Error("Invalid site origin");
    error.code = "invalid_request";
    throw error;
  }
  await updateSiteAutoOrigin(origin, false);
  clearAutomaticOriginRecords(origin, true);
  if (revokePermission) {
    const pageURL = new URL(origin);
    await chrome.permissions.remove({ origins: [pageURL.protocol + "//" + pageURL.hostname + "/*"] });
  }
  return listSiteAutoOrigins();
}

function normalizeSiteOrigin(value) {
  if (typeof value !== "string") {
    return "";
  }
  try {
    const pageURL = new URL(value);
    return pageURL.protocol === "https:" || pageURL.protocol === "http:"
      ? pageURL.origin
      : "";
  } catch (_error) {
    return "";
  }
}

function updateSiteAutoOrigin(origin, enabled) {
  const update = siteAutoUpdateQueue.then(async function () {
    const origins = await getSiteAutoOrigins();
    const wasEnabled = origins.has(origin);
    if (enabled) {
      origins.add(origin);
    } else {
      origins.delete(origin);
    }
    await protectLocalStorage();
    if (origins.size) {
      await chrome.storage.local.set({
        [SITE_AUTO_STORAGE_KEY]: Array.from(origins).sort()
      });
    } else {
      await chrome.storage.local.remove(SITE_AUTO_STORAGE_KEY);
    }
    return { added: enabled && !wasEnabled };
  });
  siteAutoUpdateQueue = recoverQueue(update);
  return update;
}

async function hasSitePermission(permissionPattern) {
  try {
    return Boolean(await chrome.permissions.contains({ origins: [permissionPattern] }));
  } catch (_error) {
    return false;
  }
}

function clearAutomaticOriginRecords(origin, cancelCoverage) {
  for (const [tabId, attempt] of automaticAttemptsByTab) {
    if (attempt.origin === origin) {
      automaticAttemptsByTab.delete(tabId);
    }
  }
  if (!cancelCoverage) {
    return [];
  }
  const coverageTabs = [];
  for (const [tabId, coverage] of automaticCoverageByTab) {
    if (coverage.origin !== origin) {
      continue;
    }
    coverage.cancelled = cancelCoverage;
    automaticCoverageByTab.delete(tabId);
    coverageTabs.push(tabId);
  }
  return coverageTabs;
}

async function disableAutomaticOrigin(context) {
  const coverageTabs = new Set(clearAutomaticOriginRecords(context.origin, true));
  coverageTabs.add(context.tabId);
  let tabs = [];
  try {
    tabs = await chrome.tabs.query({ url: [context.permissionPattern] });
  } catch (_error) {
    tabs = [];
  }
  tabs.forEach(function (tab) {
    const tabContext = siteContextForTab(tab);
    if (tabContext && tabContext.origin === context.origin) {
      coverageTabs.add(tab.id);
    }
  });
  await Promise.all(Array.from(coverageTabs, function (tabId) {
    return stopAutomaticCoverage(tabId, context.origin);
  }));
  await removeSitePermissionSafely(context.permissionPattern);
}

async function stopAutomaticCoverage(tabId, origin) {
  let stopped = false;
  try {
    const results = await chrome.scripting.executeScript({
      target: { tabId, frameIds: [0] },
      func: function (sourceKey, expectedOrigin) {
        const source = globalThis[sourceKey];
        if (!source || source.kind !== "automatic" || source.origin !== expectedOrigin) {
          return false;
        }
        delete globalThis[sourceKey];
        if (globalThis.GoiCoverage) {
          globalThis.GoiCoverage.stop();
        }
        return true;
      },
      args: [COVERAGE_SOURCE_KEY, origin]
    });
    stopped = Boolean(results[0] && results[0].result);
  } catch (_error) {
    return;
  }
  if (stopped) {
    await clearCoverageBadgeBestEffort(tabId);
  }
}

async function markManualCoverageBestEffort(tabId) {
  try {
    await chrome.scripting.executeScript({
      target: { tabId, frameIds: [0] },
      func: function (sourceKey) {
        globalThis[sourceKey] = {
          kind: "manual",
          origin: location.origin,
          url: location.href
        };
      },
      args: [COVERAGE_SOURCE_KEY]
    });
  } catch (_error) {
    return;
  }
}

async function claimAutomaticCoverage(context) {
  try {
    const results = await chrome.scripting.executeScript({
      target: { tabId: context.tabId, frameIds: [0] },
      func: function (sourceKey, expectedURL, expectedOrigin) {
        if (location.href !== expectedURL) {
          return "changed";
        }
        const source = globalThis[sourceKey];
        if (source && source.url === expectedURL) {
          return "existing";
        }
        globalThis[sourceKey] = {
          kind: "automatic",
          origin: expectedOrigin,
          url: expectedURL
        };
        return "claimed";
      },
      args: [COVERAGE_SOURCE_KEY, context.url, context.origin]
    });
    return results[0] && results[0].result;
  } catch (_error) {
    return "changed";
  }
}

function navigationState(tabId, url) {
  let state = tabNavigationStates.get(tabId);
  if (!state) {
    state = { revision: 0, url: url || "", loading: false };
    tabNavigationStates.set(tabId, state);
  }
  return state;
}

function handleAutomaticTabUpdate(tabId, changeInfo, tab) {
  const nextURL = typeof changeInfo.url === "string"
    ? changeInfo.url
    : tab && typeof tab.url === "string"
      ? tab.url
      : "";
  const state = navigationState(tabId, nextURL);
  if (changeInfo.status === "loading") {
    if (!state.loading) {
      state.revision += 1;
    }
    state.loading = true;
    if (nextURL) {
      state.url = nextURL;
    }
    automaticCoverageByTab.delete(tabId);
    return Promise.resolve();
  }
  if (typeof changeInfo.url === "string" && changeInfo.url !== state.url) {
    state.revision += 1;
    state.url = changeInfo.url;
    automaticCoverageByTab.delete(tabId);
  }
  if (changeInfo.status === "complete") {
    state.loading = false;
    if (nextURL) {
      state.url = nextURL;
    }
    return scheduleAutomaticAnalysis(tabId);
  }
  if (typeof changeInfo.url === "string" && tab && tab.status === "complete") {
    state.loading = false;
    return scheduleAutomaticAnalysis(tabId);
  }
  return Promise.resolve();
}

async function analyzeActiveAutomaticSite() {
  try {
    const tab = await getActiveTab();
    const context = siteContextForTab(tab);
    if (!context) {
      return;
    }
    await scheduleAutomaticAnalysis(context.tabId);
  } catch (_error) {
    return;
  }
}

async function updateYouTubeOverlay(context, enabled) {
  const previous = await getSettings();
  const settings = await updateSettings({ overlayEnabled: enabled });
  if (!settings.overlayEnabled) {
    return settings;
  }
  try {
    await ensureYouTubeOverlayScript(context.tabId);
    return settings;
  } catch (error) {
    if (previous.overlayEnabled !== settings.overlayEnabled) {
      await updateSettings({ overlayEnabled: previous.overlayEnabled });
    }
    throw error;
  }
}

function scheduleAutomaticAnalysis(tabId) {
  if (!Number.isInteger(tabId)) {
    return Promise.resolve();
  }
  const previous = automaticAnalysisQueues.get(tabId) || Promise.resolve();
  const scheduled = previous.then(function () {
    return runAutomaticSiteAnalysis(tabId);
  }).catch(function ignoreAutomaticAnalysisFailure() {});
  automaticAnalysisQueues.set(tabId, scheduled);
  scheduled.then(function () {
    if (automaticAnalysisQueues.get(tabId) === scheduled) {
      automaticAnalysisQueues.delete(tabId);
    }
  });
  return scheduled;
}

async function runAutomaticSiteAnalysis(tabId) {
  let tab;
  try {
    tab = await chrome.tabs.get(tabId);
  } catch (_error) {
    return;
  }
  const context = siteContextForTab(tab);
  if (!context || tab.status === "loading") {
    return;
  }
  if (context.kind === "youtube") {
    const settings = await getSettings();
    if (settings.overlayEnabled) {
      await ensureYouTubeOverlayScript(context.tabId);
    }
    return;
  }
  const origins = await getSiteAutoOrigins();
  if (!origins.has(context.origin) || !await hasSitePermission(context.permissionPattern)) {
    return;
  }

  const state = navigationState(tabId, context.url);
  if (state.url !== context.url) {
    state.revision += 1;
    state.url = context.url;
    state.loading = false;
    automaticCoverageByTab.delete(tabId);
  }
  const previousAttempt = automaticAttemptsByTab.get(tabId);
  if (previousAttempt && previousAttempt.url === context.url &&
      previousAttempt.revision === state.revision) {
    return;
  }
  if (pendingCoverageTabs.has(tabId)) {
    return;
  }
  const claim = await claimAutomaticCoverage(context);
  if (claim === "changed") {
    return;
  }
  automaticAttemptsByTab.set(tabId, {
    origin: context.origin,
    url: context.url,
    revision: state.revision
  });
  if (claim !== "claimed") {
    return;
  }

  const coverage = {
    origin: context.origin,
    url: context.url,
    revision: state.revision,
    cancelled: false
  };
  automaticCoverageByTab.set(tabId, coverage);
  try {
    await analyzeTab(tabId);
  } catch {} finally {
    if (!coverage.cancelled && automaticCoverageByTab.get(tabId) !== coverage) {
      await stopAutomaticCoverage(tabId, context.origin);
    }
  }
}
