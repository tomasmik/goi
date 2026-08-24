(function (root, factory) {
  const api = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = api;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.captureDelivery = api;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  function create(options) {
    const chrome = options.chrome;
    const captureModel = options.captureModel;
    const captureOutbox = options.captureOutbox;
    const apiClient = options.apiClient;
    const fetch = options.fetch;
    const getConnection = options.getConnection;
    const protectStorage = options.protectStorage;
    const storageKey = options.storageKey;
    const alarmName = options.alarmName;
    let queue = Promise.resolve();
    let revision = 0;
    let retryController;

    function recoverQueue(operation) {
      return operation.catch(function () {
        return undefined;
      });
    }

    function serialize(operation) {
      const update = queue.then(operation);
      queue = recoverQueue(update);
      return update;
    }

    async function deliver(payload) {
      const connection = await getConnection();
      if (!connection) {
        const error = new Error("Goi is not connected");
        error.code = "not_connected";
        throw error;
      }

      try {
        const capture = await apiClient.create(fetch, connection).capture(payload);
        return { queued: false, capture, connection };
      } catch (error) {
        if (!captureOutbox.isRetryableError(error)) {
          throw error;
        }
        await enqueue(payload, 1, connection.baseUrl);
        return { queued: true, connection };
      }
    }

    async function runRetry(force) {
      try {
        await retry(force);
      } catch (_error) {
        await scheduleFallbackRetry();
      }
    }

    async function scheduleFallbackRetry() {
      try {
        await chrome.alarms.create(alarmName, {
          when: Date.now() + captureOutbox.RETRY_MAX_MS
        });
      } catch (_error) {
        return;
      }
    }

    function status() {
      return serialize(async function () {
        return captureOutbox.status(await read(Date.now()));
      });
    }

    function discard() {
      return serialize(async function () {
        await write([]);
        await chrome.alarms.clear(alarmName);
      });
    }

    function enqueue(payload, attempts, baseUrl) {
      return serialize(async function () {
        const now = Date.now();
        const nonce = String(payload.capture_nonce || "");
        const normalizedBaseUrl = captureModel.normalizeOrigin(baseUrl);
        const entries = await read(now);
        if (!nonce || entries.some(function (entry) {
          return entry.payload.capture_nonce === nonce;
        })) {
          await schedule(entries, now);
          return;
        }
        if (entries.length >= captureOutbox.LIMIT) {
          await schedule(entries, now);
          const error = new Error("The capture queue is full");
          error.code = "queue_full";
          throw error;
        }

        entries.push({
          payload,
          baseUrl: normalizedBaseUrl,
          createdAt: now,
          attempts,
          nextAttemptAt: now + captureOutbox.retryDelay(attempts)
        });
        entries.sort(function (left, right) {
          return left.createdAt - right.createdAt;
        });
        await write(entries);
        await schedule(entries, now);
      });
    }

    function retry(force) {
      return serialize(async function () {
        const now = Date.now();
        const entries = await read(now);
        if (entries.length === 0) {
          await write([]);
          await chrome.alarms.clear(alarmName);
          return;
        }

        let connection;
        try {
          connection = await getConnection();
        } catch (_error) {
          await deferAll(entries, now);
          return;
        }
        if (!connection) {
          await deferAll(entries, now);
          return;
        }

        const remaining = [];
        let attempted = 0;
        let connectionBlockedUntil = 0;
        const startedAtRevision = revision;
        const controller = new AbortController();
        retryController = controller;
        try {
          for (const entry of entries) {
            if (startedAtRevision !== revision || controller.signal.aborted) {
              remaining.push(entry);
              continue;
            }
            if (entry.baseUrl !== connection.baseUrl) {
              entry.nextAttemptAt = Math.max(
                entry.nextAttemptAt,
                now + captureOutbox.RETRY_MAX_MS
              );
              remaining.push(entry);
              continue;
            }
            const isDue = force || entry.nextAttemptAt <= now;
            if (!isDue || attempted >= captureOutbox.RETRY_BATCH_SIZE || connectionBlockedUntil) {
              if (connectionBlockedUntil && entry.nextAttemptAt < connectionBlockedUntil) {
                entry.nextAttemptAt = connectionBlockedUntil;
              }
              remaining.push(entry);
              continue;
            }

            attempted += 1;
            try {
              await apiClient.create(fetch, connection).capture(entry.payload, controller.signal);
            } catch (error) {
              if (startedAtRevision !== revision || controller.signal.aborted) {
                remaining.push(entry);
                continue;
              }
              if (captureOutbox.isAuthenticationError(error)) {
                connectionBlockedUntil = now + captureOutbox.RETRY_MAX_MS;
                entry.nextAttemptAt = connectionBlockedUntil;
                remaining.push(entry);
                continue;
              }
              if (captureOutbox.isPermanentError(error)) {
                continue;
              }
              entry.attempts += 1;
              entry.nextAttemptAt = now + captureOutbox.retryDelay(entry.attempts);
              if (captureOutbox.isConnectionWideRetry(error)) {
                connectionBlockedUntil = entry.nextAttemptAt;
              }
              remaining.push(entry);
            }
          }
        } finally {
          if (retryController === controller) {
            retryController = undefined;
          }
        }

        await write(remaining);
        await schedule(remaining, now);
      });
    }

    async function deferAll(entries, now) {
      await write(entries);
      await chrome.alarms.create(alarmName, {
        when: now + captureOutbox.RETRY_MAX_MS
      });
    }

    async function read(now) {
      const stored = await chrome.storage.local.get(storageKey);
      const current = Array.isArray(stored[storageKey]) ? stored[storageKey] : [];
      const entries = captureOutbox.normalizeEntries(current, now, captureModel);
      if (entries.length !== current.length) {
        await write(entries);
      }
      return entries;
    }

    async function write(entries) {
      await protectStorage();
      if (entries.length === 0) {
        await chrome.storage.local.remove(storageKey);
        return;
      }
      await chrome.storage.local.set({ [storageKey]: entries });
    }

    async function schedule(entries, now) {
      if (entries.length === 0) {
        await chrome.alarms.clear(alarmName);
        return;
      }
      const nextAttemptAt = entries.reduce(function (earliest, entry) {
        return Math.min(earliest, entry.nextAttemptAt);
      }, Number.POSITIVE_INFINITY);
      await chrome.alarms.create(alarmName, {
        when: Math.max(now + 1000, nextAttemptAt)
      });
    }

    function wake(baseUrl) {
      return serialize(async function () {
        const now = Date.now();
        const entries = await read(now);
        entries.forEach(function (entry) {
          if (entry.baseUrl === baseUrl) {
            entry.nextAttemptAt = now;
          }
        });
        await write(entries);
        await schedule(entries, now);
      });
    }

    function cancel() {
      revision += 1;
      if (retryController) {
        retryController.abort();
      }
    }

    function currentRevision() {
      return revision;
    }

    function isCurrentRevision(value) {
      return value === revision;
    }

    return {
      cancel,
      currentRevision,
      deliver,
      discard,
      enqueue,
      isCurrentRevision,
      retry,
      runRetry,
      scheduleFallbackRetry,
      status,
      wake
    };
  }

  return { create };
});
