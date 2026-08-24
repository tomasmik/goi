(function (root, factory) {
  const api = factory();
  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.captureOutbox = api;
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
})(globalThis, function () {
  "use strict";

  const LIMIT = 100;
  const RETRY_BASE_MS = 30 * 1000;
  const RETRY_MAX_MS = 60 * 60 * 1000;
  const RETRY_BATCH_SIZE = 10;

  function errorStatus(error) {
    return error && typeof error.status === "number" ? error.status : 0;
  }

  function isRetryableError(error) {
    if (error && (error.code === "not_connected" || error.code === "insecure_transport")) {
      return false;
    }
    const status = errorStatus(error);
    return status === 0 || status === 408 || status === 425 || status === 429 || status >= 500;
  }

  function isAuthenticationError(error) {
    const status = errorStatus(error);
    return status === 401 || status === 403;
  }

  function isPermanentError(error) {
    const status = errorStatus(error);
    return status === 400 || status === 409 || status === 413 || status === 422;
  }

  function isConnectionWideRetry(error) {
    const status = errorStatus(error);
    return status === 0 || status === 408 || status === 425 || status === 429;
  }

  function retryDelay(attempts) {
    const exponent = Math.max(0, Math.min(Number(attempts) - 1, 10));
    return Math.min(RETRY_MAX_MS, RETRY_BASE_MS * (2 ** exponent));
  }

  function normalizeEntry(entry, now, captureModel) {
    if (!entry || typeof entry !== "object" || !entry.payload || typeof entry.payload !== "object") {
      return null;
    }
    const nonce = String(entry.payload.capture_nonce || "");
    let baseUrl = "";
    if (entry.baseUrl) {
      try {
        baseUrl = captureModel.normalizeOrigin(entry.baseUrl);
      } catch (_error) {
        return null;
      }
    }
    if (!nonce) {
      return null;
    }
    const payload = captureModel.buildCapturePayload(entry.payload, nonce);
    if (!payload.expression || !payload.context_text) {
      return null;
    }
    const createdAt = Number.isFinite(entry.createdAt) ? entry.createdAt : now;
    const attempts = Number.isSafeInteger(entry.attempts) && entry.attempts > 0
      ? entry.attempts
      : 1;
    const nextAttemptAt = Number.isFinite(entry.nextAttemptAt)
      ? entry.nextAttemptAt
      : createdAt + retryDelay(attempts);
    return { payload, baseUrl, createdAt, attempts, nextAttemptAt };
  }

  function normalizeEntries(values, now, captureModel) {
    const seenNonces = new Set();
    const entries = values.map(function (entry) {
      return normalizeEntry(entry, now, captureModel);
    }).filter(function (entry) {
      if (!entry || !entry.baseUrl || seenNonces.has(entry.payload.capture_nonce)) {
        return false;
      }
      seenNonces.add(entry.payload.capture_nonce);
      return true;
    });
    entries.sort(function (left, right) {
      return left.createdAt - right.createdAt;
    });
    return entries;
  }

  function status(entries) {
    return {
      pending: entries.length,
      oldestAt: entries.length ? entries[0].createdAt : undefined,
      destinations: Array.from(new Set(entries.map(function (entry) {
        return entry.baseUrl;
      }))).sort()
    };
  }

  return {
    LIMIT,
    RETRY_BATCH_SIZE,
    RETRY_MAX_MS,
    isAuthenticationError,
    isConnectionWideRetry,
    isPermanentError,
    isRetryableError,
    normalizeEntries,
    retryDelay,
    status
  };
});
