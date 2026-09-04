(function (root, factory) {
  const api = factory();
  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.apiClient = api;
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
})(globalThis, function () {
  "use strict";

  const CAPTURE_PATH = "/api/extension/v1/captures";
  const COVERAGE_PATH = "/api/extension/v1/coverage";
  const DICTIONARY_PATH = "/api/extension/v1/dictionary";
  const KNOWN_PATH = "/api/extension/v1/known";
  const STATUS_PATH = "/api/extension/v1/status";
  const TRANSLATE_PATH = "/api/extension/v1/translate";
  const DEFAULT_TIMEOUT_MS = 10000;
  const TRANSLATION_TIMEOUT_MS = 70000;

  async function request(fetchImpl, connection, path, options) {
    const controller = new AbortController();
    const externalSignal = options.signal;
    const abortRequest = function () {
      controller.abort();
    };
    if (externalSignal) {
      if (externalSignal.aborted) {
        controller.abort();
      } else {
        externalSignal.addEventListener("abort", abortRequest, { once: true });
      }
    }
    const timeout = setTimeout(function () {
      controller.abort();
    }, options.timeoutMs || DEFAULT_TIMEOUT_MS);

    try {
      const response = await fetchImpl(connection.baseUrl + path, {
        method: options.method,
        headers: {
          Accept: "application/json",
          Authorization: "Bearer " + connection.token,
          ...(options.body ? { "Content-Type": "application/json" } : {})
        },
        body: options.body ? JSON.stringify(options.body) : undefined,
        cache: "no-store",
        credentials: "omit",
        redirect: "error",
        signal: controller.signal
      });

      let data = null;
      try {
        data = await response.json();
      } catch (_error) {
        data = null;
      }

      if (!response.ok) {
        const error = new Error("Goi returned HTTP " + response.status);
        error.status = response.status;
        error.code = data && data.code;
        throw error;
      }
      return data || {};
    } finally {
      clearTimeout(timeout);
      if (externalSignal) {
        externalSignal.removeEventListener("abort", abortRequest);
      }
    }
  }

  function create(fetchImpl, connection) {
    return {
      async capture(payload, signal) {
        const response = await request(fetchImpl, connection, CAPTURE_PATH, {
          method: "POST",
          body: payload,
          signal
        });

        if (
          !Number.isSafeInteger(response.id) ||
          response.id < 1 ||
          !Number.isSafeInteger(response.revision) ||
          response.revision < 1 ||
          !["pending", "accepted", "discarded"].includes(response.status) ||
          typeof response.replayed !== "boolean" ||
          typeof response.review_url !== "string"
        ) {
          throw unexpectedResponse();
        }
        return response;
      },
      async coverage(blocks, signal) {
        const response = await request(fetchImpl, connection, COVERAGE_PATH, {
          method: "POST",
          body: { blocks },
          signal
        });
        if (!validCoverageResponse(response)) {
          throw unexpectedResponse();
        }
        return response;
      },
      async dictionary(expression, signal) {
        const query = String(expression || "").trim();
        const response = await request(
          fetchImpl,
          connection,
          DICTIONARY_PATH + "?expression=" + encodeURIComponent(query),
          { method: "GET", signal }
        );
        if (!validDictionaryResponse(response)) {
          throw unexpectedResponse();
        }
        return response;
      },
      async markKnown(expression, signal) {
        const response = await request(fetchImpl, connection, KNOWN_PATH, {
          method: "POST",
          body: { expression: String(expression || "").trim() },
          signal
        });
        if (!response || !["marked_known", "already_known", "in_lessons"].includes(response.state)) {
          throw unexpectedResponse();
        }
        return response;
      },
      async translate(text, signal) {
        const response = await request(fetchImpl, connection, TRANSLATE_PATH, {
          method: "POST",
          body: { text: String(text || "").trim() },
          signal,
          timeoutMs: TRANSLATION_TIMEOUT_MS
        });
        if (!response || typeof response.translation !== "string" || !response.translation.trim() ||
            typeof response.provider !== "string") {
          throw unexpectedResponse();
        }
        return response;
      },
      async status() {
        const response = await request(fetchImpl, connection, STATUS_PATH, { method: "GET" });
        if (response.ok !== true) {
          throw unexpectedResponse();
        }
        return response;
      }
    };
  }

  function nonNegativeInteger(value) {
    return Number.isSafeInteger(value) && value >= 0;
  }

  function validCoverageResponse(response) {
    const summary = response && response.summary;
    if (!summary || !Array.isArray(response.blocks)) {
      return false;
    }
    if (![
      summary.known_occurrences,
      summary.total_occurrences,
      summary.unknown_unique,
      summary.excluded_names
    ].every(nonNegativeInteger) || summary.known_occurrences > summary.total_occurrences) {
      return false;
    }
    return response.blocks.every(function (block) {
      return Number.isSafeInteger(block.id) && Array.isArray(block.tokens) && block.tokens.every(function (token) {
        return typeof token.surface === "string" &&
          typeof token.expression === "string" &&
          (token.reading === undefined || typeof token.reading === "string") &&
          nonNegativeInteger(token.start_utf16) &&
          nonNegativeInteger(token.end_utf16) &&
          token.end_utf16 >= token.start_utf16 &&
          ["known", "unknown", "leech", "suspended_leech"].includes(token.status);
      });
    });
  }

  function validDictionaryResponse(response) {
    return Boolean(response) &&
      typeof response.query === "string" &&
      ["ready", "ambiguous", "no_match"].includes(response.state) &&
      Array.isArray(response.candidates) &&
      response.candidates.every(function (candidate) {
        return candidate && typeof candidate.written === "string" &&
          typeof candidate.reading === "string" &&
          [candidate.global_rank, candidate.novel_rank].every(function (rank) {
            return rank === undefined || rank === null ||
              Number.isSafeInteger(rank) && rank >= 1 && rank <= 2147483647;
          }) &&
          Array.isArray(candidate.meanings) &&
          candidate.meanings.every(function (meaning) { return typeof meaning === "string"; }) &&
          (candidate.senses === undefined || Array.isArray(candidate.senses) &&
            candidate.senses.every(function (sense) {
              return sense && Array.isArray(sense.parts_of_speech) &&
                sense.parts_of_speech.every(function (part) { return typeof part === "string"; }) &&
                Array.isArray(sense.meanings) &&
                sense.meanings.every(function (meaning) { return typeof meaning === "string"; });
            }));
      });
  }

  function unexpectedResponse() {
    const error = new Error("Goi returned an unexpected API response");
    error.status = 502;
    error.code = "unexpected_response";
    return error;
  }

  return {
    CAPTURE_PATH,
    COVERAGE_PATH,
    DEFAULT_TIMEOUT_MS,
    DICTIONARY_PATH,
    KNOWN_PATH,
    STATUS_PATH,
    TRANSLATE_PATH,
    TRANSLATION_TIMEOUT_MS,
    create
  };
});
