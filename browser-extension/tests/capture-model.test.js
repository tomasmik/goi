const test = require("node:test");
const assert = require("node:assert/strict");

const {
  buildCapturePayload,
  classifyAPIError,
  coveragePercent,
  normalizeOrigin,
  normalizeWhitespace,
  permissionPattern,
  resolveCaptureAttribution,
  sentenceContext,
  truncateCodePoints,
} = require("../shared/capture-model.js");

test("normalizes whitespace and truncates by Unicode code point", () => {
  assert.equal(normalizeWhitespace("  食べる\n\t now  "), "食べる now");
  assert.equal(truncateCodePoints("a😀b", 2), "a😀");
  assert.throws(() => truncateCodePoints("text", -1), RangeError);
});

test("only reports complete coverage as 100 percent", () => {
  assert.equal(coveragePercent(200, 200), "100");
  assert.equal(coveragePercent(199, 200), "99.5");
  assert.equal(coveragePercent(999999, 1000000), "99.9");
  assert.equal(coveragePercent(1, 3), "33.3");
  assert.equal(coveragePercent(0, 0), "—");
});

test("finds Japanese and English sentence context", () => {
  const japanese = "最初です。猫を見た！最後です。";
  const japaneseStart = japanese.indexOf("猫");
  assert.equal(
    sentenceContext(japanese, japaneseStart, japaneseStart + 1, "ja"),
    "猫を見た！",
  );

  const english = "First sentence. The target is here! Last sentence?";
  const englishStart = english.indexOf("target");
  assert.equal(
    sentenceContext(english, englishStart, englishStart + "target".length, "en"),
    "The target is here!",
  );
});

test("punctuation fallback finds Japanese and English sentence boundaries", () => {
  const originalSegmenter = Intl.Segmenter;
  Intl.Segmenter = undefined;
  try {
    const japanese = "一つ目。探している語彙！？三つ目。";
    const japaneseStart = japanese.indexOf("語彙");
    assert.equal(
      sentenceContext(japanese, japaneseStart, japaneseStart + 2, "ja"),
      "探している語彙！？"
    );

    const english = "Before. Find this word! After?";
    const englishStart = english.indexOf("word");
    assert.equal(
      sentenceContext(english, englishStart, englishStart + 4, "en"),
      "Find this word!"
    );
  } finally {
    Intl.Segmenter = originalSegmenter;
  }
});

test("falls back to the normalized block when offsets are unavailable", () => {
  assert.equal(sentenceContext(" first\n  second ", null, null, "en"), "first second");
});

test("normalizes HTTP and HTTPS origins", () => {
  assert.equal(normalizeOrigin("https://goi.example:8443/"), "https://goi.example:8443");
  assert.equal(normalizeOrigin("http://localhost:8080"), "http://localhost:8080");
  assert.equal(normalizeOrigin("http://127.42.0.9:8080"), "http://127.42.0.9:8080");
  assert.equal(normalizeOrigin("http://[::1]:8080"), "http://[::1]:8080");
  assert.equal(normalizeOrigin("http://goi.example:8080"), "http://goi.example:8080");
  assert.equal(normalizeOrigin("http://192.168.1.20:8080"), "http://192.168.1.20:8080");
  assert.throws(() => normalizeOrigin("https://goi.example/path"), /only an origin/);
  assert.throws(() => normalizeOrigin("javascript:alert(1)"), /HTTP or HTTPS/);
  assert.throws(() => normalizeOrigin("https://user:secret@goi.example"), /credentials/);
});

test("preserves secure-transport errors for UI reporting", () => {
  const error = new Error("Remote Goi servers must use HTTPS");
  error.code = "insecure_transport";
  assert.equal(classifyAPIError(error), "insecure_transport");
  assert.equal(classifyAPIError({ status: 403, code: "secure_transport_required" }), "insecure_transport");
});

test("builds Chrome host patterns without origin ports", () => {
  assert.equal(permissionPattern("http://localhost:8080"), "http://localhost/*");
  assert.equal(permissionPattern("http://127.0.0.1:8080"), "http://127.0.0.1/*");
  assert.equal(permissionPattern("https://goi.example:8443"), "https://goi.example/*");
  assert.equal(permissionPattern("http://[::1]:8080"), "http://[::1]/*");
});

test("builds the exact bounded capture payload", () => {
  const nonce = "0123456789abcdef0123456789abcdef";
  const payload = buildCapturePayload({
    rawText: "  食べる  ",
    expression: " 食べる ",
    contextText: "昨日、 寿司を\n食べる。",
    sourceKind: "video",
    sourceTitle: " Lesson ",
    sourceURL: "https://example.com/watch?v=1",
    sourcePositionMs: 1234.6,
    suggestedEntrySequence: 1358280,
    ignored: "value",
  }, nonce);

  assert.deepEqual(payload, {
    capture_nonce: nonce,
    raw_text: "食べる",
    expression: "食べる",
    context_text: "昨日、 寿司を 食べる。",
    source_kind: "video",
    source_title: "Lesson",
    source_url: "https://example.com/watch?v=1",
    source_position_ms: 1235,
    suggested_entry_sequence: 1358280,
  });
  assert.match(payload.capture_nonce, /^[0-9a-f]{32}$/);
});

test("attributes captions and video pages without replacing ordinary reading context", () => {
  assert.deepEqual(
    resolveCaptureAttribution({
      contextText: "A comment about grammar.",
      activeCaption: "Unrelated active subtitle.",
      selectionInCaption: false,
      hostname: "www.youtube.com",
      pathname: "/watch",
      hasVideo: true,
    }),
    { contextText: "A comment about grammar.", sourceKind: "video" },
  );
  assert.deepEqual(
    resolveCaptureAttribution({
      contextText: "selected caption fragment",
      activeCaption: "  Complete active caption.  ",
      selectionInCaption: true,
      hostname: "www.youtube.com",
      pathname: "/watch",
      hasVideo: true,
    }),
    { contextText: "Complete active caption.", sourceKind: "video" },
  );
  assert.deepEqual(
    resolveCaptureAttribution({
      contextText: "The sentence from an article.",
      activeCaption: "",
      selectionInCaption: false,
      hostname: "news.example",
      pathname: "/story",
      hasVideo: true,
    }),
    { contextText: "The sentence from an article.", sourceKind: "web" },
  );
  assert.equal(
    resolveCaptureAttribution({
      contextText: "Search result text",
      hostname: "www.youtube.com",
      pathname: "/results",
      hasVideo: true,
    }).sourceKind,
    "web",
  );
});

test("never emits empty context for a selected word", () => {
  const payload = buildCapturePayload({ expression: "語彙" }, "a".repeat(32));
  assert.equal(payload.context_text, "語彙");
});

test("capture limits preserve complete Unicode and UTF-8 characters", () => {
  const payload = buildCapturePayload({
    capture_nonce: "n",
    expression: "😀".repeat(300),
    context_text: "文".repeat(2100),
    source_title: "題".repeat(400),
    source_url: `https://example.com/${"界".repeat(1000)}`,
  });

  assert.equal(Array.from(payload.expression).length, 256);
  assert.equal(Array.from(payload.raw_text).length, 256);
  assert.equal(Array.from(payload.context_text).length, 2000);
  assert.equal(Array.from(payload.source_title).length, 300);
  assert.ok(Buffer.byteLength(payload.source_url, "utf8") <= 2048);
  assert.equal(payload.source_position_ms, null);
  assert.equal(payload.source_kind, "web");
});

test("long source URLs remain valid after bounding", () => {
  const payload = buildCapturePayload({
    expression: "語彙",
    source_url: `https://example.com/read?term=${"%E8%AA%9E".repeat(800)}`,
  }, "a".repeat(32));

  assert.doesNotThrow(() => new URL(payload.source_url));
  assert.ok(Buffer.byteLength(payload.source_url, "utf8") <= 2048);
  assert.equal(payload.source_url, "https://example.com/read");
});

test("classifies API and transport failures", () => {
  assert.equal(classifyAPIError(401), "unauthorized");
  assert.equal(classifyAPIError({ status: 409 }), "idempotency_conflict");
  assert.equal(classifyAPIError(422), "invalid_capture");
  assert.equal(classifyAPIError(429), "rate_limited");
  assert.equal(classifyAPIError(503), "server");
  assert.equal(classifyAPIError(new TypeError("fetch failed")), "network");
  assert.equal(classifyAPIError({ code: "not_connected" }), "not_connected");
  assert.equal(classifyAPIError({ code: "queue_full" }), "queue_full");
  assert.equal(classifyAPIError({ code: "dictionary_unavailable" }), "dictionary_unavailable");
  assert.equal(classifyAPIError({ code: "dictionary_api_unavailable" }), "dictionary_api_unavailable");
  assert.equal(classifyAPIError({ code: "translation_unavailable" }), "translation_unavailable");
  assert.equal(classifyAPIError({ code: "translation_failed" }), "translation_failed");
  assert.equal(classifyAPIError(404), "server");
});
