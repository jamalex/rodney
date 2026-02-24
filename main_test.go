package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/ysmood/gson"
)

// testEnv holds a shared browser and test HTTP server for all tests.
type testEnv struct {
	browser *rod.Browser
	server  *httptest.Server
}

var env *testEnv

func TestMain(m *testing.M) {
	// Launch headless Chrome once for all tests
	l := launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Set("single-process").
		Headless(true).
		Leakless(false)

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	u := l.MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()

	// Start test HTTP server with known HTML fixtures
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex)
	mux.HandleFunc("/form", handleForm)
	mux.HandleFunc("/upload", handleUpload)
	mux.HandleFunc("/download", handleDownload)
	mux.HandleFunc("/testfile.txt", handleTestFile)
	mux.HandleFunc("/empty", handleEmpty)
	mux.HandleFunc("/stealth-check", handleStealthCheck)
	mux.HandleFunc("/stealth-trap", handleStealthTrap)
	server := httptest.NewServer(mux)

	env = &testEnv{browser: browser, server: server}

	code := m.Run()

	server.Close()
	browser.MustClose()
	os.Exit(code)
}

// --- HTML fixtures ---

func handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Test Page</title></head>
<body>
  <nav aria-label="Main">
    <a href="/about">About</a>
    <a href="/contact">Contact</a>
  </nav>
  <main>
    <h1>Welcome</h1>
    <p>Hello world</p>
    <button id="submit-btn">Submit</button>
    <button id="cancel-btn" disabled>Cancel</button>
  </main>
</body>
</html>`))
}

func handleForm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Form Page</title></head>
<body>
  <h1>Contact Us</h1>
  <form>
    <label for="name-input">Name</label>
    <input id="name-input" type="text" aria-required="true">
    <label for="email-input">Email</label>
    <input id="email-input" type="email">
    <select id="topic" aria-label="Topic">
      <option value="general">General</option>
      <option value="support">Support</option>
    </select>
    <button type="submit">Send</button>
  </form>
</body>
</html>`))
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Upload Page</title></head>
<body>
  <input id="file-input" type="file" accept="image/*">
  <span id="file-name"></span>
  <script>
    document.getElementById('file-input').addEventListener('change', function(e) {
      document.getElementById('file-name').textContent = e.target.files[0] ? e.target.files[0].name : '';
    });
  </script>
</body>
</html>`))
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Download Page</title></head>
<body>
  <a id="file-link" href="/testfile.txt">Download file</a>
  <a id="data-link" href="data:text/plain;base64,SGVsbG8gV29ybGQ=">Download data</a>
  <img id="test-img" src="/testfile.txt">
</body>
</html>`))
}

func handleTestFile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.Write([]byte("Hello World"))
}

func handleEmpty(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html lang="en">
<head><title>Empty Page</title></head>
<body></body>
</html>`))
}

// --- Helper: navigate to a fixture and return the page ---

func navigateTo(t *testing.T, path string) *rod.Page {
	t.Helper()
	page := env.browser.MustPage(env.server.URL + path)
	page.MustWaitLoad()
	t.Cleanup(func() { page.MustClose() })
	return page
}

// =====================
// ax-tree tests (RED)
// =====================

func TestAXTree_ReturnsNodes(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	// Sanity: we should get nodes back
	if len(result.Nodes) == 0 {
		t.Fatal("expected nodes in accessibility tree, got 0")
	}

	// Now test our formatting function
	out := formatAXTree(result.Nodes)
	if out == "" {
		t.Fatal("formatAXTree returned empty string")
	}
	if !strings.Contains(out, "Welcome") {
		t.Errorf("tree should contain heading text 'Welcome', got:\n%s", out)
	}
	if !strings.Contains(out, "button") {
		t.Errorf("tree should contain 'button' role, got:\n%s", out)
	}
	if !strings.Contains(out, "Submit") {
		t.Errorf("tree should contain button name 'Submit', got:\n%s", out)
	}
}

func TestAXTree_Indentation(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	out := formatAXTree(result.Nodes)
	lines := strings.Split(out, "\n")

	// Root node should have no indentation
	if len(lines) == 0 {
		t.Fatal("no lines in output")
	}
	if strings.HasPrefix(lines[0], " ") {
		t.Errorf("root node should not be indented, got: %q", lines[0])
	}

	// Some lines should be indented (children)
	hasIndented := false
	for _, line := range lines {
		if strings.HasPrefix(line, "  ") {
			hasIndented = true
			break
		}
	}
	if !hasIndented {
		t.Errorf("expected some indented lines for child nodes, got:\n%s", out)
	}
}

func TestAXTree_SkipsIgnoredNodes(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	out := formatAXTree(result.Nodes)

	// Count ignored vs total
	ignoredCount := 0
	for _, node := range result.Nodes {
		if node.Ignored {
			ignoredCount++
		}
	}

	// If there are ignored nodes, they shouldn't appear in text output
	if ignoredCount > 0 {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) >= len(result.Nodes) {
			t.Errorf("text output should skip ignored nodes: %d lines for %d nodes (%d ignored)",
				len(lines), len(result.Nodes), ignoredCount)
		}
	}
}

func TestAXTree_DepthLimit(t *testing.T) {
	page := navigateTo(t, "/")
	full, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}

	depth := 2
	limited, err := proto.AccessibilityGetFullAXTree{Depth: &depth}.Call(page)
	if err != nil {
		t.Fatalf("CDP call with depth failed: %v", err)
	}

	if len(limited.Nodes) >= len(full.Nodes) {
		t.Errorf("depth-limited tree (%d nodes) should have fewer nodes than full tree (%d nodes)",
			len(limited.Nodes), len(full.Nodes))
	}
}

func TestAXTree_JSONOutput(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := proto.AccessibilityGetFullAXTree{}.Call(page)
	if err != nil {
		t.Fatalf("CDP call failed: %v", err)
	}
	out := formatAXTreeJSON(result.Nodes)
	// Must be valid JSON
	var parsed []interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\nOutput:\n%s", err, out[:min(len(out), 500)])
	}
	if len(parsed) == 0 {
		t.Error("JSON output should contain nodes")
	}
}

// =====================
// ax-find tests (RED)
// =====================

func TestAXFind_ByRole(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "", "button")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) < 2 {
		t.Fatalf("expected at least 2 buttons, got %d", len(nodes))
	}

	out := formatAXNodeList(nodes)
	if !strings.Contains(out, "Submit") {
		t.Errorf("output should contain 'Submit' button, got:\n%s", out)
	}
	if !strings.Contains(out, "Cancel") {
		t.Errorf("output should contain 'Cancel' button, got:\n%s", out)
	}
}

func TestAXFind_ByName(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "Submit", "")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected at least 1 node named 'Submit', got 0")
	}
	out := formatAXNodeList(nodes)
	if !strings.Contains(out, "Submit") {
		t.Errorf("output should contain 'Submit', got:\n%s", out)
	}
}

func TestAXFind_ByNameAndRoleExact(t *testing.T) {
	page := navigateTo(t, "/")
	// Combining name + role should give exactly one result
	nodes, err := queryAXNodes(page, "Submit", "button")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected exactly 1 button named 'Submit', got %d", len(nodes))
	}
}

func TestAXFind_ByNameAndRole(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "About", "link")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 link named 'About', got %d", len(nodes))
	}
}

func TestAXFind_NoResults(t *testing.T) {
	page := navigateTo(t, "/")
	nodes, err := queryAXNodes(page, "NonexistentThing", "")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 results for nonexistent name, got %d", len(nodes))
	}
}

func TestAXFind_FormPage(t *testing.T) {
	page := navigateTo(t, "/form")
	nodes, err := queryAXNodes(page, "", "textbox")
	if err != nil {
		t.Fatalf("queryAXNodes failed: %v", err)
	}
	if len(nodes) < 2 {
		t.Fatalf("expected at least 2 textboxes on form page, got %d", len(nodes))
	}
}

// =====================
// ax-node tests (RED)
// =====================

func TestAXNode_ButtonBySelector(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "#submit-btn")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "button") {
		t.Errorf("should show role 'button', got:\n%s", out)
	}
	if !strings.Contains(out, "Submit") {
		t.Errorf("should show name 'Submit', got:\n%s", out)
	}
}

func TestAXNode_DisabledButton(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "#cancel-btn")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "button") {
		t.Errorf("should show role 'button', got:\n%s", out)
	}
	if !strings.Contains(out, "disabled") {
		t.Errorf("should show disabled property, got:\n%s", out)
	}
}

func TestAXNode_InputWithLabel(t *testing.T) {
	page := navigateTo(t, "/form")
	node, err := getAXNode(page, "#name-input")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "textbox") {
		t.Errorf("should show role 'textbox', got:\n%s", out)
	}
	if !strings.Contains(out, "Name") {
		t.Errorf("should show accessible name 'Name' from label, got:\n%s", out)
	}
}

func TestAXNode_HeadingLevel(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "h1")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetail(node)
	if !strings.Contains(out, "heading") {
		t.Errorf("should show role 'heading', got:\n%s", out)
	}
	if !strings.Contains(out, "level") {
		t.Errorf("should show level property for heading, got:\n%s", out)
	}
}

func TestAXNode_JSONOutput(t *testing.T) {
	page := navigateTo(t, "/")
	node, err := getAXNode(page, "#submit-btn")
	if err != nil {
		t.Fatalf("getAXNode failed: %v", err)
	}
	out := formatAXNodeDetailJSON(node)
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\nOutput:\n%s", err, out)
	}
	if _, ok := parsed["nodeId"]; !ok {
		t.Error("JSON should contain nodeId field")
	}
}

func TestAXNode_SelectorNotFound(t *testing.T) {
	page := navigateTo(t, "/")
	// Use a short timeout so we don't block for 30s waiting for a nonexistent element
	shortPage := page.Timeout(2 * time.Second)
	_, err := getAXNode(shortPage, "#does-not-exist")
	if err == nil {
		t.Error("expected error for nonexistent selector, got nil")
	}
}

// =====================
// file command tests
// =====================

func TestFile_SetFileOnInput(t *testing.T) {
	page := navigateTo(t, "/upload")

	// Create a temp file to upload
	tmp, err := os.CreateTemp("", "rodney-test-*.txt")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer os.Remove(tmp.Name())
	tmp.Write([]byte("test content"))
	tmp.Close()

	el, err := page.Element("#file-input")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	if err := el.SetFiles([]string{tmp.Name()}); err != nil {
		t.Fatalf("SetFiles failed: %v", err)
	}

	// Wait for the change event to fire and check the file name
	page.MustWaitStable()
	nameEl, err := page.Element("#file-name")
	if err != nil {
		t.Fatalf("file-name element not found: %v", err)
	}
	text, _ := nameEl.Text()
	if text == "" {
		t.Error("expected file name to be set after SetFiles, got empty string")
	}
}

func TestFile_MultipleFiles(t *testing.T) {
	page := navigateTo(t, "/upload")

	tmp1, _ := os.CreateTemp("", "rodney-test1-*.txt")
	defer os.Remove(tmp1.Name())
	tmp1.Write([]byte("file 1"))
	tmp1.Close()

	tmp2, _ := os.CreateTemp("", "rodney-test2-*.txt")
	defer os.Remove(tmp2.Name())
	tmp2.Write([]byte("file 2"))
	tmp2.Close()

	el, err := page.Element("#file-input")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}

	// Setting files should not error even with multiple files
	if err := el.SetFiles([]string{tmp1.Name(), tmp2.Name()}); err != nil {
		t.Fatalf("SetFiles with multiple files failed: %v", err)
	}
}

// =====================
// download command tests
// =====================

func TestDownload_DataURL(t *testing.T) {
	// Test decoding a data: URL directly
	data, err := decodeDataURL("data:text/plain;base64,SGVsbG8gV29ybGQ=")
	if err != nil {
		t.Fatalf("decodeDataURL failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_DataURL_URLEncoded(t *testing.T) {
	data, err := decodeDataURL("data:text/plain,Hello%20World")
	if err != nil {
		t.Fatalf("decodeDataURL failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_InferFilename_URL(t *testing.T) {
	name := inferDownloadFilename("https://example.com/images/photo.png")
	if name != "photo.png" {
		t.Errorf("expected 'photo.png', got %q", name)
	}
}

func TestDownload_InferFilename_DataURL(t *testing.T) {
	name := inferDownloadFilename("data:image/png;base64,abc")
	if !strings.HasPrefix(name, "download") || !strings.Contains(name, ".png") {
		t.Errorf("expected 'download*.png', got %q", name)
	}
}

func TestDownload_FetchLink(t *testing.T) {
	page := navigateTo(t, "/download")

	el, err := page.Element("#file-link")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	href := el.MustAttribute("href")
	if href == nil {
		t.Fatal("expected href attribute")
	}

	// Fetch using JS in the page context, same as cmdDownload does
	js := fmt.Sprintf(`async () => {
		const resp = await fetch(%q);
		if (!resp.ok) throw new Error('HTTP ' + resp.status);
		const buf = await resp.arrayBuffer();
		const bytes = new Uint8Array(buf);
		let binary = '';
		for (let i = 0; i < bytes.length; i++) {
			binary += String.fromCharCode(bytes[i]);
		}
		return btoa(binary);
	}`, *href)
	result, err := page.Eval(js)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}

	data, err := base64.StdEncoding.DecodeString(result.Value.Str())
	if err != nil {
		t.Fatalf("base64 decode failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_DataLinkElement(t *testing.T) {
	page := navigateTo(t, "/download")

	el, err := page.Element("#data-link")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	href := el.MustAttribute("href")
	if href == nil {
		t.Fatal("expected href attribute")
	}

	data, err := decodeDataURL(*href)
	if err != nil {
		t.Fatalf("decodeDataURL failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestDownload_ImgSrc(t *testing.T) {
	page := navigateTo(t, "/download")

	el, err := page.Element("#test-img")
	if err != nil {
		t.Fatalf("element not found: %v", err)
	}
	src := el.MustAttribute("src")
	if src == nil {
		t.Fatal("expected src attribute")
	}
	if *src != "/testfile.txt" {
		t.Errorf("expected '/testfile.txt', got %q", *src)
	}
}

// =====================
// Directory-scoped sessions tests
// =====================

func TestExtractScopeArgs_NoFlags(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"open", "https://example.com"})
	if mode != scopeAuto {
		t.Errorf("expected scopeAuto, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LocalFlag(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"--local", "start"})
	if mode != scopeLocal {
		t.Errorf("expected scopeLocal, got %v", mode)
	}
	if len(remaining) != 1 || remaining[0] != "start" {
		t.Errorf("expected [start], got %v", remaining)
	}
}

func TestExtractScopeArgs_GlobalFlag(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"--global", "open", "https://example.com"})
	if mode != scopeGlobal {
		t.Errorf("expected scopeGlobal, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LocalFlagAfterCommand(t *testing.T) {
	mode, remaining := extractScopeArgs([]string{"open", "--local", "https://example.com"})
	if mode != scopeLocal {
		t.Errorf("expected scopeLocal, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LastFlagWins(t *testing.T) {
	mode, _ := extractScopeArgs([]string{"--local", "--global", "start"})
	if mode != scopeGlobal {
		t.Errorf("expected last flag (scopeGlobal) to win, got %v", mode)
	}
}

func TestResolveStateDir_Global(t *testing.T) {
	dir := resolveStateDir(scopeGlobal, "/some/working/dir")
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".rodney")
	if dir != expected {
		t.Errorf("expected %q, got %q", expected, dir)
	}
}

func TestResolveStateDir_Local(t *testing.T) {
	dir := resolveStateDir(scopeLocal, "/some/working/dir")
	expected := filepath.Join("/some/working/dir", ".rodney")
	if dir != expected {
		t.Errorf("expected %q, got %q", expected, dir)
	}
}

func TestResolveStateDir_AutoPrefersLocal(t *testing.T) {
	// Create a temp directory with a .rodney/state.json to simulate local session
	tmpDir := t.TempDir()
	localRodney := filepath.Join(tmpDir, ".rodney")
	os.MkdirAll(localRodney, 0755)
	os.WriteFile(filepath.Join(localRodney, "state.json"), []byte(`{}`), 0644)

	dir := resolveStateDir(scopeAuto, tmpDir)
	if dir != localRodney {
		t.Errorf("auto mode should prefer local when .rodney/state.json exists: expected %q, got %q", localRodney, dir)
	}
}

func TestResolveStateDir_AutoFallsBackToGlobal(t *testing.T) {
	// Use a temp directory with NO .rodney/ — should fall back to global
	tmpDir := t.TempDir()
	dir := resolveStateDir(scopeAuto, tmpDir)
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".rodney")
	if dir != expected {
		t.Errorf("auto mode should fall back to global: expected %q, got %q", expected, dir)
	}
}

func TestResolveStateDir_LocalUsesWorkingDir(t *testing.T) {
	tmpDir := t.TempDir()
	dir := resolveStateDir(scopeLocal, tmpDir)
	expected := filepath.Join(tmpDir, ".rodney")
	if dir != expected {
		t.Errorf("local mode should use working dir: expected %q, got %q", expected, dir)
	}
}

// =====================
// RODNEY_HOME env var tests
// =====================

func TestStateDir_Default(t *testing.T) {
	t.Setenv("RODNEY_HOME", "")
	home, _ := os.UserHomeDir()
	want := home + "/.rodney"
	got := stateDir()
	if got != want {
		t.Errorf("stateDir() = %q, want %q", got, want)
	}
}

func TestStateDir_EnvVar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RODNEY_HOME", dir)
	got := stateDir()
	if got != dir {
		t.Errorf("stateDir() = %q, want %q", got, dir)
	}
}

func TestMimeToExt(t *testing.T) {
	tests := []struct {
		mime string
		ext  string
	}{
		{"image/png", ".png"},
		{"image/jpeg", ".jpg"},
		{"application/pdf", ".pdf"},
		{"text/plain", ".txt"},
		{"unknown/type", ""},
	}
	for _, tt := range tests {
		got := mimeToExt(tt.mime)
		if got != tt.ext {
			t.Errorf("mimeToExt(%q) = %q, want %q", tt.mime, got, tt.ext)
		}
	}
}

// =====================
// assert command tests
// =====================

func TestAssert_TruthyPass_String(t *testing.T) {
	page := navigateTo(t, "/")
	// document.title is "Test Page" which is truthy
	result, err := page.Eval(`() => { return (document.title); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	// Should not be falsy
	switch raw {
	case "false", "0", "null", "undefined", `""`:
		t.Errorf("document.title should be truthy, got raw=%q", raw)
	}
	if result.Value.Str() != "Test Page" {
		t.Errorf("expected 'Test Page', got %q", result.Value.Str())
	}
}

func TestAssert_TruthyPass_True(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (1 === 1); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "true" {
		t.Errorf("1 === 1 should be true, got %q", raw)
	}
}

func TestAssert_TruthyPass_Number(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (42); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw == "0" || raw == "false" || raw == "null" || raw == "undefined" || raw == `""` {
		t.Errorf("42 should be truthy, got raw=%q", raw)
	}
}

func TestAssert_TruthyFail_Null(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.querySelector(".nonexistent")); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "null" {
		t.Errorf("querySelector for nonexistent should return null, got %q", raw)
	}
}

func TestAssert_TruthyFail_False(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (false); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "false" {
		t.Errorf("false should be false, got %q", raw)
	}
}

func TestAssert_TruthyFail_Zero(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (0); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "0" {
		t.Errorf("0 should be 0, got %q", raw)
	}
}

func TestAssert_TruthyFail_EmptyString(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (""); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != `""` {
		t.Errorf("empty string should have JSON repr '\"\"', got %q", raw)
	}
}

func TestAssert_EqualityPass_Title(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.title); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	actual := result.Value.Str()
	if actual != "Test Page" {
		t.Errorf("expected 'Test Page', got %q", actual)
	}
}

func TestAssert_EqualityPass_Count(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.querySelectorAll("button").length); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "2" {
		t.Errorf("expected 2 buttons, got %q", raw)
	}
}

func TestAssert_EqualityFail_WrongTitle(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (document.title); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	actual := result.Value.Str()
	if actual == "Wrong Title" {
		t.Error("title should NOT equal 'Wrong Title'")
	}
}

func TestAssert_EqualityPass_BoolString(t *testing.T) {
	page := navigateTo(t, "/")
	result, err := page.Eval(`() => { return (1 === 1); }`)
	if err != nil {
		t.Fatalf("eval failed: %v", err)
	}
	raw := result.Value.JSON("", "")
	if raw != "true" {
		t.Errorf("1 === 1 should produce 'true', got %q", raw)
	}
}

func TestAssert_ValueFormatting_MatchesJSCommand(t *testing.T) {
	// Verify that the value formatting used by assert matches what rodney js outputs
	page := navigateTo(t, "/")

	tests := []struct {
		expr     string
		expected string
	}{
		{`document.title`, "Test Page"},   // string unquoted
		{`1 + 2`, "3"},                    // number
		{`true`, "true"},                  // boolean
		{`null`, "null"},                  // null
		{`document.querySelectorAll("button").length`, "2"}, // number from DOM
	}

	for _, tt := range tests {
		js := fmt.Sprintf(`() => { return (%s); }`, tt.expr)
		result, err := page.Eval(js)
		if err != nil {
			t.Fatalf("eval %q failed: %v", tt.expr, err)
		}

		v := result.Value
		raw := v.JSON("", "")
		var actual string
		switch {
		case raw == "null" || raw == "undefined":
			actual = raw
		case raw == "true" || raw == "false":
			actual = raw
		case len(raw) > 0 && raw[0] == '"':
			actual = v.Str()
		case len(raw) > 0 && (raw[0] == '{' || raw[0] == '['):
			actual = v.JSON("", "  ")
		default:
			actual = raw
		}

		if actual != tt.expected {
			t.Errorf("expr %q: expected %q, got %q (raw=%q)", tt.expr, tt.expected, actual, raw)
		}
	}
}

// =====================
// assert --message tests
// =====================

func TestParseAssertArgs_ExprOnly(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected != nil {
		t.Errorf("expected should be nil, got %q", *expected)
	}
	if message != "" {
		t.Errorf("message should be empty, got %q", message)
	}
}

func TestParseAssertArgs_ExprAndExpected(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "Dashboard"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected == nil || *expected != "Dashboard" {
		t.Errorf("expected = %v, want %q", expected, "Dashboard")
	}
	if message != "" {
		t.Errorf("message should be empty, got %q", message)
	}
}

func TestParseAssertArgs_MessageLong(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "--message", "Page title check"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected != nil {
		t.Errorf("expected should be nil for truthy with --message, got %q", *expected)
	}
	if message != "Page title check" {
		t.Errorf("message = %q, want %q", message, "Page title check")
	}
}

func TestParseAssertArgs_MessageShort(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "-m", "Title check"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected != nil {
		t.Errorf("expected should be nil, got %q", *expected)
	}
	if message != "Title check" {
		t.Errorf("message = %q, want %q", message, "Title check")
	}
}

func TestParseAssertArgs_EqualityWithMessage(t *testing.T) {
	expr, expected, message := parseAssertArgs([]string{"document.title", "Dashboard", "--message", "Wrong page"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected == nil || *expected != "Dashboard" {
		t.Errorf("expected = %v, want %q", expected, "Dashboard")
	}
	if message != "Wrong page" {
		t.Errorf("message = %q, want %q", message, "Wrong page")
	}
}

func TestParseAssertArgs_MessageBeforeExpr(t *testing.T) {
	// --message can appear anywhere; positional args still work
	expr, expected, message := parseAssertArgs([]string{"-m", "Check", "document.title", "Home"})
	if expr != "document.title" {
		t.Errorf("expr = %q, want %q", expr, "document.title")
	}
	if expected == nil || *expected != "Home" {
		t.Errorf("expected = %v, want %q", expected, "Home")
	}
	if message != "Check" {
		t.Errorf("message = %q, want %q", message, "Check")
	}
}

func TestFormatAssertFail_TruthyNoMessage(t *testing.T) {
	got := formatAssertFail("null", nil, "")
	if got != "fail: got null" {
		t.Errorf("got %q, want %q", got, "fail: got null")
	}
}

func TestFormatAssertFail_TruthyWithMessage(t *testing.T) {
	got := formatAssertFail("null", nil, "User should be logged in")
	if got != "fail: User should be logged in (got null)" {
		t.Errorf("got %q, want %q", got, "fail: User should be logged in (got null)")
	}
}

func TestFormatAssertFail_EqualityNoMessage(t *testing.T) {
	expected := "Dashboard"
	got := formatAssertFail("Task Tracker", &expected, "")
	want := `fail: got "Task Tracker", expected "Dashboard"`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatAssertFail_EqualityWithMessage(t *testing.T) {
	expected := "Dashboard"
	got := formatAssertFail("Task Tracker", &expected, "Wrong page loaded")
	want := `fail: Wrong page loaded (got "Task Tracker", expected "Dashboard")`
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestInsecureFlag_WithSelfSignedCert(t *testing.T) {
	// Create HTTPS server with self-signed certificate
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Secure Test</title></head>
<body><h1>HTTPS Test Page</h1></body></html>`))
	})
	httpsServer := httptest.NewUnstartedServer(mux)
	// Suppress expected TLS handshake errors to keep test output clean
	httpsServer.Config.ErrorLog = log.New(io.Discard, "", 0)
	httpsServer.StartTLS()
	defer httpsServer.Close()

	// Test 1: Browser WITHOUT --ignore-certificate-errors should fail
	t.Run("WithoutInsecureFlag", func(t *testing.T) {
		l := launcher.New().
			Set("no-sandbox").
			Set("disable-gpu").
			Set("single-process").
			Headless(true).
			Leakless(false)

		if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
			l = l.Bin(bin)
		}

		u := l.MustLaunch()
		browser := rod.New().ControlURL(u).MustConnect()
		defer browser.MustClose()

		page := browser.MustPage("")
		defer page.MustClose()

		err := page.Navigate(httpsServer.URL)
		if err == nil {
			t.Fatal("expected ERR_CERT_AUTHORITY_INVALID error, but navigation succeeded")
		}
		if !strings.Contains(err.Error(), "ERR_CERT_AUTHORITY_INVALID") {
			t.Errorf("expected ERR_CERT_AUTHORITY_INVALID, got: %v", err)
		}
	})

	// Test 2: Browser WITH --ignore-certificate-errors should succeed
	t.Run("WithInsecureFlag", func(t *testing.T) {
		l := launcher.New().
			Set("no-sandbox").
			Set("disable-gpu").
			Set("single-process").
			Set("ignore-certificate-errors"). // This is what --insecure sets
			Headless(true).
			Leakless(false)

		if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
			l = l.Bin(bin)
		}

		u := l.MustLaunch()
		browser := rod.New().ControlURL(u).MustConnect()
		defer browser.MustClose()

		// Try to navigate to HTTPS server with invalid cert
		page := browser.MustPage(httpsServer.URL)
		defer page.MustClose()

		page.MustWaitLoad()
		title := page.MustInfo().Title

		if title != "Secure Test" {
			t.Errorf("expected page to load successfully with title 'Secure Test', got %q", title)
		}
	})
}

// =====================
// parseStartFlags tests
// =====================

func TestParseStartFlags_ShowFlag(t *testing.T) {
	flags, err := parseStartFlags([]string{"--show"})
	if err != nil {
		t.Fatalf("--show should be accepted, got error: %v", err)
	}
	if flags.headless {
		t.Error("expected headless=false when --show is passed")
	}
}

func TestParseStartFlags_ShowAndInsecure(t *testing.T) {
	flags, err := parseStartFlags([]string{"--show", "--insecure"})
	if err != nil {
		t.Fatalf("--show --insecure should be accepted, got error: %v", err)
	}
	if flags.headless {
		t.Error("expected headless=false when --show is passed")
	}
	if !flags.ignoreCertErrors {
		t.Error("expected ignoreCertErrors=true when --insecure is passed")
	}
}

func TestParseStartFlags_InsecureOnly(t *testing.T) {
	flags, err := parseStartFlags([]string{"--insecure"})
	if err != nil {
		t.Fatalf("--insecure should be accepted, got error: %v", err)
	}
	if !flags.headless {
		t.Error("expected headless=true (default) when --show is not passed")
	}
	if !flags.ignoreCertErrors {
		t.Error("expected ignoreCertErrors=true when --insecure is passed")
	}
}

func TestParseStartFlags_KShorthand(t *testing.T) {
	flags, err := parseStartFlags([]string{"-k"})
	if err != nil {
		t.Fatalf("-k should be accepted, got error: %v", err)
	}
	if !flags.ignoreCertErrors {
		t.Error("expected ignoreCertErrors=true when -k is passed")
	}
}

func TestParseStartFlags_NoArgs(t *testing.T) {
	flags, err := parseStartFlags([]string{})
	if err != nil {
		t.Fatalf("no args should be accepted, got error: %v", err)
	}
	if !flags.headless {
		t.Error("expected headless=true by default")
	}
	if flags.ignoreCertErrors {
		t.Error("expected ignoreCertErrors=false by default")
	}
}

func TestParseStartFlags_Stealth(t *testing.T) {
	flags, err := parseStartFlags([]string{"--stealth"})
	if err != nil {
		t.Fatalf("--stealth should be accepted, got error: %v", err)
	}
	if !flags.stealth {
		t.Error("expected stealth=true")
	}
	if !flags.headless {
		t.Error("expected headless=true (default)")
	}
}

func TestParseStartFlags_StealthWithShow(t *testing.T) {
	flags, err := parseStartFlags([]string{"--stealth", "--show"})
	if err != nil {
		t.Fatalf("--stealth --show should be accepted, got error: %v", err)
	}
	if !flags.stealth {
		t.Error("expected stealth=true")
	}
	if flags.headless {
		t.Error("expected headless=false with --show")
	}
}

func TestParseStartFlags_UnknownFlag(t *testing.T) {
	_, err := parseStartFlags([]string{"--bogus"})
	if err == nil {
		t.Fatal("expected error for unknown flag --bogus")
	}
	if !strings.Contains(err.Error(), "unknown flag: --bogus") {
		t.Errorf("expected 'unknown flag: --bogus' in error, got: %v", err)
	}
}

// =====================
// Stealth check fixture
// =====================

func handleStealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Stealth Check</title></head>
<body>
  <div id="main-webdriver"></div>
  <div id="main-ua"></div>
  <div id="worker-webdriver"></div>
  <div id="worker-ua"></div>
  <div id="worker-done"></div>
  <script>
    document.getElementById('main-webdriver').textContent = String(navigator.webdriver);
    document.getElementById('main-ua').textContent = navigator.userAgent;

    try {
      var code = 'postMessage({ userAgent: navigator.userAgent, webdriver: String(navigator.webdriver) });';
      var blob = new Blob([code], {type: 'application/javascript'});
      var worker = new Worker(URL.createObjectURL(blob));
      worker.onmessage = function(e) {
        document.getElementById('worker-webdriver').textContent = e.data.webdriver;
        document.getElementById('worker-ua').textContent = e.data.userAgent;
        document.getElementById('worker-done').textContent = 'true';
      };
      worker.onerror = function(e) {
        document.getElementById('worker-done').textContent = 'error: ' + e.message;
      };
    } catch(err) {
      document.getElementById('worker-done').textContent = 'error: ' + err.message;
    }
  </script>
</body>
</html>`))
}

// =====================
// Stealth smoke tests
// =====================

// launchStealthBrowser creates a browser with stealth config for smoke tests.
// Uses rod's bundled Chromium (which supports --load-extension) with the
// worker-fix extension loaded, mirroring what cmdStart --stealth does.
func launchStealthBrowser(t *testing.T) *rod.Browser {
	t.Helper()

	dataDir := t.TempDir()
	extDir := writeStealthExtension(dataDir, workerFixJS)

	l := launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Set("single-process").
		Headless(true).
		Leakless(false).
		Set("disable-blink-features", "AutomationControlled").
		Set("load-extension", extDir)
	l.Delete("disable-extensions")

	// Use rod's default Chromium (don't set system Chrome — it may be
	// Google Chrome which blocks --load-extension)
	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	u := l.MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()
	t.Cleanup(func() { browser.MustClose() })
	return browser
}

func TestStealth_NavigatorWebdriverHidden(t *testing.T) {
	browser := launchStealthBrowser(t)
	page := browser.MustPage(env.server.URL + "/stealth-check")
	defer page.MustClose()
	page.MustWaitLoad()

	webdriver := page.MustEval(`() => String(navigator.webdriver)`).Str()
	if webdriver == "true" {
		t.Error("navigator.webdriver should not be true in stealth mode")
	}
}

func TestStealth_UserAgentNormal(t *testing.T) {
	browser := launchStealthBrowser(t)
	page := browser.MustPage(env.server.URL + "/stealth-check")
	defer page.MustClose()
	page.MustWaitLoad()

	ua := page.MustEval(`() => navigator.userAgent`).Str()
	if strings.Contains(ua, "HeadlessChrome") {
		t.Errorf("user agent should not contain 'HeadlessChrome', got: %s", ua)
	}
}

func TestStealth_WebWorkerConsistency(t *testing.T) {
	browser := launchStealthBrowser(t)
	page := browser.MustPage(env.server.URL + "/stealth-check")
	defer page.MustClose()
	page.MustWaitLoad()

	// Wait for worker to complete
	page.Timeout(5 * time.Second).MustEval(`() => new Promise((resolve, reject) => {
		const timeout = setTimeout(() => reject(new Error('worker timeout')), 4000);
		const check = () => {
			const el = document.getElementById('worker-done');
			if (el && el.textContent) {
				clearTimeout(timeout);
				resolve(el.textContent);
			} else {
				setTimeout(check, 50);
			}
		};
		check();
	})`)

	mainUA := page.MustEval(`() => document.getElementById('main-ua').textContent`).Str()
	workerUA := page.MustEval(`() => document.getElementById('worker-ua').textContent`).Str()

	if mainUA == "" || workerUA == "" {
		t.Fatalf("failed to get user agent strings (main=%q, worker=%q)", mainUA, workerUA)
	}
	if mainUA != workerUA {
		t.Errorf("web worker UA inconsistent:\n  main:   %s\n  worker: %s", mainUA, workerUA)
	}

	mainWD := page.MustEval(`() => document.getElementById('main-webdriver').textContent`).Str()
	workerWD := page.MustEval(`() => document.getElementById('worker-webdriver').textContent`).Str()

	if mainWD != workerWD {
		t.Errorf("web worker navigator.webdriver inconsistent:\n  main:   %s\n  worker: %s", mainWD, workerWD)
	}
}

func TestStealth_BotIncolumitas(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live site test in short mode")
	}

	browser := launchStealthBrowser(t)
	page := browser.MustPage("https://bot.incolumitas.com/")
	defer page.MustClose()

	// The site runs two batches of detection tests asynchronously:
	// "old tests" in #detection-tests and "new tests" in #new-tests.
	// Wait for both to populate, then collect all test results.
	allResults := page.Timeout(60 * time.Second).MustEval(`() => new Promise((resolve, reject) => {
		const timeout = setTimeout(() => reject(new Error('timed out waiting for detection results')), 55000);
		const check = () => {
			const nt = document.getElementById('new-tests');
			const dt = document.getElementById('detection-tests');
			const hasNew = nt && nt.textContent.includes('inconsistentWebWorkerNavigatorPropery');
			const hasOld = dt && dt.textContent.trim().length > 2;
			if (hasNew && hasOld) {
				clearTimeout(timeout);
				// Merge both result objects into one flat map
				try {
					const oldObj = JSON.parse(dt.textContent.trim());
					const newObj = JSON.parse(nt.textContent.trim());
					// Old tests have nested structure {intoli: {...}, fpscanner: {...}}
					const merged = {};
					for (const [section, tests] of Object.entries(oldObj)) {
						if (typeof tests === 'object' && tests !== null) {
							for (const [k, v] of Object.entries(tests)) {
								merged[section + '/' + k] = v;
							}
						}
					}
					// New tests are flat {key: "OK"|"FAIL"}
					for (const [k, v] of Object.entries(newObj)) {
						merged[k] = v;
					}
					resolve(JSON.stringify(merged));
				} catch(e) {
					resolve(JSON.stringify({
						_oldRaw: dt.textContent.trim(),
						_newRaw: nt.textContent.trim(),
					}));
				}
			} else {
				setTimeout(check, 500);
			}
		};
		check();
	})`).Str()

	var results map[string]interface{}
	if err := json.Unmarshal([]byte(allResults), &results); err != nil {
		t.Fatalf("failed to parse detection results: %v\nraw: %s", err, allResults)
	}

	// Known unfixable detections:
	// - Service workers run in a separate context that can't be patched
	//   from an extension (they require a URL, not interceptable like
	//   Web Workers via blob URL rewriting).
	// - fpscanner/WEBDRIVER uses a custom detection method beyond
	//   navigator.webdriver (which IS false). Likely detects CDP protocol
	//   artifacts inherent to rod's browser automation.
	skip := map[string]bool{
		"inconsistentServiceWorkerNavigatorPropery": true,
		"fpscanner/WEBDRIVER":                       true,
	}

	for name, val := range results {
		status, ok := val.(string)
		if !ok {
			continue
		}
		if skip[name] {
			if status == "FAIL" {
				t.Logf("(skipped) %s: %s", name, status)
			}
			continue
		}
		if status == "FAIL" {
			t.Errorf("%s: FAIL (expected OK)", name)
		} else {
			t.Logf("%s: %s", name, status)
		}
	}
}

func TestStealth_RebrowserBotDetector(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live site test in short mode")
	}

	// Launch stealth browser with non-default viewport to avoid detection
	dataDir := t.TempDir()
	extDir := writeStealthExtension(dataDir, workerFixJS)

	l := launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Set("single-process").
		Headless(true).
		Leakless(false).
		Set("disable-blink-features", "AutomationControlled").
		Set("load-extension", extDir).
		Set("window-size", "1920,1080")
	l.Delete("disable-extensions")

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	u := l.MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()

	// Create a page and expose a function BEFORE navigating (tests exposeFunctionLeak)
	page := browser.MustPage("")
	defer page.MustClose()
	page.MustExpose("exposedFn", func(g gson.JSON) (interface{}, error) {
		return nil, nil
	})

	// Navigate to the bot detector
	page.MustNavigate("https://bot-detector.rebrowser.net/")
	page.MustWaitLoad()

	// Trigger the grey-bubble tests that need explicit actions:
	// 1. dummyFn: call the page's window.dummyFn()
	page.MustEval(`() => { if (typeof window.dummyFn === 'function') window.dummyFn() }`)
	// 2. sourceUrlLeak: call getElementById (the page monkey-patches it to check the stack)
	page.MustEval(`() => document.getElementById('detections-json')`)
	// 3. mainWorldExecution: call getElementsByClassName (grey = good = isolated world)
	//    We intentionally trigger this so it evaluates rather than staying grey.
	page.MustEval(`() => document.getElementsByClassName('div')`)

	// Wait for async tests to settle (runtimeEnableLeak, bypassCsp, useragent poll)
	time.Sleep(3 * time.Second)

	// Read the detections JSON from the textarea
	resultsJSON := page.Timeout(30 * time.Second).MustEval(`() => {
		const el = document.getElementById('detections-json');
		return el ? el.value : '[]';
	}`).Str()

	var detections []struct {
		Type   string      `json:"type"`
		Rating float64     `json:"rating"`
		Note   string      `json:"note"`
		Debug  interface{} `json:"debug"`
	}
	if err := json.Unmarshal([]byte(resultsJSON), &detections); err != nil {
		t.Fatalf("failed to parse detections JSON: %v\nraw: %s", err, resultsJSON)
	}

	if len(detections) == 0 {
		t.Fatal("no detections returned from bot-detector.rebrowser.net")
	}

	// rating < 0 = green/pass, 0 = grey/untested, 0.5 = yellow/warning, > 0 = red/fail
	//
	// Known unfixable detections:
	//
	// mainWorldExecution: rod evaluates ALL JavaScript in the main world
	// (the same context as the page's own scripts). It has no isolated
	// world support. This means any page can monkey-patch DOM APIs like
	// document.querySelector, querySelectorAll, getElementById, etc. and
	// detect when our automation calls them. In practice this affects:
	//   - All selector-based commands (click, text, input, wait, etc.)
	//     because rod's page.Element() uses querySelector internally
	//   - Any page.Eval()/MustEval() calls that touch the DOM
	// Pure CDP operations (screenshot, navigation, accessibility tree)
	// are NOT affected since they don't execute JavaScript.
	// Fixing this would require rod to add isolated world support or
	// using rebrowser-patches to the Chromium binary.
	//
	// runtimeEnableLeak: rod must send CDP Runtime.enable to evaluate JS.
	// The page detects this by using console.debug() with a trapped error
	// stack getter — when Runtime.enable is active, Chrome reads the stack
	// to send Runtime.consoleAPICalled events. Unfixable without patching
	// the Chromium binary (rebrowser-patches).
	//
	// useragent: rod's bundled Chromium doesn't expose
	// navigator.userAgentData, so the test can't determine the version.
	skip := map[string]bool{
		"mainWorldExecution": true,
		"runtimeEnableLeak":  true,
		"useragent":          true,
	}

	for _, d := range detections {
		ratingDesc := "unknown"
		switch {
		case d.Rating < 0:
			ratingDesc = "green"
		case d.Rating == 0:
			ratingDesc = "grey"
		case d.Rating == 0.5:
			ratingDesc = "yellow"
		case d.Rating > 0:
			ratingDesc = "red"
		}

		if skip[d.Type] {
			t.Logf("(skipped) %s: %s (rating=%.1f) %s", d.Type, ratingDesc, d.Rating, stripHTML(d.Note))
			continue
		}

		if d.Rating > 0 {
			t.Errorf("%s: %s (rating=%.1f) %s", d.Type, ratingDesc, d.Rating, stripHTML(d.Note))
		} else {
			t.Logf("%s: %s (rating=%.1f) %s", d.Type, ratingDesc, d.Rating, stripHTML(d.Note))
		}
	}
}

// stripHTML removes HTML tags from a string for cleaner test output.
func stripHTML(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
		} else if r == '>' {
			inTag = false
		} else if !inTag {
			result.WriteRune(r)
		}
	}
	return result.String()
}

// =====================
// Stealth trap fixture
// =====================

func handleStealthTrap(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Stealth Trap</title></head>
<body>
  <button id="target-btn">Click Me</button>
  <p class="info">Some text</p>
  <span id="qs-count">0</span>
  <script>
    var origQS = document.querySelector.bind(document);
    var qsCount = 0;
    document.querySelector = function(sel) {
      qsCount++;
      origQS('#qs-count').textContent = String(qsCount);
      return origQS(sel);
    };
    var origQSA = document.querySelectorAll.bind(document);
    document.querySelectorAll = function(sel) {
      qsCount++;
      origQS('#qs-count').textContent = String(qsCount);
      return origQSA(sel);
    };
  </script>
</body>
</html>`))
}

// =====================
// stealthCtx tests
// =====================

func TestStealthCtx_ElementFindsWithoutTriggeringMonkeyPatch(t *testing.T) {
	page := navigateTo(t, "/stealth-trap")
	sc := getStealthCtx(page)

	nodeID, err := sc.element("#target-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if nodeID == 0 {
		t.Fatal("expected non-zero nodeID for #target-btn")
	}

	// Verify the monkey-patched querySelector was never called.
	// We must read the qs-count span via CDP too (not page.Eval which uses main world).
	countNodeID, err := sc.element("#qs-count", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#qs-count) failed: %v", err)
	}
	result, err := sc.callOn(countNodeID, "function() { return this.textContent; }")
	if err != nil {
		t.Fatalf("callOn failed: %v", err)
	}
	count := result.Result.Value.Str()
	if count != "0" {
		t.Errorf("monkey-patched querySelector was called %s times, expected 0", count)
	}
}

func TestStealthCtx_ElementsFindsAll(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page)

	nodeIDs, err := sc.elements("button", defaultTimeout)
	if err != nil {
		t.Fatalf("elements() failed: %v", err)
	}
	if len(nodeIDs) < 2 {
		t.Fatalf("expected >= 2 buttons, got %d", len(nodeIDs))
	}
}

func TestStealthCtx_ElementTimesOut(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page)

	_, err := sc.element("#nonexistent", 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for #nonexistent, got nil")
	}
}

func TestStealthCtx_IsolatedWorldEval(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page)

	result, err := sc.eval("document.title")
	if err != nil {
		t.Fatalf("eval() failed: %v", err)
	}
	title := result.Result.Value.Str()
	if title != "Test Page" {
		t.Errorf("expected 'Test Page', got %q", title)
	}
}

func TestStealthCtx_Exists(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page)

	// #submit-btn should exist
	found, err := sc.exists("#submit-btn")
	if err != nil {
		t.Fatalf("exists() failed: %v", err)
	}
	if !found {
		t.Error("expected #submit-btn to exist, got false")
	}

	// #nonexistent should not exist
	found, err = sc.exists("#nonexistent")
	if err != nil {
		t.Fatalf("exists() failed: %v", err)
	}
	if found {
		t.Error("expected #nonexistent to not exist, got true")
	}
}

func TestStealthCtx_Count(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page)

	n, err := sc.count("button")
	if err != nil {
		t.Fatalf("count() failed: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 buttons, got %d", n)
	}
}

func TestStealthCtx_Attr(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page)

	nodeID, err := sc.element("#cancel-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}

	val, err := sc.attr(nodeID, "id")
	if err != nil {
		t.Fatalf("attr() failed: %v", err)
	}
	if val != "cancel-btn" {
		t.Errorf("expected attr id='cancel-btn', got %q", val)
	}
}

func TestStealthCtx_HTML(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page)

	nodeID, err := sc.element("#submit-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}

	html, err := sc.outerHTML(nodeID)
	if err != nil {
		t.Fatalf("outerHTML() failed: %v", err)
	}
	if !strings.Contains(html, "Submit") {
		t.Errorf("expected outerHTML to contain 'Submit', got %q", html)
	}
}

func TestStealthCtx_Text(t *testing.T) {
	page := navigateTo(t, "/stealth-trap")
	sc := getStealthCtx(page)

	nodeID, err := sc.element("#target-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}

	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Click Me" {
		t.Errorf("expected 'Click Me', got %q", txt)
	}

	// Verify the monkey-patched querySelector counter is still 0,
	// proving we didn't trigger page JS for the selector.
	countNodeID, err := sc.element("#qs-count", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#qs-count) failed: %v", err)
	}
	countResult, err := sc.callOn(countNodeID, "function() { return this.textContent; }")
	if err != nil {
		t.Fatalf("callOn(#qs-count) failed: %v", err)
	}
	count := countResult.Result.Value.Str()
	if count != "0" {
		t.Errorf("monkey-patched querySelector was called %s times, expected 0", count)
	}
}

func TestStealthCtx_Visible(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page)

	nodeID, err := sc.element("#submit-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}

	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	if !vis {
		t.Error("expected #submit-btn to be visible, got false")
	}
}

func TestStealthCtx_Focus(t *testing.T) {
	page := navigateTo(t, "/form")
	sc := getStealthCtx(page)

	nodeID, err := sc.element("#name-input", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}

	if err := sc.focus(nodeID); err != nil {
		t.Fatalf("focus() failed: %v", err)
	}

	// Verify via isolated world eval that the active element is the input
	result, err := sc.eval("document.activeElement.id")
	if err != nil {
		t.Fatalf("eval() failed: %v", err)
	}
	activeID := result.Result.Value.Str()
	if activeID != "name-input" {
		t.Errorf("expected activeElement.id='name-input', got %q", activeID)
	}
}
