# Session-Centric State Management Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace rodney's start/stop browser lifecycle with session-centric state management where `newsession`/`endsession`/`sessions` are the primary commands, sessions are the unit of continuity, and a global registry enables cross-data-dir session lookup.

**Architecture:** The global registry (`~/.rodney/sessions.json`) maps session IDs to data directories. Each data dir has its own `state.json` tracking one browser and its sessions. `newsession` lazily launches Chrome or reuses a compatible running browser. `endsession` closes a page and shuts down Chrome when the last session ends. All interaction commands require `--session <id>` (or undocumented `RODNEY_SESSION` env var).

**Tech Stack:** Go, go-rod (Chrome DevTools Protocol), flock for file locking, atomic file writes

**Spec:** `docs/superpowers/specs/2026-03-16-session-centric-state-management-design.md`

---

## Chunk 1: State Model, Registry, and Locking Infrastructure

### Task 1: Update State struct and add SessionInfo type

**Files:**
- Modify: `main.go:134-145` (State struct)

- [ ] **Step 1: Write failing tests for new state model**

Add to `main_test.go` after the existing state tests (around line 877):

```go
func TestSessionInfo_JSONRoundTrip(t *testing.T) {
	info := SessionInfo{
		TargetID:       "ABCDEF123",
		ViewportWidth:  1280,
		ViewportHeight: 720,
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var got SessionInfo
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got != info {
		t.Fatalf("roundtrip mismatch: got %+v, want %+v", got, info)
	}
}

func TestState_NewFields_JSONRoundTrip(t *testing.T) {
	s := State{
		DebugURL:        "ws://127.0.0.1:9222/devtools/browser/abc",
		ChromePID:       12345,
		DataDir:         "/tmp/rodney-abc/chrome-data",
		Headless:        true,
		Stealth:         true,
		Insecure:        false,
		Profile:         "Default",
		ProxyServer:     "proxy.corp:3128",
		ProxyConfigHash: "sha256:abc123",
		Sessions: map[string]SessionInfo{
			"abc123": {TargetID: "TID1", ViewportWidth: 1920, ViewportHeight: 935},
		},
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var got State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Headless != true || got.Profile != "Default" || got.ProxyConfigHash != "sha256:abc123" {
		t.Fatalf("new fields not preserved: %+v", got)
	}
	si, ok := got.Sessions["abc123"]
	if !ok || si.TargetID != "TID1" || si.ViewportWidth != 1920 {
		t.Fatalf("session info not preserved: %+v", got.Sessions)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestSessionInfo_JSONRoundTrip|TestState_NewFields_JSONRoundTrip' -v`
Expected: FAIL (SessionInfo type not defined)

- [ ] **Step 3: Implement the new types**

Replace the State struct and surrounding code at `main.go:134-148`:

```go
// SessionInfo holds per-session state within a browser instance.
type SessionInfo struct {
	TargetID       string `json:"target_id"`
	ViewportWidth  int    `json:"viewport_width,omitempty"`
	ViewportHeight int    `json:"viewport_height,omitempty"`
}

// State persisted between CLI invocations
type State struct {
	DebugURL        string                 `json:"debug_url"`
	ChromePID       int                    `json:"chrome_pid"`
	DataDir         string                 `json:"data_dir"`
	Headless        bool                   `json:"headless,omitempty"`
	Stealth         bool                   `json:"stealth,omitempty"`
	Insecure        bool                   `json:"insecure,omitempty"`
	Profile         string                 `json:"profile,omitempty"`
	ProxyPID        int                    `json:"proxy_pid,omitempty"`
	ProxyPort       int                    `json:"proxy_port,omitempty"`
	ProxyServer     string                 `json:"proxy_server,omitempty"`
	ProxyConfigHash string                 `json:"proxy_config_hash,omitempty"`
	Sessions        map[string]SessionInfo `json:"sessions,omitempty"`
}

// activeSessionID is set by --session <id> flag or RODNEY_SESSION env var.
var activeSessionID string
```

**Important: Keep `ActivePage`, `ViewportWidth`, `ViewportHeight`, and `SessionIDs` as deprecated fields for now.** They will be removed in Task 10 alongside the old commands that reference them. This ensures the codebase stays compilable between tasks.

```go
// Deprecated: kept for compilation until old commands are removed in Task 10
ActivePage     int               `json:"active_page,omitempty"`
ViewportWidth  int               `json:"viewport_width,omitempty"`
ViewportHeight int               `json:"viewport_height,omitempty"`
SessionIDs     map[string]string `json:"session_ids,omitempty"`
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestSessionInfo_JSONRoundTrip|TestState_NewFields_JSONRoundTrip' -v`
Expected: PASS

- [ ] **Step 5: Verify full compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`
Expected: Compiles cleanly (old commands still reference deprecated fields, which still exist)

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "refactor: update State struct for session-centric model"
```

### Task 2: Implement global registry with file locking and atomic writes

**Files:**
- Modify: `main.go` (add new functions after state management section, around line 190)
- Modify: `main_test.go` (add registry tests)

- [ ] **Step 1: Write failing tests for registry operations**

Add to `main_test.go`:

```go
func TestRegistry_AddAndLookup(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "sessions.json")
	lock := filepath.Join(dir, "sessions.lock")

	if err := registryAdd(reg, lock, "abc123", "/tmp/rodney-xyz"); err != nil {
		t.Fatal(err)
	}
	got, err := registryLookup(reg, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/rodney-xyz" {
		t.Fatalf("got %q, want %q", got, "/tmp/rodney-xyz")
	}
}

func TestRegistry_LookupMissing(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "sessions.json")

	_, err := registryLookup(reg, "nothere")
	if err == nil {
		t.Fatal("expected error for missing session")
	}
}

func TestRegistry_Remove(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "sessions.json")
	lock := filepath.Join(dir, "sessions.lock")

	registryAdd(reg, lock, "abc123", "/tmp/rodney-xyz")
	registryAdd(reg, lock, "def456", "/tmp/rodney-xyz")

	if err := registryRemove(reg, lock, "abc123"); err != nil {
		t.Fatal(err)
	}
	_, err := registryLookup(reg, "abc123")
	if err == nil {
		t.Fatal("expected error after removal")
	}
	got, err := registryLookup(reg, "def456")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/rodney-xyz" {
		t.Fatalf("wrong dir: %q", got)
	}
}

func TestRegistry_LoadAll(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "sessions.json")
	lock := filepath.Join(dir, "sessions.lock")

	registryAdd(reg, lock, "aaa111", "/path/a")
	registryAdd(reg, lock, "bbb222", "/path/b")

	all, err := registryLoadAll(reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
}

func TestRegistry_AtomicWrite_ConcurrentRead(t *testing.T) {
	dir := t.TempDir()
	reg := filepath.Join(dir, "sessions.json")
	lock := filepath.Join(dir, "sessions.lock")

	// Write initial entry
	registryAdd(reg, lock, "abc123", "/tmp/rodney-xyz")

	// Read should never see partial/empty content
	for i := 0; i < 100; i++ {
		all, err := registryLoadAll(reg)
		if err != nil {
			t.Fatal(err)
		}
		if len(all) < 1 {
			t.Fatal("read returned empty during concurrent access")
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestRegistry_' -v`
Expected: FAIL (registry functions not defined)

- [ ] **Step 3: Implement registry functions**

Add to `main.go` after the `removeState()` function (around line 190). The registry lives at `~/.rodney/sessions.json` with a lock file at `~/.rodney/sessions.lock`.

```go
import (
	"crypto/sha256"
	"encoding/hex"
)

// registryDir returns the global registry directory (~/.rodney/).
func registryDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rodney")
}

// registryPath returns the path to the global sessions.json registry.
func registryPath() string {
	return filepath.Join(registryDir(), "sessions.json")
}

// registryLockPath returns the path to the global sessions.lock file.
func registryLockPath() string {
	return filepath.Join(registryDir(), "sessions.lock")
}

// withFileLock acquires an exclusive flock on the given lock file path,
// runs fn, then releases the lock.
func withFileLock(lockPath string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

// atomicWriteJSON marshals v to JSON and atomically writes to path
// (write to temp file in same dir, then rename).
func atomicWriteJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return err
	}
	tmpPath := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

// registryLoadAll reads the entire registry. Returns empty map if file doesn't exist.
// Safe to call without lock (reads are atomic due to rename-based writes).
func registryLoadAll(regPath string) (map[string]string, error) {
	data, err := os.ReadFile(regPath)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	var m map[string]string
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// registryLookup finds the data dir for a session ID. Returns error if not found.
func registryLookup(regPath, sessionID string) (string, error) {
	m, err := registryLoadAll(regPath)
	if err != nil {
		return "", err
	}
	dir, ok := m[sessionID]
	if !ok {
		return "", fmt.Errorf("session %q not found", sessionID)
	}
	return dir, nil
}

// registryAdd adds a session ID -> data dir mapping under file lock.
func registryAdd(regPath, lockPath, sessionID, dataDir string) error {
	return withFileLock(lockPath, func() error {
		m, err := registryLoadAll(regPath)
		if err != nil {
			return err
		}
		m[sessionID] = dataDir
		if err := os.MkdirAll(filepath.Dir(regPath), 0755); err != nil {
			return err
		}
		return atomicWriteJSON(regPath, m)
	})
}

// registryRemove removes a session ID from the registry under file lock.
func registryRemove(regPath, lockPath, sessionID string) error {
	return withFileLock(lockPath, func() error {
		m, err := registryLoadAll(regPath)
		if err != nil {
			return err
		}
		delete(m, sessionID)
		return atomicWriteJSON(regPath, m)
	})
}

// proxyConfigHash computes a SHA-256 hash of the proxy URL for
// compatibility checking without storing credentials in plaintext.
func proxyConfigHash(proxyURL string) string {
	h := sha256.Sum256([]byte(proxyURL))
	return "sha256:" + hex.EncodeToString(h[:])
}
```

Note: The `registryAdd`/`registryRemove`/`registryLoadAll` functions take explicit file paths so they can be tested with temp dirs. The `registryPath()`/`registryLockPath()` convenience functions provide the default paths for production use.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestRegistry_' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add global session registry with file locking and atomic writes"
```

### Task 3: Add per-data-dir state locking and atomic state writes

**Files:**
- Modify: `main.go:177-189` (saveState, removeState functions)

- [ ] **Step 1: Write failing test for locked state operations**

```go
func TestSaveState_AtomicWrite(t *testing.T) {
	dir := t.TempDir()
	sp := filepath.Join(dir, "state.json")
	lp := filepath.Join(dir, "state.lock")

	s := &State{
		DebugURL:  "ws://test",
		ChromePID: 999,
		Sessions:  map[string]SessionInfo{"abc": {TargetID: "T1"}},
	}
	if err := saveStateAt(sp, lp, s); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(sp)
	if err != nil {
		t.Fatal(err)
	}
	var got State
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ChromePID != 999 {
		t.Fatalf("expected PID 999, got %d", got.ChromePID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestSaveState_AtomicWrite' -v`
Expected: FAIL

- [ ] **Step 3: Implement locked state operations**

Update `saveState` and add `saveStateAt` and `stateLockPath` functions in `main.go`:

```go
func stateLockPath() string {
	return filepath.Join(stateDir(), "state.lock")
}

// saveStateAt writes state to a specific path under file lock with atomic write.
func saveStateAt(statePath, lockPath string, s *State) error {
	return withFileLock(lockPath, func() error {
		if err := os.MkdirAll(filepath.Dir(statePath), 0755); err != nil {
			return err
		}
		return atomicWriteJSON(statePath, s)
	})
}

// saveState writes state to the default state path under lock.
func saveState(s *State) error {
	return saveStateAt(statePath(), stateLockPath(), s)
}
```

Update the existing `saveState` callers -- the function signature changes from no return value to returning `error`. Check all call sites and handle the error (typically with `fatal()`).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestSaveState_AtomicWrite' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add file locking and atomic writes for state.json"
```

---

## Chunk 2: Default Scoping, Session Lookup, and RODNEY_HOME Removal

### Task 4: Update default scoping logic

**Files:**
- Modify: `main.go:87-131` (extractScopeArgs, resolveStateDir)
- Modify: `main.go:150-159` (stateDir -- remove RODNEY_HOME)
- Modify: `main_test.go` (update existing scope tests, add new tests)

- [ ] **Step 1: Write failing tests for new default scoping**

Update `TestResolveStateDir_AutoFallsBackToGlobal` (around line 807) -- it currently expects fallback to `~/.rodney/`. The new behavior is: auto-detect returns empty string (meaning "no existing session found"), signaling that `newsession` should create a temp dir. For non-newsession commands, if no session ID is provided the command will error. So `resolveStateDir` with `scopeAuto` when no `.rodney/` exists should still return the global path (as a fallback for reading), but `newsession` will handle the temp-dir creation at a higher level.

Actually, the cleaner approach: keep `resolveStateDir` as-is for existing commands (they look up sessions via registry anyway). The new default-scoping logic is specific to `newsession`. Add a new function:

```go
func TestResolveNewSessionDir_LocalExists(t *testing.T) {
	dir := t.TempDir()
	localDir := filepath.Join(dir, ".rodney")
	os.MkdirAll(localDir, 0755)

	got := resolveNewSessionDir(scopeAuto, dir, "")
	if got != localDir {
		t.Fatalf("expected %q, got %q", localDir, got)
	}
}

func TestResolveNewSessionDir_NoLocal_CreatesTmp(t *testing.T) {
	dir := t.TempDir() // no .rodney/ inside

	got := resolveNewSessionDir(scopeAuto, dir, "")
	if !strings.HasPrefix(got, filepath.Join(os.TempDir(), "rodney-")) {
		t.Fatalf("expected temp dir starting with rodney-, got %q", got)
	}
	// Verify dir was created
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("temp dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("not a directory")
	}
	os.RemoveAll(got) // cleanup
}

func TestResolveNewSessionDir_ExplicitLocal(t *testing.T) {
	dir := t.TempDir()
	got := resolveNewSessionDir(scopeLocal, dir, "")
	expected := filepath.Join(dir, ".rodney")
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestResolveNewSessionDir_ExplicitHomeDir(t *testing.T) {
	dir := t.TempDir()
	customDir := filepath.Join(dir, "custom")
	got := resolveNewSessionDir(scopeAuto, dir, customDir)
	if got != customDir {
		t.Fatalf("expected %q, got %q", customDir, got)
	}
}

func TestResolveNewSessionDir_HomeDirTmp(t *testing.T) {
	dir := t.TempDir()
	got := resolveNewSessionDir(scopeAuto, dir, "tmp")
	if !strings.HasPrefix(got, filepath.Join(os.TempDir(), "rodney-")) {
		t.Fatalf("expected temp dir, got %q", got)
	}
	os.RemoveAll(got) // cleanup
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestResolveNewSessionDir' -v`
Expected: FAIL

- [ ] **Step 3: Implement resolveNewSessionDir**

Add to `main.go` after `resolveStateDir`:

```go
// resolveNewSessionDir determines the data directory for a new session.
// For newsession only: if no .rodney/ exists locally and no explicit scope is given,
// creates a temp dir. This differs from resolveStateDir which falls back to global.
func resolveNewSessionDir(mode scopeMode, workingDir string, homeDir string) string {
	// Explicit --home-dir takes precedence
	if homeDir != "" {
		if homeDir == "tmp" {
			dir, err := os.MkdirTemp(os.TempDir(), "rodney-")
			if err != nil {
				fatal("failed to create temp dir: %v", err)
			}
			return dir
		}
		return homeDir
	}
	switch mode {
	case scopeLocal:
		return filepath.Join(workingDir, ".rodney")
	case scopeGlobal:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".rodney")
	default: // scopeAuto
		localDir := filepath.Join(workingDir, ".rodney")
		if info, err := os.Stat(localDir); err == nil && info.IsDir() {
			return localDir
		}
		dir, err := os.MkdirTemp(os.TempDir(), "rodney-")
		if err != nil {
			fatal("failed to create temp dir: %v", err)
		}
		return dir
	}
}
```

- [ ] **Step 4: Remove RODNEY_HOME from stateDir()**

In `stateDir()` (line 150-159), remove the `RODNEY_HOME` check:

```go
func stateDir() string {
	if activeStateDir != "" {
		return activeStateDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rodney")
}
```

Update `TestStateDir_EnvVar` test -- it should be removed or updated since `RODNEY_HOME` is no longer supported.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestResolveNewSessionDir|TestStateDir' -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: new default scoping for newsession (local or temp dir)"
```

### Task 5: Update session lookup to require session ID

**Files:**
- Modify: `main.go:207-234` (getActivePage function)
- Modify: `main.go:87-112` (extractScopeArgs -- add RODNEY_SESSION env var fallback)

- [ ] **Step 1: Write failing tests for session-based page lookup**

```go
func TestResolveSessionID_FlagTakesPrecedence(t *testing.T) {
	t.Setenv("RODNEY_SESSION", "envid1")
	got := resolveSessionID("flagid", "")
	if got != "flagid" {
		t.Fatalf("expected flagid, got %q", got)
	}
}

func TestResolveSessionID_EnvVarFallback(t *testing.T) {
	t.Setenv("RODNEY_SESSION", "envid1")
	got := resolveSessionID("", "")
	if got != "envid1" {
		t.Fatalf("expected envid1, got %q", got)
	}
}

func TestResolveSessionID_ErrorWhenMissing(t *testing.T) {
	t.Setenv("RODNEY_SESSION", "")
	got := resolveSessionID("", "")
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestResolveSessionID_PositionalArgFirst(t *testing.T) {
	t.Setenv("RODNEY_SESSION", "envid1")
	got := resolveSessionID("flagid", "posid")
	if got != "posid" {
		t.Fatalf("expected posid, got %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestResolveSessionID' -v`
Expected: FAIL

- [ ] **Step 3: Implement resolveSessionID and update getActivePage**

```go
// resolveSessionID determines the session ID from available sources.
// Priority: positionalArg > --session flag > RODNEY_SESSION env var.
// Returns empty string if none provided.
func resolveSessionID(flagValue, positionalArg string) string {
	if positionalArg != "" {
		return positionalArg
	}
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("RODNEY_SESSION"); env != "" {
		return env
	}
	return ""
}
```

Rewrite `getActivePage` to only support session-based lookup:

```go
// getActivePage returns the page for the active session.
// Requires activeSessionID to be set (via --session flag or RODNEY_SESSION env var).
func getActivePage(browser *rod.Browser, s *State) (*rod.Page, error) {
	if activeSessionID == "" {
		return nil, fmt.Errorf("session ID required; pass --session <id>")
	}

	si, ok := s.Sessions[activeSessionID]
	if !ok {
		return nil, fmt.Errorf("session %q not found in state", activeSessionID)
	}

	pages, err := browser.Pages()
	if err != nil {
		return nil, err
	}
	for _, p := range pages {
		if string(p.TargetID) == si.TargetID {
			return p, nil
		}
	}
	return nil, fmt.Errorf("page for session %q no longer exists", activeSessionID)
}
```

Update `extractScopeArgs` or the `main()` function to check `RODNEY_SESSION` env var as fallback when `--session` is not provided:

In `main()` after extractScopeArgs returns, add:

```go
if sessionID == "" {
	sessionID = os.Getenv("RODNEY_SESSION")
}
activeSessionID = sessionID
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestResolveSessionID' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: session-based page lookup with --session and RODNEY_SESSION"
```

---

## Chunk 3: The `newsession` Command

### Task 6: Implement browser compatibility checking

**Files:**
- Modify: `main.go` (add new function)
- Modify: `main_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestCheckBrowserCompat_AllMatch(t *testing.T) {
	s := &State{Headless: true, Stealth: true, Insecure: false, Profile: "Default"}
	flags := &startFlags{
		headless: true, stealth: true, ignoreCertErrors: false, profile: "Default",
		explicitFlags: map[string]bool{"headless": true, "stealth": true, "insecure": true, "profile": true},
	}
	if err := checkBrowserCompat(s, flags, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckBrowserCompat_HeadlessMismatch(t *testing.T) {
	s := &State{Headless: true, Stealth: true}
	flags := &startFlags{
		headless: false, stealth: true,
		explicitFlags: map[string]bool{"headless": true},
	}
	err := checkBrowserCompat(s, flags, "")
	if err == nil {
		t.Fatal("expected error for headless mismatch")
	}
	if !strings.Contains(err.Error(), "headless") {
		t.Fatalf("error should mention headless: %v", err)
	}
}

func TestCheckBrowserCompat_OmittedFlagsNotConflict(t *testing.T) {
	s := &State{Headless: true, Stealth: true, Insecure: true, Profile: "Work"}
	// nil flags means nothing was explicitly passed -- no conflict
	if err := checkBrowserCompat(s, nil, ""); err != nil {
		t.Fatalf("nil flags should not conflict: %v", err)
	}
}

func TestCheckBrowserCompat_ProxyMismatch(t *testing.T) {
	s := &State{ProxyConfigHash: "sha256:aaa"}
	flags := &startFlags{explicitFlags: map[string]bool{}}
	err := checkBrowserCompat(s, flags, "sha256:bbb")
	if err == nil {
		t.Fatal("expected error for proxy mismatch")
	}
}

func TestCheckBrowserCompat_InsecureMismatch_BothDirections(t *testing.T) {
	// Browser has insecure, user omits it (no explicit flag) -- no error
	s := &State{Insecure: true}
	flags := &startFlags{explicitFlags: map[string]bool{}}
	if err := checkBrowserCompat(s, flags, ""); err != nil {
		t.Fatalf("omitted flag should not conflict: %v", err)
	}

	// Browser has insecure, user explicitly passes non-insecure -- error
	flags2 := &startFlags{
		ignoreCertErrors: false,
		explicitFlags:    map[string]bool{"insecure": true},
	}
	err := checkBrowserCompat(s, flags2, "")
	if err == nil {
		t.Fatal("expected error for insecure mismatch")
	}
}
```

- [ ] **Step 2: Run tests**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestCheckBrowserCompat' -v`
Expected: FAIL

- [ ] **Step 3: Implement checkBrowserCompat**

The function needs to know which flags were explicitly passed vs. defaulted. Update `parseStartFlags` to track which flags were explicitly set. One approach: add a `flagsSet` map to `startFlags`:

```go
type startFlags struct {
	headless         bool
	ignoreCertErrors bool
	stealth          bool
	viewport         string
	profile          string
	explicitFlags    map[string]bool // tracks which flags were explicitly passed
}
```

In `parseStartFlags`, set `explicitFlags["headless"] = true` when `--headless` is parsed, etc.

Then:

```go
// checkBrowserCompat verifies that explicitly-passed flags are compatible with
// the running browser. Returns nil if compatible, descriptive error if not.
// If flags is nil, no flags were passed and there's no conflict.
func checkBrowserCompat(s *State, flags *startFlags, currentProxyHash string) error {
	if flags == nil {
		return nil
	}
	if flags.explicitFlags["headless"] && flags.headless != s.Headless {
		if s.Headless {
			return fmt.Errorf("browser already running with --headless; pass --headless or omit the flag")
		}
		return fmt.Errorf("browser already running without --headless; omit the --headless flag or end existing sessions first")
	}
	if flags.explicitFlags["stealth"] && flags.stealth != s.Stealth {
		if s.Stealth {
			return fmt.Errorf("browser already running with stealth; omit --no-stealth or end existing sessions first")
		}
		return fmt.Errorf("browser already running with --no-stealth; pass --no-stealth or omit the flag")
	}
	if flags.explicitFlags["insecure"] && flags.ignoreCertErrors != s.Insecure {
		if s.Insecure {
			return fmt.Errorf("browser already running with --insecure; pass --insecure or omit the flag")
		}
		return fmt.Errorf("browser already running without --insecure; omit the --insecure flag or end existing sessions first")
	}
	if flags.explicitFlags["profile"] && flags.profile != s.Profile {
		return fmt.Errorf("browser already running with profile %q; pass --profile %s or omit the flag", s.Profile, s.Profile)
	}
	if currentProxyHash != "" && s.ProxyConfigHash != "" && currentProxyHash != s.ProxyConfigHash {
		return fmt.Errorf("browser already running with a different proxy configuration")
	}
	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestCheckBrowserCompat' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: browser compatibility checking for newsession"
```

### Task 7: Implement cmdNewSession

**Files:**
- Modify: `main.go` (replace cmdStart + cmdNewPage with cmdNewSession)

This is the largest single task. `cmdNewSession` combines browser launch logic from `cmdStart` with page creation from `cmdNewPage`.

- [ ] **Step 1: Update parseStartFlags to accept a positional URL argument**

Currently `parseStartFlags` rejects unknown arguments. Modify it to collect positional arguments into a new field. Add `url string` and the `explicitFlags` map to the struct:

```go
type startFlags struct {
	headless         bool
	ignoreCertErrors bool
	stealth          bool
	viewport         string
	profile          string
	url              string              // positional arg (URL)
	explicitFlags    map[string]bool     // tracks which flags were explicitly passed
}
```

In the `parseStartFlags` loop, instead of `return f, fmt.Errorf("unknown flag: %s", ...)`, collect non-flag args: if the arg doesn't start with `--` or `-`, treat it as the URL. Track explicit flags by setting `explicitFlags["headless"] = true` when `--headless` is parsed, etc.

Also, make the `scopeMode` available to `cmdNewSession` by adding a global variable (similar to `activeStateDir`):

```go
var activeScopeMode scopeMode // set once in main() from extractScopeArgs result
```

Set it in `main()` right after the `extractScopeArgs` call.

- [ ] **Step 2: Write the cmdNewSession function**

Create `cmdNewSession` by extracting and combining code from `cmdStart` (lines 776-962) and `cmdNewPage` (lines 2052-2116). The structure:

```go
func cmdNewSession(args []string) {
	flags := parseStartFlags(args)

	// 1. Resolve data dir (uses resolveNewSessionDir)
	wd, _ := os.Getwd()
	dataDir := resolveNewSessionDir(activeScopeMode, wd, homeDirFlag)
	activeStateDir = dataDir

	// 2. Check for running browser
	s, err := loadState()
	var browser *rod.Browser
	browserAlreadyRunning := false

	if err == nil && s.ChromePID > 0 {
		// Check if PID is alive
		if proc, findErr := os.FindProcess(s.ChromePID); findErr == nil {
			if killErr := proc.Signal(syscall.Signal(0)); killErr == nil {
				// PID alive -- check compatibility
				currentProxyHash := ""
				if _, _, _, needed := detectProxy(); needed {
					proxyEnv := /* get proxy URL */
					currentProxyHash = proxyConfigHash(proxyEnv)
				}
				if err := checkBrowserCompat(s, flags, currentProxyHash); err != nil {
					fatal("%v", err)
				}
				browser, err = connectBrowser(s)
				if err != nil {
					fatal("browser PID alive but cannot connect: %v", err)
				}
				browserAlreadyRunning = true
			}
		}
	}

	if !browserAlreadyRunning {
		// Clean up stale state if it exists
		removeState()

		// Launch Chrome -- reuse the launch logic from cmdStart lines 793-924
		// (Chrome binary detection, flag building, proxy setup, stealth flags)
		// ... [extract from cmdStart]

		// Save initial state with browser config
		s = &State{
			DebugURL:  debugURL,
			ChromePID: pid,
			DataDir:   chromeDataDir,
			Headless:  flags.headless,
			Stealth:   flags.stealth,
			Insecure:  flags.ignoreCertErrors,
			Profile:   flags.profile,
			// proxy fields...
			Sessions:  make(map[string]SessionInfo),
		}
		if err := saveState(s); err != nil {
			fatal("save state: %v", err)
		}
	}

	// 3. Create page
	var page *rod.Page
	if len(s.Sessions) == 0 {
		// First session: claim the initial blank tab
		pages, _ := browser.Pages()
		if len(pages) > 0 {
			page = pages[0]
		} else {
			// Shouldn't happen, but create one
			page = browser.MustPage("")
		}
	} else {
		// Subsequent session: new window
		t, err := proto.TargetCreateTarget{URL: "", NewWindow: true}.Call(browser)
		if err != nil {
			fatal("create window: %v", err)
		}
		page = browser.MustPageFromTargetID(t.TargetID)
	}

	// Apply stealth if enabled
	if s.Stealth {
		applyStealthToPage(page, browser, s)
	}

	// Navigate to URL if provided
	if flags.url != "" {
		u := flags.url
		if !strings.Contains(u, "://") {
			u = "http://" + u
		}
		page.MustNavigate(u).MustWaitLoad()
	}

	// 4. Generate session ID
	sessionID := shortID()
	// Check uniqueness against global registry
	reg, _ := registryLoadAll(registryPath())
	for _, exists := reg[sessionID]; exists; _, exists = reg[sessionID] {
		sessionID = shortID()
	}

	// 5. Store viewport per-session
	vw, vh := 0, 0
	if flags.viewport != "" {
		// parse viewport
	}

	// 6. Persist
	s.Sessions[sessionID] = SessionInfo{
		TargetID:       string(page.TargetID),
		ViewportWidth:  vw,
		ViewportHeight: vh,
	}
	if err := saveState(s); err != nil {
		fatal("save state: %v", err)
	}

	// Set viewport via CDP if specified
	if vw > 0 && vh > 0 {
		// EmulationSetDeviceMetricsOverride
	}

	if err := registryAdd(registryPath(), registryLockPath(), sessionID, dataDir); err != nil {
		fatal("registry add: %v", err)
	}

	// 7. Output session ID only
	fmt.Println(sessionID)
}
```

Important: Extract the Chrome launch logic from `cmdStart` (lines 793-924) into a helper function like `launchChrome(flags *startFlags, dataDir string) (debugURL string, pid int, chromeDataDir string, proxyPID int, proxyPort int, proxyServer string, proxyHash string)` to keep `cmdNewSession` readable.

Note on `applyStealthToPage`: This function currently references `s.ViewportWidth` and `s.ViewportHeight` at the State level. Since viewport is now per-session, update `applyStealthToPage` to accept viewport dimensions as parameters instead of reading from State, or pass the SessionInfo. Update the function signature:

```go
func applyStealthToPage(page *rod.Page, browser *rod.Browser, s *State, vpWidth, vpHeight int)
```

- [ ] **Step 2: Extract launchChrome helper from cmdStart**

Move lines 793-924 of `cmdStart` into a new function. This includes:
- Chrome data dir creation
- Profile resolution
- Launcher configuration (flags, binary detection)
- Proxy detection and helper launch
- Stealth flags
- Getting debug URL and PID

```go
type launchResult struct {
	debugURL     string
	pid          int
	chromeDataDir string
	proxyPID     int
	proxyPort    int
	proxyServer  string
	proxyHash    string
}

func launchChrome(flags *startFlags, dataDir string) launchResult {
	// ... extracted from cmdStart
}
```

- [ ] **Step 3: Update applyStealthToPage signature and getStealthCtx**

Change `applyStealthToPage(page *rod.Page, browser *rod.Browser, s *State)` to accept viewport dimensions directly:

```go
func applyStealthToPage(page *rod.Page, browser *rod.Browser, stealth bool, vpWidth, vpHeight int)
```

Update all existing callers (in cmdOpen and anywhere else that calls it).

**Also update `getStealthCtx` in `stealth_exec.go`** (around line 49-57). This function currently reads `s.ViewportWidth` and `s.ViewportHeight` from the top-level State. Update it to accept viewport dimensions as parameters, or look them up from `s.Sessions[activeSessionID]`:

```go
func getStealthCtx(page *rod.Page, s *State) *stealthCtx {
	tid := string(page.TargetID)
	if v, ok := stealthCtxMap.Load(tid); ok {
		return v.(*stealthCtx)
	}
	sc := newStealthCtx(page)
	// Look up viewport from per-session info
	if activeSessionID != "" {
		if si, ok := s.Sessions[activeSessionID]; ok {
			if si.ViewportWidth > 0 && si.ViewportHeight > 0 {
				sc.viewport = [2]int{si.ViewportWidth, si.ViewportHeight}
			}
		}
	}
	stealthCtxMap.Store(tid, sc)
	return sc
}
```

This ensures interaction commands (click, input, etc.) use the correct per-session viewport.

- [ ] **Step 4: Verify compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`
Expected: Builds successfully (ignoring commands not yet rewritten)

- [ ] **Step 5: Commit**

```bash
git add main.go
git commit -m "feat: implement newsession command with lazy browser launch"
```

---

## Chunk 4: The `endsession` and `sessions` Commands

### Task 8: Implement cmdEndSession

**Files:**
- Modify: `main.go` (add new function, replacing cmdClosePage + cmdStop)

- [ ] **Step 1: Implement cmdEndSession**

```go
func cmdEndSession(args []string) {
	// 1. Resolve session ID (positional > --session > env var)
	positionalID := ""
	if len(args) > 0 {
		positionalID = args[0]
	}
	sid := resolveSessionID(activeSessionID, positionalID)
	if sid == "" {
		fatal("session ID required; pass --session <id>")
	}

	// 2. Look up in registry
	regPath := registryPath()
	lockPath := registryLockPath()
	dataDir, err := registryLookup(regPath, sid)
	if err != nil {
		fatal("session %q not found", sid)
	}

	// 3. Load state from that data dir
	sp := filepath.Join(dataDir, "state.json")
	data, err := os.ReadFile(sp)
	if err != nil {
		fatal("cannot read state for session %q: %v", sid, err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		fatal("corrupt state for session %q: %v", sid, err)
	}

	si, ok := s.Sessions[sid]
	if !ok {
		// Session not in state but in registry -- clean up registry
		registryRemove(regPath, lockPath, sid)
		fatal("session %q not found in state", sid)
	}

	// 4. Close the page via CDP (gracefully handle already-closed tabs)
	browser, err := connectBrowser(&s)
	if err == nil {
		pages, _ := browser.Pages()
		for _, p := range pages {
			if string(p.TargetID) == si.TargetID {
				proto.TargetCloseTarget{TargetID: p.TargetID}.Call(browser)
				break
			}
		}
		// Also clean up stealthCtxMap
		stealthCtxMap.Delete(si.TargetID)
	}

	// 5. Remove session from state (under lock) and check if last session
	slp := filepath.Join(dataDir, "state.lock")
	wasLastSession := false
	withFileLock(slp, func() error {
		// Re-read state under lock in case of concurrent modification
		data, err := os.ReadFile(sp)
		if err != nil {
			return err
		}
		var fresh State
		json.Unmarshal(data, &fresh)
		delete(fresh.Sessions, sid)
		wasLastSession = len(fresh.Sessions) == 0
		return atomicWriteJSON(sp, &fresh)
	})

	// 6. Remove from registry
	registryRemove(regPath, lockPath, sid)

	// 7. If no sessions remain, shut down browser
	if wasLastSession && browser != nil {
		// Check for remaining pages
		pages, _ := browser.Pages()
		hasUserPages := false
		for _, p := range pages {
			info, err := p.Info()
			if err != nil {
				continue
			}
			if !strings.HasPrefix(info.URL, "chrome://") && info.URL != "about:blank" {
				hasUserPages = true
				break
			}
		}
		if !hasUserPages {
			browser.MustClose()
		}

		// Wait for PID to exit
		if final.ChromePID > 0 {
			waitForProcessExit(final.ChromePID, 3*time.Second)
			// If still alive, SIGTERM
			if proc, err := os.FindProcess(final.ChromePID); err == nil {
				proc.Signal(syscall.SIGTERM)
				waitForProcessExit(final.ChromePID, 2*time.Second)
			}
		}

		// Kill proxy helper
		if final.ProxyPID > 0 {
			if proc, err := os.FindProcess(final.ProxyPID); err == nil {
				proc.Signal(syscall.SIGTERM)
			}
		}

		// Remove state.json
		os.Remove(sp)
		os.Remove(slp)

		// Remove temp dir if applicable
		if strings.HasPrefix(dataDir, filepath.Join(os.TempDir(), "rodney-")) {
			os.RemoveAll(dataDir)
		}
	}
}
```

- [ ] **Step 2: Verify compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`

- [ ] **Step 3: Commit**

```bash
git add main.go
git commit -m "feat: implement endsession command"
```

### Task 9: Implement cmdSessions

**Files:**
- Modify: `main.go` (add new function, replacing cmdPages)

- [ ] **Step 1: Write tests for sessions output formatting**

```go
func TestFormatSessionsGroup(t *testing.T) {
	entries := []sessionEntry{
		{ID: "abc123", Title: "Dashboard", URL: "https://app.example.com"},
		{ID: "def456", Title: "Login", URL: "https://app.example.com/login"},
	}
	got := formatSessionsGroup(".rodney/", entries, "abc123")
	if !strings.Contains(got, "[abc123]") {
		t.Fatal("should contain session ID in brackets")
	}
	if !strings.Contains(got, ".rodney/") {
		t.Fatal("should contain group header")
	}
	if !strings.Contains(got, "*") {
		t.Fatal("should mark active session")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestFormatSessionsGroup' -v`
Expected: FAIL

- [ ] **Step 3: Implement cmdSessions**

```go
type sessionEntry struct {
	ID    string
	Title string
	URL   string
}

func formatSessionsGroup(header string, entries []sessionEntry, activeSID string) string {
	var b strings.Builder
	b.WriteString(header + "\n")
	for _, e := range entries {
		marker := " "
		if e.ID == activeSID {
			marker = "*"
		}
		title := e.Title
		if title == "" {
			title = "Blank"
		}
		b.WriteString(fmt.Sprintf("%s [%s] %s - %s\n", marker, e.ID, title, e.URL))
	}
	return b.String()
}

func cmdSessions(args []string) {
	showAll := false
	for _, a := range args {
		if a == "--all" {
			showAll = true
		}
	}

	regPath := registryPath()
	all, err := registryLoadAll(regPath)
	if err != nil {
		fatal("read registry: %v", err)
	}

	activeSID := resolveSessionID(activeSessionID, "")

	// Determine if we should filter to one data dir
	filterDir := ""
	if activeSID != "" && !showAll {
		if dir, ok := all[activeSID]; ok {
			filterDir = dir
		}
	}

	// Group sessions by data dir
	groups := make(map[string][]string) // dataDir -> []sessionID
	var groupOrder []string
	for sid, dir := range all {
		if filterDir != "" && dir != filterDir {
			continue
		}
		if _, seen := groups[dir]; !seen {
			groupOrder = append(groupOrder, dir)
		}
		groups[dir] = append(groups[dir], sid)
	}

	if len(groups) == 0 {
		fmt.Fprintln(os.Stderr, "no sessions")
		return
	}

	home, _ := os.UserHomeDir()
	wd, _ := os.Getwd()

	for i, dir := range groupOrder {
		// Format header
		header := dir
		globalDir := filepath.Join(home, ".rodney")
		localDir := filepath.Join(wd, ".rodney")
		switch dir {
		case globalDir:
			header = "~/.rodney/"
		case localDir:
			header = ".rodney/"
		}

		// Check browser health and get page info
		sp := filepath.Join(dir, "state.json")
		data, err := os.ReadFile(sp)
		if err != nil {
			// Stale -- prune all sessions for this dir
			for _, sid := range groups[dir] {
				registryRemove(regPath, registryLockPath(), sid)
			}
			continue
		}
		var s State
		if err := json.Unmarshal(data, &s); err != nil {
			continue
		}

		// Check PID
		pidAlive := false
		if s.ChromePID > 0 {
			if proc, err := os.FindProcess(s.ChromePID); err == nil {
				if proc.Signal(syscall.Signal(0)) == nil {
					pidAlive = true
				}
			}
		}

		if !pidAlive {
			// PID dead -- check if state.json still exists to distinguish cases
			if _, statErr := os.Stat(sp); statErr != nil {
				// PID dead + state.json missing: fully stale, prune silently
				for _, sid := range groups[dir] {
					registryRemove(regPath, registryLockPath(), sid)
				}
				continue
			}
			// PID dead + state.json exists: mark with (stale) indicator, don't prune
			for _, sid := range groups[dir] {
				entries = append(entries, sessionEntry{ID: sid, Title: "(stale)", URL: ""})
			}
			if len(entries) > 0 {
				if i > 0 {
					fmt.Println()
				}
				fmt.Print(formatSessionsGroup(header, entries, activeSID))
			}
			continue
		}

		var entries []sessionEntry
		browser, err := connectBrowser(&s)
		if err != nil {
			// Can't connect but PID alive -- show stale
			for _, sid := range groups[dir] {
				entries = append(entries, sessionEntry{ID: sid, Title: "(stale)", URL: ""})
			}
		} else {
			pages, _ := browser.Pages()
			pageMap := make(map[string]*rod.Page)
			for _, p := range pages {
				pageMap[string(p.TargetID)] = p
			}
			for _, sid := range groups[dir] {
				si, ok := s.Sessions[sid]
				if !ok {
					continue
				}
				p, ok := pageMap[si.TargetID]
				if !ok {
					entries = append(entries, sessionEntry{ID: sid, Title: "(closed)", URL: ""})
					continue
				}
				info, err := p.Info()
				title := ""
				url := ""
				if err == nil {
					title = info.Title
					url = info.URL
				}
				entries = append(entries, sessionEntry{ID: sid, Title: title, URL: url})
			}
		}

		if len(entries) > 0 {
			if i > 0 {
				fmt.Println()
			}
			fmt.Print(formatSessionsGroup(header, entries, activeSID))
		}
	}
}
```

- [ ] **Step 4: Run tests**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestFormatSessionsGroup' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: implement sessions command with grouping and staleness detection"
```

---

## Chunk 5: Update Dispatch, Remove Old Commands, Update Help and Skill

### Task 10: Update main() dispatch and remove old commands

**Files:**
- Modify: `main.go:292-388` (dispatch switch)
- Modify: `main.go` (delete cmdStart, cmdStop, cmdConnect, cmdPages, cmdPage, cmdNewPage, cmdClosePage, cmdStatus)

- [ ] **Step 1: Update the dispatch switch**

Replace the switch statement entries. Add the new commands and helpful errors for removed ones:

```go
case "newsession":
	cmdNewSession(args)
case "endsession":
	cmdEndSession(args)
case "sessions":
	cmdSessions(args)

// Helpful errors for removed commands
case "start":
	fatal("unknown command: start (did you mean newsession?)")
case "stop":
	fatal("unknown command: stop (did you mean endsession?)")
case "connect":
	fatal("unknown command: connect (removed; use newsession)")
case "pages":
	fatal("unknown command: pages (did you mean sessions?)")
case "page":
	fatal("unknown command: page (removed; use --session <id>)")
case "newpage":
	fatal("unknown command: newpage (did you mean newsession?)")
case "closepage":
	fatal("unknown command: closepage (did you mean endsession?)")
case "status":
	fatal("unknown command: status (did you mean sessions?)")
```

- [ ] **Step 2: Remove deprecated fields from State struct**

Now that the old commands are replaced, remove the deprecated fields added in Task 1:
- `ActivePage`
- `ViewportWidth` (top-level)
- `ViewportHeight` (top-level)
- `SessionIDs`

Fix any remaining compilation errors from these removals (should be few or none since old commands are gone).

- [ ] **Step 3: Delete old command functions**

Remove these functions entirely from main.go:
- `cmdStart` (lines 776-962)
- `cmdConnect` (lines 964-1008)
- `cmdStop` (lines 1010-1043)
- `cmdStatus` (lines 1046-1068)
- `cmdPages` (lines 1981-2017)
- `cmdPage` (lines 2019-2050)
- `cmdNewPage` (lines 2052-2116)
- `cmdClosePage` (lines 2118-2200)

- [ ] **Step 4: Remove the homeDirFlag global and cleanupSessionDir**

The `homeDirFlag` (line 49) and `cleanupSessionDir` (line 78) are no longer needed since temp dir cleanup is handled in `cmdEndSession`. Remove them. Also remove `resolveTempHomeDir` (line 69) since `resolveNewSessionDir` handles the "tmp" case.

- [ ] **Step 5: Update withPage helper**

The `withPage()` function (line 623-638) needs to resolve the data dir from the session's registry entry, not from `activeStateDir`. Update it:

```go
func withPage() (*rod.Page, *State) {
	sid := resolveSessionID(activeSessionID, "")
	if sid == "" {
		fatal("session ID required; pass --session <id>")
	}

	// Look up data dir from registry
	dataDir, err := registryLookup(registryPath(), sid)
	if err != nil {
		fatal("session %q not found", sid)
	}
	activeStateDir = dataDir

	s, err := loadState()
	if err != nil {
		fatal("no browser running for session %q: %v", sid, err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("browser for session %q is no longer running: %v", sid, err)
	}
	page, err := getActivePage(browser, s)
	if err != nil {
		fatal("%v", err)
	}
	page = page.Timeout(defaultTimeout)
	return page, s
}
```

- [ ] **Step 6: Update cmdOpen**

`cmdOpen` (line 1070-1127) currently creates a page if none exist. With the session model, it should just navigate the session's page. Remove the "create page if none exist" logic. The session must already exist.

- [ ] **Step 7: Verify compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`
Expected: Clean build

- [ ] **Step 8: Commit**

```bash
git add main.go stealth_exec.go
git commit -m "refactor: remove old commands, wire up newsession/endsession/sessions"
```

### Task 11: Update help.txt

**Files:**
- Modify: `help.txt`

- [ ] **Step 1: Rewrite help.txt**

```
rodney - Chrome automation from the command line

Sessions:
  rodney newsession [url] [flags]    Create session (launches browser if needed)
  rodney endsession [id]             Close a session (shuts down browser if last)
  rodney sessions [--all]            List sessions grouped by data dir

Navigation:
  rodney open <url>                  Navigate to URL
  rodney back                        Go back in history
  rodney forward                     Go forward in history
  rodney reload [--hard]             Reload page (--hard bypasses cache)
  rodney clear-cache                 Clear the browser cache

Page info:
  rodney url                         Print current URL
  rodney title                       Print page title
  rodney html [selector]             Print HTML (page or element)
  rodney text <selector>             Print text content of element
  rodney attr <selector> <name>      Print attribute value
  rodney pdf [file]                  Save page as PDF

Interaction:
  rodney js <expression>             Evaluate JavaScript expression
  rodney click <selector>            Click an element
  rodney input <selector> <text>     Type text into an input field
  rodney clear <selector>            Clear an input field
  rodney file <selector> <path|->    Set file on a file input (- for stdin)
  rodney download <sel> [file|-]     Download href/src target (- for stdout)
  rodney select <selector> <val>     Select dropdown option by value
  rodney submit <selector>           Submit a form
  rodney hover <selector>            Hover over an element
  rodney focus <selector>            Focus an element

Waiting:
  rodney wait <selector>             Wait for element to appear
  rodney waitload                    Wait for page load
  rodney waitstable                  Wait for DOM to stabilize
  rodney waitidle                    Wait for network idle
  rodney sleep <seconds>             Sleep for N seconds

Screenshots:
  rodney screenshot [-w N] [-h N] [file]  Take page screenshot
  rodney screenshot-el <sel> [f]     Screenshot an element

Element checks:
  rodney exists <selector>           Check if element exists (exit 1 if not)
  rodney count <selector>            Count matching elements
  rodney visible <selector>          Check if element is visible (exit 1 if not)
  rodney assert <expr> [expected] [-m msg]  Assert JS expression

Accessibility:
  rodney ax-tree [--depth N] [--json]       Dump accessibility tree
  rodney ax-find [--name N] [--role R] [--json]  Find accessible nodes
  rodney ax-node <selector> [--json]        Show accessibility info for element

newsession flags:
  Browser (only matter on first launch for a data dir):
    --headless                       Launch Chrome in headless mode
    --no-stealth                     Disable stealth mode (stealth on by default)
    --profile <name|email>           Chrome profile
    --insecure | -k                  Ignore SSL certificate errors

  Per-session:
    --viewport WxH                   Set viewport dimensions

  Scope:
    --local                          Use ./.rodney/ data dir
    --global                         Use ~/.rodney/ data dir
    --home-dir <path|tmp>            Use specified dir ("tmp" for auto-create)

  Default: uses ./.rodney/ if it exists, otherwise creates /tmp/rodney-XXXXX

Options (all commands):
  --session <id>                     Target a specific session by ID
  --version                          Print version and exit
  --help, -h, help                   Show this help message

Usage:
  export RODNEY_SESSION=$(rodney newsession https://example.com --local)
  rodney text 'h1'
  rodney endsession

Exit codes:
  0  Success
  1  Check failed (exists, visible, assert, ax-find returned no match)
  2  Error (bad arguments, no browser, timeout, etc.)
```

- [ ] **Step 2: Commit**

```bash
git add help.txt
git commit -m "docs: rewrite help.txt for session-centric model"
```

### Task 12: Update the browser-based-website-extraction skill

**Files:**
- Modify: `/home/jamalex/.claude/skills/browser-based-website-extraction/SKILL.md`

- [ ] **Step 1: Rewrite the skill**

Simplify to only reference three concepts: newsession, interaction with --session, endsession. Remove all mentions of start/stop, --home-dir, --show, --stealth, browser lifecycle, env vars.

Key changes:
- Phase 1 Setup: `rodney newsession '<url>'` returns a session ID. Capture it.
- All subsequent commands: use `--session <id>` suffix instead of `--home-dir <session>`
- Phase 5 Cleanup: `rodney endsession <id>`
- Quick reference: remove Lifecycle and Tabs rows, update the suffix note
- Common mistakes: update for new model
- Stealth retry: `endsession` then `newsession` with `--no-stealth`

- [ ] **Step 2: Commit**

```bash
git add /home/jamalex/.claude/skills/browser-based-website-extraction/SKILL.md
git commit -m "docs: update web-extract skill for session-centric model"
```

### Task 13: Update tests

**Files:**
- Modify: `main_test.go`

- [ ] **Step 1: Remove tests for deleted functions**

Remove or update tests that reference deleted commands/functions:
- `TestParseStartFlags_*` -- keep these since `parseStartFlags` is still used by `newsession`, but update if the struct changed (explicitFlags map)
- `TestExtractScopeArgs_*` -- keep, these test the unchanged extraction function
- `TestResolveStateDir_*` -- keep, but update `TestResolveStateDir_AutoFallsBackToGlobal` if behavior changed
- `TestStateDir_EnvVar` -- remove (RODNEY_HOME removed)
- `TestCleanupSessionDir_*` -- remove (function removed)
- `TestResolveTempHomeDir_*` -- remove (function removed)

- [ ] **Step 2: Update parseStartFlags tests for explicitFlags**

Update the existing `TestParseStartFlags_*` tests to verify `explicitFlags` is populated correctly:

```go
func TestParseStartFlags_ExplicitHeadless(t *testing.T) {
	f := parseStartFlags([]string{"--headless"})
	if !f.explicitFlags["headless"] {
		t.Fatal("headless should be explicitly set")
	}
	if f.explicitFlags["stealth"] {
		t.Fatal("stealth should not be explicitly set (it's a default)")
	}
}
```

- [ ] **Step 3: Run the full test suite**

Run: `cd /exports/projectpool/dev/rodney && go test -v -count=1 ./...`

Fix any remaining compilation or test failures. The stealth integration tests should still pass since they test the stealthCtx layer, which hasn't changed.

- [ ] **Step 4: Commit**

```bash
git add main.go main_test.go
git commit -m "test: update test suite for session-centric model"
```

### Task 14: Final verification

- [ ] **Step 1: Verify clean build**

Run: `cd /exports/projectpool/dev/rodney && go build -o /dev/null ./...`
Expected: Clean build with no errors

- [ ] **Step 2: Run full test suite**

Run: `cd /exports/projectpool/dev/rodney && go test -v -count=1 ./...`
Expected: All tests pass

- [ ] **Step 3: Manual smoke test**

```bash
cd /tmp
SID=$(rodney newsession https://example.com --local)
echo "Session: $SID"
rodney title --session "$SID"
rodney sessions
rodney endsession "$SID"
rodney sessions  # should be empty
```

- [ ] **Step 4: Verify old commands give helpful errors**

```bash
rodney start 2>&1 | grep "did you mean newsession"
rodney stop 2>&1 | grep "did you mean endsession"
rodney pages 2>&1 | grep "did you mean sessions"
```

- [ ] **Step 5: Final commit if any fixes needed**

```bash
git add -A
git commit -m "fix: address issues found in final verification"
```
