package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
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
