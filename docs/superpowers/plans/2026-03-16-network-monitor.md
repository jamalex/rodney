# Network Monitor Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a background network monitoring process to rodney that captures HTTP/WebSocket traffic, page lifecycle events, and user interactions via CDP, writing per-session JSONL + body files to disk for agent consumption.

**Architecture:** A `_netmonitor` background process (one per data dir) connects to Chrome via CDP, enables Network/Page domain events on all attached targets, and injects isolated world event listeners for user interaction capture. CLI commands send interaction markers via a unix domain socket. Data is written as append-only JSONL with separate body files. New `net-log`, `net-body`, and `net-clear` commands provide filtered access.

**Tech Stack:** Go, go-rod (CDP), unix domain sockets, JSONL

**Spec:** `docs/superpowers/specs/2026-03-16-network-monitor-design.md`

---

## Chunk 1: State Model, Flag, and JSONL Infrastructure

### Task 1: Add NoCapture to SessionInfo and MonitorPID to State

**Files:**
- Modify: `main.go:154-158` (SessionInfo struct)
- Modify: `main.go:161-173` (State struct)
- Modify: `main_test.go` (add tests after line 956)

- [ ] **Step 1: Write failing tests for new fields**

Add to `main_test.go` after `TestSaveState_AtomicWrite` (line 978):

```go
func TestSessionInfo_NoCapture_JSONRoundTrip(t *testing.T) {
	// NoCapture false (default) should be omitted from JSON
	info := SessionInfo{TargetID: "T1", NoCapture: false}
	data, _ := json.Marshal(info)
	if strings.Contains(string(data), "no_capture") {
		t.Fatalf("NoCapture=false should be omitted, got %s", data)
	}

	// NoCapture true should be present
	info2 := SessionInfo{TargetID: "T2", NoCapture: true}
	data2, _ := json.Marshal(info2)
	if !strings.Contains(string(data2), `"no_capture":true`) {
		t.Fatalf("NoCapture=true should be present, got %s", data2)
	}

	// Round-trip
	var got SessionInfo
	json.Unmarshal(data2, &got)
	if !got.NoCapture {
		t.Fatal("NoCapture not preserved in round-trip")
	}
}

func TestState_MonitorPID_JSONRoundTrip(t *testing.T) {
	s := State{
		DebugURL:   "ws://test",
		ChromePID:  123,
		MonitorPID: 456,
		Sessions:   map[string]SessionInfo{},
	}
	data, _ := json.Marshal(s)
	if !strings.Contains(string(data), `"monitor_pid":456`) {
		t.Fatalf("MonitorPID not serialized: %s", data)
	}
	var got State
	json.Unmarshal(data, &got)
	if got.MonitorPID != 456 {
		t.Fatalf("MonitorPID not preserved: %d", got.MonitorPID)
	}

	// MonitorPID 0 should be omitted
	s2 := State{DebugURL: "ws://test", ChromePID: 123}
	data2, _ := json.Marshal(s2)
	if strings.Contains(string(data2), "monitor_pid") {
		t.Fatalf("MonitorPID=0 should be omitted: %s", data2)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestSessionInfo_NoCapture|TestState_MonitorPID' -v`
Expected: FAIL (fields not defined)

- [ ] **Step 3: Add the new fields**

In `main.go`, add `NoCapture` to `SessionInfo` at line 157 (before closing brace):

```go
type SessionInfo struct {
	TargetID       string `json:"target_id"`
	ViewportWidth  int    `json:"viewport_width,omitempty"`
	ViewportHeight int    `json:"viewport_height,omitempty"`
	NoCapture      bool   `json:"no_capture,omitempty"`
}
```

Add `MonitorPID` to `State` at line 172 (after `ProxyConfigHash`, before `Sessions`):

```go
	MonitorPID      int                    `json:"monitor_pid,omitempty"`
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestSessionInfo_NoCapture|TestState_MonitorPID' -v`
Expected: PASS

- [ ] **Step 5: Verify full compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`
Expected: Clean build

- [ ] **Step 6: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add NoCapture and MonitorPID fields for network monitoring"
```

### Task 2: Add --no-capture flag to parseStartFlags

**Files:**
- Modify: `main.go:825-833` (startFlags struct)
- Modify: `main.go:836-891` (parseStartFlags function)
- Modify: `main_test.go` (add test after TestParseStartFlags block, around line 1625)

- [ ] **Step 1: Write failing test**

Add to `main_test.go` after the existing `TestParseStartFlags_*` tests (around line 1625):

```go
func TestParseStartFlags_NoCapture(t *testing.T) {
	f, err := parseStartFlags([]string{"--no-capture", "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.noCapture {
		t.Fatal("expected noCapture=true")
	}
	if f.url != "https://example.com" {
		t.Fatalf("expected URL, got %q", f.url)
	}
}

func TestParseStartFlags_NoCaptureDefault(t *testing.T) {
	f, err := parseStartFlags([]string{})
	if err != nil {
		t.Fatal(err)
	}
	if f.noCapture {
		t.Fatal("noCapture should default to false")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestParseStartFlags_NoCapture' -v`
Expected: FAIL

- [ ] **Step 3: Add noCapture to startFlags and parseStartFlags**

In `main.go`, add `noCapture` to the `startFlags` struct (line 831, before `explicitFlags`):

```go
type startFlags struct {
	headless         bool
	ignoreCertErrors bool
	stealth          bool
	viewport         string
	profile          string
	url              string          // positional arg (URL for newsession)
	noCapture        bool
	explicitFlags    map[string]bool // tracks which flags were explicitly passed
}
```

In `parseStartFlags`, add a case for `--no-capture` inside the switch (after the `--no-stealth` case, around line 858):

```go
		case "--no-capture":
			f.noCapture = true
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestParseStartFlags_NoCapture' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add main.go main_test.go
git commit -m "feat: add --no-capture flag to newsession"
```

### Task 3: Create JSONL event types and writer

**Files:**
- Create: `netmonitor.go`
- Create: `netmonitor_test.go`

- [ ] **Step 1: Write failing tests for JSONL event types and writer**

Create `netmonitor_test.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetEvent_JSONSerialization(t *testing.T) {
	e := NetEvent{
		Seq:    1,
		TS:     "2026-03-16T14:30:01.123Z",
		Type:   "request",
		ID:     "R1",
		Method: "GET",
		URL:    "https://example.com/api/users",
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if !strings.Contains(s, `"seq":1`) {
		t.Fatal("missing seq")
	}
	if !strings.Contains(s, `"type":"request"`) {
		t.Fatal("missing type")
	}
	// Fields with zero values should be omitted
	if strings.Contains(s, `"status"`) {
		t.Fatal("status=0 should be omitted")
	}
	if strings.Contains(s, `"bodyFile"`) {
		t.Fatal("empty bodyFile should be omitted")
	}
}

func TestNetEvent_ResponseWithTruncation(t *testing.T) {
	e := NetEvent{
		Seq:          2,
		Type:         "response",
		ID:           "R1",
		Status:       200,
		MIME:         "application/json",
		Size:         8431022,
		BodyFile:     "bodies/000002_resp.json",
		Truncated:    true,
		OriginalSize: 8431022,
	}
	data, _ := json.Marshal(e)
	s := string(data)
	if !strings.Contains(s, `"truncated":true`) {
		t.Fatal("missing truncated")
	}
	if !strings.Contains(s, `"originalSize":8431022`) {
		t.Fatal("missing originalSize")
	}
}

func TestNetEvent_BodyError(t *testing.T) {
	e := NetEvent{
		Seq:       3,
		Type:      "response",
		ID:        "R1",
		Status:    302,
		BodyError: "redirect response",
	}
	data, _ := json.Marshal(e)
	s := string(data)
	if !strings.Contains(s, `"bodyError":"redirect response"`) {
		t.Fatal("missing bodyError")
	}
	if strings.Contains(s, `"bodyFile"`) {
		t.Fatal("bodyFile should be omitted when bodyError is present")
	}
}

func TestJSONLWriter_AppendAndRead(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "net", "abc123")

	w, err := newJSONLWriter(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()

	w.append(NetEvent{Type: "request", ID: "R1", Method: "GET", URL: "https://example.com"})
	w.append(NetEvent{Type: "response", ID: "R1", Status: 200})

	// Read back
	data, err := os.ReadFile(filepath.Join(sessionDir, "index.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	var e1 NetEvent
	json.Unmarshal([]byte(lines[0]), &e1)
	if e1.Seq != 1 || e1.Type != "request" {
		t.Fatalf("unexpected first event: %+v", e1)
	}

	var e2 NetEvent
	json.Unmarshal([]byte(lines[1]), &e2)
	if e2.Seq != 2 || e2.Type != "response" {
		t.Fatalf("unexpected second event: %+v", e2)
	}
}

func TestJSONLWriter_WriteBody(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "net", "abc123")

	w, err := newJSONLWriter(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()

	bodyFile, err := w.writeBody(5, "resp", ".json", []byte(`{"users":["alice"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if bodyFile != "bodies/000005_resp.json" {
		t.Fatalf("unexpected bodyFile path: %s", bodyFile)
	}

	// Verify file exists and has correct content
	fullPath := filepath.Join(sessionDir, bodyFile)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != `{"users":["alice"]}` {
		t.Fatalf("unexpected body content: %s", content)
	}
}

func TestJSONLWriter_WriteBodyTruncated(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "net", "abc123")

	w, err := newJSONLWriter(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()

	bigBody := strings.Repeat("x", 100)
	bodyFile, err := w.writeBodyTruncated(1, "resp", ".json", []byte(bigBody), 50)
	if err != nil {
		t.Fatal(err)
	}

	fullPath := filepath.Join(sessionDir, bodyFile)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 50 {
		t.Fatalf("expected 50 bytes, got %d", len(content))
	}
}

func TestJSONLWriter_Clear(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "net", "abc123")

	w, err := newJSONLWriter(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()

	w.append(NetEvent{Type: "request", ID: "R1"})
	w.writeBody(1, "resp", ".json", []byte("hello"))

	w.clear()

	// Verify JSONL is empty
	data, _ := os.ReadFile(filepath.Join(sessionDir, "index.jsonl"))
	if len(data) != 0 {
		t.Fatalf("expected empty JSONL after clear, got %d bytes", len(data))
	}

	// Verify bodies dir is gone
	if _, err := os.Stat(filepath.Join(sessionDir, "bodies")); !os.IsNotExist(err) {
		t.Fatal("bodies dir should not exist after clear")
	}

	// New events should start at seq 1
	w.append(NetEvent{Type: "request", ID: "R2"})
	data, _ = os.ReadFile(filepath.Join(sessionDir, "index.jsonl"))
	var e NetEvent
	json.Unmarshal(data, &e)
	if e.Seq != 1 {
		t.Fatalf("expected seq=1 after clear, got %d", e.Seq)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestNetEvent|TestJSONLWriter' -v`
Expected: FAIL (types not defined)

- [ ] **Step 3: Implement NetEvent type and JSONLWriter**

Create `netmonitor.go`:

```go
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// NetEvent represents a single entry in the per-session JSONL log.
type NetEvent struct {
	Seq          int               `json:"seq"`
	TS           string            `json:"ts,omitempty"`
	Type         string            `json:"type"`
	ID           string            `json:"id,omitempty"`
	Method       string            `json:"method,omitempty"`
	URL          string            `json:"url,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Initiator    string            `json:"initiator,omitempty"`
	Status       int               `json:"status,omitempty"`
	MIME         string            `json:"mime,omitempty"`
	Size         int               `json:"size,omitempty"`
	BodyFile     string            `json:"bodyFile,omitempty"`
	Truncated    bool              `json:"truncated,omitempty"`
	OriginalSize int               `json:"originalSize,omitempty"`
	BodyError    string            `json:"bodyError,omitempty"`
	Dir          string            `json:"dir,omitempty"`
	Opcode       string            `json:"opcode,omitempty"`
	Cmd          string            `json:"cmd,omitempty"`
	Args         []string          `json:"args,omitempty"`
	Source       string            `json:"source,omitempty"`
	Selector     string            `json:"selector,omitempty"`
	X            int               `json:"x,omitempty"`
	Y            int               `json:"y,omitempty"`
	TextContent  string            `json:"textContent,omitempty"`
	Value        string            `json:"value,omitempty"`
	Action       string            `json:"action,omitempty"`
	Key          string            `json:"key,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	TransType    string            `json:"transitionType,omitempty"`
}

// jsonlWriter manages writing to a session's index.jsonl and bodies/ directory.
type jsonlWriter struct {
	mu         sync.Mutex
	sessionDir string
	file       *os.File
	seq        int
}

func newJSONLWriter(sessionDir string) (*jsonlWriter, error) {
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return nil, fmt.Errorf("create session dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(sessionDir, "index.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open jsonl: %w", err)
	}
	return &jsonlWriter{sessionDir: sessionDir, file: f, seq: 0}, nil
}

func (w *jsonlWriter) append(e NetEvent) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	e.Seq = w.seq
	if e.TS == "" {
		e.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	w.file.Write(append(data, '\n'))
}

func (w *jsonlWriter) writeBody(seq int, kind string, ext string, body []byte) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	bodiesDir := filepath.Join(w.sessionDir, "bodies")
	if err := os.MkdirAll(bodiesDir, 0755); err != nil {
		return "", err
	}
	relPath := fmt.Sprintf("bodies/%06d_%s%s", seq, kind, ext)
	fullPath := filepath.Join(w.sessionDir, relPath)
	if err := os.WriteFile(fullPath, body, 0644); err != nil {
		return "", err
	}
	return relPath, nil
}

func (w *jsonlWriter) writeBodyTruncated(seq int, kind string, ext string, body []byte, maxSize int) (string, error) {
	if len(body) > maxSize {
		body = body[:maxSize]
	}
	return w.writeBody(seq, kind, ext, body)
}

func (w *jsonlWriter) clear() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.file.Close()
	os.Remove(filepath.Join(w.sessionDir, "index.jsonl"))
	os.RemoveAll(filepath.Join(w.sessionDir, "bodies"))
	w.seq = 0
	f, err := os.OpenFile(filepath.Join(w.sessionDir, "index.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	w.file = f
}

func (w *jsonlWriter) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		w.file.Close()
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestNetEvent|TestJSONLWriter' -v`
Expected: PASS

- [ ] **Step 5: Verify full compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`
Expected: Clean build

- [ ] **Step 6: Commit**

```bash
git add netmonitor.go netmonitor_test.go
git commit -m "feat: add NetEvent type and JSONL writer for network monitoring"
```

### Task 4: Add capture-eligible MIME type checking and body extension mapping

**Files:**
- Modify: `netmonitor.go` (add functions)
- Modify: `netmonitor_test.go` (add tests)

- [ ] **Step 1: Write failing tests**

Add to `netmonitor_test.go`:

```go
func TestIsCaptureEligible_TextBased(t *testing.T) {
	eligible := []string{
		"application/json",
		"application/ld+json",
		"text/html",
		"text/plain",
		"text/xml",
		"application/xml",
		"text/css",
		"application/javascript",
		"text/javascript",
		"image/svg+xml",
		"application/vnd.api+json",
	}
	for _, m := range eligible {
		if !isCaptureEligible(m, nil) {
			t.Errorf("expected %q to be eligible", m)
		}
	}
}

func TestIsCaptureEligible_Binary(t *testing.T) {
	ineligible := []string{
		"image/png",
		"image/jpeg",
		"font/woff2",
		"application/octet-stream",
		"video/mp4",
		"audio/mpeg",
	}
	for _, m := range ineligible {
		if isCaptureEligible(m, nil) {
			t.Errorf("expected %q to be ineligible", m)
		}
	}
}

func TestIsCaptureEligible_CustomOverride(t *testing.T) {
	overrides := []string{"all"}
	if !isCaptureEligible("image/png", overrides) {
		t.Fatal("'all' override should make everything eligible")
	}

	overrides2 := []string{"json", "png"}
	if !isCaptureEligible("image/png", overrides2) {
		t.Fatal("png override should make image/png eligible")
	}
	if isCaptureEligible("image/jpeg", overrides2) {
		t.Fatal("jpeg should not be eligible with json,png overrides")
	}
}

func TestBodyFileExt(t *testing.T) {
	tests := []struct {
		mime string
		ext  string
	}{
		{"application/json", ".json"},
		{"application/ld+json", ".json"},
		{"text/html", ".html"},
		{"text/plain", ".txt"},
		{"text/xml", ".xml"},
		{"application/xml", ".xml"},
		{"text/css", ".css"},
		{"application/javascript", ".js"},
		{"text/javascript", ".js"},
		{"image/svg+xml", ".svg"},
		{"image/png", ".png"},
		{"unknown/type", ".body"},
	}
	for _, tt := range tests {
		got := bodyFileExt(tt.mime)
		if got != tt.ext {
			t.Errorf("bodyFileExt(%q) = %q, want %q", tt.mime, got, tt.ext)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestIsCaptureEligible|TestBodyFileExt' -v`
Expected: FAIL

- [ ] **Step 3: Implement the functions**

Add to `netmonitor.go`:

```go
// isCaptureEligible returns true if the MIME type's response body should be saved.
// Default: text-based types only. overrides: ["all"] captures everything,
// or specific short names like ["json", "html", "png"].
func isCaptureEligible(mime string, overrides []string) bool {
	// Strip parameters (e.g., "application/json; charset=utf-8" -> "application/json")
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	mime = strings.ToLower(mime)

	if len(overrides) > 0 {
		for _, o := range overrides {
			if o == "all" {
				return true
			}
			if strings.Contains(mime, o) {
				return true
			}
		}
		return false
	}

	// Default: text-based types
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	if mime == "application/json" || strings.HasSuffix(mime, "+json") {
		return true
	}
	if mime == "application/xml" || strings.HasSuffix(mime, "+xml") {
		return true
	}
	if mime == "application/javascript" {
		return true
	}
	if mime == "image/svg+xml" {
		return true
	}
	return false
}

// bodyFileExt returns a file extension for a MIME type, used for body files.
func bodyFileExt(mime string) string {
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	mime = strings.ToLower(mime)

	// Check +json, +xml subtypes first
	if strings.HasSuffix(mime, "+json") {
		return ".json"
	}
	if strings.HasSuffix(mime, "+xml") {
		return ".xml"
	}

	switch mime {
	case "application/json":
		return ".json"
	case "text/html":
		return ".html"
	case "text/plain":
		return ".txt"
	case "text/xml", "application/xml":
		return ".xml"
	case "text/css":
		return ".css"
	case "application/javascript", "text/javascript":
		return ".js"
	case "image/svg+xml":
		return ".svg"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	default:
		return ".body"
	}
}
```

Note: Add `"strings"` to the import block in `netmonitor.go` if not already present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestIsCaptureEligible|TestBodyFileExt' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add netmonitor.go netmonitor_test.go
git commit -m "feat: add MIME type eligibility checking and body file extensions"
```

---

## Chunk 2: IPC Mechanism and Net Commands

### Task 5: Implement IPC client and server

**Files:**
- Create: `netmonitor_ipc.go`
- Modify: `netmonitor_test.go` (add IPC tests)

- [ ] **Step 1: Write failing tests for IPC**

Add to `netmonitor_test.go`:

```go
func TestIPC_SendAndReceive(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "monitor.sock")

	// Start server
	received := make(chan IPCMessage, 10)
	srv, err := newIPCServer(sockPath, func(msg IPCMessage) {
		received <- msg
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.close()

	// Send a message
	err = ipcSend(sockPath, IPCMessage{
		Session: "abc123",
		Type:    "interaction",
		Cmd:     "click",
		Args:    []string{"#btn"},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-received:
		if msg.Session != "abc123" || msg.Cmd != "click" {
			t.Fatalf("unexpected message: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestIPC_ClearMessage(t *testing.T) {
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "monitor.sock")

	received := make(chan IPCMessage, 10)
	srv, err := newIPCServer(sockPath, func(msg IPCMessage) {
		received <- msg
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.close()

	err = ipcSend(sockPath, IPCMessage{Session: "abc123", Type: "clear"})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case msg := <-received:
		if msg.Type != "clear" || msg.Session != "abc123" {
			t.Fatalf("unexpected: %+v", msg)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestIPC_SendFailsGracefully(t *testing.T) {
	// Non-existent socket should return error, not panic
	err := ipcSend("/tmp/nonexistent-rodney-test.sock", IPCMessage{Type: "interaction"})
	if err == nil {
		t.Fatal("expected error for missing socket")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestIPC_' -v`
Expected: FAIL

- [ ] **Step 3: Implement IPC server and client**

Create `netmonitor_ipc.go`:

```go
package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"sync"
)

// IPCMessage is sent from CLI commands to the network monitor.
type IPCMessage struct {
	Session string   `json:"session"`
	Type    string   `json:"type"`
	Cmd     string   `json:"cmd,omitempty"`
	Args    []string `json:"args,omitempty"`
}

// ipcServer listens on a unix domain socket for IPCMessages.
type ipcServer struct {
	listener net.Listener
	wg       sync.WaitGroup
	done     chan struct{}
}

func newIPCServer(sockPath string, handler func(IPCMessage)) (*ipcServer, error) {
	os.Remove(sockPath) // clean up any stale socket
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	srv := &ipcServer{listener: ln, done: make(chan struct{})}
	srv.wg.Add(1)
	go func() {
		defer srv.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-srv.done:
					return
				default:
					continue
				}
			}
			go func(c net.Conn) {
				defer c.Close()
				scanner := bufio.NewScanner(c)
				for scanner.Scan() {
					var msg IPCMessage
					if err := json.Unmarshal(scanner.Bytes(), &msg); err == nil {
						handler(msg)
					}
				}
			}(conn)
		}
	}()
	return srv, nil
}

func (s *ipcServer) close() {
	close(s.done)
	s.listener.Close()
	s.wg.Wait()
}

// ipcSend sends a single message to the monitor's unix socket (fire-and-forget).
func ipcSend(sockPath string, msg IPCMessage) error {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestIPC_' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add netmonitor_ipc.go netmonitor_test.go
git commit -m "feat: add IPC unix socket server and client for network monitor"
```

### Task 6: Implement net-log command with filtering

**Files:**
- Create: `net_commands.go`
- Modify: `netmonitor_test.go` (add tests)

- [ ] **Step 1: Write failing tests for net-log filtering**

Add to `netmonitor_test.go`:

```go
func TestParseNetLogFlags(t *testing.T) {
	flags := parseNetLogFlags([]string{"--since", "nav", "--method", "GET,POST", "--path", "/api", "--domain", "*.example.com", "--headers", "--timestamps", "--tail", "10"})
	if flags.since != "nav" {
		t.Fatalf("since: %q", flags.since)
	}
	if flags.method != "GET,POST" {
		t.Fatalf("method: %q", flags.method)
	}
	if flags.pathPrefix != "/api" {
		t.Fatalf("path: %q", flags.pathPrefix)
	}
	if flags.domain != "*.example.com" {
		t.Fatalf("domain: %q", flags.domain)
	}
	if !flags.headers || !flags.timestamps {
		t.Fatal("headers/timestamps should be true")
	}
	if flags.tail != 10 {
		t.Fatalf("tail: %d", flags.tail)
	}
}

func TestFilterNetEvents_ByMethod(t *testing.T) {
	events := []NetEvent{
		{Seq: 1, Type: "request", Method: "GET", URL: "https://example.com/a"},
		{Seq: 2, Type: "response", ID: "R1", Status: 200},
		{Seq: 3, Type: "request", Method: "POST", URL: "https://example.com/b"},
		{Seq: 4, Type: "page-loaded"},
	}
	flags := netLogFlags{method: "GET"}
	filtered := filterNetEvents(events, flags)
	if len(filtered) != 1 {
		t.Fatalf("expected 1 event, got %d", len(filtered))
	}
	if filtered[0].Seq != 1 {
		t.Fatalf("wrong event: seq=%d", filtered[0].Seq)
	}
}

func TestFilterNetEvents_ByPath(t *testing.T) {
	events := []NetEvent{
		{Seq: 1, Type: "request", URL: "https://example.com/api/users"},
		{Seq: 2, Type: "request", URL: "https://example.com/static/logo.png"},
	}
	flags := netLogFlags{pathPrefix: "/api"}
	filtered := filterNetEvents(events, flags)
	if len(filtered) != 1 || filtered[0].Seq != 1 {
		t.Fatalf("unexpected result: %+v", filtered)
	}
}

func TestFilterNetEvents_ByDomain(t *testing.T) {
	events := []NetEvent{
		{Seq: 1, Type: "request", URL: "https://api.example.com/data"},
		{Seq: 2, Type: "request", URL: "https://cdn.other.com/file.js"},
		{Seq: 3, Type: "request", URL: "https://sub.example.com/test"},
	}
	flags := netLogFlags{domain: "*.example.com"}
	filtered := filterNetEvents(events, flags)
	if len(filtered) != 2 {
		t.Fatalf("expected 2, got %d", len(filtered))
	}
}

func TestFilterNetEvents_SinceNav(t *testing.T) {
	events := []NetEvent{
		{Seq: 1, Type: "request", URL: "https://old.com"},
		{Seq: 2, Type: "page-navigated", URL: "https://new.com"},
		{Seq: 3, Type: "request", URL: "https://new.com/api"},
	}
	flags := netLogFlags{since: "nav"}
	filtered := filterNetEvents(events, flags)
	if len(filtered) != 2 {
		t.Fatalf("expected 2 (nav event + request after), got %d", len(filtered))
	}
	if filtered[0].Seq != 2 {
		t.Fatalf("first should be nav event, got seq=%d", filtered[0].Seq)
	}
}

func TestFilterNetEvents_SinceNavNotFound(t *testing.T) {
	events := []NetEvent{
		{Seq: 1, Type: "request", URL: "https://example.com"},
	}
	flags := netLogFlags{since: "nav"}
	filtered := filterNetEvents(events, flags)
	// When anchor not found, show all events
	if len(filtered) != 1 {
		t.Fatalf("expected all events when anchor not found, got %d", len(filtered))
	}
}

func TestFilterNetEvents_ByID(t *testing.T) {
	events := []NetEvent{
		{Seq: 1, Type: "request", ID: "R1", URL: "https://example.com/a"},
		{Seq: 2, Type: "request", ID: "R2", URL: "https://example.com/b"},
		{Seq: 3, Type: "response", ID: "R1", Status: 200},
		{Seq: 4, Type: "response", ID: "R2", Status: 404},
	}
	flags := netLogFlags{id: "R1"}
	filtered := filterNetEvents(events, flags)
	if len(filtered) != 2 {
		t.Fatalf("expected 2, got %d", len(filtered))
	}
}

func TestFilterNetEvents_ByType(t *testing.T) {
	events := []NetEvent{
		{Seq: 1, Type: "request"},
		{Seq: 2, Type: "response"},
		{Seq: 3, Type: "user-click"},
		{Seq: 4, Type: "page-loaded"},
	}
	flags := netLogFlags{eventType: "request,response"}
	filtered := filterNetEvents(events, flags)
	if len(filtered) != 2 {
		t.Fatalf("expected 2, got %d", len(filtered))
	}
}

func TestStripNetEvent_DefaultOmitsHeadersAndTS(t *testing.T) {
	e := NetEvent{
		Seq:     1,
		TS:      "2026-03-16T14:30:01Z",
		Type:    "request",
		Headers: map[string]string{"accept": "text/html"},
		Method:  "GET",
		URL:     "https://example.com",
	}
	stripped := stripNetEvent(e, false, false)
	if stripped.TS != "" {
		t.Fatal("TS should be stripped by default")
	}
	if stripped.Headers != nil {
		t.Fatal("Headers should be stripped by default")
	}
	if stripped.Method != "GET" {
		t.Fatal("Method should be preserved")
	}
}

func TestStripNetEvent_IncludeHeadersAndTS(t *testing.T) {
	e := NetEvent{
		Seq:     1,
		TS:      "2026-03-16T14:30:01Z",
		Type:    "request",
		Headers: map[string]string{"accept": "text/html"},
	}
	stripped := stripNetEvent(e, true, true)
	if stripped.TS == "" {
		t.Fatal("TS should be preserved with --timestamps")
	}
	if stripped.Headers == nil {
		t.Fatal("Headers should be preserved with --headers")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestParseNetLogFlags|TestFilterNetEvents|TestStripNetEvent' -v`
Expected: FAIL

- [ ] **Step 3: Implement net-log parsing, filtering, and stripping**

Create `net_commands.go`:

```go
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type netLogFlags struct {
	since      string // "nav", "render", "interaction", or ISO timestamp
	id         string
	method     string // comma-separated
	pathPrefix string
	domain     string // comma-separated, supports *.example.com
	eventType  string // comma-separated
	headers    bool
	timestamps bool
	tail       int
	follow     bool
}

func parseNetLogFlags(args []string) netLogFlags {
	var f netLogFlags
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--since":
			if i+1 < len(args) {
				i++
				f.since = args[i]
			}
		case "--id":
			if i+1 < len(args) {
				i++
				f.id = args[i]
			}
		case "--method":
			if i+1 < len(args) {
				i++
				f.method = args[i]
			}
		case "--path":
			if i+1 < len(args) {
				i++
				f.pathPrefix = args[i]
			}
		case "--domain":
			if i+1 < len(args) {
				i++
				f.domain = args[i]
			}
		case "--type":
			if i+1 < len(args) {
				i++
				f.eventType = args[i]
			}
		case "--headers":
			f.headers = true
		case "--timestamps":
			f.timestamps = true
		case "--tail":
			if i+1 < len(args) {
				i++
				f.tail, _ = strconv.Atoi(args[i])
			}
		case "--follow":
			f.follow = true
		}
	}
	return f
}

func filterNetEvents(events []NetEvent, f netLogFlags) []NetEvent {
	// Apply --since: find the anchor event and discard everything before it
	if f.since != "" {
		anchorIdx := -1
		switch f.since {
		case "nav", "render", "interaction":
			// For named anchors, find the LAST occurrence (scan backwards)
			for i := len(events) - 1; i >= 0; i-- {
				match := false
				switch f.since {
				case "nav":
					match = events[i].Type == "page-navigated"
				case "render":
					match = events[i].Type == "page-loaded"
				case "interaction":
					match = events[i].Type == "interaction" || strings.HasPrefix(events[i].Type, "user-")
				}
				if match {
					anchorIdx = i
					break
				}
			}
		default:
			// Timestamp (or prefix): find the FIRST event at or after the timestamp (scan forward)
			for i := 0; i < len(events); i++ {
				if strings.HasPrefix(events[i].TS, f.since) || events[i].TS >= f.since {
					anchorIdx = i
					break
				}
			}
		}
		if anchorIdx >= 0 {
			events = events[anchorIdx:]
		}
		// If anchor not found, show all events (no filtering)
	}

	// Apply other filters
	var result []NetEvent
	methods := splitCSV(f.method)
	types := splitCSV(f.eventType)
	domains := splitCSV(f.domain)

	for _, e := range events {
		if f.id != "" && e.ID != f.id {
			continue
		}
		if len(methods) > 0 && e.Method != "" {
			if !containsCI(methods, e.Method) {
				continue
			}
		}
		if len(methods) > 0 && e.Method == "" {
			// Non-request events don't have methods; skip them when filtering by method
			continue
		}
		if f.pathPrefix != "" && e.URL != "" {
			parsed, err := url.Parse(e.URL)
			if err != nil || !strings.HasPrefix(parsed.Path, f.pathPrefix) {
				continue
			}
		}
		if f.pathPrefix != "" && e.URL == "" {
			continue
		}
		if len(domains) > 0 && e.URL != "" {
			parsed, err := url.Parse(e.URL)
			if err != nil || !matchDomain(parsed.Hostname(), domains) {
				continue
			}
		}
		if len(domains) > 0 && e.URL == "" {
			continue
		}
		if len(types) > 0 {
			if !contains(types, e.Type) {
				continue
			}
		}
		result = append(result, e)
	}

	// Apply --tail
	if f.tail > 0 && len(result) > f.tail {
		result = result[len(result)-f.tail:]
	}

	return result
}

func stripNetEvent(e NetEvent, includeHeaders, includeTimestamps bool) NetEvent {
	if !includeTimestamps {
		e.TS = ""
	}
	if !includeHeaders {
		e.Headers = nil
	}
	return e
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func containsCI(list []string, s string) bool {
	s = strings.ToUpper(s)
	for _, v := range list {
		if strings.ToUpper(v) == s {
			return true
		}
	}
	return false
}

func matchDomain(hostname string, patterns []string) bool {
	for _, p := range patterns {
		if strings.HasPrefix(p, "*.") {
			suffix := p[1:] // ".example.com"
			if strings.HasSuffix(hostname, suffix) {
				return true
			}
		} else if hostname == p {
			return true
		}
	}
	return false
}

// readJSONLFile reads all events from an index.jsonl file.
func readJSONLFile(path string) ([]NetEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []NetEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB line buffer
	for scanner.Scan() {
		var e NetEvent
		if err := json.Unmarshal(scanner.Bytes(), &e); err == nil {
			events = append(events, e)
		}
	}
	return events, scanner.Err()
}

// resolveSessionDataDir resolves the session ID and returns the data dir and state.
// Unlike withPage, this does not connect to the browser.
func resolveSessionDataDir() (string, *State) {
	sid := resolveSessionID(activeSessionID, "")
	if sid == "" {
		fatal("session ID required; pass --session <id>")
	}
	activeSessionID = sid
	dataDir, err := registryLookup(registryPath(), sid)
	if err != nil {
		fatal("session %q not found", sid)
	}
	activeStateDir = dataDir
	s, err := loadState()
	if err != nil {
		fatal("no state for session %q: %v", sid, err)
	}
	return dataDir, s
}

// cmdNetLog implements the "net-log" command.
func cmdNetLog(args []string) {
	dataDir, s := resolveSessionDataDir()
	flags := parseNetLogFlags(args)

	si, ok := s.Sessions[activeSessionID]
	if !ok {
		fatal("session %q not found in state", activeSessionID)
	}
	if si.NoCapture {
		fatal("capture is disabled for session %q (started with --no-capture)", activeSessionID)
	}

	jsonlPath := filepath.Join(dataDir, "net", activeSessionID, "index.jsonl")

	if flags.follow {
		tailFollow(jsonlPath, flags)
		return
	}

	events, err := readJSONLFile(jsonlPath)
	if err != nil {
		if os.IsNotExist(err) {
			return // no events yet
		}
		fatal("read net log: %v", err)
	}

	filtered := filterNetEvents(events, flags)
	for _, e := range filtered {
		e = stripNetEvent(e, flags.headers, flags.timestamps)
		data, _ := json.Marshal(e)
		fmt.Println(string(data))
	}
}

// tailFollow implements --follow by polling the JSONL file for new lines.
func tailFollow(path string, flags netLogFlags) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Wait for file to appear
			for {
				time.Sleep(500 * time.Millisecond)
				f, err = os.Open(path)
				if err == nil {
					break
				}
			}
		} else {
			fatal("open: %v", err)
		}
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for {
		for scanner.Scan() {
			var e NetEvent
			if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
				continue
			}
			// Apply filters inline
			filtered := filterNetEvents([]NetEvent{e}, flags)
			for _, fe := range filtered {
				fe = stripNetEvent(fe, flags.headers, flags.timestamps)
				data, _ := json.Marshal(fe)
				fmt.Println(string(data))
			}
		}
		time.Sleep(200 * time.Millisecond)
		// Reset scanner to pick up new content
		scanner = bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	}
}

// cmdNetBody implements the "net-body" command.
func cmdNetBody(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney net-body <seq>")
	}
	seq, err := strconv.Atoi(args[0])
	if err != nil {
		fatal("invalid sequence number: %s", args[0])
	}

	dataDir, _ := resolveSessionDataDir()
	jsonlPath := filepath.Join(dataDir, "net", activeSessionID, "index.jsonl")
	sessionDir := filepath.Join(dataDir, "net", activeSessionID)

	events, err := readJSONLFile(jsonlPath)
	if err != nil {
		fatal("read net log: %v", err)
	}

	for _, e := range events {
		if e.Seq == seq && e.BodyFile != "" {
			bodyPath := filepath.Join(sessionDir, e.BodyFile)
			content, err := os.ReadFile(bodyPath)
			if err != nil {
				fatal("read body: %v", err)
			}
			os.Stdout.Write(content)
			return
		}
	}
	fatal("no body file for seq %d", seq)
}

// cmdNetClear implements the "net-clear" command.
func cmdNetClear(args []string) {
	dataDir, _ := resolveSessionDataDir()
	sockPath := filepath.Join(dataDir, "net", "monitor.sock")

	// Try to send clear via IPC (monitor will handle it)
	err := ipcSend(sockPath, IPCMessage{Session: activeSessionID, Type: "clear"})
	if err != nil {
		// Monitor not running; do it directly
		sessionDir := filepath.Join(dataDir, "net", activeSessionID)
		os.Remove(filepath.Join(sessionDir, "index.jsonl"))
		os.RemoveAll(filepath.Join(sessionDir, "bodies"))
		// Create fresh empty JSONL
		os.MkdirAll(sessionDir, 0755)
		os.WriteFile(filepath.Join(sessionDir, "index.jsonl"), nil, 0644)
	}
}
```

Note: Add `"time"` to the import block in `net_commands.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /exports/projectpool/dev/rodney && go test -run 'TestParseNetLogFlags|TestFilterNetEvents|TestStripNetEvent' -v`
Expected: PASS

- [ ] **Step 5: Verify full compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`
Expected: Clean build

- [ ] **Step 6: Commit**

```bash
git add net_commands.go netmonitor_test.go
git commit -m "feat: add net-log, net-body, net-clear commands with filtering"
```

---

## Chunk 3: Monitor Process and User Interaction Capture

### Task 7: Implement the _netmonitor background process

**Files:**
- Modify: `netmonitor.go` (add cmdNetMonitor, CDP event handlers)

This is the largest task. The `_netmonitor` process connects to Chrome via CDP, enables Network/Page events on all targets, and writes events to per-session JSONL files.

- [ ] **Step 1: Implement cmdNetMonitor entry point**

Add to `netmonitor.go` (imports already included in Task 3's initial file):

```go
// cmdNetMonitor is the long-running background process: rodney _netmonitor <data-dir>
func cmdNetMonitor(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney _netmonitor <data-dir>")
	}
	dataDir := args[0]

	// Parse optional args
	var captureTypes []string
	maxBodySize := 5 * 1024 * 1024 // 5MB default
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--capture-types":
			if i+1 < len(args) {
				i++
				captureTypes = splitCSV(args[i])
			}
		case "--capture-max-body":
			if i+1 < len(args) {
				i++
				if v, err := strconv.Atoi(args[i]); err == nil {
					maxBodySize = v
				}
			}
		}
	}

	// Read state to get debug URL
	activeStateDir = dataDir
	s, err := loadState()
	if err != nil {
		fatal("load state: %v", err)
	}

	browser, err := connectBrowser(s)
	if err != nil {
		fatal("connect: %v", err)
	}

	mon := &netMonitor{
		dataDir:      dataDir,
		browser:      browser,
		state:        s,
		writers:      make(map[string]*jsonlWriter),
		captureTypes: captureTypes,
		maxBodySize:  maxBodySize,
		attribution:  make(map[string]*attributionCtx),
		responseMeta: make(map[string]responseMeta),
	}

	// Set up IPC server
	netDir := filepath.Join(dataDir, "net")
	os.MkdirAll(netDir, 0755)
	sockPath := filepath.Join(netDir, "monitor.sock")
	ipcSrv, err := newIPCServer(sockPath, mon.handleIPC)
	if err != nil {
		fatal("ipc listen: %v", err)
	}
	defer ipcSrv.close()
	defer os.Remove(sockPath)

	// Enable auto-attach to all targets
	mon.setupAutoAttach()

	// Handle SIGTERM for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	// Graceful shutdown
	mon.closeAll()
}

type attributionCtx struct {
	cmd      string
	args     []string
	selector string
	ts       time.Time
}

type netMonitor struct {
	mu           sync.Mutex
	dataDir      string
	browser      *rod.Browser
	state        *State
	writers      map[string]*jsonlWriter // sessionID -> writer
	captureTypes []string
	maxBodySize  int
	attribution  map[string]*attributionCtx // sessionID -> pending attribution
	responseMeta map[string]responseMeta    // requestID -> MIME and headers from responseReceived
}

type responseMeta struct {
	mime    string
	headers map[string]string
	status  int
	size    int
}

func (m *netMonitor) handleIPC(msg IPCMessage) {
	switch msg.Type {
	case "interaction":
		m.mu.Lock()
		m.attribution[msg.Session] = &attributionCtx{
			cmd:  msg.Cmd,
			args: msg.Args,
			ts:   time.Now(),
		}
		if len(msg.Args) > 0 {
			m.attribution[msg.Session].selector = msg.Args[0]
		}
		m.mu.Unlock()

		w := m.writerForSession(msg.Session)
		if w != nil {
			w.append(NetEvent{
				Type:   "interaction",
				Cmd:    msg.Cmd,
				Args:   msg.Args,
				Source: "rodney",
			})
		}
	case "clear":
		w := m.writerForSession(msg.Session)
		if w != nil {
			w.clear()
		}
	}
}

func (m *netMonitor) writerForSession(sessionID string) *jsonlWriter {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.writers[sessionID]; ok {
		return w
	}
	sessionDir := filepath.Join(m.dataDir, "net", sessionID)
	w, err := newJSONLWriter(sessionDir)
	if err != nil {
		return nil
	}
	m.writers[sessionID] = w
	return w
}

func (m *netMonitor) sessionIDForTarget(targetID string) string {
	// Re-read state to find the session that owns this target
	activeStateDir = m.dataDir
	s, err := loadState()
	if err != nil {
		return ""
	}
	m.state = s
	for sid, si := range s.Sessions {
		if si.TargetID == targetID && !si.NoCapture {
			return sid
		}
	}
	return ""
}

func (m *netMonitor) sessionIDForTargetWithRetry(targetID string) string {
	for attempt := 0; attempt < 3; attempt++ {
		if sid := m.sessionIDForTarget(targetID); sid != "" {
			return sid
		}
		time.Sleep(500 * time.Millisecond)
	}
	return ""
}

func (m *netMonitor) setupAutoAttach() {
	// Attach to existing pages
	pages, _ := m.browser.Pages()
	for _, p := range pages {
		tid := string(p.TargetID)
		sid := m.sessionIDForTargetWithRetry(tid)
		if sid != "" {
			go m.monitorPage(p, sid)
		}
	}

	// Listen for new targets
	go m.browser.EachEvent(func(e *proto.TargetTargetCreated) {
		if e.TargetInfo.Type != "page" {
			return
		}
		tid := string(e.TargetInfo.TargetID)
		sid := m.sessionIDForTargetWithRetry(tid)
		if sid == "" {
			return
		}
		p, err := m.browser.PageFromTarget(e.TargetInfo.TargetID)
		if err != nil {
			return
		}
		go m.monitorPage(p, sid)
	}, func(e *proto.TargetTargetDestroyed) {
		// Clean up writer handles when tab closes
		tid := string(e.TargetID)
		m.mu.Lock()
		for sid, si := range m.state.Sessions {
			if si.TargetID == tid {
				if w, ok := m.writers[sid]; ok {
					w.close()
					delete(m.writers, sid)
				}
				break
			}
		}
		m.mu.Unlock()
	})()
}

func (m *netMonitor) monitorPage(page *rod.Page, sessionID string) {
	// Enable Network domain
	proto.NetworkEnable{}.Call(page)

	// Enable Page domain
	proto.PageEnable{}.Call(page)

	// Set up user interaction capture
	m.setupUserInteractionCapture(page, sessionID)

	w := m.writerForSession(sessionID)
	if w == nil {
		return
	}

	// Listen for events on this page
	page.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			w.append(NetEvent{
				Type:      "request",
				ID:        string(e.RequestID),
				Method:    e.Request.Method,
				URL:       e.Request.URL,
				Headers:   flattenHeaders(e.Request.Headers),
				Initiator: string(e.Initiator.Type),
			})
		},
		func(e *proto.NetworkResponseReceived) {
			// Buffer MIME type for use when body is captured in loadingFinished
			rid := string(e.RequestID)
			m.mu.Lock()
			m.responseMeta[rid] = responseMeta{
				mime:    e.Response.MIMEType,
				headers: flattenHeaders(e.Response.Headers),
				status:  e.Response.Status,
				size:    int(e.Response.EncodedDataLength),
			}
			m.mu.Unlock()
		},
		func(e *proto.NetworkLoadingFinished) {
			// Eagerly capture response body and write the combined response event
			m.captureResponseBody(page, sessionID, string(e.RequestID), w)
		},
		func(e *proto.NetworkWebSocketCreated) {
			w.append(NetEvent{
				Type: "ws-open",
				ID:   string(e.RequestID),
				URL:  e.URL,
			})
		},
		func(e *proto.NetworkWebSocketFrameReceived) {
			m.writeWSFrame(w, string(e.RequestID), "recv", e.Response)
		},
		func(e *proto.NetworkWebSocketFrameSent) {
			m.writeWSFrame(w, string(e.RequestID), "send", e.Response)
		},
		func(e *proto.NetworkWebSocketClosed) {
			w.append(NetEvent{Type: "ws-close", ID: string(e.RequestID)})
		},
		func(e *proto.PageFrameRequestedNavigation) {
			w.append(NetEvent{
				Type:   "page-navigation-requested",
				URL:    e.URL,
				Reason: string(e.Reason),
			})
		},
		func(e *proto.PageFrameNavigated) {
			w.append(NetEvent{
				Type: "page-navigated",
				URL:  e.Frame.URL,
			})
		},
		func(e *proto.PageNavigatedWithinDocument) {
			w.append(NetEvent{Type: "page-spa-navigated", URL: e.URL})
		},
		func(e *proto.PageDomContentEventFired) {
			w.append(NetEvent{Type: "page-dom-ready"})
		},
		func(e *proto.PageLoadEventFired) {
			w.append(NetEvent{Type: "page-loaded"})
		},
		func(e *proto.PageFrameStartedLoading) {
			w.append(NetEvent{Type: "page-loading"})
		},
	)()
}

func (m *netMonitor) captureResponseBody(page *rod.Page, sessionID, requestID string, w *jsonlWriter) {
	// Look up buffered response metadata
	m.mu.Lock()
	meta, hasMeta := m.responseMeta[requestID]
	delete(m.responseMeta, requestID) // clean up
	m.mu.Unlock()

	if !hasMeta {
		return
	}

	// Build the response event (deferred until body is available)
	event := NetEvent{
		Type:    "response",
		ID:      requestID,
		Status:  meta.status,
		MIME:    meta.mime,
		Headers: meta.headers,
		Size:    meta.size,
	}

	// Try to capture body
	body, err := proto.NetworkGetResponseBody{RequestID: proto.NetworkRequestID(requestID)}.Call(page)
	if err != nil {
		event.BodyError = err.Error()
		w.append(event)
		return
	}

	content := []byte(body.Body)
	if body.Base64Encoded {
		decoded, decErr := base64.StdEncoding.DecodeString(body.Body)
		if decErr == nil {
			content = decoded
		}
	}

	// Check MIME eligibility
	if isCaptureEligible(meta.mime, m.captureTypes) {
		ext := bodyFileExt(meta.mime)
		// Reserve the seq that will be used for this event
		nextSeq := w.seq + 1

		originalSize := len(content)
		truncated := false
		if m.maxBodySize > 0 && len(content) > m.maxBodySize {
			content = content[:m.maxBodySize]
			truncated = true
		}

		bodyFile, writeErr := w.writeBody(nextSeq, "resp", ext, content)
		if writeErr == nil {
			event.BodyFile = bodyFile
			if truncated {
				event.Truncated = true
				event.OriginalSize = originalSize
			}
		}
	}

	w.append(event)
}

func (m *netMonitor) writeWSFrame(w *jsonlWriter, requestID, dir string, response *proto.NetworkWebSocketFrame) {
	if response == nil {
		return
	}
	size := len(response.PayloadData)
	e := NetEvent{
		Type:   "ws-frame",
		ID:     requestID,
		Dir:    dir,
		Opcode: strconv.Itoa(int(response.Opcode)),
		Size:   size,
	}

	// Save frame data as body if text-based (opcode 1 = text frame)
	if int(response.Opcode) == 1 {
		// Use append to atomically get the seq, then write body with that seq
		w.mu.Lock()
		nextSeq := w.seq + 1
		w.mu.Unlock()
		bodyFile, err := w.writeBody(nextSeq, dir, ".txt", []byte(response.PayloadData))
		if err == nil {
			e.BodyFile = bodyFile
		}
	}
	w.append(e)
}

func (m *netMonitor) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.writers {
		w.close()
	}
}

// flattenHeaders converts proto headers (json.RawMessage) to a simple map.
func flattenHeaders(raw proto.NetworkHeaders) map[string]string {
	if raw == nil {
		return nil
	}
	var m map[string]string
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	json.Unmarshal(data, &m)
	return m
}
```

- [ ] **Step 2: Verify compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`
Expected: Clean build (may need import adjustments)

- [ ] **Step 3: Commit**

```bash
git add netmonitor.go
git commit -m "feat: implement _netmonitor background process with CDP event handling"
```

### Task 8: Implement user interaction capture via isolated world

**Files:**
- Modify: `netmonitor.go` (add setupUserInteractionCapture, the JS injection, and Runtime.bindingCalled handler)

- [ ] **Step 1: Add the interaction capture JS and injection code**

Add to `netmonitor.go`:

```go
// userInteractionJS is the script injected into the __rodney_monitor isolated world
// to capture user interactions and send them back via Runtime.addBinding.
const userInteractionJS = `
(function() {
  let inputTimer = null;
  let lastInputValue = '';
  let lastInputSelector = '';

  function buildSelector(el) {
    if (!el || el === document.body || el === document.documentElement) return 'body';
    if (el.id) return '#' + CSS.escape(el.id);
    if (el.getAttribute('data-testid')) return '[data-testid="' + el.getAttribute('data-testid') + '"]';
    if (el.getAttribute('aria-label')) return '[aria-label="' + el.getAttribute('aria-label') + '"]';
    let tag = el.tagName.toLowerCase();
    let cls = el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\s+/).join('.') : '';
    let selector = tag + cls;
    let parent = el.parentElement;
    if (parent) {
      let siblings = Array.from(parent.children).filter(c => c.tagName === el.tagName);
      if (siblings.length > 1) {
        let idx = siblings.indexOf(el) + 1;
        selector += ':nth-child(' + idx + ')';
      }
    }
    return selector;
  }

  function send(data) {
    try { __rodneyEvent(JSON.stringify(data)); } catch(e) {}
  }

  document.addEventListener('click', function(e) {
    send({
      type: 'user-click',
      selector: buildSelector(e.target),
      x: e.clientX,
      y: e.clientY,
      textContent: (e.target.textContent || '').slice(0, 50)
    });
  }, true);

  document.addEventListener('input', function(e) {
    let sel = buildSelector(e.target);
    lastInputSelector = sel;
    lastInputValue = (e.target.value || '').slice(0, 200);
    clearTimeout(inputTimer);
    inputTimer = setTimeout(function() {
      send({type: 'user-input', selector: lastInputSelector, value: lastInputValue});
    }, 100);
  }, true);

  document.addEventListener('change', function(e) {
    send({
      type: 'user-change',
      selector: buildSelector(e.target),
      value: (e.target.value || '').slice(0, 200)
    });
  }, true);

  document.addEventListener('submit', function(e) {
    send({
      type: 'user-submit',
      selector: buildSelector(e.target),
      action: e.target.action || ''
    });
  }, true);

  document.addEventListener('keydown', function(e) {
    if (['Enter', 'Tab', 'Escape'].includes(e.key)) {
      send({type: 'user-keydown', selector: buildSelector(e.target), key: e.key});
    }
  }, true);

  document.addEventListener('paste', function(e) {
    let val = '';
    if (e.clipboardData) val = (e.clipboardData.getData('text') || '').slice(0, 200);
    send({type: 'user-paste', selector: buildSelector(e.target), value: val});
  }, true);
})();
`

func (m *netMonitor) setupUserInteractionCapture(page *rod.Page, sessionID string) {
	// Inject the script into the __rodney_monitor isolated world on every new document
	proto.PageAddScriptToEvaluateOnNewDocument{
		Source:    userInteractionJS,
		WorldName: "__rodney_monitor",
	}.Call(page)

	// Register the binding in the isolated world
	proto.RuntimeAddBinding{
		Name:                 "__rodneyEvent",
		ExecutionContextName: "__rodney_monitor",
	}.Call(page)

	// Listen for binding calls
	go page.EachEvent(func(e *proto.RuntimeBindingCalled) {
		if e.Name != "__rodneyEvent" {
			return
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(e.Payload), &data); err != nil {
			return
		}
		eventType, _ := data["type"].(string)
		if eventType == "" {
			return
		}

		w := m.writerForSession(sessionID)
		if w == nil {
			return
		}

		event := NetEvent{Type: eventType}
		if v, ok := data["selector"].(string); ok {
			event.Selector = v
		}
		if v, ok := data["x"].(float64); ok {
			event.X = int(v)
		}
		if v, ok := data["y"].(float64); ok {
			event.Y = int(v)
		}
		if v, ok := data["textContent"].(string); ok {
			event.TextContent = v
		}
		if v, ok := data["value"].(string); ok {
			event.Value = v
		}
		if v, ok := data["action"].(string); ok {
			event.Action = v
		}
		if v, ok := data["key"].(string); ok {
			event.Key = v
		}

		// Check attribution: was this triggered by a rodney command?
		m.mu.Lock()
		attr := m.attribution[sessionID]
		if attr != nil && time.Since(attr.ts) < 2*time.Second {
			if attr.selector == "" || attr.selector == event.Selector {
				event.Source = "rodney"
			}
		}
		m.mu.Unlock()

		w.append(event)
	})()

	// Also execute the script immediately for the current document
	// (addScriptToEvaluateOnNewDocument only runs on future navigations)
	result, err := proto.PageCreateIsolatedWorld{
		FrameID:   page.FrameID,
		WorldName: "__rodney_monitor",
	}.Call(page)
	if err == nil {
		proto.RuntimeEvaluate{
			Expression:            userInteractionJS,
			ContextID:             result.ExecutionContextID,
			ReturnByValue:         true,
		}.Call(page)
	}
}
```

- [ ] **Step 2: Verify compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`
Expected: Clean build

- [ ] **Step 3: Commit**

```bash
git add netmonitor.go
git commit -m "feat: add user interaction capture via isolated world injection"
```

---

## Chunk 4: Integration with Session Lifecycle

### Task 9: Launch monitor in cmdNewSession

**Files:**
- Modify: `main.go:1160-1303` (cmdNewSession)

- [ ] **Step 1: Update session info creation and add monitor launch logic**

First, in `cmdNewSession`, update where `s.Sessions[sessionID]` is set (around line 1290) to include `NoCapture`:

```go
	s.Sessions[sessionID] = SessionInfo{
		TargetID:       string(page.TargetID),
		ViewportWidth:  vw,
		ViewportHeight: vh,
		NoCapture:      flags.noCapture,
	}
```

Then, after the session is persisted to state.json and registry (around line 1300), add monitor launch logic:

```go
	// Launch network monitor if capture is enabled and monitor isn't running
	if !flags.noCapture {
		if s.MonitorPID == 0 || !isProcessAlive(s.MonitorPID) {
			monPID := launchNetMonitor(dataDir)
			s.MonitorPID = monPID
			if err := saveState(s); err != nil {
				fatal("save state after monitor launch: %v", err)
			}
		}
	}
```

Also add the helper functions (in `netmonitor.go`):

```go
// isProcessAlive checks if a PID is still running.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// launchNetMonitor starts the _netmonitor background process.
func launchNetMonitor(dataDir string) int {
	exe, _ := os.Executable()
	args := []string{"_netmonitor", dataDir}
	// TODO: pass --capture-types and --capture-max-body flags when added to parseStartFlags
	cmd := exec.Command(exe, args...)
	setSysProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to start network monitor: %v\n", err)
		return 0
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	time.Sleep(200 * time.Millisecond) // brief pause for socket to bind
	return pid
}
```

- [ ] **Step 2: (removed -- consolidated into Step 1)**

- [ ] **Step 3: Verify compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`
Expected: Clean build

- [ ] **Step 4: Commit**

```bash
git add main.go netmonitor.go
git commit -m "feat: launch network monitor from newsession"
```

### Task 10: Kill monitor in cmdEndSession and clean up net data

**Files:**
- Modify: `main.go:1305-1417` (cmdEndSession)

- [ ] **Step 1: Add monitor kill and net cleanup**

In `cmdEndSession`, add net directory cleanup for the ended session (after registry removal, around line 1372). Add monitor kill alongside proxy kill (around line 1401):

After line 1372 (`registryRemove`), add:

```go
	// Clean up network capture data for this session
	netSessionDir := filepath.Join(dataDir, "net", sid)
	os.RemoveAll(netSessionDir)
```

In the "last session" shutdown block (around line 1401), after the proxy kill, add:

```go
		// Kill network monitor
		if s.MonitorPID > 0 {
			if proc, err := os.FindProcess(s.MonitorPID); err == nil {
				proc.Signal(syscall.SIGTERM)
			}
		}

		// Clean up net directory
		os.RemoveAll(filepath.Join(dataDir, "net"))
```

- [ ] **Step 2: Verify compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`

- [ ] **Step 3: Commit**

```bash
git add main.go
git commit -m "feat: kill monitor and clean up net data in endsession"
```

### Task 10b: Add net directory cleanup to stale session handling in cmdSessions

**Files:**
- Modify: `main.go` (cmdSessions function, in the stale session pruning blocks)

- [ ] **Step 1: Add net cleanup when pruning stale sessions**

In `cmdSessions`, there are two places where stale sessions are pruned from the registry (search for `registryRemove` calls inside `cmdSessions`). After each `registryRemove` call that prunes a stale session, add:

```go
	// Clean up network capture data for pruned session
	os.RemoveAll(filepath.Join(dir, "net", sid))
```

Where `dir` is the data directory and `sid` is the session being pruned.

- [ ] **Step 2: Commit**

```bash
git add main.go
git commit -m "feat: clean up net data when pruning stale sessions"
```

### Task 11: Add monitor health check in withPage

**Files:**
- Modify: `main.go:792-821` (withPage function)

- [ ] **Step 1: Add monitor health check after loading state**

In `withPage`, after `loadState()` succeeds (around line 808), add a check:

```go
	// Check network monitor health and re-launch if needed
	if s.MonitorPID > 0 && !isProcessAlive(s.MonitorPID) {
		// Monitor died; check if any session has capture enabled
		hasCapture := false
		for _, si := range s.Sessions {
			if !si.NoCapture {
				hasCapture = true
				break
			}
		}
		if hasCapture {
			withFileLock(stateLockPath(), func() error {
				// Re-read state under lock to avoid races
				fresh, err := loadState()
				if err != nil {
					return err
				}
				if fresh.MonitorPID > 0 && isProcessAlive(fresh.MonitorPID) {
					return nil // another command already re-launched it
				}
				pid := launchNetMonitor(dataDir)
				fresh.MonitorPID = pid
				// Write directly with atomicWriteJSON since we already hold the lock
				return atomicWriteJSON(statePath(), fresh)
			})
			// Re-load state to pick up updated MonitorPID
			s, err = loadState()
			if err != nil {
				fatal("reload state: %v", err)
			}
		}
	}
```

- [ ] **Step 2: Verify compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`

- [ ] **Step 3: Commit**

```bash
git add main.go
git commit -m "feat: re-launch network monitor on health check failure in withPage"
```

### Task 12: Send interaction markers from CLI commands

**Files:**
- Modify: `main.go` (add helper function and calls to interaction commands)

- [ ] **Step 1: Add sendInteractionMarker helper**

Add to `main.go` (near the `withPage` function):

```go
// sendInteractionMarker sends a pre-action marker to the network monitor.
func sendInteractionMarker(cmd string, args []string) {
	if activeSessionID == "" {
		return
	}
	dataDir, err := registryLookup(registryPath(), activeSessionID)
	if err != nil {
		return
	}
	sockPath := filepath.Join(dataDir, "net", "monitor.sock")
	ipcSend(sockPath, IPCMessage{
		Session: activeSessionID,
		Type:    "interaction",
		Cmd:     cmd,
		Args:    args,
	})
	// Errors are silently ignored (monitor may not be running)
}
```

- [ ] **Step 2: Add marker calls to interaction commands**

Add `sendInteractionMarker` calls at the beginning of each interaction command, right after `withPage()` resolves the session. The commands that need markers are: `cmdOpen`, `cmdClick`, `cmdInput`, `cmdClear`, `cmdSelect`, `cmdSubmit`, `cmdHover`, `cmdFocus`, `cmdJS`, `cmdBack`, `cmdForward`, `cmdReload`, `cmdFile`.

For example, in `cmdOpen` (line 1610), after `withPage()` on line 1621:

```go
	sendInteractionMarker("open", []string{url})
```

In `cmdClick` (find with grep), after `withPage()`:

```go
	sendInteractionMarker("click", args)
```

Apply the same pattern to all listed commands. The marker call goes immediately after `withPage()` and before any browser action.

- [ ] **Step 3: Verify compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat: send interaction markers from CLI commands to network monitor"
```

---

## Chunk 5: Dispatch, Help Text, and Final Wiring

### Task 13: Add dispatch entries for new commands

**Files:**
- Modify: `main.go:453-556` (dispatch switch)

- [ ] **Step 1: Add cases for new commands**

In the dispatch switch, add after the `case "_proxy"` line (line 454):

```go
	case "_netmonitor":
		cmdNetMonitor(args) // hidden: runs the network monitor
```

Add before the removed-commands block (before line 532):

```go
	case "net-log":
		cmdNetLog(args)
	case "net-body":
		cmdNetBody(args)
	case "net-clear":
		cmdNetClear(args)
```

- [ ] **Step 2: Verify compilation**

Run: `cd /exports/projectpool/dev/rodney && go build ./...`

- [ ] **Step 3: Commit**

```bash
git add main.go
git commit -m "feat: wire up net-log, net-body, net-clear, and _netmonitor commands"
```

### Task 14: Update help.txt

**Files:**
- Modify: `help.txt`

- [ ] **Step 1: Add network section to help.txt**

Add after the Accessibility section (after line 55) and before the newsession flags section:

```
Network:
  rodney net-log [flags]                  Show network event log
  rodney net-body <seq>                   Print request/response body by seq number
  rodney net-clear                        Clear captured network data
```

Add `--no-capture` to the newsession Per-session flags (after `--viewport` line 65):

```
    --no-capture                     Disable network monitoring for this session
```

Add net-log flags section (after the newsession flags section, before Options):

```
net-log flags:
  --since nav|render|interaction|<ts>  Filter events since a point in time
  --id <request-id>                    Filter to a specific request stream
  --method <method>                    Filter by HTTP method (comma-separated)
  --path <prefix>                      Filter by URL path prefix
  --domain <domain>                    Filter by domain (supports *.example.com)
  --type <event-type>                  Filter by event type (comma-separated)
  --headers                            Include headers in output
  --timestamps                         Include timestamps in output
  --tail <n>                           Show last N matching entries
  --follow                             Live tail
```

- [ ] **Step 2: Commit**

```bash
git add help.txt
git commit -m "docs: add network monitoring commands to help text"
```

### Task 15: Manual integration smoke test

This task is a manual verification checklist, not automated tests (the _netmonitor process requires a real Chrome instance).

- [ ] **Step 1: Build and run basic smoke test**

```bash
cd /exports/projectpool/dev/rodney && go build -o rodney .
```

Test sequence (requires Chrome installed):

```bash
# Create a session
export RODNEY_SESSION=$(./rodney newsession https://httpbin.org/get --local)

# Verify monitor is running
cat .rodney/state.json | grep monitor_pid

# Wait a moment for page load capture
sleep 2

# Check net-log output
./rodney net-log --since render

# Check a specific API response body
./rodney net-log --path /get
# Note a seq number from the output, then:
./rodney net-body <seq>

# Click something and check interaction markers
./rodney click 'a'  # or any valid selector
./rodney net-log --since interaction

# Clear
./rodney net-clear
./rodney net-log  # should be empty or minimal

# Clean up
./rodney endsession

# Verify net data is cleaned up
ls .rodney/net/  # should not contain the session dir
```

- [ ] **Step 2: Test --no-capture**

```bash
export RODNEY_SESSION=$(./rodney newsession https://example.com --local --no-capture)
ls .rodney/net/$RODNEY_SESSION  # should not exist
./rodney endsession
```

- [ ] **Step 3: Commit any fixes from smoke testing**

```bash
git add -A
git commit -m "fix: address issues found in integration smoke test"
```
