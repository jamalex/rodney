# Headless Cloudflare Detection Research & Fix Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Identify and fix the specific signals that let Cloudflare distinguish `--headless=new` from visible Chrome, so that headless stealth mode passes Cloudflare challenges (e.g. medium.com).

**Architecture:** Systematic A/B comparison of headless vs visible Chrome properties, followed by targeted fixes for each detected difference. Research-first — each task narrows the detection vector before writing code.

**Tech Stack:** rodney CLI, Chrome DevTools Protocol, JavaScript fingerprinting probes

---

## Context

When loading medium.com:
- `rodney start --stealth --show` → page loads fine (Cloudflare passes)
- `rodney start --stealth` (headless) → hits Cloudflare challenge page

Both modes use mainline Chrome with the same stealth patches (stealth.JS, workerFixJS, userAgentData override). The difference must come from signals that `--headless=new` leaks despite sharing the same rendering engine as visible Chrome.

### Known headless-only settings in rodney

From `cmdStart` in `main.go`:

| Setting | Headless | Visible+Stealth | Risk |
|---------|----------|-----------------|------|
| `--headless=new` | Yes | No | Chrome exposes this in subtle ways |
| `--disable-gpu` | Yes | No | Changes WebGL renderer string |
| `--single-process` | Yes | No | Detectable via `performance.memory`, process behavior |
| Viewport via `EmulationSetDeviceMetricsOverride` | 1920x935 | Window-controlled | Screen/viewport mismatches |
| `--no-startup-window` | Active | Deleted | May affect window APIs |

### Prior art

`docs/plans/2026-02-24-remaining-detection-fixes.md` addressed rebrowser-bot-detector signals (runtimeEnableLeak, useragent). Those are fixed. This plan addresses a different layer: Cloudflare's proprietary detection that goes beyond the rebrowser checks.

---

### Task 1: Capture fingerprint diff between headless and visible

**Goal:** Run a comprehensive fingerprint probe in both modes and diff the results. This identifies exactly which properties Cloudflare could be keying on.

**Step 1: Create a fingerprint extraction script**

Create a temporary file `/tmp/fingerprint-probe.js` with probes for all known headless detection vectors:

```javascript
JSON.stringify({
  // GPU / WebGL
  webglVendor: (() => { try { var c = document.createElement('canvas'); var gl = c.getContext('webgl'); return gl.getParameter(gl.getExtension('WEBGL_debug_renderer_info').UNMASKED_VENDOR_WEBGL); } catch(e) { return 'ERROR: ' + e.message; } })(),
  webglRenderer: (() => { try { var c = document.createElement('canvas'); var gl = c.getContext('webgl'); return gl.getParameter(gl.getExtension('WEBGL_debug_renderer_info').UNMASKED_RENDERER_WEBGL); } catch(e) { return 'ERROR: ' + e.message; } })(),

  // Navigator
  userAgent: navigator.userAgent,
  webdriver: navigator.webdriver,
  platform: navigator.platform,
  hardwareConcurrency: navigator.hardwareConcurrency,
  deviceMemory: navigator.deviceMemory,
  maxTouchPoints: navigator.maxTouchPoints,
  languages: navigator.languages,
  plugins: Array.from(navigator.plugins).map(p => p.name),
  pdfViewerEnabled: navigator.pdfViewerEnabled,

  // UserAgentData
  uaBrands: navigator.userAgentData ? navigator.userAgentData.brands.map(b => b.brand + '/' + b.version) : null,
  uaPlatform: navigator.userAgentData ? navigator.userAgentData.platform : null,
  uaMobile: navigator.userAgentData ? navigator.userAgentData.mobile : null,

  // Screen / Viewport
  screenWidth: screen.width,
  screenHeight: screen.height,
  screenAvailWidth: screen.availWidth,
  screenAvailHeight: screen.availHeight,
  screenColorDepth: screen.colorDepth,
  screenPixelDepth: screen.pixelDepth,
  innerWidth: window.innerWidth,
  innerHeight: window.innerHeight,
  outerWidth: window.outerWidth,
  outerHeight: window.outerHeight,
  devicePixelRatio: window.devicePixelRatio,

  // Window properties
  windowChrome: typeof window.chrome,
  windowChromeRuntime: typeof (window.chrome && window.chrome.runtime),
  windowSpeechSynthesis: typeof window.speechSynthesis,

  // Permissions API
  permissionsNotification: await navigator.permissions.query({name: 'notifications'}).then(r => r.state).catch(e => 'ERROR: ' + e.message),

  // Automation signals
  automationControlled: document.documentElement.getAttribute('webdriver'),
  cdcProps: Object.getOwnPropertyNames(document).filter(p => p.match(/^[$_]cdc/)),

  // Performance
  performanceMemory: typeof performance.memory !== 'undefined' ? { jsHeapSizeLimit: performance.memory.jsHeapSizeLimit } : null,

  // Connection
  connectionType: navigator.connection ? navigator.connection.type : null,
  connectionEffectiveType: navigator.connection ? navigator.connection.effectiveType : null,

  // Headless-specific
  chromeApp: typeof window.chrome !== 'undefined' && typeof window.chrome.app !== 'undefined',
  notificationPermission: typeof Notification !== 'undefined' ? Notification.permission : null,
})
```

**Step 2: Run in visible stealth mode**

```bash
rodney start --stealth --show --home-dir tmp
# note the session path
rodney open 'about:blank' --home-dir <session>
rodney js '(async () => { ... paste fingerprint script ... })()' --home-dir <session>
# Save output to /tmp/fingerprint-visible.json
rodney stop --home-dir <session>
```

**Step 3: Run in headless stealth mode**

```bash
rodney start --stealth --home-dir tmp
rodney open 'about:blank' --home-dir <session>
rodney js '(async () => { ... paste fingerprint script ... })()' --home-dir <session>
# Save output to /tmp/fingerprint-headless.json
rodney stop --home-dir <session>
```

**Step 4: Diff the results**

Compare the two JSON outputs. Document every difference. These are the candidate detection vectors.

**Step 5: Commit findings**

Add the diff results to `docs/stealth-issues.md` or a new `docs/headless-vs-visible-fingerprint.md`.

---

### Task 2: Test each candidate signal in isolation

**Goal:** For each difference found in Task 1, determine whether fixing it alone is sufficient to pass Cloudflare on medium.com.

**Step 1: Prioritize candidates**

From the Task 1 diff, rank by likelihood of Cloudflare use:
1. WebGL renderer (changes with `--disable-gpu`) — HIGH
2. `screen.width`/`screen.height` vs `window.outerWidth`/`outerHeight` mismatch — HIGH
3. `window.outerWidth === 0` / `window.outerHeight === 0` (headless has no real window) — HIGH
4. `performance.memory` differences (from `--single-process`) — MEDIUM
5. `navigator.plugins` differences — MEDIUM
6. `Notification.permission` — LOW

**Step 2: Test removing `--disable-gpu`**

Modify `cmdStart` temporarily (or use a local build) to NOT set `--disable-gpu` in headless stealth mode. Test medium.com.

```bash
# In main.go, comment out the disable-gpu block for stealth+headless
# Rebuild, test:
rodney start --stealth --home-dir tmp
rodney open 'https://medium.com' --home-dir <session>
rodney title --home-dir <session>
# Does it say "Medium" or show Cloudflare challenge?
rodney stop --home-dir <session>
```

**Step 3: Test removing `--single-process`**

Same approach: remove `--single-process` for stealth+headless, rebuild, test medium.com.

**Step 4: Test screen/viewport property patching**

If outerWidth/outerHeight are 0 in headless, inject a fix via `addScriptToEvaluateOnNewDocument`:

```javascript
Object.defineProperty(window, 'outerWidth', { get: () => window.innerWidth });
Object.defineProperty(window, 'outerHeight', { get: () => window.innerHeight + 85 });
```

Test medium.com with this patch.

**Step 5: Document which fix(es) are necessary**

Record which individual changes (or combination) allow headless to pass Cloudflare.

---

### Task 3: Implement the minimal fix

**Goal:** Apply the smallest change set that fixes Cloudflare detection in headless stealth mode.

**Files:**
- Modify: `main.go` (cmdStart launcher flags, possibly applyStealthToPage)
- Modify: `stealth_exec.go` (if JS injection needed)
- Test: `main_test.go`

The specific implementation depends on Task 2 findings. Likely candidates:

**If `--disable-gpu` is the issue:**

```go
// Remove the headless-only disable-gpu. If GPU causes crashes in
// specific environments, users can set ROD_CHROME_FLAGS or similar.
// Line ~682-684: delete the if-block entirely, or gate it on !stealth
if headless && !flags.stealth {
    l = l.Set("disable-gpu")
}
```

**If `--single-process` is the issue:**

```go
// Line ~690-692: already skipped for visible+stealth; also skip for headless+stealth
if !flags.stealth {
    l = l.Set("single-process")
}
```

Note: removing `--single-process` in headless may break screenshots in container environments. Need to test.

**If screen/viewport mismatch is the issue:**

Add a screen-property override script to `applyStealthToPage`:

```go
screenFixJS := fmt.Sprintf(`
Object.defineProperty(screen, 'width', { get: () => %d });
Object.defineProperty(screen, 'height', { get: () => %d });
Object.defineProperty(screen, 'availWidth', { get: () => %d });
Object.defineProperty(screen, 'availHeight', { get: () => %d });
Object.defineProperty(window, 'outerWidth', { get: () => %d });
Object.defineProperty(window, 'outerHeight', { get: () => %d + 85 });
`, vpWidth, vpHeight, vpWidth, vpHeight, vpWidth, vpHeight)
proto.PageAddScriptToEvaluateOnNewDocument{Source: screenFixJS}.Call(page)
```

**Step 1: Write failing test**

Add a test that launches headless stealth Chrome and verifies the fixed property matches visible-mode behavior (e.g., WebGL renderer not "Google SwiftShader", or outerWidth > 0).

**Step 2: Implement the fix**

Apply the minimal change from Task 2 findings.

**Step 3: Run tests**

```bash
go test -count=1 ./...
```

**Step 4: Verify against medium.com**

```bash
rodney start --stealth --home-dir tmp
rodney open 'https://medium.com' --home-dir <session>
rodney waitstable --home-dir <session>
rodney title --home-dir <session>
# Should show actual Medium page title, not Cloudflare
rodney stop --home-dir <session>
```

**Step 5: Commit**

```bash
git add main.go stealth_exec.go main_test.go
git commit -m "Fix headless Cloudflare detection: <specific fix description>"
```

---

### Task 4: Regression test

**Goal:** Add an automated test that catches headless-vs-visible fingerprint divergence.

**Files:**
- Modify: `main_test.go`

**Step 1: Write a test that compares key fingerprint properties**

The test should launch headless stealth Chrome, evaluate the fingerprint probes from Task 1, and assert that high-risk properties match expected visible-mode values:

```go
func TestStealth_HeadlessFingerprint(t *testing.T) {
    // Launch headless stealth browser
    // Navigate to about:blank
    // Evaluate fingerprint probes
    // Assert:
    //   - webglRenderer does NOT contain "SwiftShader" (if --disable-gpu was the fix)
    //   - outerWidth > 0 and outerHeight > 0
    //   - screen.width matches viewport width
    //   - navigator.webdriver === false
    //   - no $cdc properties on document
}
```

**Step 2: Run test**

```bash
go test -run TestStealth_HeadlessFingerprint -count=1 -v ./...
```

**Step 3: Commit**

```bash
git add main_test.go
git commit -m "Add headless fingerprint regression test"
```

---

## Approach summary

```
Task 1: Measure (fingerprint diff)
    ↓
Task 2: Isolate (test each signal)
    ↓
Task 3: Fix (minimal change)
    ↓
Task 4: Prevent regression (automated test)
```

## Non-goals

- Fixing ALL headless detection signals (only fix what Cloudflare actually checks)
- Replacing `--headless=new` with a different headless mechanism (Chrome doesn't offer alternatives)
- Solving CAPTCHAs (if Cloudflare serves a CAPTCHA, that's a different problem from the JS challenge)
