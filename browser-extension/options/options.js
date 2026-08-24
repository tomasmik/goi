(function () {
  "use strict";

  const captureModel = globalThis.GoiExtension.captureModel;
  const popupModel = globalThis.GoiExtension.popupModel;
  const form = document.getElementById("connection-form");
  const baseURL = document.getElementById("base-url");
  const token = document.getElementById("token");
  const saveButton = document.getElementById("save-connection");
  const testButton = document.getElementById("test-connection");
  const disconnectButton = document.getElementById("disconnect");
  const connectionSummary = document.getElementById("connection-summary");
  const status = document.getElementById("status");

  function message(type, extra) {
    return chrome.runtime.sendMessage({ type, version: 1, ...(extra || {}) });
  }

  function showStatus(text, error) {
    status.textContent = text;
    status.classList.toggle("error", Boolean(error));
  }

  function showConnection(connection) {
    const connected = Boolean(connection && connection.connected);
    connectionSummary.textContent = connected
      ? "Connected to " + connection.baseUrl
      : "Not connected";
    connectionSummary.classList.toggle("error", !connected);
    testButton.disabled = !connected;
    disconnectButton.hidden = !connected;
    if (connection && connection.baseUrl) {
      baseURL.value = connection.baseUrl;
    }
    token.placeholder = connected
      ? "Paste a token to replace the connection"
      : "Token from Goi settings";
  }

  async function testConnection() {
    testButton.disabled = true;
    showStatus("Testing…", false);
    const response = await popupModel.callSafely(function () {
      return message("goi.connection.test");
    });
    const result = popupModel.connectionTestStatus(response);
    showStatus(result.text, result.error);
    testButton.disabled = false;
    return !result.error;
  }

  async function load() {
    const response = await popupModel.callSafely(function () {
      return message("goi.connection.get");
    });
    if (!response.ok || !response.connection) {
      showConnection(null);
      showStatus("Could not load the connection.", true);
      return;
    }
    showConnection(response.connection);
  }

  form.addEventListener("submit", async function (event) {
    event.preventDefault();
    saveButton.disabled = true;
    try {
      const result = await popupModel.saveConnection({
        baseUrl: baseURL.value,
        token: token.value
      }, {
        normalizeOrigin: captureModel.normalizeOrigin,
        permissionPattern: captureModel.permissionPattern,
        containsPermission: function (permission) {
          return chrome.permissions.contains(permission);
        },
        requestPermission: function (permission) {
          return chrome.permissions.request(permission);
        },
        removePermission: function (permission) {
          return chrome.permissions.remove(permission);
        },
        verifyConnection: function (connection) {
          return message("goi.connection.verify", connection);
        },
        saveConnection: function (connection) {
          return message("goi.connection.save", connection);
        }
      });
      token.value = "";
      showConnection({ baseUrl: result.baseUrl, connected: true });
      showStatus("Connected to Goi.", false);
    } catch (error) {
      showStatus(error && error.message ? error.message : "Could not save the connection.", true);
    } finally {
      saveButton.disabled = false;
    }
  });

  testButton.addEventListener("click", function () {
    testConnection();
  });

  disconnectButton.addEventListener("click", async function () {
    disconnectButton.disabled = true;
    const response = await popupModel.callSafely(function () {
      return message("goi.connection.disconnect");
    });
    disconnectButton.disabled = false;
    if (!response.ok) {
      showStatus("Could not disconnect Goi.", true);
      return;
    }
    token.value = "";
    showConnection(null);
    showStatus("Disconnected.", false);
  });

  load();
})();
