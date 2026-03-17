# Session-Centric State Management Redesign

## Overview

Replace rodney's `start`/`stop` browser lifecycle model with a session-centric model where sessions are the primary unit of state and continuity. Sessions encapsulate page targeting, browser lifecycle is implicit, and a global registry enables session lookup without requiring callers to know which data directory a session lives in.

## Motivation

- Agents using rodney in parallel need isolation without coordinating browser lifecycle.
- The `start`/`stop` model forces callers to manage browser state explicitly, which is unnecessary when sessions can handle it implicitly.
- Session IDs (introduced for `--session` page targeting) are already the natural handle for parallel use; making them the primary concept simplifies the mental model.

## Commands

### Removed

- `start` -- absorbed into `newsession`
- `stop` -- replaced by `endsession`
- `connect` -- removed (remote browser support intentionally dropped; can be re-added later if needed)
- `pages` -- replaced by `sessions`
- `page <index>` -- removed (no index-based switching)
- `newpage` -- replaced by `newsession`
- `closepage` -- replaced by `endsession`

No backward-compatibility shims. Old commands are deleted. Using a removed command name (e.g. `rodney start`) should produce a helpful error like `unknown command: start (did you mean newsession?)`.

### New Commands

#### `rodney newsession [url] [flags]`

Creates a new browser session. Launches Chrome if needed, or reuses the running browser for the resolved data directory.

**Flags (browser-level, only matter on first launch):**
- `--headless` -- launch Chrome in headless mode (default: visible)
- `--no-stealth` -- disable stealth mode (default: stealth enabled)
- `--profile <name|email>` -- Chrome profile
- `--insecure` / `-k` -- ignore SSL certificate errors

**Flags (per-session):**
- `--viewport WxH` -- set viewport dimensions for this session

**Flags (scope):**
- `--local` -- use `./.rodney/`
- `--global` -- use `~/.rodney/`
- `--home-dir <path|tmp>` -- use specified directory (or "tmp" to auto-create)

**Default scoping:** If `./.rodney/` directory exists in the current working directory, use it. Otherwise, create a temp dir (`/tmp/rodney-XXXXX`). This is a deliberate change from the previous default of falling back to `~/.rodney/` (global); the global scope is now only accessible via explicit `--global`. Note: if the user happens to be in their home directory where `~/.rodney/` exists, it will be used as the local dir, which is fine since it is the same path either way.

**Output:** Session ID only (6-char alphanumeric), printed to stdout. Nothing else.

**Lifecycle:**

1. Resolve data directory from scope flags/defaults.
2. Check for running browser (load state.json):
   - No state.json: launch Chrome with provided flags, write state.json.
   - state.json exists, PID alive: validate browser-level flag compatibility. Error on conflict. Proceed if compatible or no browser-level flags passed.
   - state.json exists, PID dead: clean up stale state.json, launch fresh.
3. Create page:
   - First session (sessions empty): claim the initial blank tab Chrome opened.
   - Subsequent sessions: create new window via `TargetCreateTarget{NewWindow: true}`.
   - Navigate to URL if provided. Apply stealth scripts if stealth mode.
4. Generate 6-char session ID. Verify uniqueness against global registry (regenerate on collision).
5. Persist (under data-dir lock): add to state.json's sessions. Then (under global registry lock) add to sessions.json.
6. Print session ID to stdout.

#### `rodney endsession [id]`

Closes a session (a single browser page/tab).

**Session ID resolution order for endsession** (differs from the general lookup order because endsession takes the ID as a natural positional argument):
1. Positional argument (`rodney endsession abc123`)
2. `--session <id>` flag
3. `RODNEY_SESSION` env var
4. Error if none provided

**Lifecycle:**

1. Resolve session: look up in global registry to get data dir. Load state.json (under data-dir lock), get TargetID.
2. Close the page via CDP `TargetCloseTarget`. If the target is already gone (user manually closed the tab), skip this step and proceed with cleanup.
3. Remove session from state.json's sessions. Save.
4. Remove session from global registry (under file lock).
5. If no sessions remain:
   - Check `browser.Pages()`. If empty or only `chrome://` internal pages, call `browser.Close()`.
   - Wait for ChromePID to exit (short timeout), then SIGTERM, then SIGKILL as last resort.
   - Kill auth proxy helper if ProxyPID > 0.
   - Remove state.json.
   - If data dir is a temp dir (`/tmp/rodney-*`), remove the entire directory.
6. No output on success. Error on failure.

#### `rodney sessions [--all]`

Lists sessions grouped by data directory.

**Scoping:**
- If `--session` or `RODNEY_SESSION` is set: show only sessions in that session's data dir.
- Otherwise: show all sessions across all data dirs.
- `--all`: show everything regardless of session context (only changes behavior when a session context is set; otherwise the default already shows all).

**Output format:**

```
.rodney/
  [abc123] Dashboard - https://app.example.com
  [def456] Login - https://app.example.com/login

/tmp/rodney-a8f2x/
  [ghi789] Search - https://google.com

~/.rodney/
  [jkl012] Blank
```

Group headers use relative paths for local (`.rodney/`) and global (`~/.rodney/`), full paths for temp/custom dirs.

**Staleness handling (lazy, during listing):**
- PID dead + state.json missing: prune from registry silently.
- PID dead + state.json exists: mark with `(stale)` indicator.
- PID alive: healthy.

### Unchanged Commands

All interaction commands (`open`, `click`, `text`, `screenshot`, `js`, `wait`, etc.) work as before. They require a session ID via `--session <id>` or `RODNEY_SESSION` env var.

`status` is removed; `sessions` (with potential `--verbose` flag) covers its purpose.

## Session Lookup Order

For all commands that need a target page:

1. `--session <id>` flag (explicit)
2. `RODNEY_SESSION` environment variable (undocumented; for human shell convenience)
3. Error: `session ID required; pass --session <id>`

The error message deliberately does not mention the env var, to prevent agents from trying to set it (which doesn't work across tool calls).

## State and Data Model

### Global Session Registry (`~/.rodney/sessions.json`)

Maps session ID to data directory path:

```json
{
  "abc123": "/home/user/project/.rodney",
  "def456": "/home/user/project/.rodney",
  "ghi789": "/tmp/rodney-a8f2x"
}
```

- Guarded by `~/.rodney/sessions.lock` (flock). Platform note: `syscall.Flock` works on Linux/macOS; Windows is not a target platform.
- Any mutation (add/remove entries) acquires the lock for read-modify-write.
- Writes must be atomic: write to a temp file, then rename over `sessions.json`. This makes read-only lookups (resolving a session ID) safe without acquiring the lock.

### Per-Data-Dir State (`<data-dir>/state.json`)

Mutations to state.json are guarded by `<data-dir>/state.lock` (flock), using atomic writes (temp file + rename). This prevents races when multiple `newsession` calls target the same data dir concurrently (e.g. two agents in the same project creating sessions simultaneously).

```json
{
  "debug_url": "ws://127.0.0.1:9222/devtools/browser/...",
  "chrome_pid": 48291,
  "data_dir": "/home/user/project/.rodney/chrome-data",
  "headless": false,
  "stealth": true,
  "insecure": false,
  "profile": "Default",
  "proxy_pid": 0,
  "proxy_port": 0,
  "proxy_server": "proxy.corp:3128",
  "proxy_config_hash": "sha256:a1b2c3...",
  "sessions": {
    "abc123": {
      "target_id": "TARGET_ID_1",
      "viewport_width": 1920,
      "viewport_height": 935
    },
    "def456": {
      "target_id": "TARGET_ID_2",
      "viewport_width": 1280,
      "viewport_height": 720
    }
  }
}
```

Changes from current state.json:
- `active_page` removed (no index-based tracking).
- `sessions` (flat map of ID to TargetID) replaced by `sessions` (map of ID to per-session struct with target_id and viewport dimensions).
- `viewport_width`/`viewport_height` moved from top-level to per-session, since each session can have its own viewport.
- `headless`, `insecure`, `profile` added (for compatibility checking).
- `proxy_server` (human-readable, no credentials; informational only, for diagnostic display in `sessions` output) and `proxy_config_hash` (SHA-256 of full proxy URL including credentials; used for compatibility checking) added.

### Browser-Level Compatibility Check

When `newsession` is called against a data dir with a running browser, these fields are checked for conflicts. A flag is only a conflict if explicitly passed and different from the stored value. Omitted flags are not conflicts.

- `headless`
- `stealth`
- `insecure`
- `profile`
- `proxy_config_hash` (computed from current `HTTPS_PROXY`/`HTTP_PROXY` env vars)

`viewport` is per-session (applied via CDP `Emulation.setDeviceMetricsOverride`), not a browser-level conflict.

### Auth Proxy

Authenticated HTTP proxies are handled transparently:

1. `newsession` detects `HTTPS_PROXY`/`HTTP_PROXY` env vars with embedded credentials.
2. Launches a local unauthenticated proxy relay (`rodney _proxy`) on a free port.
3. Points Chrome at the local relay.
4. Each browser launch discovers its own free port, so parallel browsers don't conflict.
5. Proxy config hash stored for compatibility checking; credentials never stored in plaintext.
6. Proxy process killed when browser shuts down (last session closed).

## Error Messages

| Situation | Message |
|---|---|
| No session ID provided | `session ID required; pass --session <id>` |
| Session ID not in registry | `session "abc123" not found` |
| Session's browser is dead | `browser for session "abc123" is no longer running` |
| Headless mismatch | `browser already running with --headless; pass --headless or omit the flag` |
| Stealth mismatch | `browser already running with --no-stealth; pass --no-stealth or omit the flag` |
| Profile mismatch | `browser already running with profile "Work"; pass --profile Work or omit the flag` |
| Insecure mismatch | `browser already running with --insecure; pass --insecure or omit the flag` |
| Proxy config mismatch | `browser already running with a different proxy configuration` |

## Skill Update

The browser-based-website-extraction skill should be simplified to only reference:

1. `rodney newsession [url]` -- start a session, capture the session ID
2. `rodney <command> --session <id>` -- interact with the browser
3. `rodney endsession <id>` -- clean up when done

No mention of browser lifecycle, env vars, scope flags, headless/stealth configuration, or any other machinery. The skill uses default arguments for everything.

## RODNEY_HOME Environment Variable

`RODNEY_HOME` is removed. It was previously the highest-priority override for the data directory. With the session-centric model, data directory resolution is either implicit (auto-detect local, or create temp) or explicit (`--local`, `--global`, `--home-dir`). Once a session exists, its data dir is tracked in the global registry, so `RODNEY_HOME` is no longer needed. Remove it from code, help text, and documentation.

## Terminology

A "session" in rodney is a single browser page/tab with a stable short ID. It is not an HTTP session, cookie session, or auth session. Each session maps to one Chrome target (tab/window) within a browser instance.

Session IDs are 6-character lowercase alphanumeric strings (charset: `a-z0-9`, 36^6 ~= 2.2 billion possible values).

## Temp Directory Naming

Temp directories use the pattern `/tmp/rodney-XXXXX` (not "session" in the name, to avoid confusion with the session concept).
