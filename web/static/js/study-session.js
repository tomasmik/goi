import { initKanaInputs } from "./kana-input.js";
import { initLocalTimes } from "./settings-controls.js";

let requestInFlight = false;
let primedFeedbackAudio = null;

export function reviewConfirmationKeyboardAction(event) {
  if (
    event.defaultPrevented ||
    event.repeat ||
    event.isComposing ||
    event.metaKey ||
    event.ctrlKey ||
    event.altKey ||
    event.shiftKey
  ) {
    return "";
  }
  if (event.key === "Enter") {
    return "confirm";
  }
  if (event.key === "Escape") {
    return "retry";
  }
  return "";
}

export function lessonNavigationKeyboardAction(event, interactiveTarget = false) {
  if (
    interactiveTarget ||
    event.defaultPrevented ||
    event.repeat ||
    event.isComposing ||
    event.metaKey ||
    event.ctrlKey ||
    event.altKey ||
    event.shiftKey
  ) {
    return "";
  }
  if (event.key === "ArrowLeft") {
    return "back";
  }
  if (event.key === "ArrowRight") {
    return "next";
  }
  return "";
}

export function selfGradeKeyboardAction(event, revealed) {
  if (
    event.defaultPrevented ||
    event.repeat ||
    event.isComposing ||
    event.metaKey ||
    event.ctrlKey ||
    event.altKey ||
    event.shiftKey
  ) {
    return "";
  }
  if (!revealed && (event.key === " " || event.key === "Spacebar")) {
    return "reveal";
  }
  if (revealed && event.key === "1") {
    return "again";
  }
  if (revealed && event.key === "2") {
    return "good";
  }
  return "";
}

function parseStage(html) {
  const template = document.createElement("template");
  template.innerHTML = html.trim();
  return template.content.querySelector("#study-stage");
}

export function stageFocusTarget(stage, includeHeading = true) {
  if (!stage) {
    return null;
  }
  const autofocus = stage.querySelector("[autofocus]");
  if (autofocus || !includeHeading) {
    return autofocus;
  }
  return stage.querySelector(
    ".correction-panel h2, .study-completion h1, .lesson-subject h1, .review-subject h1"
  );
}

function focusStage(stage, includeHeading = true) {
  const target = stageFocusTarget(stage, includeHeading);
  if (target instanceof HTMLElement) {
    if (!target.hasAttribute("autofocus")) {
      target.tabIndex = -1;
    }
    requestAnimationFrame(() => target.focus());
  }
}

function replaceStage(nextStage) {
  const current = document.querySelector("#study-stage");
  if (!current || !nextStage) {
    return false;
  }
  current.replaceWith(nextStage);
  initKanaInputs(nextStage);
  initLocalTimes(nextStage);
  focusStage(nextStage);
  return true;
}

async function loadReview(url) {
  const response = await fetch(url, {
    headers: { "X-Goi-Fragment": "review" },
    credentials: "same-origin"
  });
  if (response.redirected) {
    window.location.assign(response.url);
    return;
  }
  if (!response.ok) {
    throw new Error("review refresh failed");
  }
  const stage = parseStage(await response.text());
  if (!replaceStage(stage)) {
    throw new Error("review response was incomplete");
  }
}

function stopPrimedFeedbackAudio() {
  if (primedFeedbackAudio instanceof HTMLAudioElement) {
    primedFeedbackAudio.pause();
  }
  primedFeedbackAudio = null;
}

function primeFeedbackAudio(stage) {
  const src = stage?.dataset.feedbackAudioSrc;
  if (!src) {
    return;
  }

  stopPrimedFeedbackAudio();
  const audio = new Audio(src);
  audio.preload = "auto";
  audio.loop = true;
  audio.volume = 0;
  primedFeedbackAudio = audio;
  audio.play().catch(() => {
    if (primedFeedbackAudio === audio) {
      primedFeedbackAudio = null;
    }
  });
}

function playFeedbackAudio(stage) {
  const renderedAudio = stage.querySelector("[data-feedback-audio]");
  if (!(renderedAudio instanceof HTMLAudioElement)) {
    stopPrimedFeedbackAudio();
    return;
  }

  const audio = primedFeedbackAudio;
  if (audio instanceof HTMLAudioElement && audio.src === renderedAudio.src) {
    audio.controls = true;
    audio.loop = false;
    audio.volume = 1;
    audio.setAttribute("aria-label", renderedAudio.getAttribute("aria-label") || "Pronunciation audio");
    audio.setAttribute("data-feedback-audio", "");
    renderedAudio.replaceWith(audio);
    if (audio.readyState > 0) {
      audio.currentTime = 0;
    } else {
      audio.addEventListener("loadedmetadata", () => {
        audio.currentTime = 0;
      }, { once: true });
    }
    primedFeedbackAudio = null;
    if (audio.paused) {
      audio.play().catch(function ignoreBlockedAutoplay() {});
    }
    return;
  }

  stopPrimedFeedbackAudio();
  renderedAudio.play().catch(function ignoreBlockedAutoplay() {});
}

async function submitFragment(form, kind) {
  if (requestInFlight) {
    return;
  }
  requestInFlight = true;
  form.setAttribute("aria-busy", "true");
  const formData = new FormData(form);
  form.querySelectorAll("button, input, select, textarea").forEach((control) => {
    control.disabled = true;
  });

  try {
    const response = await fetch(form.action, {
      method: form.method || "POST",
      body: formData,
      headers: { "X-Goi-Fragment": kind },
      credentials: "same-origin"
    });
    if (response.redirected) {
      window.location.assign(response.url);
      return;
    }
    if (!response.ok) {
      throw new Error("study action failed");
    }
    const stage = parseStage(await response.text());
    if (!replaceStage(stage)) {
      throw new Error("study response was incomplete");
    }
    playFeedbackAudio(stage);
  } catch {
    window.location.reload();
  } finally {
    requestInFlight = false;
  }
}

async function retryConfirmation(confirmation) {
  if (requestInFlight) {
    return;
  }
  const retry = confirmation.querySelector("[data-review-retry]");
  if (!(retry instanceof HTMLAnchorElement)) {
    return;
  }

  requestInFlight = true;
  confirmation.setAttribute("aria-busy", "true");
  retry.setAttribute("aria-disabled", "true");
  try {
    await loadReview(retry.href);
  } catch {
    window.location.assign(retry.href);
  } finally {
    requestInFlight = false;
  }
}

function handleStudySubmit(event) {
  const form = event.target;
  if (!(form instanceof HTMLFormElement)) {
    return;
  }
  if (form.matches("[data-prime-feedback-audio]")) {
    primeFeedbackAudio(form.closest("#study-stage"));
  }
  if (form.matches("[data-review-answer], [data-review-confirm], [data-review-action]")) {
    event.preventDefault();
    submitFragment(form, "review");
    return;
  }
  if (form.matches("[data-lesson-nav]")) {
    event.preventDefault();
    submitFragment(form, "lesson");
  }
}

function handleReviewConfirmationKey(event) {
  const confirmation = document.querySelector("[data-review-confirmation]");
  if (!(confirmation instanceof HTMLElement)) {
    return false;
  }

  const action = reviewConfirmationKeyboardAction(event);
  if (action === "retry") {
    const retry = confirmation.querySelector("[data-review-retry]");
    if (!(retry instanceof HTMLAnchorElement)) {
      return false;
    }
    event.preventDefault();
    retryConfirmation(confirmation);
    return true;
  }
  if (action !== "confirm") {
    return false;
  }

  const target = event.target;
  if (
    target instanceof Element &&
    target.closest("a, button, input, textarea, select, audio, video, [contenteditable='true']") &&
    !target.closest("[data-review-confirm]")
  ) {
    return true;
  }
  const form = confirmation.querySelector("[data-review-confirm]");
  if (form instanceof HTMLFormElement) {
    event.preventDefault();
    form.requestSubmit();
  }
  return true;
}

function handleSelfGradeKey(event) {
  const selfGrade = document.querySelector("[data-review-self-grade]");
  const revealForm = document.querySelector("[data-review-reveal]");
  if (!(selfGrade instanceof HTMLElement) && !(revealForm instanceof HTMLFormElement)) {
    return false;
  }

  const target = event.target;
  const interactiveTarget =
    target instanceof Element &&
    Boolean(target.closest("input, textarea, select, audio, video, [contenteditable='true']"));
  if (interactiveTarget) {
    return false;
  }

  const action = selfGradeKeyboardAction(event, selfGrade instanceof HTMLElement);
  if (action === "reveal" && revealForm instanceof HTMLFormElement) {
    event.preventDefault();
    revealForm.requestSubmit();
    return true;
  }
  if (action !== "again" && action !== "good") {
    return false;
  }

  const button = selfGrade?.querySelector(`[data-review-grade="${action}"]`);
  if (!(button instanceof HTMLButtonElement) || !button.form) {
    return false;
  }
  event.preventDefault();
  button.form.requestSubmit(button);
  return true;
}

function handleReviewAnswerKey(event) {
  const target = event.target;
  if (
    event.key !== "Enter" ||
    event.defaultPrevented ||
    event.repeat ||
    event.isComposing ||
    event.metaKey ||
    event.ctrlKey ||
    event.altKey ||
    event.shiftKey ||
    !(target instanceof HTMLInputElement) ||
    !target.form?.matches("[data-review-answer]")
  ) {
    return false;
  }

  event.preventDefault();
  target.form.requestSubmit();
  return true;
}

function handleLessonNavigationKey(event) {
  const target = event.target;
  const interactiveTarget =
    target instanceof Element &&
    Boolean(
      target.closest(
        "a, button, input, textarea, select, audio, video, summary, [contenteditable]:not([contenteditable='false']), [role='button'], [role='link'], [tabindex]:not([tabindex='-1'])"
      )
    );
  const direction = lessonNavigationKeyboardAction(event, interactiveTarget);
  if (!direction) {
    return;
  }
  const form = document.querySelector(`[data-lesson-direction="${direction}"]`);
  if (!(form instanceof HTMLFormElement)) {
    return;
  }
  event.preventDefault();
  form.requestSubmit();
}

function handleStudyKeydown(event) {
  if (
    handleReviewConfirmationKey(event) ||
    handleSelfGradeKey(event) ||
    handleReviewAnswerKey(event)
  ) {
    return;
  }
  handleLessonNavigationKey(event);
}

export function initStudySessions() {
  document.addEventListener("submit", handleStudySubmit);
  document.addEventListener("keydown", handleStudyKeydown);

  const initialStage = document.querySelector("#study-stage");
  if (initialStage) {
    focusStage(initialStage, false);
    playFeedbackAudio(initialStage);
  }
}
