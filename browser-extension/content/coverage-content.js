(function () {
  "use strict";

  if (globalThis.GoiCoverage) {
    return;
  }

  const HIGHLIGHT_NAME = "goi-unknown-words";
  const ACTIVE_HIGHLIGHT_NAME = "goi-active-word";
  const MAX_BLOCKS = 1000;
  const MAX_BLOCK_CHARACTERS = 18000;
  const MAX_CHARACTERS = 120000;
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
  let wordOccurrences = [];
  let wordCursor = -1;
  let highlightsVisible = true;
  let collectionPartial = false;
  let readerHost;
  let readerRoot;
  let readerStyle;
  let readerCSS = "";
  let panel;
  let panelSummary;
  let panelDetail;
  let toggleButton;
  let nextWordButton;
  let wordActionMenu;
  let wordLookupContent;
  let wordAnchor;
  let dictionaryReturnFocus;
  let mineStatusRevision = 0;
  let lookupRevision = 0;
  let selectedWord;
  let analysisRoot;
  let activeAnalysisID = "";
  let activeAnalysisURL = "";
  let panelPreferenceRevision = 0;
  let pageObserver;
  let automaticRefreshTimer;
  let lastAutomaticRefreshAt = 0;

  function ignorePanelPreferenceWriteFailure() {}

  function applyPanelPreference(collapseButton, preference) {
    const corner = PANEL_CORNERS.includes(preference.corner) ? preference.corner : PANEL_CORNERS[0];
    const collapsed = preference.collapsed !== false;
    panel.dataset.goiCorner = corner;
    panel.classList.toggle("goi-ext-coverage--collapsed", collapsed);
    collapseButton.textContent = collapsed ? "Controls" : "Less";
    collapseButton.setAttribute("aria-expanded", String(!collapsed));
    collapseButton.setAttribute("aria-label", collapsed ? "Show reading controls" : "Hide reading controls");
  }

  async function loadPanelPreference(collapseButton) {
    const revision = panelPreferenceRevision;
    try {
      const response = await chrome.runtime.sendMessage({ type: "goi.coverage.panel.get", version: 1 });
      if (revision === panelPreferenceRevision && response && response.ok && response.preference) {
        applyPanelPreference(collapseButton, response.preference);
      }
    } catch (_error) {}
  }

  function savePanelPreference(collapseButton) {
    panelPreferenceRevision += 1;
    const preference = {
      collapsed: panel.classList.contains("goi-ext-coverage--collapsed"),
      corner: panel.dataset.goiCorner || PANEL_CORNERS[0]
    };
    applyPanelPreference(collapseButton, preference);
    chrome.runtime.sendMessage({
      type: "goi.coverage.panel.update", version: 1, preference
    }).catch(ignorePanelPreferenceWriteFailure);
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
    readerHost = document.createElement("div");
    readerHost.id = "goi-ext-reader";
    readerHost.dataset.goiCoverageUi = "true";
    readerRoot = readerHost.attachShadow({ mode: "open" });
    readerStyle = document.createElement("style");
    readerStyle.textContent = readerCSS;
    readerRoot.appendChild(readerStyle);
    document.documentElement.appendChild(readerHost);
    panel = document.createElement("section");
    panel.id = "goi-ext-coverage";
    panel.dataset.goiCoverageUi = "true";
    panel.setAttribute("role", "region");
    panel.setAttribute("aria-label", "Goi page reader");

    const header = document.createElement("div");
    header.className = "goi-ext-coverage-header";
    const copy = document.createElement("div");
    copy.className = "goi-ext-coverage-copy";
    copy.setAttribute("role", "status");
    copy.setAttribute("aria-live", "polite");
    panelSummary = document.createElement("strong");
    panelDetail = document.createElement("span");
    panelDetail.className = "goi-ext-coverage-detail";
    const hint = document.createElement("span");
    hint.className = "goi-ext-coverage-hint";
    hint.textContent = "Click a word to read its meaning";
    copy.append(panelSummary, panelDetail, hint);
    header.appendChild(copy);
    const collapseButton = makeButton("Controls", "Show reading controls", function () {
      panel.classList.toggle("goi-ext-coverage--collapsed");
      savePanelPreference(collapseButton);
    });
    collapseButton.setAttribute("aria-controls", "goi-ext-coverage-controls");
    header.append(collapseButton, makeButton("Close", "Close Goi reader", stop));
    panel.appendChild(header);

    const actions = document.createElement("div");
    actions.id = "goi-ext-coverage-controls";
    actions.className = "goi-ext-coverage-controls";
    nextWordButton = makeButton("Next word", "Look up next word", function (event) {
      if (!event.isTrusted) return;
      showNextWord(event.currentTarget);
    });
    nextWordButton.id = "goi-ext-coverage-next";
    nextWordButton.hidden = true;
    actions.appendChild(nextWordButton);
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
    panel.appendChild(actions);
    readerRoot.appendChild(panel);
    panel.dataset.goiCorner = PANEL_CORNERS[0];
    panel.classList.add("goi-ext-coverage--collapsed");
    applyPanelPreference(collapseButton, { collapsed: true });
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
    resetWordNavigation();
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
    resetWordNavigation();
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
    applyHighlights();
  }

  function setTokens(resultBlocks) {
    tokensByNode = new Map();
    highlightRanges = [];
    resetWordNavigation();
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
      const tokens = block.tokens.filter(function (token) {
        return Number.isInteger(token.start_utf16) && Number.isInteger(token.end_utf16) &&
          token.start_utf16 >= 0 &&
          token.end_utf16 > token.start_utf16 &&
          token.end_utf16 <= record.text.length &&
          record.text.slice(token.start_utf16, token.end_utf16) === token.surface;
      });
      tokens.forEach(function (token) {
        const nodeToken = {
          ...token,
          groupStart: record.start + token.start_utf16,
          groupEnd: record.start + token.end_utf16,
          record,
          marked: token.status === "unknown",
          ranges: []
        };
        record.group.segments.forEach(function (segment) {
          const start = Math.max(nodeToken.groupStart, segment.start);
          const end = Math.min(nodeToken.groupEnd, segment.end);
          if (end <= start) {
            return;
          }
          const nodePart = {
            token: nodeToken,
            nodeStart: start - segment.start,
            nodeEnd: end - segment.start
          };
          const range = textRange(segment.node, nodePart.nodeStart, nodePart.nodeEnd);
          if (range) {
            const nodeTokens = tokensByNode.get(segment.node) || [];
            nodeTokens.push(nodePart);
            tokensByNode.set(segment.node, nodeTokens);
            if (nodeToken.marked) highlightRanges.push(range);
            nodeToken.ranges.push(range);
          }
        });
        if (nodeToken.ranges.length) {
          wordOccurrences.push(nodeToken);
        }
      });
    });
    updateWordNavigationLabel();
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
    if (globalThis.CSS && CSS.highlights && typeof Highlight === "function") {
      if (highlightsVisible && highlightRanges.length) {
        CSS.highlights.set(HIGHLIGHT_NAME, new Highlight(...highlightRanges));
      }
      if (selectedWord && selectedWord.ranges.length) {
        CSS.highlights.set(ACTIVE_HIGHLIGHT_NAME, new Highlight(...selectedWord.ranges));
      }
    }
    if (toggleButton) {
      toggleButton.textContent = highlightsVisible ? "Hide marks" : "Show marks";
      toggleButton.setAttribute("aria-label", highlightsVisible
        ? "Hide unknown-word highlights"
        : "Show unknown-word highlights");
    }
    if (nextWordButton) {
      nextWordButton.hidden = wordOccurrences.length === 0;
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

  function wordAtPoint(event) {
    if (event.target.closest && event.target.closest(INTERACTIVE)) {
      return null;
    }
    const caret = caretAtPoint(event);
    let tokens = caret && tokensByNode.get(caret.node) || [];
    tokens = tokens.filter(function (candidate) {
      return candidate.nodeStart <= caret.offset && caret.offset <= candidate.nodeEnd;
    }).map(function (candidate) { return candidate.token; });
    const ruby = event.target.closest && event.target.closest("ruby");
    if (ruby) {
      tokens = tokens.concat(wordOccurrences.filter(function (candidate) {
        return candidate.ranges.some(function (range) { return ruby.contains(range.startContainer); });
      }));
    }
    const token = tokens.find(function (candidate) { return rectAtPoint(candidate, event); });
    if (token && !groupIsCurrent(token.record.group)) {
      invalidateCoverage();
      return null;
    }
    return token;
  }

  function rectAtPoint(token, point) {
    for (const range of token.ranges) {
      for (const rect of range.getClientRects()) {
        if (point.clientX >= rect.left && point.clientX < rect.right &&
            point.clientY >= rect.top && point.clientY < rect.bottom) return rect;
      }
    }
    return null;
  }

  function invalidateCoverage() {
    clearHighlights();
    highlightRanges = [];
    tokensByNode = new Map();
    resetWordNavigation();
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

  function showDictionary(token, anchor, returnFocus) {
    dismissDictionary(false);
    selectedWord = token;
    wordCursor = wordOccurrences.indexOf(token);
    updateWordNavigationLabel();
    wordAnchor = anchor;
    dictionaryReturnFocus = returnFocus;
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
      dismissDictionary(true);
    });
    closeAction.id = "goi-ext-coverage-word-close";
    header.append(heading, closeAction);

    wordLookupContent = document.createElement("div");
    wordLookupContent.className = "dictionary-lookup";
    const loading = document.createElement("p");
    loading.className = "goi-dictionary-message";
    loading.textContent = "Looking up “" + token.surface + "”…";
    wordLookupContent.appendChild(loading);

    wordActionMenu.append(header, wordLookupContent);
    appendWordOptions(token);
    readerRoot.appendChild(wordActionMenu);
    positionDictionary();
    applyHighlights();
    wordActionMenu.focus({ preventScroll: true });
    lookupSelectedWord(token);
  }

  function positionDictionary() {
    if (!wordActionMenu || !wordAnchor) return;
    const margin = 8;
    const gap = 8;
    wordActionMenu.style.maxHeight = Math.max(0, Math.min(620, innerHeight - margin * 2)) + "px";
    const bounds = wordActionMenu.getBoundingClientRect();
    const below = innerHeight - margin - wordAnchor.bottom - gap;
    const above = wordAnchor.top - gap - margin;
    const top = bounds.height <= below || below >= above
      ? wordAnchor.bottom + gap
      : wordAnchor.top - gap - bounds.height;
    wordActionMenu.style.left = Math.max(margin, Math.min(wordAnchor.left, innerWidth - bounds.width - margin)) + "px";
    wordActionMenu.style.top = Math.max(margin, Math.min(top, innerHeight - bounds.height - margin)) + "px";
  }

  function appendWordOptions(token) {
    if (!token.marked) return;
    const options = document.createElement("details");
    options.className = "goi-ext-coverage-word-options";
    options.addEventListener("toggle", positionDictionary);
    const summary = document.createElement("summary");
    summary.textContent = "Word options";
    const actions = document.createElement("div");
    const ignoreAction = makeButton("Ignore on this page", "Ignore " + token.surface + " on this page", ignoreUnknown);
    ignoreAction.id = "goi-ext-coverage-ignore";
    const globalIgnoreAction = makeButton("Ignore everywhere", "Ignore " + token.surface + " on every page", ignoreUnknownEverywhere);
    globalIgnoreAction.id = "goi-ext-coverage-ignore-global";
    actions.append(ignoreAction, globalIgnoreAction);
    options.append(summary, actions);
    wordLookupContent.appendChild(options);
  }

  async function lookupSelectedWord(token) {
    const revision = ++lookupRevision;
    try {
      const response = await dictionaryClient.lookup(chrome.runtime, token.expression || token.surface);
      if (revision !== lookupRevision || token !== selectedWord || !wordLookupContent) {
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
        actionLabel: "Save to mining",
        selectedEntrySequence: null,
        onSelect(candidate) {
          captureSelectedWord(candidate.entrySequence);
          return false;
        }
      });
    } catch (_error) {
      if (revision !== lookupRevision || token !== selectedWord || !wordLookupContent) {
        return;
      }
      wordLookupContent.replaceChildren();
      const message = document.createElement("p");
      message.className = "goi-dictionary-message";
      message.textContent = "Could not reach Goi.";
      wordLookupContent.appendChild(message);
      appendFallbackMineAction(token);
    } finally {
      if (revision === lookupRevision && token === selectedWord && wordLookupContent) {
        appendWordOptions(token);
        positionDictionary();
      }
    }
  }

  function appendFallbackMineAction(token) {
    const mineAction = makeButton("Save to mining", "Save " + token.surface + " to mining", mineSelectedWord);
    mineAction.id = "goi-ext-coverage-mine";
    wordLookupContent.appendChild(mineAction);
  }

  function showNextWord(returnFocus) {
    if (wordOccurrences.length === 0) {
      return;
    }
    wordCursor = (wordCursor + 1) % wordOccurrences.length;
    const token = wordOccurrences[wordCursor];
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
        scrollTarget.scrollIntoView({ block: "center", inline: "nearest", behavior: "instant" });
        bounds = rangeBounds(range);
      }
    }
    const anchor = boundsAreVisible(bounds)
      ? bounds
      : { left: innerWidth / 2, top: innerHeight / 2, bottom: innerHeight / 2 };
    updateWordNavigationLabel();
    showDictionary(token, anchor, returnFocus);
  }

  function rangeBounds(range) {
    return range
      ? range.getClientRects()[0] || null
      : null;
  }

  function boundsAreVisible(bounds) {
    return Boolean(bounds &&
      Number.isFinite(bounds.top) && Number.isFinite(bounds.right) &&
      Number.isFinite(bounds.bottom) && Number.isFinite(bounds.left) &&
      bounds.bottom >= 0 && bounds.right >= 0 &&
      bounds.left <= innerWidth && bounds.top <= innerHeight);
  }

  function updateWordNavigationLabel() {
    if (!nextWordButton) {
      return;
    }
    const progress = wordCursor >= 0
      ? ", currently " + (wordCursor + 1) + " of " + wordOccurrences.length
      : ", " + wordOccurrences.length + " total";
    nextWordButton.setAttribute("aria-label", "Look up next word" + progress);
  }

  function mineSelectedWord(event) {
    if (!event.isTrusted) {
      return;
    }
    captureSelectedWord(null);
  }

  async function captureSelectedWord(suggestedEntrySequence) {
    const token = selectedWord;
    dismissDictionary(true);
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
    const token = selectedWord;
    const returnFocus = dictionaryReturnFocus;
    dismissDictionary(false);
    if (!token) {
      return;
    }
    const key = token.expression || token.surface;
    const removedRanges = new Set();
    wordOccurrences.forEach(function (candidate) {
      if ((candidate.expression || candidate.surface) === key) {
        candidate.marked = false;
        candidate.ranges.forEach(function (range) { removedRanges.add(range); });
      }
    });
    highlightRanges = highlightRanges.filter(function (range) { return !removedRanges.has(range); });
    const remaining = wordOccurrences.filter(function (candidate) { return candidate.marked; }).length;
    panelDetail.textContent = "Ignored 「" + token.surface + "」 on this page · " +
      remaining + (remaining === 1 ? " highlighted word remains" : " highlighted words remain");
    toggleButton.hidden = highlightRanges.length === 0;
    applyHighlights();
    if (returnFocus && returnFocus.isConnected && typeof returnFocus.focus === "function") {
      returnFocus.focus({ preventScroll: true });
    }
  }

  async function ignoreUnknownEverywhere(event) {
    if (!event.isTrusted || !selectedWord) {
      return;
    }
    const token = selectedWord;
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
      if (token !== selectedWord) return;
      ignoreUnknown(event);
      panelDetail.textContent = "Ignored 「" + token.surface + "」 everywhere";
    } catch (_error) {
      panelDetail.textContent = "Could not update ignored words";
    }
  }

  function dismissDictionary(restoreFocus) {
    lookupRevision += 1;
    const returnFocus = dictionaryReturnFocus;
    selectedWord = undefined;
    dictionaryReturnFocus = undefined;
    if (wordActionMenu) wordActionMenu.remove();
    wordActionMenu = undefined;
    wordAnchor = undefined;
    wordLookupContent = undefined;
    if (globalThis.CSS && CSS.highlights) {
      CSS.highlights.delete(ACTIVE_HIGHLIGHT_NAME);
    }
    if (restoreFocus && returnFocus && returnFocus.isConnected && typeof returnFocus.focus === "function") {
      returnFocus.focus({ preventScroll: true });
    }
  }

  function resetWordNavigation() {
    mineStatusRevision += 1;
    wordOccurrences = [];
    wordCursor = -1;
    dismissDictionary(false);
    if (nextWordButton) {
      nextWordButton.hidden = true;
      nextWordButton.setAttribute("aria-label", "Look up next word");
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
    resetWordNavigation();
    if (panel) {
      panelPreferenceRevision += 1;
      readerHost.remove();
      readerHost = undefined;
      readerRoot = undefined;
      readerStyle = undefined;
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
    const token = wordAtPoint(event);
    if (token) {
      const selection = typeof getSelection === "function" ? getSelection() : null;
      if (event.button !== undefined && event.button !== 0 ||
          event.metaKey || event.ctrlKey || event.altKey || event.shiftKey ||
          selection && !selection.isCollapsed) {
        dismissDictionary();
        return;
      }
      event.preventDefault();
      showDictionary(token, rectAtPoint(token, event), document.activeElement);
    } else {
      dismissDictionary();
    }
  }, true);
  document.addEventListener("keydown", function (event) {
    if (event.key === "Escape") {
      dismissDictionary(true);
    }
  });
  document.addEventListener("scroll", function (event) {
    if (event.target === readerHost ||
        (event.target.closest && event.target.closest("[data-goi-coverage-ui]"))) return;
    // The scroll from keyboard navigation can arrive after the popup opens.
    if (selectedWord && selectedWord.ranges.some(function (range) {
      return Array.from(range.getClientRects()).some(function (rect) {
        return rect.left === wordAnchor.left && rect.top === wordAnchor.top;
      });
    })) return;
    dismissDictionary(false);
  }, true);
  globalThis.addEventListener("resize", positionDictionary);

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
    setStyles(css) {
      readerCSS = css;
      if (readerStyle) readerStyle.textContent = css;
    },
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
