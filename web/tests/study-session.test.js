import assert from "node:assert/strict";
import test from "node:test";

import {
  lessonNavigationKeyboardAction,
  reviewConfirmationKeyboardAction,
  reviewCorrectionKeyboardAction,
  selfGradeKeyboardAction,
  stageFocusTarget
} from "../static/js/study-session.js";

test("maps confirmation keys to deliberate review actions", () => {
  assert.equal(reviewConfirmationKeyboardAction({ key: "Enter" }), "confirm");
  assert.equal(reviewConfirmationKeyboardAction({ key: "Enter" }, false), "block");
  assert.equal(reviewConfirmationKeyboardAction({ key: "Enter", repeat: true }), "block");
  assert.equal(reviewConfirmationKeyboardAction({ key: "Escape" }), "retry");
  assert.equal(reviewConfirmationKeyboardAction({ key: "ArrowRight" }), "");
});

test("ignores composing and modified confirmation keys", () => {
  for (const blocked of [
    { isComposing: true },
    { metaKey: true },
    { ctrlKey: true },
    { altKey: true },
    { shiftKey: true },
    { defaultPrevented: true }
  ]) {
    assert.equal(reviewConfirmationKeyboardAction({ key: "Enter", ...blocked }), "");
    assert.equal(reviewConfirmationKeyboardAction({ key: "Escape", ...blocked }), "");
  }
});

test("maps correction keys to review recovery actions", () => {
  assert.equal(reviewCorrectionKeyboardAction({ key: "e" }), "details");
  assert.equal(reviewCorrectionKeyboardAction({ key: "s" }), "synonym");
  assert.equal(reviewCorrectionKeyboardAction({ key: "m" }), "mark-correct");
  assert.equal(reviewCorrectionKeyboardAction({ key: "Enter" }), "");
});

test("ignores repeated, composing, and modified correction keys", () => {
  for (const blocked of [
    { repeat: true },
    { isComposing: true },
    { metaKey: true },
    { ctrlKey: true },
    { altKey: true },
    { shiftKey: true },
    { defaultPrevented: true }
  ]) {
    assert.equal(reviewCorrectionKeyboardAction({ key: "m", ...blocked }), "");
  }
});

test("maps deliberate lesson arrow keys to navigation", () => {
  assert.equal(lessonNavigationKeyboardAction({ key: "ArrowLeft" }), "back");
  assert.equal(lessonNavigationKeyboardAction({ key: "ArrowRight" }), "next");
  assert.equal(lessonNavigationKeyboardAction({ key: "Enter" }), "");
});

test("ignores unsafe lesson navigation keys", () => {
  for (const blocked of [
    { repeat: true },
    { isComposing: true },
    { metaKey: true },
    { ctrlKey: true },
    { altKey: true },
    { shiftKey: true },
    { defaultPrevented: true }
  ]) {
    assert.equal(lessonNavigationKeyboardAction({ key: "ArrowRight", ...blocked }), "");
  }
  assert.equal(lessonNavigationKeyboardAction({ key: "ArrowRight" }, true), "");
});

test("maps self-grade keys before and after reveal", () => {
  assert.equal(selfGradeKeyboardAction({ key: " " }, false), "reveal");
  assert.equal(selfGradeKeyboardAction({ key: "Spacebar" }, false), "reveal");
  assert.equal(selfGradeKeyboardAction({ key: "1" }, true), "again");
  assert.equal(selfGradeKeyboardAction({ key: "2" }, true), "good");
  assert.equal(selfGradeKeyboardAction({ key: "1" }, false), "");
  assert.equal(selfGradeKeyboardAction({ key: " " }, true), "");
});

test("ignores unsafe self-grade keys", () => {
  for (const blocked of [
    { repeat: true },
    { isComposing: true },
    { metaKey: true },
    { ctrlKey: true },
    { altKey: true },
    { shiftKey: true },
    { defaultPrevented: true }
  ]) {
    assert.equal(selfGradeKeyboardAction({ key: " ", ...blocked }, false), "");
    assert.equal(selfGradeKeyboardAction({ key: "2", ...blocked }, true), "");
  }
});

test("focuses the useful control or heading after replacing a study stage", () => {
  const autofocus = {};
  const autofocusStage = {
    dataset: {},
    querySelector(selector) {
      assert.equal(selector, "[autofocus]");
      return autofocus;
    }
  };
  assert.equal(stageFocusTarget(autofocusStage), autofocus);

  const heading = {};
  const selectors = [];
  const headingStage = {
    dataset: {},
    querySelector(selector) {
      selectors.push(selector);
      return selector === "[autofocus]" ? null : heading;
    }
  };
  assert.equal(stageFocusTarget(headingStage), heading);
  assert.equal(selectors.length, 2);
  assert.equal(stageFocusTarget(headingStage, false), null);
  assert.equal(selectors.length, 3);
});
