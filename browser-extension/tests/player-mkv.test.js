const test = require("node:test");
const assert = require("node:assert/strict");
const zlib = require("node:zlib");

const playerMkv = require("../player/player-mkv.js");
const { parseSubtitleFile } = require("../shared/subtitle-file-model.js");

function idBytes(id) {
  const bytes = [];
  for (let value = id; value > 0; value = Math.floor(value / 256)) {
    bytes.unshift(value & 0xff);
  }
  return bytes;
}

function element(id, ...content) {
  const data = Buffer.concat(content.map(function (part) {
    return Buffer.isBuffer(part) ? part : Buffer.from(typeof part === "string" ? part : [part]);
  }));
  const size = Buffer.alloc(8);
  size.writeBigUInt64BE(BigInt(data.length));
  size[0] = 0x01;
  return Buffer.concat([Buffer.from(idBytes(id)), size, data]);
}

function block(track, relative, payload) {
  const header = Buffer.from([0x80 | track, (relative >> 8) & 0xff, relative & 0xff, 0]);
  return Buffer.concat([header, Buffer.from(payload)]);
}

function track(number, type, codecID, extra = []) {
  return element(0xae, element(0xd7, number), element(0x83, type), element(0x86, codecID), ...extra);
}

function fixture() {
  const zlibEncoding = element(0x6d80, element(0x6240, element(0x5034, element(0x4254, 0))));
  const stripEncoding = element(0x6d80, element(0x6240, element(0x5034, element(0x4254, 3), element(0x4255, "Dia"))));
  const assHeader = "[Script Info]\nScriptType: v4.00+\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n";
  const tracks = element(0x1654ae6b,
    track(1, 1, "V_VP9"),
    track(2, 2, "A_OPUS", [element(0x22b59c, "jpn"), element(0x88, 1)]),
    track(3, 2, "A_OPUS", [element(0x22b59c, "eng"), element(0x536e, "Commentary"), element(0x88, 0)]),
    track(4, 17, "S_TEXT/UTF8", [element(0x22b59c, "jpn"), zlibEncoding]),
    track(5, 17, "S_TEXT/ASS", [element(0x63a2, assHeader)]),
    track(6, 17, "S_TEXT/UTF8", [stripEncoding]),
    track(7, 17, "S_HDMV/PGS")
  );
  const cluster = element(0x1f43b675,
    element(0xbf, Buffer.alloc(4)),
    element(0xe7, Buffer.from([0x03, 0xe8])),
    element(0xa3, block(1, 0, "video-frame")),
    element(0xa0, element(0xa1, block(4, 500, zlib.deflateSync("こんにちは"))), element(0x9b, Buffer.from([0x05, 0xdc]))),
    element(0xa0, element(0xa1, block(5, 0, "0,0,Default,,0,0,0,,{\\b1}Hello\\Nthere")), element(0x9b, Buffer.from([0x03, 0xe8]))),
    element(0xa3, block(6, 0, "logue")),
    element(0xa3, block(2, 0, "audio-frame"))
  );
  return new Blob([
    element(0x1a45dfa3, element(0x4282, "matroska")),
    element(0x18538067, element(0x1549a966, element(0x2ad7b1, Buffer.from([0x0f, 0x42, 0x40]))), tracks, cluster)
  ]);
}

test("readInfo lists tracks and only text subtitles are importable", async function () {
  const info = await playerMkv.readInfo(fixture());
  assert.deepEqual(info.tracks.map(function (entry) {
    return [entry.number, entry.type, entry.language, entry.name];
  }), [
    [1, "video", "eng", ""],
    [2, "audio", "jpn", ""],
    [3, "audio", "eng", "Commentary"],
    [4, "subtitle", "jpn", ""],
    [5, "subtitle", "eng", ""],
    [6, "subtitle", "eng", ""],
    [7, "subtitle", "eng", ""]
  ]);
  assert.deepEqual(info.tracks.filter(playerMkv.isTextSubtitle).map(function (entry) { return entry.number; }), [4, 5, 6]);
  assert.equal(await playerMkv.readInfo(new Blob(["not a video"])), null);
});

test("extractSubtitles decodes compressed SRT and ASS into parseable files", async function () {
  const blob = fixture();
  const extracted = await playerMkv.extractSubtitles(blob, await playerMkv.readInfo(blob));
  const cues = Object.fromEntries(extracted.map(function (item) {
    return [item.track.number, parseSubtitleFile(new TextEncoder().encode(item.text)).cues.map(function (cue) {
      return [cue.startMs, cue.endMs, cue.text];
    })];
  }));
  assert.deepEqual(cues, {
    4: [[1500, 3000, "こんにちは"]],
    5: [[1000, 2000, "Hello\nthere"]],
    6: [[1000, 6000, "Dialogue"]]
  });
});

test("withAudioTrackFirst moves the chosen audio entry first without changing file size", async function () {
  const blob = fixture();
  const info = await playerMkv.readInfo(blob);
  const swapped = playerMkv.withAudioTrackFirst(blob, info, 3);
  assert.equal(swapped.size, blob.size);
  const swappedInfo = await playerMkv.readInfo(swapped);
  assert.deepEqual(swappedInfo.tracks.map(function (entry) {
    return [entry.number, entry.flagDefault];
  }), [[1, true], [3, true], [2, false], [4, true], [5, true], [6, true], [7, true]]);
  assert.deepEqual(
    new Uint8Array(await swapped.slice(info.tracksElement.end).arrayBuffer()),
    new Uint8Array(await blob.slice(info.tracksElement.end).arrayBuffer())
  );
  assert.equal(playerMkv.withAudioTrackFirst(blob, info, 2).size, blob.size);
});
