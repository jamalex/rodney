# Network Monitor Design

## Overview

Add a background network monitoring process to rodney that captures all HTTP/WebSocket traffic, page lifecycle events, and user interactions via CDP. Data is written to disk per-session as JSONL with body files, giving agents direct file access to network data without DOM traversal or additional rodney commands.

## Motivation

- Agents using rodney for web data extraction currently rely on DOM traversal and element interaction to find data. Many pages load data via XHR/fetch API calls whose raw JSON responses are more useful than the rendered DOM.
- A persistent network log lets agents grep for API endpoints, read response bodies directly, and understand the request/response flow without needing to interact with page elements.
- Recording user interactions enables workflows where an agent opens a browser, a human interacts with it, and the agent reviews what happened (requests made, pages visited, data loaded).

## Architecture

### Background Process Model

A `_netmonitor` internal command runs as a long-lived background process, following the existing `_proxy` pattern:

- **One process per data directory** (not per session). Monitors all sessions in that browser.
- **Launched by `newsession`** when the first capture-enabled session is created for that data dir.
- **PID tracked in `state.json`** as `monitor_pid`.
- **Killed on last `endsession`** via SIGTERM, alongside the proxy process.
- **Crash recovery**: Any command that resolves a session via `withPage` checks whether `monitor_pid` is alive. If dead and capture is expected, the command re-launches the monitor before proceeding.

### CDP Connection

The monitor reads `state.json` from its data directory to get the Chrome debug URL. It connects via CDP and uses `Target.setAutoAttach` to automatically attach to all current and future page targets. For each attached target with capture enabled, it:

1. Enables `Network` domain events (request/response lifecycle, WebSocket frames).
2. Enables `Page` domain events (navigation, load lifecycle).
3. Creates an isolated world via `Page.addScriptToEvaluateOnNewDocument` with `worldName: "__rodney_monitor"` for user interaction capture. This is separate from the `"rodney"` isolated world used by stealth command execution in `stealth_exec.go`; both are invisible to the main world and do not interfere with each other.
4. Registers `Runtime.addBinding` with `name: "__rodneyEvent"` and `executionContextName: "__rodney_monitor"` for receiving user interaction events from the injected script.

### Per-Session Opt-Out

`SessionInfo` gains a `NoCapture bool` field (default false, meaning capture is enabled). Set to true via `--no-capture` on `newsession`. The monitor attaches to all targets but only enables Network/Page events and injects user interaction listeners on sessions where `!NoCapture`. Sessions with `--no-capture` get no `net/<session-id>/` directory.

If all sessions in a data dir have `NoCapture: true`, the monitor is not launched.

## Data Capture

### Network Events

Captured via CDP `Network` domain:

| CDP Event | JSONL `type` | Key Fields |
|-----------|-------------|------------|
| `Network.requestWillBeSent` | `request` | `id`, `method`, `url`, `headers`, `initiator` |
| `Network.responseReceived` + `Network.loadingFinished` | `response` | `id`, `status`, `mime`, `headers`, `size`, `bodyFile`, `truncated`, `originalSize` |
| `Network.webSocketCreated` | `ws-open` | `id`, `url`, `headers` |
| `Network.webSocketFrameSent` | `ws-frame` | `id`, `dir:"send"`, `opcode`, `size`, `bodyFile` |
| `Network.webSocketFrameReceived` | `ws-frame` | `id`, `dir:"recv"`, `opcode`, `size`, `bodyFile` |
| `Network.webSocketClosed` | `ws-close` | `id` |

Response bodies are captured eagerly on `Network.loadingFinished` via `Network.getResponseBody`, to avoid Chrome evicting them from its internal cache. If `Network.getResponseBody` fails (e.g., redirect, service worker response without cache, eviction), the `response` entry is still written but without a `bodyFile` field. A `"bodyError":"<reason>"` field is included for diagnostics.

### Page Lifecycle Events

Captured via CDP `Page` domain:

| CDP Event | JSONL `type` | Key Fields |
|-----------|-------------|------------|
| `Page.frameRequestedNavigation` | `page-navigation-requested` | `url`, `reason` (formSubmissionGet, formSubmissionPost, anchorClick, scriptInitiated, reload, etc.) |
| `Page.frameNavigated` | `page-navigated` | `url` |
| `Page.navigatedWithinDocument` | `page-spa-navigated` | `url` |
| `Page.domContentEventFired` | `page-dom-ready` | |
| `Page.loadEventFired` | `page-loaded` | |
| `Page.frameStartedLoading` | `page-loading` | |

Navigation cause is captured via `Page.frameRequestedNavigation`, which fires before the navigation and carries a `reason` field distinguishing user-typed URLs, link clicks, form submissions, script-initiated navigations, reloads, etc. `Page.frameNavigated` fires after the navigation completes and provides the final URL. Both are recorded as separate events, giving the agent both the intent and the result.

### User Interaction Events

Captured via isolated world event listeners. The monitor injects a script into the `__rodney_monitor` isolated world that listens for DOM events and sends them back via `Runtime.addBinding`. The main world cannot detect these listeners or the binding.

| DOM Event | JSONL `type` | Key Fields |
|-----------|-------------|------------|
| `click` | `user-click` | `selector`, `x`, `y`, `textContent` (truncated to 50 chars) |
| `input` (debounced 100ms) | `user-input` | `selector`, `value` (truncated to 200 chars) |
| `change` | `user-change` | `selector`, `value` |
| `submit` | `user-submit` | `selector` (form), `action` |
| `keydown` (Enter, Tab, Escape only) | `user-keydown` | `selector`, `key` |
| `paste` | `user-paste` | `selector`, `value` (truncated to 200 chars) |

**Selector generation**: The injected script builds a CSS selector from the event target: prefers `#id`, then `[data-testid]`/`[aria-label]`, then `tag.class`, falling back to `nth-child` chains. Kept short and human-readable.

**Detectability**: All listeners run in the isolated world. The main world cannot enumerate listeners from other execution contexts or access the `__rodneyEvent` binding. No DOM modifications are made.

### Rodney Command Interaction Markers

When a rodney CLI command performs a browser action, it sends a pre-action marker to the monitor via IPC. The monitor writes this to the JSONL and uses it for attribution: subsequent DOM interaction events on the same session and same target element (matched by selector) within a short window (~2 seconds) are tagged with `"source":"rodney"` to distinguish them from truly user-initiated actions.

Commands that send markers: `open`, `click`, `input`, `clear`, `select`, `submit`, `hover`, `focus`, `js`, `back`, `forward`, `reload`, `file`.

**Attribution limitations**: This is a best-effort heuristic. Edge cases where attribution may be inaccurate:
- A rodney action that triggers cascading events on different elements (e.g., click causes focus change on another element) -- only events matching the original target selector are attributed.
- A human interacting with the same element within the attribution window immediately after a rodney command -- the human action may be incorrectly attributed to rodney.
- A rodney `js` command that triggers DOM events indirectly -- the monitor records the `js` interaction marker but cannot attribute subsequent DOM events since there is no target selector to match.

Example JSONL showing attribution:

```jsonl
{"seq":5,"type":"interaction","cmd":"click","args":["#load-more"],"source":"rodney"}
{"seq":6,"type":"user-click","selector":"button.load-more","x":450,"y":320,"source":"rodney"}
{"seq":7,"type":"request","id":"R3","method":"GET","url":"https://example.com/api/feed?page=2"}
```

## IPC: CLI to Monitor

The monitor listens on a unix domain socket at `<data-dir>/net/monitor.sock`. CLI commands connect, write a single newline-terminated JSON message, and disconnect (fire-and-forget).

**Message types:**

Interaction marker (sent by CLI commands before performing browser actions):
```json
{"session":"abc123","type":"interaction","cmd":"click","args":["#load-more"]}
```

Clear request (sent by `net-clear`):
```json
{"session":"abc123","type":"clear"}
```
The monitor handles `clear` by closing the session's JSONL file handle, truncating it, deleting the `bodies/` directory, resetting the sequence counter, and reopening the file.

The monitor adds `seq` and `ts` when writing to the JSONL, ensuring consistent ordering with network and user events.

If the socket is absent or the write fails, the CLI command proceeds normally. A warning is printed to stderr (`warning: network monitor not running, interaction not recorded`) and the command attempts to restart the monitor. The interaction marker for the triggering command is lost, but subsequent commands are captured.

**`net-log --follow`** works by tailing the JSONL file directly (polling for new lines), not via the IPC socket. This keeps the IPC protocol simple and unidirectional.

## Disk Layout

### Directory Structure

```
<data-dir>/
  net/
    monitor.sock                    # unix domain socket for IPC
    <session-id>/
      index.jsonl                   # append-only event log
      bodies/
        000001_resp.json            # response body (extension from MIME type)
        000007_req.json             # request body
        000012_resp.html
        000015_resp.js
```

### JSONL Format

One JSON object per line. Each entry has:

- `seq` (int): monotonically increasing sequence number, per session.
- `type` (string): event type (see tables above).
- Type-specific fields.

`ts` (ISO 8601 timestamp) and `headers` (object) are stored in the file but omitted from `net-log` output by default. Use `--timestamps` and `--headers` to include them.

`bodyFile` values are relative to the session's net directory (e.g., `bodies/000003_resp.json`).

Example:

```jsonl
{"seq":1,"ts":"2026-03-16T14:30:01.123Z","type":"request","id":"R1","method":"GET","url":"https://example.com/api/users","headers":{"accept":"application/json"},"initiator":"script"}
{"seq":2,"ts":"2026-03-16T14:30:01.456Z","type":"response","id":"R1","status":200,"mime":"application/json","headers":{"content-type":"application/json"},"size":842,"bodyFile":"bodies/000002_resp.json"}
{"seq":3,"ts":"2026-03-16T14:30:01.500Z","type":"ws-open","id":"WS1","url":"wss://example.com/realtime","headers":{}}
{"seq":4,"ts":"2026-03-16T14:30:02.100Z","type":"ws-frame","id":"WS1","dir":"recv","opcode":"text","size":128,"bodyFile":"bodies/000004_resp.json"}
{"seq":5,"ts":"2026-03-16T14:30:05.000Z","type":"interaction","cmd":"click","args":["#load-more"],"source":"rodney"}
{"seq":6,"ts":"2026-03-16T14:30:05.200Z","type":"user-click","selector":"button.load-more","x":450,"y":320,"source":"rodney"}
{"seq":7,"ts":"2026-03-16T14:30:06.000Z","type":"user-input","selector":"input#search","value":"hello world"}
{"seq":8,"ts":"2026-03-16T14:30:06.400Z","type":"page-navigation-requested","url":"https://example.com/search?q=hello+world","reason":"formSubmissionGet"}
{"seq":9,"ts":"2026-03-16T14:30:06.500Z","type":"page-navigated","url":"https://example.com/search?q=hello+world"}
{"seq":10,"ts":"2026-03-16T14:30:07.000Z","type":"page-loaded"}
```

### Body File Rules

**Captured content types** (text-based, by default):
- `application/json`, `application/ld+json`, and other `+json` subtypes
- `text/html`
- `text/plain`
- `text/xml`, `application/xml`, and other `+xml` subtypes
- `text/css`
- `application/javascript`, `text/javascript`
- `image/svg+xml`

Configurable via `--capture-types` on `newsession`:
- `--capture-types=all` captures everything including binary
- `--capture-types=json,html,png` captures specific types

These are per-data-dir settings (since there is one monitor per data dir). They are passed as command-line arguments to `_netmonitor` and apply to all capture-enabled sessions in that browser. If different sessions need different capture settings, use separate data dirs.

**File extensions**: Derived from the MIME type (`application/json` becomes `.json`, `text/html` becomes `.html`, etc.). Falls back to `.body` for unknown types.

**Body size cap**: 5MB by default. Configurable via `--capture-max-body <bytes>` on `newsession` (also per-data-dir). Bodies exceeding the cap are truncated, and the JSONL entry includes `"truncated":true,"originalSize":<bytes>`.

**Request bodies**: Saved for POST/PUT/PATCH requests when present and matching a captured content type.

### Sequence Number Padding

Filenames use 6-digit zero-padded sequence numbers (e.g., `000001_resp.json`, `000042_req.json`). The `seq` field in the JSONL remains an unpadded integer. Body file sequence numbers are sparse (they match the JSONL `seq` of their parent entry, not a separate counter), so the `bodies/` directory will have gaps (e.g., `000002_resp.json`, `000007_resp.json`, `000015_req.json`). Six digits supports 999,999 entries per session, which is sufficient for expected usage.

### Cleanup

The `net/<session-id>/` directory is deleted when the session ends (via `endsession` or stale session cleanup). Data does not persist after session termination. If the agent needs to retain data, it should copy files out before ending the session.

## State Model Changes

### SessionInfo

Add `NoCapture bool` field:

```go
type SessionInfo struct {
    TargetID       string `json:"target_id"`
    ViewportWidth  int    `json:"viewport_width,omitempty"`
    ViewportHeight int    `json:"viewport_height,omitempty"`
    NoCapture      bool   `json:"no_capture,omitempty"`
}
```

`NoCapture` defaults to `false` (zero value, omitted from JSON). Set to `true` by `--no-capture`. The monitor checks `!si.NoCapture` to determine if capture is enabled. This uses Go's `omitempty` correctly: the field is omitted when false (the common case, capture enabled), and only present when true (the opt-out case).

### State

Add `MonitorPID int` field:

```go
type State struct {
    // ... existing fields ...
    MonitorPID int `json:"monitor_pid,omitempty"`
}
```

## CLI Changes

### New Flag on `newsession`

- `--no-capture` -- disables network/interaction monitoring for this session. Sets `NoCapture: true` in `SessionInfo`.

### New Internal Command

- `rodney _netmonitor <data-dir>` -- the long-running background process. Reads `state.json` from the data dir for the Chrome debug URL. Not user-facing.

### New User-Facing Commands

#### `rodney net-log [flags]`

Print the network event log for a session. Reads `index.jsonl` and outputs matching entries to stdout.

**Flags:**

| Flag | Description |
|------|-------------|
| `--session <id>` | Target session (standard session resolution) |
| `--since nav` | Show events since the last `page-navigated` event |
| `--since render` | Since the last `page-loaded` event |
| `--since interaction` | Since the last `interaction` or `user-*` event |
| `--since <timestamp>` | Since a specific ISO 8601 timestamp |
| `--id <request-id>` | Filter to a specific request/response stream |
| `--method <method>` | Filter by HTTP method (comma-separated) |
| `--path <prefix>` | Filter to requests whose URL path starts with this string |
| `--domain <domain>` | Filter by domain. Supports exact match and `*.example.com` wildcard. Comma-separated. |
| `--type <event-type>` | Filter by event type (comma-separated) |
| `--headers` | Include `headers` field in output (off by default) |
| `--timestamps` | Include `ts` field in output (off by default) |
| `--tail <n>` | Show only the last N matching entries |
| `--follow` | Live tail (like `tail -f`) |

Default output includes `seq`, `type`, and type-specific fields. `ts` and `headers` are omitted to reduce token usage.

If a `--since` anchor event does not exist in the log (e.g., `--since nav` but no navigation has occurred), all events are shown (no filtering applied).

`--path` performs prefix matching on the parsed URL path component (not substring on the full URL). `--path /api` matches `/api/users` but not `https://cdn.example.com/static/api-logo.png`.

`--domain` matches on the URL's hostname. `*.example.com` matches any subdomain. Multiple domains can be comma-separated.

#### `rodney net-body <seq> [--session <id>]`

Print the body file for a given sequence number to stdout. Resolves the `bodyFile` path from the JSONL entry.

#### `rodney net-clear [--session <id>]`

Clear captured network data for a session. Sends a `clear` message to the monitor via the IPC socket (see IPC section). The monitor truncates the JSONL, deletes all body files, and resets the sequence counter. If the monitor is not running, `net-clear` performs the file operations directly.

### Help Text Additions

```
Network:
  rodney net-log [flags]                  Show network event log
  rodney net-body <seq>                   Print request/response body by seq number
  rodney net-clear                        Clear captured network data

net-log flags:
  --since nav|render|interaction|<ts>     Filter events since a point in time
  --id <request-id>                       Filter to a specific request stream
  --method <method>                       Filter by HTTP method (comma-separated)
  --path <prefix>                         Filter by URL path prefix
  --domain <domain>                       Filter by domain (supports *.example.com)
  --type <event-type>                     Filter by event type (comma-separated)
  --headers                               Include headers in output
  --timestamps                            Include timestamps in output
  --tail <n>                              Show last N matching entries
  --follow                                Live tail
```

## Process Lifecycle

### Startup

1. `newsession` determines whether capture is enabled for the new session (default: yes, unless `--no-capture`).
2. Under the `state.lock` critical section (the same lock used for writing the session to `state.json`), check whether a monitor is needed:
   - If capture is enabled and `state.MonitorPID` is 0 (or PID is dead):
     - Launch `rodney _netmonitor <data-dir>` as a detached background process.
     - Save PID to `state.MonitorPID` as part of the same locked state write.
   - If a monitor is already running (PID alive), no action needed. The monitor auto-attaches to new targets.

This ensures that two concurrent `newsession` commands against the same data dir cannot both launch a monitor (the state.lock serializes them).

### Runtime

The `_netmonitor` process:
1. Reads `state.json` to get `DebugURL`.
2. Connects to Chrome via CDP.
3. Calls `Target.setAutoAttach{AutoAttach: true, WaitForDebuggerOnStart: false, Flatten: true}`.
4. On each `Target.attachedToTarget` event, reads `state.json` to find which session ID maps to the new target's ID and checks `NoCapture`. There is a race condition: `newsession` creates the page (which triggers the attach event) before writing the session to `state.json`. The monitor handles this by retrying the state.json lookup up to 3 times over 1.5 seconds if the target ID is not found, then skipping capture for unknown targets.
5. If capture enabled: enables `Network`, `Page` domains; injects isolated world listeners.
6. Listens on `<data-dir>/net/monitor.sock` for interaction markers from CLI commands.
7. Writes events to per-session `index.jsonl` and body files.
8. If the CDP WebSocket connection breaks (Chrome crash/restart), the monitor exits cleanly. The next `withPage` call detects the dead PID and re-launches the monitor after the browser is restarted.

### Crash Recovery

Any command that calls `withPage` (all interaction commands) checks:
1. Does state have `MonitorPID > 0`?
2. Is that PID alive (signal 0)?
3. If dead, does any session in this data dir have capture enabled?
4. If yes, re-launch the monitor and update `MonitorPID` in state.json.

### Session End (not last session)

When `endsession` closes a session but other sessions remain:
1. The monitor detects the tab closure via `Target.detachedFromTarget` CDP event.
2. The monitor stops capturing for that target, flushes pending writes, and closes its file handles for that session's JSONL.
3. `endsession` then safely deletes `net/<session-id>/` (no open handles).

### Shutdown (last session)

`endsession`, when closing the last session:
1. Sends SIGTERM to `MonitorPID` (alongside `ProxyPID`).
2. The monitor handles SIGTERM: flushes pending writes, closes all JSONL file handles, removes `monitor.sock`, exits.
3. `endsession` deletes `net/<session-id>/` for the ended session.
4. If the data dir is a temp dir, `os.RemoveAll` cleans up everything including `net/`.

### Stale Session Cleanup

When `sessions` command detects stale sessions (PID dead), it cleans up the session's `net/<session-id>/` directory alongside removing the session from the registry.

## Dependencies

This feature depends on the session-centric state management redesign, which is now complete. It extends the existing model with:

- `NoCapture` field on `SessionInfo`
- `MonitorPID` field on `State`
- Monitor launch logic in `cmdNewSession`
- Monitor kill logic in `cmdEndSession`
- Monitor health check in `withPage`
- New dispatch entries for `_netmonitor`, `net-log`, `net-body`, `net-clear`

All changes are additive; no existing behavior is modified.

## Known Limitations

- **Disk usage**: A long-lived session with heavy traffic can accumulate significant data (JSONL + body files), especially with the 5MB-per-body default. There is no automatic rotation or total size cap. Use `net-clear` to manually reset if the data grows too large. A future `--capture-max-size` option could cap total per-session storage.
- **Attribution heuristic**: See the limitations documented in the Rodney Command Interaction Markers section.
- **Capture settings are per-data-dir**: `--capture-types` and `--capture-max-body` apply to all sessions in a data directory. Use separate data dirs for different capture configurations.

## Future Work

- **Total size cap**: `--capture-max-size <bytes>` to cap per-session storage. When exceeded, oldest bodies are pruned and their JSONL entries updated with `"bodyPruned":true`.
- **HAR export**: `rodney net-export --format har` to produce standard HAR files for use with browser dev tools.
- **Selective WebSocket monitoring**: A `--ws-filter` to limit WebSocket frame capture to specific URLs.
