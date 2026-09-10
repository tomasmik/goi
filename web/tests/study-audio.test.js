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

test("plays primed audio when a correct answer advances directly", (t) => {
  const { instances } = mockAudio(t);
  primeFeedbackAudio({ dataset: { feedbackAudioSrc: "/media/9" } });
  assert.equal(instances[0].playCalls, 1);
  assert.equal(instances[0].loop, true);
  assert.equal(instances[0].volume, 0);

  playFeedbackAudio(feedbackStage(null, true));
  assert.equal(instances.length, 1);
  assert.equal(instances[0].playCalls, 1);
  assert.equal(instances[0].loop, false);
  assert.equal(instances[0].volume, 1);
  assert.equal(instances[0].currentTime, 0);
  assert.equal(instances[0].paused, false);
});

test("correction controls reuse the primed element and accessible label", (t) => {
  const { instances, FakeAudio } = mockAudio(t);
  primeFeedbackAudio({ dataset: { feedbackAudioSrc: "/media/9" } });
  const primed = instances[0];
  const rendered = new FakeAudio("/media/9");
  rendered.setAttribute("aria-label", "Word pronunciation");

  playFeedbackAudio(feedbackStage(rendered));
  assert.equal(rendered.replacement, primed);
  assert.equal(primed.controls, true);
  assert.equal(primed.getAttribute("aria-label"), "Word pronunciation");
  assert.equal(primed.getAttribute("data-feedback-audio"), "");
  assert.equal(primed.volume, 1);
  assert.equal(primed.loop, false);
  assert.equal(rendered.playCalls, 0);
});

test("a stage without pronunciation stops and discards the primed element", (t) => {
  const { instances } = mockAudio(t);
  primeFeedbackAudio({ dataset: { feedbackAudioSrc: "/media/9" } });
  playFeedbackAudio(feedbackStage());
  assert.equal(instances[0].paused, true);

  playFeedbackAudio(feedbackStage(null, true));
  assert.equal(instances[0].playCalls, 1);
  assert.equal(instances[0].volume, 0);
});

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
