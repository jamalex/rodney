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

	data, _ := os.ReadFile(filepath.Join(sessionDir, "index.jsonl"))
	if len(data) != 0 {
		t.Fatalf("expected empty JSONL after clear, got %d bytes", len(data))
	}

	if _, err := os.Stat(filepath.Join(sessionDir, "bodies")); !os.IsNotExist(err) {
		t.Fatal("bodies dir should not exist after clear")
	}

	w.append(NetEvent{Type: "request", ID: "R2"})
	data, _ = os.ReadFile(filepath.Join(sessionDir, "index.jsonl"))
	var e NetEvent
	json.Unmarshal(data, &e)
	if e.Seq != 1 {
		t.Fatalf("expected seq=1 after clear, got %d", e.Seq)
	}
}

// Task 4 tests

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
