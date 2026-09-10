let primedFeedbackAudio = null;

function stopPrimedFeedbackAudio() {
  if (primedFeedbackAudio instanceof HTMLAudioElement) {
    primedFeedbackAudio.pause();
  }
  primedFeedbackAudio = null;
}

export function primeFeedbackAudio(stage) {
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

function startPrimedFeedbackAudio(audio) {
  audio.loop = false;
  audio.volume = 1;
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
}

export function playFeedbackAudio(stage) {
  const renderedAudio = stage.querySelector("[data-feedback-audio]");
  const audio = primedFeedbackAudio;
  if (stage.hasAttribute("data-play-primed-feedback-audio") && audio instanceof HTMLAudioElement) {
    startPrimedFeedbackAudio(audio);
    return;
  }
  if (!(renderedAudio instanceof HTMLAudioElement)) {
    stopPrimedFeedbackAudio();
    return;
  }

  if (audio instanceof HTMLAudioElement && audio.src === renderedAudio.src) {
    audio.controls = true;
    audio.setAttribute("aria-label", renderedAudio.getAttribute("aria-label") || "Pronunciation audio");
    audio.setAttribute("data-feedback-audio", "");
    renderedAudio.replaceWith(audio);
    startPrimedFeedbackAudio(audio);
    return;
  }

  stopPrimedFeedbackAudio();
  renderedAudio.play().catch(function ignoreBlockedAutoplay() {});
}
