# Stealth Mode: Known Detection Gaps

Current stealth implementation uses a Chrome extension + `--disable-blink-features=AutomationControlled` flag. This passes most bot detection tests but has inherent limitations from using CDP (Chrome DevTools Protocol) via go-rod.

Tested against:
- **bot.incolumitas.com** — 33/36 pass
- **bot-detector.rebrowser.net** — 8/10 pass (in stealth mode)

## 1. Main World Execution (rebrowser: `mainWorldExecution`) — RESOLVED

Rod executes all JavaScript in the **main world** — the same execution context where the page's own scripts run. Chrome supports **isolated worlds** (separate JS contexts that share the DOM but get pristine, unpatched built-in APIs), but rod has no API for them.

**Status:** Resolved. In stealth mode, rodney now bypasses rod's JS-based APIs and uses:
- `DOM.querySelector` / `DOM.querySelectorAll` (pure CDP C++ calls) for element finding
- `Page.createIsolatedWorld` for JS that must run (pristine, unpatched built-ins)
- `Input.*` for mouse/keyboard interaction (identical to real user input)

Any page can monkey-patch DOM APIs (`document.querySelector`, `querySelectorAll`, `getElementById`, etc.) and detect when automation calls them. This affects most rodney commands that resolve CSS selectors:

| Detectable | Safe (CDP-only) |
|---|---|
| `click`, `text`, `input`, `wait` | `open`, `reload`, `back`, `forward` |
| `exists`, `count`, `visible` | `screenshot` (full page) |
| `hover`, `focus`, `select`, `submit` | `ax-tree`, `ax-find`, `ax-node` |
| `js <expr>`, `assert <expr>` | `html <selector>` (retrieval part) |
| `attr`, `download`, `screenshot-el` | |

**Possible fixes:**
- Rod adds isolated world support (upstream feature request)
- Use [rebrowser-patches](https://github.com/nicktate/puppeteer-extra-stealth-js) to patch Chromium binary
- Switch to a framework with isolated world support (Playwright)

## 2. Runtime.enable Leak (rebrowser: `runtimeEnableLeak`)

Rod must send CDP `Runtime.enable` to evaluate JavaScript. Pages can detect this by using `console.debug()` with a trapped error stack getter — when `Runtime.enable` is active, Chrome reads the stack to send `Runtime.consoleAPICalled` events, incrementing a counter the page monitors.

**Possible fixes:**
- Patch Chromium binary to not leak stack reads (rebrowser-patches)
- No JS-level fix exists; this is a protocol-level artifact

## 3. User Agent Version (rebrowser: `useragent`)

Rod's bundled Chromium (v128.0.6568.0, open-source snapshot) doesn't expose `navigator.userAgentData`, so version-checking tests can't determine our Chrome version. Additionally, the version is a dev snapshot that may not match any real Chrome stable release, which could be flagged by UA-based detection.

**Possible fixes:**
- Download a stable Chromium release instead of rod's dev snapshot
- Override `navigator.userAgentData` via the extension to report a plausible version

## 4. Service Worker Navigator Inconsistency (incolumitas: `inconsistentServiceWorkerNavigatorPropery`)

Service workers run in a completely separate context registered via a URL. Unlike web workers (which we patch by intercepting `URL.createObjectURL` and the `Worker` constructor), service workers cannot be intercepted from a content script or extension.

**Possible fixes:**
- Intercept `navigator.serviceWorker.register()` to rewrite the service worker script URL through a local proxy
- Not practically fixable without browser-level patches

## 5. fpscanner WEBDRIVER (incolumitas: `fpscanner/WEBDRIVER`)

This detection uses a custom/updated fpscanner that detects CDP control through a method beyond `navigator.webdriver` (which IS correctly set to `false`). Note: this test also fails in regular Chrome, so it may be a false positive in the detection suite itself.
