package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
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

// isCaptureEligible returns true if the MIME type's response body should be saved.
func isCaptureEligible(mime string, overrides []string) bool {
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

// bodyFileExt returns a file extension for a MIME type.
func bodyFileExt(mime string) string {
	if idx := strings.Index(mime, ";"); idx >= 0 {
		mime = strings.TrimSpace(mime[:idx])
	}
	mime = strings.ToLower(mime)

	switch mime {
	case "application/json", "application/ld+json":
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
	}

	if strings.HasSuffix(mime, "+json") {
		return ".json"
	}
	if strings.HasSuffix(mime, "+xml") {
		return ".xml"
	}

	return ".body"
}

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
	cmd := exec.Command(exe, args...)
	setSysProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to start network monitor: %v\n", err)
		return 0
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	time.Sleep(200 * time.Millisecond)
	return pid
}

// --- Task 7 & 8: _netmonitor background process and user interaction capture ---

type responseMeta struct {
	mime    string
	headers map[string]string
	status  int
	size    int
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
	writers      map[string]*jsonlWriter
	captureTypes []string
	maxBodySize  int
	attribution  map[string]*attributionCtx
	responseMeta map[string]responseMeta
}

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

// flattenHeaders converts proto headers (map[string]gson.JSON) to a simple map.
func flattenHeaders(raw proto.NetworkHeaders) map[string]string {
	if raw == nil {
		return nil
	}
	var result map[string]string
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	json.Unmarshal(data, &result)
	return result
}

// --- Task 8: User interaction capture via isolated world ---

// userInteractionJS is the script injected into the __rodney_monitor isolated world
// to capture user interactions and send them back via Runtime.addBinding.
const userInteractionJS = `
(function() {
  var inputTimer = null;
  var lastInputValue = '';
  var lastInputSelector = '';

  function buildSelector(el) {
    if (!el || el === document.body || el === document.documentElement) return 'body';
    if (el.id) return '#' + CSS.escape(el.id);
    if (el.getAttribute('data-testid')) return '[data-testid="' + el.getAttribute('data-testid') + '"]';
    if (el.getAttribute('aria-label')) return '[aria-label="' + el.getAttribute('aria-label') + '"]';
    var tag = el.tagName.toLowerCase();
    var cls = el.className && typeof el.className === 'string' ? '.' + el.className.trim().split(/\s+/).join('.') : '';
    var selector = tag + cls;
    var parent = el.parentElement;
    if (parent) {
      var siblings = Array.from(parent.children).filter(function(c) { return c.tagName === el.tagName; });
      if (siblings.length > 1) {
        var idx = siblings.indexOf(el) + 1;
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
    var sel = buildSelector(e.target);
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
    if (e.key === 'Enter' || e.key === 'Tab' || e.key === 'Escape') {
      send({type: 'user-keydown', selector: buildSelector(e.target), key: e.key});
    }
  }, true);

  document.addEventListener('paste', function(e) {
    var val = '';
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
			Expression:    userInteractionJS,
			ContextID:     result.ExecutionContextID,
			ReturnByValue: true,
		}.Call(page)
	}
}
