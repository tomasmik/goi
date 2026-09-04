const syllables = {
  a: "あ", i: "い", u: "う", e: "え", o: "お",
  xa: "ぁ", xi: "ぃ", xu: "ぅ", xe: "ぇ", xo: "ぉ",
  la: "ぁ", li: "ぃ", lu: "ぅ", le: "ぇ", lo: "ぉ",
  ka: "か", ki: "き", ku: "く", ke: "け", ko: "こ",
  ga: "が", gi: "ぎ", gu: "ぐ", ge: "げ", go: "ご",
  sa: "さ", shi: "し", si: "し", su: "す", se: "せ", so: "そ",
  za: "ざ", ji: "じ", zi: "じ", zu: "ず", ze: "ぜ", zo: "ぞ",
  ta: "た", chi: "ち", ti: "ち", tsu: "つ", tu: "つ", te: "て", to: "と",
  da: "だ", di: "ぢ", du: "づ", de: "で", do: "ど",
  na: "な", ni: "に", nu: "ぬ", ne: "ね", no: "の",
  ha: "は", hi: "ひ", fu: "ふ", hu: "ふ", he: "へ", ho: "ほ",
  ba: "ば", bi: "び", bu: "ぶ", be: "べ", bo: "ぼ",
  pa: "ぱ", pi: "ぴ", pu: "ぷ", pe: "ぺ", po: "ぽ",
  ma: "ま", mi: "み", mu: "む", me: "め", mo: "も",
  ya: "や", yi: "い", yu: "ゆ", ye: "いぇ", yo: "よ",
  xya: "ゃ", xyu: "ゅ", xyo: "ょ",
  lya: "ゃ", lyu: "ゅ", lyo: "ょ",
  ra: "ら", ri: "り", ru: "る", re: "れ", ro: "ろ",
  wa: "わ", wi: "うぃ", we: "うぇ", wo: "を",
  wha: "うぁ", whi: "うぃ", whe: "うぇ", who: "うぉ",
  xwa: "ゎ", lwa: "ゎ",
  xtu: "っ", xtsu: "っ", ltu: "っ", ltsu: "っ",
  kya: "きゃ", kyu: "きゅ", kyo: "きょ",
  gya: "ぎゃ", gyu: "ぎゅ", gyo: "ぎょ",
  sha: "しゃ", shu: "しゅ", she: "しぇ", sho: "しょ",
  sya: "しゃ", syu: "しゅ", syo: "しょ",
  ja: "じゃ", ju: "じゅ", je: "じぇ", jo: "じょ",
  jya: "じゃ", jyu: "じゅ", jyo: "じょ",
  cha: "ちゃ", chu: "ちゅ", che: "ちぇ", cho: "ちょ",
  tya: "ちゃ", tyu: "ちゅ", tyo: "ちょ",
  nya: "にゃ", nyu: "にゅ", nyo: "にょ",
  hya: "ひゃ", hyu: "ひゅ", hyo: "ひょ",
  bya: "びゃ", byu: "びゅ", byo: "びょ",
  pya: "ぴゃ", pyu: "ぴゅ", pyo: "ぴょ",
  mya: "みゃ", myu: "みゅ", myo: "みょ",
  rya: "りゃ", ryu: "りゅ", ryo: "りょ",
  tsa: "つぁ", tsi: "つぃ", tse: "つぇ", tso: "つぉ",
  tha: "てゃ", thi: "てぃ", thu: "てゅ", the: "てぇ", tho: "てょ",
  dha: "でゃ", dhi: "でぃ", dhu: "でゅ", dhe: "でぇ", dho: "でょ",
  twa: "とぁ", twi: "とぃ", twu: "とぅ", twe: "とぇ", two: "とぉ",
  dwa: "どぁ", dwi: "どぃ", dwu: "どぅ", dwe: "どぇ", dwo: "どぉ",
  kwa: "くぁ", kwi: "くぃ", kwe: "くぇ", kwo: "くぉ",
  gwa: "ぐぁ", gwi: "ぐぃ", gwe: "ぐぇ", gwo: "ぐぉ",
  fa: "ふぁ", fi: "ふぃ", fe: "ふぇ", fo: "ふぉ",
  fya: "ふゃ", fyu: "ふゅ", fyo: "ふょ",
  va: "ゔぁ", vi: "ゔぃ", vu: "ゔ", ve: "ゔぇ", vo: "ゔぉ",
  vya: "ゔゃ", vyu: "ゔゅ", vyo: "ゔょ"
};

const syllableKeys = Object.keys(syllables);
const romajiRunPattern = /[A-Za-z']+/g;
const syllableLengths = [4, 3, 2, 1];

function toKatakana(value) {
  return [...value]
    .map((character) => {
      const code = character.charCodeAt(0);
      return code >= 0x3041 && code <= 0x3096
        ? String.fromCharCode(code + 0x60)
        : character;
    })
    .join("");
}

function isConsonant(character) {
  return /^[a-z]$/.test(character) && !"aeiou".includes(character);
}

function isPendingRomaji(value) {
  return syllableKeys.some((syllable) => syllable.startsWith(value));
}

function isUpperRomaji(value) {
  return /[A-Z]/.test(value) && value === value.toUpperCase();
}

function convertRun(value, flush) {
  const lower = value.toLowerCase();
  const useKatakana = isUpperRomaji(value);
  let output = "";
  let index = 0;

  const writeKana = (kana) => {
    output += useKatakana ? toKatakana(kana) : kana;
  };

  while (index < lower.length) {
    if (lower[index] === "'") {
      index += 1;
      continue;
    }

    if (lower.startsWith("tch", index)) {
      writeKana("っ");
      index += 1;
      continue;
    }

    if (
      index + 1 < lower.length &&
      lower[index] === lower[index + 1] &&
      isConsonant(lower[index]) &&
      lower[index] !== "n"
    ) {
      writeKana("っ");
      index += 1;
      continue;
    }

    if (lower[index] === "n") {
      if (index + 1 === lower.length) {
        if (flush) {
          writeKana("ん");
        } else {
          output += value.slice(index);
        }
        break;
      }

      const next = lower[index + 1];
      if (next === "'") {
        writeKana("ん");
        index += 2;
        continue;
      }
      if (next === "n") {
        writeKana("ん");
        index += 2;
        continue;
      }
      if (isConsonant(next) && next !== "y") {
        writeKana("ん");
        index += 1;
        continue;
      }
    }

    let matched = false;
    for (const length of syllableLengths) {
      const part = lower.slice(index, index + length);
      if (!syllables[part]) {
        continue;
      }
      writeKana(syllables[part]);
      index += length;
      matched = true;
      break;
    }
    if (matched) {
      continue;
    }

    const remainder = lower.slice(index);
    if (!flush && (isPendingRomaji(remainder) || isConsonant(remainder))) {
      output += value.slice(index);
      break;
    }

    output += value[index];
    index += 1;
  }
  return output;
}

export function convertKana(value, flush = false) {
  const converted = value.replace(romajiRunPattern, (run, offset) => {
    const endsAtBoundary = offset + run.length < value.length;
    return convertRun(run, flush || endsAtBoundary);
  });
  return converted.replaceAll("-", "ー");
}

function updateMode(input) {
  const indicator = input.parentElement?.querySelector("[data-kana-mode]");
  if (!indicator) {
    return;
  }
  const lastRun = input.value.match(/[A-Za-z']+$/)?.[0] || "";
  const katakanaRun = isUpperRomaji(lastRun);
  const katakanaValue = /[\u30a1-\u30f6]/.test(input.value) && !/[\u3041-\u3096]/.test(input.value);
  indicator.textContent = katakanaRun || katakanaValue ? "Katakana" : "Hiragana";
}

function bind(input) {
  let composing = false;
  let dispatchingInput = false;

  const update = (flush = false) => {
    const before = input.value;
    const selectionStart = input.selectionStart ?? before.length;
    const selectionEnd = input.selectionEnd ?? selectionStart;
    const after = convertKana(before, flush);
    if (before !== after) {
      const nextStart = convertKana(before.slice(0, selectionStart), flush).length;
      const nextEnd = convertKana(before.slice(0, selectionEnd), flush).length;
      input.value = after;
      input.setSelectionRange(nextStart, nextEnd);
    }
    updateMode(input);
  };

  input.addEventListener("compositionstart", () => {
    composing = true;
  });
  input.addEventListener("compositionend", () => {
    composing = false;
    update();
  });
  input.addEventListener("beforeinput", (event) => {
    if (
      composing ||
      event.isComposing ||
      !event.cancelable ||
      event.inputType !== "insertText" ||
      typeof event.data !== "string" ||
      !/^[A-Za-z']+$/.test(event.data)
    ) {
      return;
    }

    event.preventDefault();
    const start = input.selectionStart ?? input.value.length;
    const end = input.selectionEnd ?? start;
    input.value = input.value.slice(0, start) + event.data + input.value.slice(end);
    const caret = start + event.data.length;
    input.setSelectionRange(caret, caret);
    update();

    dispatchingInput = true;
    try {
      input.dispatchEvent(new Event("input", { bubbles: true }));
    } finally {
      dispatchingInput = false;
    }
  });
  input.addEventListener("input", (event) => {
    if (!dispatchingInput && !composing && !event.isComposing) {
      update();
    }
  });
  input.addEventListener("blur", () => {
    if (!composing) {
      update(true);
    }
  });
  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && !composing && !event.isComposing) {
      update(true);
    }
  });
  input.form?.addEventListener("submit", () => {
    update(true);
  });
  update();
}

export function initKanaInputs(root = document) {
  root.querySelectorAll("[data-kana-input]").forEach((input) => {
    if (!(input instanceof HTMLInputElement) || input.dataset.kanaBound) {
      return;
    }
    input.dataset.kanaBound = "true";
    bind(input);
  });
}
