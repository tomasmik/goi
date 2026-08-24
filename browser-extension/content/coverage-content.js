(function () {
  "use strict";

  if (globalThis.GoiCoverage) {
    return;
  }

  const HIGHLIGHT_NAME = "goi-unknown-words";
  const ACTIVE_HIGHLIGHT_NAME = "goi-active-unknown-word";
  const MAX_BLOCKS = 1000;
  const MAX_BLOCK_CHARACTERS = 18000;
  const MAX_CHARACTERS = 120000;
  const PANEL_STORAGE_PREFIX = "goiCoveragePanel:";
  const PANEL_CORNERS = ["bottom-right", "bottom-left", "top-left", "top-right"];
  const JAPANESE = /[\u3040-\u30ff\u3400-\u9fff\uf900-\ufaff]/u;
  const EXCLUDED = [
    "script", "style", "noscript", "template", "textarea", "input", "select", "option",
    "button", "code", "pre", "kbd", "samp", "rt", "rp", "svg", "canvas", "nav", "aside",
    "footer", "[contenteditable]:not([contenteditable='false'])", "[aria-hidden='true']",
    "[data-goi-coverage-ui]"
  ].join(", ");
  const INTERACTIVE = [
    "a", "button", "input", "textarea", "select", "option", "label", "summary",
    "[contenteditable]:not([contenteditable='false'])", "[tabindex]:not([tabindex='-1'])",
    "[onclick]", "[role='button']", "[role='checkbox']", "[role='combobox']", "[role='link']",
    "[role='menuitem']", "[role='menuitemcheckbox']", "[role='menuitemradio']", "[role='option']",
    "[role='radio']", "[role='slider']", "[role='spinbutton']", "[role='switch']", "[role='tab']",
    "[role='textbox']", "[role='treeitem']"
  ].join(", ");
  const READING_BLOCK = [
    "p", "li", "blockquote", "h1", "h2", "h3", "h4", "h5", "h6", "dt", "dd", "td",
    "th", "caption", "figcaption", "div", "section", "article"
  ].join(", ");
  const captureModel = globalThis.GoiExtension.captureModel;
  const dictionaryClient = globalThis.GoiExtension.dictionaryClient;
  const dictionaryRenderer = globalThis.GoiExtension.dictionaryView;
  const subtitleModel = globalThis.GoiExtension.subtitleModel;
  let blocksByID = new Map();
  let tokensByNode = new Map();
  let highlightRanges = [];
  let unknownOccurrences = [];
  let unknownCursor = -1;
  let highlightsVisible = true;
  let collectionPartial = false;
  let panel;
  let panelSummary;
  let panelDetail;
  let toggleButton;
  let nextUnknownButton;
  let wordActionMenu;
  let wordLookupContent;
  let mineAction;
  let ignoreAction;
  let globalIgnoreAction;
  let mineReturnFocus;
  let mineStatusRevision = 0;
  let lookupRevision = 0;
  let selectedUnknown;
  let analysisRoot;
  let activeAnalysisID = "";
  let activeAnalysisURL = "";
  let panelPreferenceRevision = 0;
  let pageObserver;
  let automaticRefreshTimer;
  let lastAutomaticRefreshAt = 0;

  function ignorePanelPreferenceWriteFailure() {}

  function panelStorageKey() {
    return PANEL_STORAGE_PREFIX + String(location.origin || location.href);
  }

  function applyPanelPreference(collapseButton, preference) {
    const corner = PANEL_CORNERS.includes(preference.corner) ? preference.corner : PANEL_CORNERS[0];
    const collapsed = preference.collapsed !== false;
    panel.dataset.goiCorner = corner;
    panel.classList.toggle("goi-ext-coverage--collapsed", collapsed);
    collapseButton.textContent = collapsed ? "Options" : "Done";
    collapseButton.setAttribute("aria-label", collapsed ? "Show reading controls" : "Hide reading controls");
  }

  async function storedPanelPreference(key) {
    try {
      return await chrome.storage.local.get(key);
    } catch (_error) {
      return {};
    }
  }

  async function loadPanelPreference(collapseButton) {
    if (!chrome.storage || !chrome.storage.local) {
      return;
    }
    const revision = panelPreferenceRevision;
    const key = panelStorageKey();
    const stored = await storedPanelPreference(key);
    if (revision === panelPreferenceRevision && stored[key]) {
      applyPanelPreference(collapseButton, stored[key]);
    }
  }

  function savePanelPreference(collapseButton) {
    if (!chrome.storage || !chrome.storage.local) {
      return;
    }
    panelPreferenceRevision += 1;
    const key = panelStorageKey();
    const value = {
      collapsed: panel.classList.contains("goi-ext-coverage--collapsed"),
      corner: panel.dataset.goiCorner || PANEL_CORNERS[0]
    };
    chrome.storage.local.set({ [key]: value }).catch(ignorePanelPreferenceWriteFailure);
    applyPanelPreference(collapseButton, value);
  }

  function rootForPage() {
    const selectors = ["main, [role='main']", "article"];
    for (const selector of selectors) {
      const visible = Array.from(document.querySelectorAll(selector)).find(visibleElement);
      if (visible) {
        return visible;
      }
    }
    return document.body;
  }

  function visibleElement(element) {
    if (!element || !element.isConnected || element.closest(EXCLUDED)) {
      return false;
    }
    if (typeof getComputedStyle === "function") {
      for (let current = element; current; current = current.parentElement) {
        const style = getComputedStyle(current);
        if (style.display === "none" || style.visibility === "hidden" ||
            style.visibility === "collapse" || Number(style.opacity) === 0 ||
            style.contentVisibility === "hidden") {
          return false;
        }
      }
    }
    return typeof element.getClientRects !== "function" || element.getClientRects().length > 0;
  }

  function collectBlocks() {
    blocksByID = new Map();
    tokensByNode = new Map();
    collectionPartial = false;
    const root = rootForPage();
    analysisRoot = root;
    if (!root) {
      return [];
    }
    const collection = collectTextGroups(root, MAX_CHARACTERS);
    const groups = collection.groups;
    collectionPartial = collection.partial;
    const blocks = [];
    let totalCharacters = 0;
    let blocksFull = false;
    for (const group of groups) {
      if (!JAPANESE.test(group.text)) {
        continue;
      }
      let start = 0;
      while (start < group.text.length) {
        if (blocks.length >= MAX_BLOCKS || totalCharacters >= MAX_CHARACTERS) {
          collectionPartial = true;
          blocksFull = true;
          break;
        }
        const remaining = MAX_CHARACTERS - totalCharacters;
        const end = blockEnd(group.text, start, Math.min(MAX_BLOCK_CHARACTERS, remaining));
        const blockText = group.text.slice(start, end);
        const id = blocks.length + 1;
        blocks.push({ id, text: blockText });
        blocksByID.set(id, { id, group, text: blockText, start });
        totalCharacters += blockText.length;
        start = end;
      }
      if (blocksFull) {
        break;
      }
    }
    return blocks;
  }

  function collectTextGroups(root, characterLimit) {
    const groupsByContainer = new Map();
    const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
    const limit = Number.isFinite(characterLimit)
      ? Math.max(0, characterLimit)
      : Number.POSITIVE_INFINITY;
    let characters = 0;
    let partial = false;
    let node;
    while ((node = walker.nextNode())) {
      const text = node.nodeValue || "";
      if (!text || !visibleElement(node.parentElement)) {
        continue;
      }
      if (characters >= limit) {
        partial = true;
        break;
      }
      let includedLength = Math.min(text.length, limit - characters);
      if (includedLength < text.length && /^[\uDC00-\uDFFF]$/u.test(text.charAt(includedLength))) {
        includedLength -= 1;
      }
      const includedText = text.slice(0, includedLength);
      if (!includedText) {
        partial = true;
        break;
      }
      const container = readingContainer(node.parentElement, root);
      let group = groupsByContainer.get(container);
      if (!group) {
        group = { container, root, segments: [], text: "", truncated: false };
        groupsByContainer.set(container, group);
      }
      const start = group.text.length;
      group.segments.push({ node, text: includedText, start, end: start + includedText.length });
      group.text += includedText;
      characters += includedText.length;
      if (includedText.length < text.length) {
        group.truncated = true;
        partial = true;
        break;
      }
    }
    return { groups: Array.from(groupsByContainer.values()), partial };
  }

  function readingContainer(element, root) {
    const container = element && element.closest(READING_BLOCK);
    return container && (container === root || root.contains(container)) ? container : root;
  }

  function groupIsCurrent(group) {
    if (!group || !group.container.isConnected || !analysisRoot || group.root !== analysisRoot) {
      return false;
    }
    const current = collectTextGroups(group.container).groups.find(function (candidate) {
      return candidate.container === group.container;
    });
    if (!current) {
      return false;
    }
    if (group.truncated) {
      if (!current.text.startsWith(group.text) || current.segments.length < group.segments.length) {
        return false;
      }
      return group.segments.every(function (segment, index) {
        const currentSegment = current.segments[index];
        return segment.node === currentSegment.node && currentSegment.text.startsWith(segment.text);
      });
    }
    if (current.text !== group.text || current.segments.length !== group.segments.length) {
      return false;
    }
    return current.segments.every(function (segment, index) {
      const original = group.segments[index];
      return segment.node === original.node && segment.text === original.text;
    });
  }

  function blockEnd(text, start, limit) {
    let end = Math.min(text.length, start + limit);
    if (end < text.length) {
      const minimumBoundary = start + Math.floor(limit / 2);
      const boundaries = ["。", "！", "？", "\n"].map(function (character) {
        return text.lastIndexOf(character, end - 1);
      });
      const boundary = Math.max(...boundaries);
      if (boundary >= minimumBoundary) {
        end = boundary + 1;
      } else if (/^[\uDC00-\uDFFF]$/u.test(text.charAt(end))) {
        end -= 1;
      }
    }
    if (end <= start) {
      return Math.min(text.length, start + 2);
    }
    return end;
  }

  function makeButton(text, label, onClick) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = text;
    button.setAttribute("aria-label", label);
    button.addEventListener("click", onClick);
    return button;
  }

  function ensurePanel() {
    if (panel && panel.isConnected) {
      return panel;
    }
    panel = document.createElement("section");
    panel.id = "goi-ext-coverage";
    panel.dataset.goiCoverageUi = "true";
    panel.setAttribute("role", "region");
    panel.setAttribute("aria-label", "Goi page reader");

    const copy = document.createElement("div");
    copy.setAttribute("role", "status");
    copy.setAttribute("aria-live", "polite");
    panelSummary = document.createElement("strong");
    panelDetail = document.createElement("span");
    copy.append(panelSummary, panelDetail);
    panel.appendChild(copy);

    const actions = document.createElement("div");
    nextUnknownButton = makeButton("Next", "Open next unknown word", function (event) {
      showNextUnknown(event.currentTarget);
    });
    nextUnknownButton.id = "goi-ext-coverage-next";
    nextUnknownButton.hidden = true;
    actions.appendChild(nextUnknownButton);
    const collapseButton = makeButton("Options", "Show reading controls", function () {
      panel.classList.toggle("goi-ext-coverage--collapsed");
      savePanelPreference(collapseButton);
    });
    actions.appendChild(collapseButton);
    actions.appendChild(makeButton("Move", "Move Goi coverage panel", function () {
      const current = PANEL_CORNERS.indexOf(panel.dataset.goiCorner);
      panel.dataset.goiCorner = PANEL_CORNERS[(current + 1) % PANEL_CORNERS.length];
      savePanelPreference(collapseButton);
    }));
    toggleButton = makeButton("Hide marks", "Hide unknown-word highlights", function () {
      highlightsVisible = !highlightsVisible;
      applyHighlights();
    });
    actions.appendChild(toggleButton);
    actions.appendChild(makeButton("Refresh", "Refresh Goi page reader", function (event) {
      if (!event.isTrusted) {
        return;
      }
      try {
        chrome.runtime.sendMessage({ type: "goi.coverage.refresh", version: 1 })
          .catch(showError);
      } catch (_error) {
        showError();
      }
    }));
    actions.appendChild(makeButton("Close", "Close Goi coverage", stop));
    panel.appendChild(actions);
    document.documentElement.appendChild(panel);
    panel.dataset.goiCorner = PANEL_CORNERS[0];
    panel.classList.add("goi-ext-coverage--collapsed");
    loadPanelPreference(collapseButton);
    return panel;
  }

  function showLoading() {
    ensurePanel();
    panel.hidden = false;
    panelSummary.textContent = "Goi · Analyzing…";
    panelDetail.textContent = "Reading visible Japanese text";
    toggleButton.hidden = true;
    clearHighlights();
    highlightRanges = [];
    tokensByNode = new Map();
    resetUnknownNavigation();
  }

  function showError() {
    ensurePanel();
    panel.hidden = false;
    panelSummary.textContent = "Goi · Coverage unavailable";
    panelDetail.textContent = "Check the Goi connection, then refresh";
    toggleButton.hidden = true;
    clearHighlights();
    highlightRanges = [];
    tokensByNode = new Map();
    resetUnknownNavigation();
  }

  function render(result) {
    ensurePanel();
    panel.hidden = false;
    const summary = result.summary;
    if (!summary.total_occurrences) {
      panelSummary.textContent = "Goi · —";
      panelDetail.textContent = "No Japanese vocabulary found";
    } else {
      const percent = captureModel.coveragePercent(
        summary.known_occurrences,
        summary.total_occurrences
      );
      panelSummary.textContent = "Goi · " + percent + "% known";
      panelDetail.textContent = summary.known_occurrences + " / " + summary.total_occurrences +
        " words · " + summary.unknown_unique + " unique unknown " +
        (summary.unknown_unique === 1 ? "word" : "words") +
        (summary.excluded_names ? " · " + summary.excluded_names + " names skipped" : "") +
        (collectionPartial ? " · visible sample" : "");
    }
    setTokens(result.blocks);
    toggleButton.hidden = highlightRanges.length === 0;
    highlightsVisible = true;
    applyHighlights();
  }

  function setTokens(resultBlocks) {
    tokensByNode = new Map();
    highlightRanges = [];
    resetUnknownNavigation();
    const currentGroups = new Map();
    resultBlocks.forEach(function (block) {
      const record = blocksByID.get(block.id);
      if (!record) {
        return;
      }
      if (!currentGroups.has(record.group)) {
        currentGroups.set(record.group, groupIsCurrent(record.group));
      }
      if (!currentGroups.get(record.group)) {
        return;
      }
      const unknown = block.tokens.filter(function (token) {
        return token.status === "unknown" &&
          token.start_utf16 >= 0 &&
          token.end_utf16 > token.start_utf16 &&
          token.end_utf16 <= record.text.length &&
          record.text.slice(token.start_utf16, token.end_utf16) === token.surface;
      });
      if (!unknown.length) {
        return;
      }
      unknown.forEach(function (token) {
        const nodeToken = {
          ...token,
          groupStart: record.start + token.start_utf16,
          groupEnd: record.start + token.end_utf16,
          record,
          ranges: []
        };
        record.group.segments.forEach(function (segment) {
          const start = Math.max(nodeToken.groupStart, segment.start);
          const end = Math.min(nodeToken.groupEnd, segment.end);
          if (end <= start) {
            return;
          }
          const nodePart = {
            ...nodeToken,
            nodeStart: start - segment.start,
            nodeEnd: end - segment.start
          };
          const range = textRange(segment.node, nodePart.nodeStart, nodePart.nodeEnd);
          if (range) {
            const nodeTokens = tokensByNode.get(segment.node) || [];
            nodeTokens.push(nodePart);
            tokensByNode.set(segment.node, nodeTokens);
            highlightRanges.push(range);
            nodeToken.ranges.push(range);
          }
        });
        if (nodeToken.ranges.length) {
          unknownOccurrences.push(nodeToken);
        }
      });
    });
    updateUnknownNavigationLabel();
  }

  function textRange(node, start, end) {
    try {
      const range = new Range();
      range.setStart(node, start);
      range.setEnd(node, end);
      return range;
    } catch (_error) {
      return null;
    }
  }

  function clearHighlights() {
    if (globalThis.CSS && CSS.highlights) {
      CSS.highlights.delete(HIGHLIGHT_NAME);
      CSS.highlights.delete(ACTIVE_HIGHLIGHT_NAME);
    }
  }

  function applyHighlights() {
    clearHighlights();
    if (highlightsVisible && highlightRanges.length && globalThis.CSS && CSS.highlights && typeof Highlight === "function") {
      CSS.highlights.set(HIGHLIGHT_NAME, new Highlight(...highlightRanges));
      if (selectedUnknown && selectedUnknown.ranges.length) {
        CSS.highlights.set(ACTIVE_HIGHLIGHT_NAME, new Highlight(...selectedUnknown.ranges));
      }
    }
    if (toggleButton) {
      toggleButton.textContent = highlightsVisible ? "Hide marks" : "Show marks";
      toggleButton.setAttribute("aria-label", highlightsVisible
        ? "Hide unknown-word highlights"
        : "Show unknown-word highlights");
    }
    if (nextUnknownButton) {
      nextUnknownButton.hidden = !highlightsVisible || unknownOccurrences.length === 0;
    }
    if (!highlightsVisible) {
      dismissMineAction(false);
    }
  }

  function caretAtPoint(event) {
    if (document.caretPositionFromPoint) {
      const caret = document.caretPositionFromPoint(event.clientX, event.clientY);
      return caret && { node: caret.offsetNode, offset: caret.offset };
    }
    if (document.caretRangeFromPoint) {
      const range = document.caretRangeFromPoint(event.clientX, event.clientY);
      return range && { node: range.startContainer, offset: range.startOffset };
    }
    return null;
  }

  function unknownAtPoint(event) {
    if (!highlightsVisible || (event.target.closest && event.target.closest(INTERACTIVE))) {
      return null;
    }
    const caret = caretAtPoint(event);
    const tokens = caret && tokensByNode.get(caret.node);
    if (!tokens) {
      return null;
    }
    const token = tokens.find(function (candidate) {
      return candidate.nodeStart <= caret.offset && caret.offset < candidate.nodeEnd;
    }) || tokens.find(function (candidate) {
      return candidate.nodeStart < caret.offset && caret.offset === candidate.nodeEnd;
    }) || null;
    if (token && !groupIsCurrent(token.record.group)) {
      invalidateCoverage();
      return null;
    }
    return token;
  }

  function invalidateCoverage() {
    clearHighlights();
    highlightRanges = [];
    tokensByNode = new Map();
    resetUnknownNavigation();
    if (panelSummary) {
      panelSummary.textContent = "Goi · Page changed";
    }
    if (panelDetail) {
      panelDetail.textContent = "Page changed · analyze again";
    }
    if (toggleButton) {
      toggleButton.hidden = true;
    }
  }

  function observePageChanges() {
    if (pageObserver || typeof MutationObserver !== "function" || !document.body) {
      return;
    }
    pageObserver = new MutationObserver(function (mutations) {
      if (!activeAnalysisID || mutations.every(function (mutation) {
        const target = mutation.target && mutation.target.nodeType === 1
          ? mutation.target
          : mutation.target && mutation.target.parentElement;
        const extensionUI = target && target.closest && target.closest("[data-goi-coverage-ui]");
        return Boolean(extensionUI) || !mutationHasJapaneseText(mutation);
      })) {
        return;
      }
      clearTimeout(automaticRefreshTimer);
      const elapsed = Date.now() - lastAutomaticRefreshAt;
      automaticRefreshTimer = setTimeout(refreshChangedPage, Math.max(1200, 3000 - elapsed));
    });
    pageObserver.observe(document.body, { childList: true, characterData: true, subtree: true });
  }

  function mutationHasJapaneseText(mutation) {
    const nodes = mutation.type === "characterData"
      ? [mutation.target]
      : [...mutation.addedNodes, ...mutation.removedNodes];
    return nodes.some(function (node) {
      return JAPANESE.test(String(node && (node.nodeValue || node.textContent) || ""));
    });
  }

  function refreshChangedPage() {
    automaticRefreshTimer = undefined;
    if (!activeAnalysisID) return;
    lastAutomaticRefreshAt = Date.now();
    if (panelDetail) panelDetail.textContent = "Page changed · refreshing…";
    try {
      chrome.runtime.sendMessage({ type: "goi.coverage.refresh", version: 1, automatic: true }).catch(showError);
    } catch (_error) {
      showError();
    }
  }

  function showMineAction(token, point, returnFocus) {
    dismissMineAction(false);
    selectedUnknown = token;
    mineReturnFocus = returnFocus;
    wordActionMenu = document.createElement("section");
    wordActionMenu.id = "goi-ext-coverage-word-menu";
    wordActionMenu.dataset.goiCoverageUi = "true";
    wordActionMenu.setAttribute("role", "dialog");
    wordActionMenu.setAttribute("aria-label", "Dictionary entry for " + token.surface);
    wordActionMenu.tabIndex = -1;

    const header = document.createElement("div");
    header.className = "goi-ext-coverage-word-header";
    const heading = document.createElement("strong");
    heading.textContent = token.surface;
    heading.lang = "ja";
    const closeAction = makeButton("Close", "Close dictionary", function () {
      dismissMineAction(true);
    });
    closeAction.id = "goi-ext-coverage-word-close";
    header.append(heading, closeAction);

    wordLookupContent = document.createElement("div");
    wordLookupContent.className = "dictionary-lookup";
    const loading = document.createElement("p");
    loading.className = "goi-dictionary-message";
    loading.textContent = "Looking up “" + token.surface + "”…";
    wordLookupContent.appendChild(loading);

    const actions = document.createElement("div");
    ignoreAction = makeButton("Ignore on this page", "Ignore " + token.surface + " on this page", ignoreUnknown);
    ignoreAction.id = "goi-ext-coverage-ignore";
    globalIgnoreAction = makeButton("Ignore everywhere", "Ignore " + token.surface + " on every page", ignoreUnknownEverywhere);
    globalIgnoreAction.id = "goi-ext-coverage-ignore-global";
    actions.append(ignoreAction, globalIgnoreAction);
    wordActionMenu.append(header, wordLookupContent, actions);

    const left = Math.max(8, Math.min(point.clientX, innerWidth - 396));
    const top = Math.max(8, Math.min(point.clientY + 12, innerHeight - 260));
    wordActionMenu.style.left = left + "px";
    wordActionMenu.style.top = top + "px";
    document.documentElement.appendChild(wordActionMenu);
    applyHighlights();
    wordActionMenu.focus();
    lookupSelectedUnknown(token);
  }

  async function lookupSelectedUnknown(token) {
    const revision = ++lookupRevision;
    try {
      const response = await dictionaryClient.lookup(chrome.runtime, token.expression || token.surface);
      if (revision !== lookupRevision || token !== selectedUnknown || !wordLookupContent) {
        return;
      }
      const view = subtitleModel.dictionaryView(response);
      if (!view.candidates.length) {
        dictionaryRenderer.render(wordLookupContent, view, { document });
        appendFallbackMineAction(token);
        return;
      }
      dictionaryRenderer.render(wordLookupContent, view, {
        document,
        actionLabel: "Mine",
        selectedEntrySequence: null,
        onSelect(candidate) {
          captureSelectedUnknown(candidate.entrySequence);
          return false;
        }
      });
    } catch (_error) {
      if (revision !== lookupRevision || token !== selectedUnknown || !wordLookupContent) {
        return;
      }
      wordLookupContent.replaceChildren();
      const message = document.createElement("p");
      message.className = "goi-dictionary-message";
      message.textContent = "Could not reach Goi.";
      wordLookupContent.appendChild(message);
      appendFallbackMineAction(token);
    }
  }

  function appendFallbackMineAction(token) {
    mineAction = makeButton("Send to mining", "Send " + token.surface + " to mining", mineUnknown);
    mineAction.id = "goi-ext-coverage-mine";
    wordLookupContent.appendChild(mineAction);
  }

  function showNextUnknown(returnFocus) {
    if (!highlightsVisible || unknownOccurrences.length === 0) {
      return;
    }
    unknownCursor = (unknownCursor + 1) % unknownOccurrences.length;
    const token = unknownOccurrences[unknownCursor];
    if (!groupIsCurrent(token.record.group)) {
      invalidateCoverage();
      return;
    }
    const range = token.ranges[0];
    let bounds = rangeBounds(range);
    if (!boundsAreVisible(bounds)) {
      const node = range && range.startContainer;
      const scrollTarget = node && node.nodeType === 1
        ? node
        : node && node.parentElement;
      if (scrollTarget && typeof scrollTarget.scrollIntoView === "function") {
        scrollTarget.scrollIntoView({ block: "center", inline: "nearest" });
        bounds = rangeBounds(range);
      }
    }
    const point = boundsAreVisible(bounds)
      ? { clientX: bounds.left, clientY: bounds.bottom }
      : { clientX: innerWidth - 232, clientY: innerHeight - 64 };
    updateUnknownNavigationLabel();
    showMineAction(token, point, returnFocus);
  }

  function rangeBounds(range) {
    return range && typeof range.getBoundingClientRect === "function"
      ? range.getBoundingClientRect()
      : null;
  }

  function boundsAreVisible(bounds) {
    return Boolean(bounds &&
      Number.isFinite(bounds.top) && Number.isFinite(bounds.right) &&
      Number.isFinite(bounds.bottom) && Number.isFinite(bounds.left) &&
      bounds.bottom >= 0 && bounds.right >= 0 &&
      bounds.left <= innerWidth && bounds.top <= innerHeight);
  }

  function updateUnknownNavigationLabel() {
    if (!nextUnknownButton) {
      return;
    }
    const progress = unknownCursor >= 0
      ? ", currently " + (unknownCursor + 1) + " of " + unknownOccurrences.length
      : ", " + unknownOccurrences.length + " total";
    nextUnknownButton.setAttribute("aria-label", "Open next unknown word" + progress);
  }

  function mineUnknown(event) {
    if (!event.isTrusted) {
      return;
    }
    captureSelectedUnknown(null);
  }

  async function captureSelectedUnknown(suggestedEntrySequence) {
    const token = selectedUnknown;
    dismissMineAction(true);
    if (!token) {
      return;
    }
    if (!groupIsCurrent(token.record.group)) {
      invalidateCoverage();
      return;
    }
    const contextText = captureModel.sentenceContext(
      token.record.group.text,
      token.groupStart,
      token.groupEnd,
      document.documentElement.lang || navigator.language
    );
    const statusTarget = panelDetail;
    const statusRevision = ++mineStatusRevision;
    statusTarget.textContent = "Adding 「" + token.surface + "」…";
    try {
      const response = await chrome.runtime.sendMessage({
        type: "goi.capture.direct",
        version: 1,
        capture: {
          rawText: token.surface,
          expression: token.expression || token.surface,
          suggestedEntrySequence,
          contextText,
          sourceKind: "web",
          sourceTitle: document.title,
          sourceURL: location.href
        }
      });
      if (statusRevision === mineStatusRevision &&
          statusTarget === panelDetail && statusTarget.isConnected) {
        if (response && response.ok) {
          statusTarget.textContent = response.queued
            ? "Queued 「" + token.surface + "」"
            : "Added 「" + token.surface + "」";
        } else if (response && response.errorCode === "queue_full") {
          statusTarget.textContent = "Queue full · retry when Goi is reachable";
        } else {
          statusTarget.textContent = "Could not add 「" + token.surface + "」";
        }
      }
    } catch (_error) {
      if (statusRevision === mineStatusRevision &&
          statusTarget === panelDetail && statusTarget.isConnected) {
        statusTarget.textContent = "Could not add 「" + token.surface + "」";
      }
    }
  }

  function ignoreUnknown(event) {
    if (!event.isTrusted) {
      return;
    }
    const token = selectedUnknown;
    const returnFocus = mineReturnFocus;
    dismissMineAction(false);
    if (!token) {
      return;
    }
    const key = token.expression || token.surface;
    const removedRanges = new Set();
    unknownOccurrences.forEach(function (candidate) {
      if ((candidate.expression || candidate.surface) === key) {
        candidate.ranges.forEach(function (range) { removedRanges.add(range); });
      }
    });
    unknownOccurrences = unknownOccurrences.filter(function (candidate) {
      return (candidate.expression || candidate.surface) !== key;
    });
    highlightRanges = highlightRanges.filter(function (range) { return !removedRanges.has(range); });
    tokensByNode.forEach(function (tokens, node) {
      const remaining = tokens.filter(function (candidate) {
        return (candidate.expression || candidate.surface) !== key;
      });
      if (remaining.length) {
        tokensByNode.set(node, remaining);
      } else {
        tokensByNode.delete(node);
      }
    });
    unknownCursor = -1;
    updateUnknownNavigationLabel();
    panelDetail.textContent = "Ignored 「" + token.surface + "」 on this page · " +
      unknownOccurrences.length + (unknownOccurrences.length === 1 ? " highlighted word remains" : " highlighted words remain");
    toggleButton.hidden = highlightRanges.length === 0;
    applyHighlights();
    if (returnFocus && returnFocus.isConnected && typeof returnFocus.focus === "function") {
      returnFocus.focus();
    }
  }

  async function ignoreUnknownEverywhere(event) {
    if (!event.isTrusted || !selectedUnknown) {
      return;
    }
    const token = selectedUnknown;
    if (!confirm("Ignore 「" + token.surface + "」 on every page? You can undo this from Goi Capture.")) {
      return;
    }
    try {
      const response = await chrome.runtime.sendMessage({
        type: "goi.coverage.ignore.add",
        version: 1,
        word: token.expression || token.surface
      });
      if (!response || !response.ok) {
        panelDetail.textContent = "Could not update ignored words";
        return;
      }
      ignoreUnknown(event);
      panelDetail.textContent = "Ignored 「" + token.surface + "」 everywhere";
    } catch (_error) {
      panelDetail.textContent = "Could not update ignored words";
    }
  }

  function dismissMineAction(restoreFocus) {
    lookupRevision += 1;
    const returnFocus = mineReturnFocus;
    selectedUnknown = undefined;
    mineReturnFocus = undefined;
    if (wordActionMenu) wordActionMenu.remove();
    wordActionMenu = undefined;
    wordLookupContent = undefined;
    mineAction = undefined;
    ignoreAction = undefined;
    globalIgnoreAction = undefined;
    if (globalThis.CSS && CSS.highlights) {
      CSS.highlights.delete(ACTIVE_HIGHLIGHT_NAME);
    }
    if (restoreFocus && returnFocus && returnFocus.isConnected && typeof returnFocus.focus === "function") {
      returnFocus.focus();
    }
  }

  function resetUnknownNavigation() {
    mineStatusRevision += 1;
    unknownOccurrences = [];
    unknownCursor = -1;
    dismissMineAction(false);
    if (nextUnknownButton) {
      nextUnknownButton.hidden = true;
      nextUnknownButton.setAttribute("aria-label", "Open next unknown word");
    }
  }

  function notifyCoverageClosed() {
    try {
      chrome.runtime.sendMessage({ type: "goi.coverage.closed", version: 1 })
        .catch(function ignoreUnavailableWorker() {});
    } catch (_error) {
      return;
    }
  }

  function stop(event) {
    if (event && !event.isTrusted) {
      return;
    }
    activeAnalysisID = "";
    activeAnalysisURL = "";
    clearHighlights();
    highlightRanges = [];
    tokensByNode = new Map();
    blocksByID = new Map();
    analysisRoot = undefined;
    resetUnknownNavigation();
    if (panel) {
      panel.remove();
      panel = undefined;
    }
    if (typeof clearTimeout === "function") clearTimeout(automaticRefreshTimer);
    automaticRefreshTimer = undefined;
    if (pageObserver) pageObserver.disconnect();
    pageObserver = undefined;
    lastAutomaticRefreshAt = 0;
    notifyCoverageClosed();
  }

  document.addEventListener("click", function (event) {
    if (!event.isTrusted) {
      return;
    }
    if (event.target.closest && event.target.closest("[data-goi-coverage-ui]")) {
      return;
    }
    const token = unknownAtPoint(event);
    if (token) {
      const selection = typeof getSelection === "function" ? getSelection() : null;
      if (event.button !== undefined && event.button !== 0 ||
          event.metaKey || event.ctrlKey || event.altKey || event.shiftKey ||
          selection && !selection.isCollapsed) {
        dismissMineAction();
        return;
      }
      event.preventDefault();
      showMineAction(token, event);
    } else {
      dismissMineAction();
    }
  }, true);
  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape") {
      dismissMineAction(true);
    }
  });

  function beginAnalysis(analysisID) {
    activeAnalysisID = String(analysisID || "");
    activeAnalysisURL = location.href;
    showLoading();
    observePageChanges();
    return {
      analysisID: activeAnalysisID,
      url: activeAnalysisURL,
      blocks: collectBlocks()
    };
  }

  function finishAnalysis(analysisID, url, result) {
    if (analysisID !== activeAnalysisID || url !== activeAnalysisURL || location.href !== url) {
      return false;
    }
    const groups = new Set(Array.from(blocksByID.values(), function (record) {
      return record.group;
    }));
    if (Array.from(groups).some(function (group) { return !groupIsCurrent(group); })) {
      activeAnalysisID = "";
      activeAnalysisURL = "";
      invalidateCoverage();
      return false;
    }
    render(result);
    return true;
  }

  function failAnalysis(analysisID, url) {
    if (analysisID !== activeAnalysisID || url !== activeAnalysisURL || location.href !== url) {
      return false;
    }
    showError();
    return true;
  }

  globalThis.GoiCoverage = {
    beginAnalysis,
    collectBlocks,
    failAnalysis,
    finishAnalysis,
    render,
    showError,
    showLoading,
    stop
  };
})();
