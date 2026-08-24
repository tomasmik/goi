# Testing Goi


This is a useful document if you want to test using an AI agent.

It does come in handy because when you're the only person developing something
running manual QA takes forever.

Automated tests should be deterministic. Use a real browser for the things a
test runner cannot faithfully reproduce: Chrome permissions, live YouTube,
fullscreen, audio, and local media.

## While you work

Run the smallest useful test first:

```sh
go test ./internal/reviews
go test ./internal/reviews -run TestName
node --test browser-extension/tests/overlay.test.js
node --test web/tests/study-session.test.js
```

Before finishing a change, run the relevant parts of the full suite:

```sh
go fmt ./...
go test ./...
go test -race ./...
go vet ./...
go build ./cmd/server ./cmd/backup ./cmd/devdata
node --test browser-extension/tests/*.test.js web/tests/*.test.js
node --test browser-extension/tests/chrome-install-smoke.js
git diff --check
```

The Chrome smoke test launches an isolated profile and installs the same files
Goi puts in its downloadable extension ZIP. Set `CHROME_BIN` when Chrome is in
an unusual location, or `GOI_CHROME_HEADED=1` to watch the test run. No npm
installation is needed.

If a required program is unavailable, record the skipped check. A skip is not a
pass.

## Safe test data

Never use personal study data for destructive testing. Build the QA fixture
from the repository root:

```sh
go run ./cmd/devdata qa
APP_DATA_DIR="$PWD/data/test" APP_AUTH_MODE=false go run ./cmd/server
```

For an extension release, create a fresh token, download a fresh ZIP from
**Settings → Browser extension**, and install it in a clean Chrome profile.
Loading `browser-extension` directly is quicker during development, but it does
not test the downloadable package.

Keep the browser console, extension service-worker console, and Goi logs open.
Unexpected errors, repeated requests, or background work that continues after
its page closes are failures even when the screen looks right.

## Manual pass

### Website

- Check login, navigation, light and dark themes, keyboard use, a narrow
  viewport, and 200% zoom.
- Add and edit vocabulary, process a mining capture, complete a lesson, and
  complete a review. Confirm that context and media stay with the right item.
- Create a backup and restore it into a fresh data directory. Compare study
  state, captures, context, and media.

### Browser extension

- Connect the downloaded extension, restart Chrome, and make sure it recovers.
  Revoked credentials and an unreachable server should produce different,
  useful errors.
- Analyze a normal Japanese page, navigate without a full reload, and disable
  site access again. Highlights, listeners, and requests should follow the
  current page without stale updates.
- Capture text from an ordinary page and from YouTube. Check the saved word,
  sentence, page title, URL, and timestamp.
- Try human and automatic YouTube captions plus a Short. Exercise seeking,
  display modes, fullscreen, translation, subtitle browsing, instant mining,
  and a small batch.
- Take Goi offline, queue captures, restart Chrome, reconnect, and confirm each
  capture reaches the original Goi server exactly once.
- Confirm the bearer token stays in extension storage and requests only use the
  fixed API paths at the configured Goi origin.

### Local player

- Open MP4 and WebM videos with SRT and WebVTT subtitles. Include malformed,
  overlapping, UTF-16, and out-of-range subtitle fixtures.
- Check playback, seeking, subtitle offset, coverage, transcript selection,
  translation, one capture, and one small batch.
- Reload or close the tab while work is running. Video bytes, subtitle files,
  local paths, and Blob URLs must stay inside the player and pending work must
  stop.
- Complete the core flow with only the keyboard at 200% zoom.

## Before a release

Do not release with a known privacy or security leak, lost or duplicate
capture, wrong source attribution, partial coverage presented as complete,
broken extension package, or background work that outlives its page.

Set the release version in `browser-extension/manifest.json`, merge a green
`main`, then push a matching tag such as `v0.2.0` or `v0.2.0-rc.1`. The release
workflow runs CI again, publishes `ghcr.io/tomasmik/goi`, and creates the GitHub
Release. Prereleases do not replace the `latest` image.

Keep a short record of the final pass:

```text
Commit:
Extension artifact SHA-256:
Chrome and OS:
Automated checks:
Manual checks:
Failures or skipped environments:
Release decision:
```
