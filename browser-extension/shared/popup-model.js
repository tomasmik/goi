(function (root, factory) {
  const exports = factory();

  if (typeof module === "object" && module.exports) {
    module.exports = exports;
  }

  root.GoiExtension = root.GoiExtension || {};
  root.GoiExtension.popupModel = exports;
})(typeof globalThis === "undefined" ? self : globalThis, function () {
  "use strict";

  async function saveConnection(input, operations) {
    const baseUrl = operations.normalizeOrigin(input.baseUrl);
    const token = String(input.token || "").trim();
    if (!token) {
      throw new Error("Paste a token to save or replace the connection.");
    }

    const permission = {
      origins: [operations.permissionPattern(baseUrl)]
    };
    const alreadyGranted = await operations.containsPermission(permission);
    let newlyGranted = false;
    if (!alreadyGranted) {
      const granted = await operations.requestPermission(permission);
      if (!granted) {
        throw new Error("Goi address access was not granted.");
      }
      newlyGranted = true;
    }

    try {
      const verified = await operations.verifyConnection({ baseUrl, token });
      if (!verified || !verified.ok) {
        const detail = verified && typeof verified.error === "string"
          ? verified.error.trim()
          : "";
        throw new Error(detail || connectionTestStatus(verified).text);
      }
      const response = await operations.saveConnection({ baseUrl, token });
      if (!response || !response.ok) {
        const detail = response && typeof response.error === "string"
          ? response.error.trim()
          : "";
        throw new Error(detail || "Could not save the connection.");
      }
      return { baseUrl, response };
    } catch (error) {
      if (newlyGranted) {
        try {
          await operations.removePermission(permission);
        } catch (_) {
          // Report the save failure, not permission cleanup.
        }
      }
      throw error;
    }
  }

  async function callSafely(action) {
    try {
      const response = await action();
      if (!response || typeof response !== "object") {
        return { ok: false, unavailable: true };
      }
      return response;
    } catch (_) {
      return { ok: false, unavailable: true };
    }
  }

  async function updateSetting(key, previousValue, nextValue, save) {
    const response = await callSafely(function () {
      return save({ [key]: nextValue });
    });
    if (!response.ok) {
      return { ok: false, value: previousValue, response };
    }
    const value = response.settings && Object.prototype.hasOwnProperty.call(response.settings, key)
      ? response.settings[key]
      : nextValue;
    return { ok: true, value, response };
  }

  function connectionTestStatus(response) {
    const result = response && typeof response === "object" ? response : {};
    if (result.ok) {
      return { text: "Connected to Goi.", error: false };
    }

    if (result.errorCode === "not_connected") {
      return {
        text: "Goi is not connected. Save an address and token first.",
        error: true,
      };
    }
    if (result.errorCode === "unauthorized") {
      return {
        text: "Goi rejected the token. Copy the full token from Goi and try again.",
        error: true,
      };
    }
    if (result.errorCode === "insecure_transport") {
      return {
        text: "This server rejected plain HTTP. Use HTTPS or a private network.",
        error: true,
      };
    }
    if (result.unavailable || result.errorCode === "network") {
      return { text: "Goi could not be reached.", error: true };
    }
    if (result.errorCode === "server") {
      return { text: "Goi returned a server error.", error: true };
    }
    return {
      text: "The connection test failed.",
      error: true,
    };
  }

  return {
    callSafely,
    connectionTestStatus,
    saveConnection,
    updateSetting,
  };
});
