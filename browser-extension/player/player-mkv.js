(function (root, factory) {
  const api = factory();
  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.playerMkv = api;
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
})(globalThis, function () {
  "use strict";

  const ID = {
    EBML: 0x1a45dfa3,
    Segment: 0x18538067,
    Info: 0x1549a966,
    TimestampScale: 0x2ad7b1,
    Tracks: 0x1654ae6b,
    TrackEntry: 0xae,
    TrackNumber: 0xd7,
    TrackType: 0x83,
    CodecID: 0x86,
    CodecPrivate: 0x63a2,
    Language: 0x22b59c,
    LanguageBCP47: 0x22b59d,
    Name: 0x536e,
    FlagDefault: 0x88,
    ContentEncodings: 0x6d80,
    ContentEncoding: 0x6240,
    ContentCompression: 0x5034,
    ContentCompAlgo: 0x4254,
    ContentCompSettings: 0x4255,
    ContentEncryption: 0x5035,
    Cluster: 0x1f43b675,
    Timestamp: 0xe7,
    SimpleBlock: 0xa3,
    BlockGroup: 0xa0,
    Block: 0xa1,
    BlockDuration: 0x9b
  };
  const CLUSTER_CHILDREN = new Set([0xe7, 0xa3, 0xa0, 0xa7, 0xab, 0x5854, 0xaf, 0xec, 0xbf]);
  const TRACK_TYPE = { 1: "video", 2: "audio", 17: "subtitle" };
  const TEXT_CODECS = new Set(["S_TEXT/UTF8", "S_TEXT/ASS", "S_TEXT/SSA", "S_TEXT/WEBVTT", "D_WEBVTT/SUBTITLES"]);
  const CHUNK_BYTES = 4 * 1024 * 1024;
  const HEADER_SCAN_BYTES = 16 * 1024 * 1024;
  const UNKNOWN_SIZE = -1;

  function readVint(bytes, offset, keepMarker) {
    const first = bytes[offset];
    if (!first) {
      return null;
    }
    const length = Math.clz32(first) - 23;
    if (offset + length > bytes.length) {
      return null;
    }
    const mask = 0xff >> length;
    let value = keepMarker ? first : first & mask;
    let allOnes = (first & mask) === mask;
    for (let index = 1; index < length; index += 1) {
      value = value * 256 + bytes[offset + index];
      allOnes = allOnes && bytes[offset + index] === 0xff;
    }
    return { value: !keepMarker && allOnes ? UNKNOWN_SIZE : value, length };
  }

  function readHeader(bytes, offset) {
    const id = readVint(bytes, offset, true);
    if (!id) {
      return null;
    }
    const size = readVint(bytes, offset + id.length, false);
    if (!size) {
      return null;
    }
    const dataStart = offset + id.length + size.length;
    return {
      id: id.value,
      start: offset,
      dataStart,
      size: size.value,
      end: size.value === UNKNOWN_SIZE ? Infinity : dataStart + size.value
    };
  }

  function children(bytes, start, end) {
    const result = [];
    let offset = start;
    while (offset < end) {
      const header = readHeader(bytes, offset);
      if (!header || header.end > end) {
        break;
      }
      result.push(header);
      offset = header.end;
    }
    return result;
  }

  function child(bytes, parent, id) {
    return children(bytes, parent.dataStart, parent.end).find(function (element) {
      return element.id === id;
    });
  }

  function uint(bytes, element) {
    let value = 0;
    for (let index = element.dataStart; index < element.end; index += 1) {
      value = value * 256 + bytes[index];
    }
    return value;
  }

  function text(bytes, element) {
    return new TextDecoder().decode(bytes.subarray(element.dataStart, element.end)).replace(/\0+$/u, "");
  }

  function createReader(blob) {
    let bufferStart = 0;
    let buffer = new Uint8Array(0);
    return {
      size: blob.size,
      async view(position, length) {
        const end = Math.min(blob.size, position + length);
        if (position < bufferStart || end > bufferStart + buffer.length) {
          bufferStart = position;
          buffer = new Uint8Array(await blob.slice(position, Math.min(blob.size, position + Math.max(length, CHUNK_BYTES))).arrayBuffer());
        }
        return buffer.subarray(position - bufferStart, end - bufferStart);
      },
      async header(position) {
        const bytes = await this.view(position, 12);
        const header = readHeader(bytes, 0);
        if (!header) {
          return null;
        }
        const shift = position;
        return {
          id: header.id,
          start: shift,
          dataStart: shift + header.dataStart,
          size: header.size,
          end: header.size === UNKNOWN_SIZE ? Infinity : shift + header.end
        };
      }
    };
  }

  function parseTrackEntry(bytes, entry) {
    const fields = new Map(children(bytes, entry.dataStart, entry.end).map(function (element) {
      return [element.id, element];
    }));
    const value = function (id, read, fallback) {
      return fields.has(id) ? read(bytes, fields.get(id)) : fallback;
    };
    let compression = null;
    let encrypted = false;
    const encodings = fields.get(ID.ContentEncodings);
    if (encodings) {
      children(bytes, encodings.dataStart, encodings.end).forEach(function (encoding) {
        if (encoding.id !== ID.ContentEncoding) {
          return;
        }
        if (child(bytes, encoding, ID.ContentEncryption)) {
          encrypted = true;
        }
        const compressionElement = child(bytes, encoding, ID.ContentCompression);
        if (compressionElement) {
          const algorithm = child(bytes, compressionElement, ID.ContentCompAlgo);
          const settings = child(bytes, compressionElement, ID.ContentCompSettings);
          compression = {
            algorithm: algorithm ? uint(bytes, algorithm) : 0,
            settings: settings ? bytes.slice(settings.dataStart, settings.end) : new Uint8Array(0)
          };
        }
      });
    }
    const codecPrivate = fields.get(ID.CodecPrivate);
    return {
      number: value(ID.TrackNumber, uint, 0),
      type: TRACK_TYPE[value(ID.TrackType, uint, 0)] || "other",
      codecID: value(ID.CodecID, text, ""),
      language: value(ID.LanguageBCP47, text, value(ID.Language, text, "eng")),
      name: value(ID.Name, text, ""),
      flagDefault: value(ID.FlagDefault, uint, 1) === 1,
      codecPrivate: codecPrivate ? text(bytes, codecPrivate) : "",
      compression,
      encrypted,
      flagDefaultOffset: fields.has(ID.FlagDefault) && fields.get(ID.FlagDefault).size === 1
        ? fields.get(ID.FlagDefault).dataStart - entry.start
        : -1
    };
  }

  async function readInfo(blob) {
    const reader = createReader(blob);
    const ebml = await reader.header(0);
    if (!ebml || ebml.id !== ID.EBML) {
      return null;
    }
    const segment = await reader.header(ebml.end);
    if (!segment || segment.id !== ID.Segment) {
      return null;
    }
    const segmentEnd = Math.min(segment.end, blob.size);
    let timestampScale = 1000000;
    let tracksElement = null;
    let tracks = [];
    let firstCluster = -1;
    let position = segment.dataStart;
    // shortcut: Tracks placed after the first Cluster (via SeekHead) is not supported; mkvmerge and ffmpeg write it first.
    while (position < segmentEnd && position < segment.dataStart + HEADER_SCAN_BYTES) {
      const element = await reader.header(position);
      if (!element || element.end === Infinity) {
        break;
      }
      if (element.id === ID.Cluster) {
        firstCluster = element.start;
        break;
      }
      if (element.id === ID.Info || element.id === ID.Tracks) {
        const bytes = (await reader.view(element.start, element.end - element.start)).slice();
        const local = readHeader(bytes, 0);
        if (element.id === ID.Info) {
          const scale = child(bytes, local, ID.TimestampScale);
          if (scale) {
            timestampScale = uint(bytes, scale) || timestampScale;
          }
        } else {
          tracksElement = { start: element.start, end: element.end, bytes };
          tracks = children(bytes, local.dataStart, local.end)
            .filter(function (entry) { return entry.id === ID.TrackEntry; })
            .map(function (entry) {
              return {
                ...parseTrackEntry(bytes, entry),
                entryStart: entry.start,
                entryEnd: entry.end
              };
            });
        }
      }
      position = element.end;
    }
    if (!tracksElement) {
      return null;
    }
    return { timestampScale, tracks, tracksElement, firstCluster, segmentEnd };
  }

  function isTextSubtitle(track) {
    return track.type === "subtitle" && TEXT_CODECS.has(track.codecID) && !track.encrypted &&
      (!track.compression || track.compression.algorithm === 0 || track.compression.algorithm === 3);
  }

  function withAudioTrackFirst(blob, info, trackNumber) {
    const source = info.tracksElement.bytes;
    const audio = info.tracks.filter(function (track) { return track.type === "audio"; });
    const chosen = audio.find(function (track) { return track.number === trackNumber; });
    if (!chosen || audio.length < 2) {
      return blob;
    }
    const output = source.slice();
    audio.forEach(function (track) {
      if (track.flagDefaultOffset >= 0) {
        output[track.entryStart + track.flagDefaultOffset] = track === chosen ? 1 : 0;
      }
    });
    const first = audio[0];
    if (chosen !== first) {
      const swapped = output.slice(first.entryStart, chosen.entryEnd);
      const chosenEntry = swapped.slice(chosen.entryStart - first.entryStart);
      const between = swapped.slice(first.entryEnd - first.entryStart, chosen.entryStart - first.entryStart);
      const firstEntry = swapped.slice(0, first.entryEnd - first.entryStart);
      output.set(chosenEntry, first.entryStart);
      output.set(between, first.entryStart + chosenEntry.length);
      output.set(firstEntry, first.entryStart + chosenEntry.length + between.length);
    }
    return new Blob([
      blob.slice(0, info.tracksElement.start),
      output,
      blob.slice(info.tracksElement.end)
    ], { type: blob.type });
  }

  async function decompress(track, data) {
    if (!track.compression) {
      return data;
    }
    if (track.compression.algorithm === 3) {
      const joined = new Uint8Array(track.compression.settings.length + data.length);
      joined.set(track.compression.settings);
      joined.set(data, track.compression.settings.length);
      return joined;
    }
    const stream = new Blob([data]).stream().pipeThrough(new DecompressionStream("deflate"));
    return new Uint8Array(await new Response(stream).arrayBuffer());
  }

  function readBlockHeader(bytes) {
    const track = readVint(bytes, 0, false);
    if (!track || bytes.length < track.length + 3) {
      return null;
    }
    const relative = (bytes[track.length] << 24 >> 16) | bytes[track.length + 1];
    return {
      track: track.value,
      relative,
      laced: (bytes[track.length + 2] & 0x06) !== 0,
      dataOffset: track.length + 3
    };
  }

  async function scanSubtitleBlocks(blob, info, options) {
    const reader = createReader(blob);
    const wanted = new Map(info.tracks.filter(isTextSubtitle).map(function (track) {
      return [track.number, []];
    }));
    const signal = options && options.signal;
    const onProgress = options && options.onProgress;
    if (!wanted.size || info.firstCluster < 0) {
      return wanted;
    }

    async function block(element, clusterTime, durationElement) {
      const head = readBlockHeader(await reader.view(element.dataStart, 16));
      if (!head || head.laced || !wanted.has(head.track)) {
        return;
      }
      const bytes = (await reader.view(element.dataStart, element.end - element.dataStart)).slice(head.dataOffset);
      wanted.get(head.track).push({
        time: clusterTime + head.relative,
        duration: durationElement,
        bytes
      });
    }

    let position = info.firstCluster;
    let lastProgress = 0;
    while (position < info.segmentEnd) {
      if (signal && signal.aborted) {
        throw new DOMException("Aborted", "AbortError");
      }
      const element = await reader.header(position);
      if (!element) {
        break;
      }
      if (element.id !== ID.Cluster) {
        if (element.end === Infinity) {
          break;
        }
        position = element.end;
        continue;
      }
      const clusterEnd = Math.min(element.end, info.segmentEnd);
      let clusterTime = 0;
      let inner = element.dataStart;
      while (inner < clusterEnd) {
        const item = await reader.header(inner);
        if (!item || !CLUSTER_CHILDREN.has(item.id) || item.end === Infinity) {
          break;
        }
        if (item.id === ID.Timestamp) {
          clusterTime = uint(await reader.view(item.start, item.end - item.start), {
            dataStart: item.dataStart - item.start,
            end: item.end - item.start
          });
        } else if (item.id === ID.SimpleBlock) {
          await block(item, clusterTime, null);
        } else if (item.id === ID.BlockGroup) {
          let blockElement = null;
          let duration = null;
          let groupPosition = item.dataStart;
          while (groupPosition < item.end) {
            const part = await reader.header(groupPosition);
            if (!part || part.end > item.end) {
              break;
            }
            if (part.id === ID.Block) {
              blockElement = part;
              const head = readBlockHeader(await reader.view(part.dataStart, 16));
              if (!head || !wanted.has(head.track)) {
                break;
              }
            } else if (part.id === ID.BlockDuration) {
              duration = uint(await reader.view(part.start, part.end - part.start), {
                dataStart: part.dataStart - part.start,
                end: part.end - part.start
              });
            }
            groupPosition = part.end;
          }
          if (blockElement) {
            await block(blockElement, clusterTime, duration);
          }
        }
        inner = item.end;
      }
      position = inner;
      if (onProgress && position - lastProgress > 64 * 1024 * 1024) {
        lastProgress = position;
        onProgress(position / blob.size);
      }
    }
    return wanted;
  }

  function assTimestamp(milliseconds) {
    const centiseconds = Math.max(0, Math.round(milliseconds / 10));
    const hours = Math.floor(centiseconds / 360000);
    const minutes = Math.floor(centiseconds / 6000) % 60;
    const seconds = Math.floor(centiseconds / 100) % 60;
    const pad = function (value) { return String(value).padStart(2, "0"); };
    return hours + ":" + pad(minutes) + ":" + pad(seconds) + "." + pad(centiseconds % 100);
  }

  function srtTimestamp(milliseconds) {
    const value = Math.max(0, Math.round(milliseconds));
    const pad = function (number, width) { return String(number).padStart(width, "0"); };
    return pad(Math.floor(value / 3600000), 2) + ":" + pad(Math.floor(value / 60000) % 60, 2) + ":" +
      pad(Math.floor(value / 1000) % 60, 2) + "," + pad(value % 1000, 3);
  }

  function toSubtitleText(track, events) {
    const ordered = events.slice().sort(function (left, right) { return left.startMs - right.startMs; });
    if (track.codecID === "S_TEXT/ASS" || track.codecID === "S_TEXT/SSA") {
      const header = /\[Events\]/iu.test(track.codecPrivate)
        ? track.codecPrivate.trimEnd()
        : track.codecPrivate.trimEnd() + "\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text";
      const lines = ordered.map(function (event) {
        const fields = event.text.split(",");
        const layer = fields[1] || "0";
        return "Dialogue: " + layer + "," + assTimestamp(event.startMs) + "," + assTimestamp(event.endMs) + "," +
          fields.slice(2).join(",").replace(/\r?\n/gu, "\\N");
      });
      return header + "\n" + lines.join("\n") + "\n";
    }
    return ordered.map(function (event, index) {
      return (index + 1) + "\n" + srtTimestamp(event.startMs) + " --> " + srtTimestamp(event.endMs) + "\n" +
        event.text.replace(/\r/gu, "").replace(/\n{2,}/gu, "\n").trim();
    }).join("\n\n") + "\n";
  }

  async function extractSubtitles(blob, info, options) {
    const blocks = await scanSubtitleBlocks(blob, info, options);
    const scale = info.timestampScale / 1000000;
    const result = [];
    for (const track of info.tracks.filter(isTextSubtitle)) {
      const trackBlocks = blocks.get(track.number) || [];
      const events = [];
      for (let index = 0; index < trackBlocks.length; index += 1) {
        const item = trackBlocks[index];
        const startMs = item.time * scale;
        const next = trackBlocks[index + 1];
        // shortcut: blocks without BlockDuration end at the next line or after 5 s.
        const endMs = item.duration !== null
          ? startMs + item.duration * scale
          : Math.min(startMs + 5000, next ? next.time * scale : Infinity);
        const bytes = await decompress(track, item.bytes);
        const value = new TextDecoder().decode(bytes).trim();
        if (value && endMs > startMs) {
          events.push({ startMs, endMs, text: value });
        }
      }
      if (events.length) {
        result.push({ track, text: toSubtitleText(track, events) });
      }
    }
    return result;
  }

  return { readInfo, isTextSubtitle, withAudioTrackFirst, extractSubtitles };
});
