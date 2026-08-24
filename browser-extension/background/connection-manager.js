(function (root, factory) {
  const value = factory();
  if (typeof module === "object" && module.exports) {
    module.exports = value;
  }
  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.connectionManager = value;
})(typeof globalThis !== "undefined" ? globalThis : this, function () {
  "use strict";

  function create(options) {
    const chrome = options.chrome;
    const captureModel = options.captureModel;
    const apiClient = options.apiClient;
    const fetchImpl = options.fetch;
    const storageKey = options.storageKey;

    async function protectStorage() {
      if (chrome.storage.local.setAccessLevel) {
        await chrome.storage.local.setAccessLevel({ accessLevel: "TRUSTED_CONTEXTS" });
      }
    }

    async function getStored() {
      const stored = await chrome.storage.local.get(storageKey);
      const connection = stored[storageKey];
      if (!connection || !connection.baseUrl || !connection.token) {
        return null;
      }
      return connection;
    }

    async function get() {
      const connection = await getStored();
      if (!connection) {
        return null;
      }
      return {
        baseUrl: captureModel.normalizeOrigin(connection.baseUrl),
        token: connection.token
      };
    }

    function candidate(baseUrl, token) {
      const connection = {
        baseUrl: captureModel.normalizeOrigin(baseUrl),
        token: String(token || "").trim()
      };
      if (!connection.token) {
        throw new Error("Paste a Goi extension token.");
      }
      if (connection.token.length > 4096) {
        throw new Error("The Goi extension token is too long.");
      }
      return connection;
    }

    async function verify(baseUrl, token) {
      const connection = candidate(baseUrl, token);
      await apiClient.create(fetchImpl, connection).status();
      return connection;
    }

    function permissionPattern(baseUrl) {
      try {
        return captureModel.permissionPattern(baseUrl);
      } catch (_error) {
        return null;
      }
    }

    async function removePermission(pattern) {
      try {
        await chrome.permissions.remove({ origins: [pattern] });
      } catch (_error) {
        return;
      }
    }

    async function removeConnectionPermission(pattern) {
      try {
        const origins = await options.getSiteOrigins();
        for (const origin of origins) {
          if (captureModel.permissionPattern(origin) === pattern) {
            return;
          }
        }
      } catch (_error) {
        return;
      }
      await removePermission(pattern);
    }

    async function save(baseUrl, token) {
      const previous = await getStored();
      const connection = candidate(baseUrl, token);
      options.cancelRetry();
      await protectStorage();
      await chrome.storage.local.set({ [storageKey]: connection });
      if (previous) {
        const nextPattern = captureModel.permissionPattern(connection.baseUrl);
        const previousPattern = permissionPattern(previous.baseUrl);
        if (previousPattern && previousPattern !== nextPattern) {
          await removeConnectionPermission(previousPattern);
        }
      }
      try {
        await options.wakeOutbox(connection.baseUrl);
      } catch (_error) {
        await options.scheduleFallbackRetry();
      }
      return connection;
    }

    async function disconnect() {
      options.cancelRetry();
      const connection = await getStored();
      await chrome.storage.local.remove(storageKey);
      if (!connection) {
        return;
      }
      const pattern = permissionPattern(connection.baseUrl);
      if (pattern) {
        await removeConnectionPermission(pattern);
      }
    }

    async function test() {
      const connection = await get();
      if (!connection) {
        const error = new Error("Goi is not connected");
        error.code = "not_connected";
        throw error;
      }
      return apiClient.create(fetchImpl, connection).status();
    }

    return {
      disconnect,
      get,
      getStored,
      protectStorage,
      removePermission,
      save,
      test,
      verify
    };
  }

  return { create };
});
