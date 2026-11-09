import assert from "node:assert/strict";
import test from "node:test";

import { primeFeedbackAudio, playFeedbackAudio } from "../static/js/study-audio.js";

function mockAudio(t) {
  const originalAudio = globalThis.Audio;
  const originalHTMLAudioElement = globalThis.HTMLAudioElement;
  const instances = [];
  class FakeAudio {
    constructor(src) {
      this.src = src;
      this.paused = true;
      this.readyState = 1;
      this.attributes = {};
      this.playCalls = 0;
      instances.push(this);
    }
    play() {
      this.paused = false;
      this.playCalls++;
      return this.playResult || Promise.resolve();
    }
    pause() { this.paused = true; }
    setAttribute(name, value) { this.attributes[name] = value; }
    getAttribute(name) { return this.attributes[name]; }
    replaceWith(audio) { this.replacement = audio; }
  }
  globalThis.Audio = FakeAudio;
  globalThis.HTMLAudioElement = FakeAudio;
  t.after(() => {
    playFeedbackAudio(feedbackStage());
    globalThis.Audio = originalAudio;
    globalThis.HTMLAudioElement = originalHTMLAudioElement;
  });
  return { instances, FakeAudio };
}

function feedbackStage(renderedAudio = null, playPrimed = false) {
  return {
    hasAttribute: (name) => playPrimed && name === "data-play-primed-feedback-audio",
    querySelector: () => renderedAudio
  };
}

test("a late priming rejection cannot discard a newer primed element", async (t) => {
  const { instances, FakeAudio } = mockAudio(t);
  let rejectFirstPlay;
  const firstPlay = new Promise((_resolve, reject) => { rejectFirstPlay = reject; });
  FakeAudio.prototype.playResult = firstPlay;
  primeFeedbackAudio({ dataset: { feedbackAudioSrc: "/media/9" } });
  delete FakeAudio.prototype.playResult;
  primeFeedbackAudio({ dataset: { feedbackAudioSrc: "/media/10" } });
  rejectFirstPlay(new Error("autoplay blocked"));
  await Promise.resolve();

  playFeedbackAudio(feedbackStage(null, true));
  assert.equal(instances[0].paused, true);
  assert.equal(instances[1].paused, false);
  assert.equal(instances[1].volume, 1);
  assert.equal(instances[1].loop, false);
});
