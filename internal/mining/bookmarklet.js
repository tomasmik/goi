(function () {
  const origin = __GOI_ORIGIN__;
  const popup = window.open(
    origin + "/mining/capture",
    "goi-mining-capture",
    "popup,width=560,height=760"
  );
  if (!popup) {
    alert("Allow popups to use Mine to Goi.");
    return;
  }

  const byteLength = (value) => new TextEncoder().encode(value).length;
  const boundedURL = (value) => {
    try {
      const candidate = new URL(value);
      if (candidate.protocol !== "http:" && candidate.protocol !== "https:") {
        return "";
      }
      if (byteLength(candidate.href) <= 2048) {
        return candidate.href;
      }
      candidate.hash = "";
      if (byteLength(candidate.href) <= 2048) {
        return candidate.href;
      }
      candidate.search = "";
      if (byteLength(candidate.href) <= 2048) {
        return candidate.href;
      }
      return byteLength(candidate.origin) <= 2048 ? candidate.origin : "";
    } catch {
      return "";
    }
  };

  const sendCapture = (event) => {
    if (
      event.source !== popup ||
      event.origin !== origin ||
      !event.data ||
      event.data.type !== "goi:mining-ready" ||
      event.data.version !== 1
    ) {
      return;
    }

    const selection = window.getSelection();
    const selectedText = String(selection || "").trim();
    const ancestor = selection && selection.rangeCount
      ? selection.getRangeAt(0).commonAncestorContainer
      : null;
    const element = ancestor && (ancestor.nodeType === 1 ? ancestor : ancestor.parentElement);
    const caption = element && element.closest
      ? element.closest("[data-goi-caption-text],.caption-window,.ytp-caption-window-container")
      : null;
    const context = element && element.closest
      ? element.closest("p,li,blockquote,article,section,[role='article']")
      : null;
    const contextText = String(
      caption ? caption.textContent : context ? context.innerText : selectedText
    ).trim();
    const isYouTube = location.hostname === "www.youtube.com" && (
      location.pathname === "/watch" ||
      /^\/(?:embed|live|shorts)(?:\/|$)/.test(location.pathname)
    );
    const video = isYouTube
      ? document.querySelector(".html5-video-player video")
      : caption
        ? document.querySelector("video")
        : null;
    const hasVideoContext = Boolean(video) && (Boolean(caption) || isYouTube);

    popup.postMessage({
      type: "goi:mining-capture",
      version: 1,
      expression: Array.from(selectedText).slice(0, 256).join(""),
      context_text: Array.from(contextText).slice(0, 2000).join(""),
      source_title: Array.from(document.title).slice(0, 300).join(""),
      source_url: boundedURL(location.href),
      source_kind: hasVideoContext ? "video" : "web",
      source_position_seconds: hasVideoContext && Number.isFinite(video.currentTime)
        ? String(video.currentTime)
        : ""
    }, origin);
    window.removeEventListener("message", sendCapture);
  };

  window.addEventListener("message", sendCapture);
})();
