import { initKanaInputs } from "./kana-input.js";
import { initCopyButtons } from "./clipboard.js";
import { initMiningCapture } from "./mining-handoff.js";
import { initMiningEnrichment } from "./mining-enrichment.js";
import { initMiningTools } from "./mining-tools.js";
import { navigationSection } from "./navigation.js";
import { initStudySessions } from "./study-session.js";
import { initConfirmations } from "./confirmation.js";
import { initLessonPicker } from "./lesson-picker.js";
import { initVocabularyTools } from "./vocabulary-tools.js";
import { initFilePickers } from "./file-picker.js";
import {
  initBackupSettings,
  initCurrentOrigins,
  initExampleProviderPresets,
  initLocalTimes,
  initCurrentTabs,
  initSettingsSaveBars,
  initTimeZoneControls,
  initTranslationProviderPanels,
} from "./settings-controls.js";

document.documentElement.classList.add("js");

function initNavigation() {
  const section = navigationSection(window.location.pathname);
  if (!section) {
    return;
  }
  const current = document.querySelector(`[data-nav-section="${section}"]`);
  current?.setAttribute("aria-current", "page");
}

document.addEventListener("DOMContentLoaded", () => {
  initNavigation();
  initKanaInputs();
  initMiningCapture();
  initMiningEnrichment();
  initMiningTools();
  initStudySessions();
  initConfirmations(document);
  initLessonPicker();
  initVocabularyTools();
  initCopyButtons();
  initFilePickers();
  initBackupSettings();
  initTimeZoneControls();
  initExampleProviderPresets();
  initTranslationProviderPanels();
  initCurrentOrigins();
  initLocalTimes();
  initSettingsSaveBars();
  initCurrentTabs();
});
