# Goi Capture

Goi Capture connects the Japanese you read and watch to Goi. Use it to check
what you know, save words with their original context, work with YouTube
captions, or study local videos with subtitle files.

## Install

Download the extension from **Settings → Browser extension** in Goi and unzip
it. Open `chrome://extensions`, enable **Developer mode**, choose **Load
unpacked**, and select the folder.

Create an extension token in Goi, then open the extension and enter your Goi
address and token under **Set up**. Public Goi servers require HTTPS; HTTP is
allowed on local and private networks.

For development, load this `browser-extension` directory directly. There is no
build step.

## Use it

- Select Japanese text and choose **Add to Goi**, or press **Alt+Shift+G**
  (`Option+Shift+G` on macOS), to save it with its sentence and source.
- Press **Alt+Shift+A** to mark unknown words on the current page. Automatic
  analysis can be enabled per site.
- On YouTube, press **Alt+Shift+Y** for selectable captions and **Alt+Shift+B**
  for the subtitle browser. You can seek, check coverage, and mine individual
  words or a small batch.
- Choose **Open local video** to study a video with SRT, WebVTT, ASS, or SSA
  subtitles. Playback support depends on the formats Chrome can play.

Chrome may block the extension on browser-owned pages, built-in PDF viewers,
and protected frames. YouTube features may occasionally need updating when
YouTube changes its player.

## Privacy

Your token stays in Chrome's extension storage and is only sent to your
configured Goi server. Local media stays in its player tab. Failed captures
wait in a local queue so they can be retried without silently losing them.

There is no analytics, remotely hosted code, or permanent access to every site.

## Development

See [TESTING.md](../TESTING.md) for tests and release checks.
