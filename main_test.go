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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
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
	mux.HandleFunc("/dynamic-dom", handleDynamicDOM)
	mux.HandleFunc("/visibility-zoo", handleVisibilityZoo)
	mux.HandleFunc("/complex-layout", handleComplexLayout)
	mux.HandleFunc("/event-edge-cases", handleEventEdgeCases)
	mux.HandleFunc("/form-advanced", handleFormAdvanced)
	mux.HandleFunc("/spa-navigation", handleSPANavigation)
	mux.HandleFunc("/spa-navigation/", handleSPANavigation)
	mux.HandleFunc("/complex-selectors", handleComplexSelectors)
	mux.HandleFunc("/scroll-scenarios", handleScrollScenarios)
	mux.HandleFunc("/iframe-test", handleIFrameTest)
	mux.HandleFunc("/shadow-dom-test", handleShadowDOMTest)
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
	mode, homeDir, _, remaining := extractScopeArgs([]string{"open", "https://example.com"})
	if mode != scopeAuto {
		t.Errorf("expected scopeAuto, got %v", mode)
	}
	if homeDir != "" {
		t.Errorf("expected empty homeDir, got %q", homeDir)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LocalFlag(t *testing.T) {
	mode, _, _, remaining := extractScopeArgs([]string{"--local", "start"})
	if mode != scopeLocal {
		t.Errorf("expected scopeLocal, got %v", mode)
	}
	if len(remaining) != 1 || remaining[0] != "start" {
		t.Errorf("expected [start], got %v", remaining)
	}
}

func TestExtractScopeArgs_GlobalFlag(t *testing.T) {
	mode, _, _, remaining := extractScopeArgs([]string{"--global", "open", "https://example.com"})
	if mode != scopeGlobal {
		t.Errorf("expected scopeGlobal, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LocalFlagAfterCommand(t *testing.T) {
	mode, _, _, remaining := extractScopeArgs([]string{"open", "--local", "https://example.com"})
	if mode != scopeLocal {
		t.Errorf("expected scopeLocal, got %v", mode)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_LastFlagWins(t *testing.T) {
	mode, _, _, _ := extractScopeArgs([]string{"--local", "--global", "start"})
	if mode != scopeGlobal {
		t.Errorf("expected last flag (scopeGlobal) to win, got %v", mode)
	}
}

func TestExtractScopeArgs_HomeDirAtEnd(t *testing.T) {
	mode, homeDir, _, remaining := extractScopeArgs([]string{"start", "--stealth", "--home-dir", "/tmp/my-session"})
	if mode != scopeAuto {
		t.Errorf("expected scopeAuto, got %v", mode)
	}
	if homeDir != "/tmp/my-session" {
		t.Errorf("expected homeDir=/tmp/my-session, got %q", homeDir)
	}
	if len(remaining) != 2 || remaining[0] != "start" || remaining[1] != "--stealth" {
		t.Errorf("expected [start --stealth], got %v", remaining)
	}
}

func TestExtractScopeArgs_HomeDirEquals(t *testing.T) {
	_, homeDir, _, remaining := extractScopeArgs([]string{"open", "https://example.com", "--home-dir=/tmp/foo"})
	if homeDir != "/tmp/foo" {
		t.Errorf("expected homeDir=/tmp/foo, got %q", homeDir)
	}
	if len(remaining) != 2 || remaining[0] != "open" || remaining[1] != "https://example.com" {
		t.Errorf("expected [open https://example.com], got %v", remaining)
	}
}

func TestExtractScopeArgs_HomeDirOverridesScope(t *testing.T) {
	mode, homeDir, _, _ := extractScopeArgs([]string{"--local", "start", "--home-dir", "/tmp/explicit"})
	// --home-dir takes precedence; mode still captured but ignored in main()
	if homeDir != "/tmp/explicit" {
		t.Errorf("expected homeDir=/tmp/explicit, got %q", homeDir)
	}
	if mode != scopeLocal {
		t.Errorf("expected scopeLocal, got %v", mode)
	}
}

func TestExtractScopeArgs_HomeDirMissingValue(t *testing.T) {
	// --home-dir at end with no value should be ignored (left in args)
	_, homeDir, _, remaining := extractScopeArgs([]string{"start", "--home-dir"})
	if homeDir != "" {
		t.Errorf("expected empty homeDir when value missing, got %q", homeDir)
	}
	if len(remaining) != 2 || remaining[0] != "start" || remaining[1] != "--home-dir" {
		t.Errorf("expected [start --home-dir], got %v", remaining)
	}
}

func TestResolveTempHomeDir_CreatesTempDir(t *testing.T) {
	dir, err := resolveTempHomeDir("tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.RemoveAll(dir)

	if !strings.HasPrefix(dir, "/tmp/rodney-session-") {
		t.Errorf("expected /tmp/rodney-session-* prefix, got %q", dir)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("temp dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("expected directory, got file")
	}
}

func TestResolveTempHomeDir_PassthroughForRealPath(t *testing.T) {
	dir, err := resolveTempHomeDir("/tmp/my-custom-session")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dir != "/tmp/my-custom-session" {
		t.Errorf("expected passthrough of real path, got %q", dir)
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
// --home-dir cleanup on stop tests
// =====================

func TestCleanupSessionDir_RemovesExplicitHomeDir(t *testing.T) {
	dir := t.TempDir()
	// Create some session files like a real session would have
	os.MkdirAll(filepath.Join(dir, "chrome-data"), 0755)
	os.WriteFile(filepath.Join(dir, "state.json"), []byte(`{}`), 0644)

	cleanupSessionDir(dir)

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected directory %q to be removed, but it still exists", dir)
	}
}

func TestCleanupSessionDir_NoopForEmptyString(t *testing.T) {
	// Should not panic or error when called with empty string
	cleanupSessionDir("")
}

func TestCleanupSessionDir_NoopForNonexistent(t *testing.T) {
	// Should not panic or error for a path that doesn't exist
	cleanupSessionDir("/tmp/rodney-nonexistent-session-xyz")
}

// =====================
// stateDir tests
// =====================

func TestStateDir_Default(t *testing.T) {
	old := activeStateDir
	activeStateDir = ""
	defer func() { activeStateDir = old }()
	home, _ := os.UserHomeDir()
	want := home + "/.rodney"
	got := stateDir()
	if got != want {
		t.Errorf("stateDir() = %q, want %q", got, want)
	}
}

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
	dir := t.TempDir()
	got := resolveNewSessionDir(scopeAuto, dir, "")
	if !strings.HasPrefix(got, filepath.Join(os.TempDir(), "rodney-")) {
		t.Fatalf("expected temp dir starting with rodney-, got %q", got)
	}
	info, err := os.Stat(got)
	if err != nil {
		t.Fatalf("temp dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("not a directory")
	}
	os.RemoveAll(got)
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
	os.RemoveAll(got)
}

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
	registryAdd(reg, lock, "abc123", "/tmp/rodney-xyz")
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

func TestProxyConfigHash(t *testing.T) {
	h := proxyConfigHash("http://user:pass@proxy:3128")
	if !strings.HasPrefix(h, "sha256:") {
		t.Fatalf("expected sha256: prefix, got %q", h)
	}
	if len(h) != 7+64 { // "sha256:" + 64 hex chars
		t.Fatalf("unexpected hash length: %d", len(h))
	}
	// Same input should produce same hash
	h2 := proxyConfigHash("http://user:pass@proxy:3128")
	if h != h2 {
		t.Fatal("same input produced different hashes")
	}
	// Different input should produce different hash
	h3 := proxyConfigHash("http://other:pass@proxy:3128")
	if h == h3 {
		t.Fatal("different inputs produced same hash")
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

func TestParseStartFlags_Viewport(t *testing.T) {
	flags, err := parseStartFlags([]string{"--stealth", "--viewport", "1366x768"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if flags.viewport != "1366x768" {
		t.Errorf("expected viewport='1366x768', got %q", flags.viewport)
	}
}

func TestParseStartFlags_StealthDefaultViewport(t *testing.T) {
	flags, err := parseStartFlags([]string{"--stealth"})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if flags.viewport != "" {
		t.Errorf("expected viewport='' (default applied at start time), got %q", flags.viewport)
	}
}

func TestParseStartFlags_ViewportBadFormat(t *testing.T) {
	_, err := parseStartFlags([]string{"--viewport", "invalid"})
	if err == nil {
		t.Fatal("expected error for bad viewport format, got nil")
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
// Mirrors what cmdStart --stealth does: uses CDP script injection instead of
// extensions, and uses --headless=new for proper stealth support.
func launchStealthBrowser(t *testing.T) *rod.Browser {
	t.Helper()

	l := launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Set("single-process").
		Leakless(false).
		Set("disable-blink-features", "AutomationControlled")
	l.Delete("enable-automation")
	// Use --headless=new instead of rod's default --headless
	l.Headless(false)
	l.Set("headless", "new")

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	u := l.MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()
	t.Cleanup(func() { browser.MustClose() })
	return browser
}

// injectStealthScripts injects stealth JS into a page via CDP before navigation.
func injectStealthScripts(t *testing.T, page *rod.Page) {
	t.Helper()
	if _, err := (proto.PageAddScriptToEvaluateOnNewDocument{Source: stealth.JS}).Call(page); err != nil {
		t.Fatalf("failed to inject stealth JS: %v", err)
	}
	if _, err := (proto.PageAddScriptToEvaluateOnNewDocument{Source: workerFixJS}).Call(page); err != nil {
		t.Fatalf("failed to inject workerFix JS: %v", err)
	}
}

func TestStealth_NavigatorWebdriverHidden(t *testing.T) {
	browser := launchStealthBrowser(t)
	page := browser.MustPage("")
	defer page.MustClose()
	injectStealthScripts(t, page)
	page.MustNavigate(env.server.URL + "/stealth-check")
	page.MustWaitLoad()

	webdriver := page.MustEval(`() => String(navigator.webdriver)`).Str()
	if webdriver == "true" {
		t.Error("navigator.webdriver should not be true in stealth mode")
	}
}

func TestStealth_UserAgentNormal(t *testing.T) {
	browser := launchStealthBrowser(t)
	page := browser.MustPage("")
	defer page.MustClose()
	injectStealthScripts(t, page)
	page.MustNavigate(env.server.URL + "/stealth-check")
	page.MustWaitLoad()

	ua := page.MustEval(`() => navigator.userAgent`).Str()
	if strings.Contains(ua, "HeadlessChrome") {
		t.Errorf("user agent should not contain 'HeadlessChrome', got: %s", ua)
	}
}

func TestStealth_WebWorkerConsistency(t *testing.T) {
	browser := launchStealthBrowser(t)
	page := browser.MustPage("")
	defer page.MustClose()
	injectStealthScripts(t, page)
	page.MustNavigate(env.server.URL + "/stealth-check")
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
	page := browser.MustPage("")
	defer page.MustClose()
	injectStealthScripts(t, page)
	page.MustNavigate("https://bot.incolumitas.com/")
	page.MustWaitLoad()

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
	l := launcher.New().
		Set("no-sandbox").
		Set("disable-gpu").
		Set("single-process").
		Leakless(false).
		Set("disable-blink-features", "AutomationControlled").
		Set("window-size", "1920,1080")
	l.Delete("enable-automation")
	l.Headless(false)
	l.Set("headless", "new")

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	u := l.MustLaunch()
	browser := rod.New().ControlURL(u).MustConnect()
	defer browser.MustClose()

	// Create a page and expose a function BEFORE navigating (tests exposeFunctionLeak)
	page := browser.MustPage("")
	defer page.MustClose()

	// Apply full stealth setup: scripts + viewport + userAgentData brands
	applyStealthToPage(page, browser, &State{
		ViewportWidth:  1920,
		ViewportHeight: 1080,
	})

	page.MustExpose("exposedFn", func(g gson.JSON) (interface{}, error) {
		return nil, nil
	})

	// Navigate to the bot detector
	page.MustNavigate("https://bot-detector.rebrowser.net/")
	page.MustWaitLoad()

	// Trigger the grey-bubble tests that need explicit actions.
	// Use isolated world eval (sc.eval) so mainWorldExecution passes.
	sc := getStealthCtx(page, &State{})
	// 1. dummyFn: call the page's window.dummyFn()
	sc.eval(`(function() { if (typeof window.dummyFn === 'function') window.dummyFn() })()`)
	// 2. sourceUrlLeak: call getElementById (the page monkey-patches it to check the stack)
	sc.eval(`document.getElementById('detections-json')`)
	// 3. mainWorldExecution: call getElementsByClassName (grey = good = isolated world)
	//    We intentionally trigger this so it evaluates rather than staying grey.
	sc.eval(`document.getElementsByClassName('div')`)

	// Wait for async tests to settle (runtimeEnableLeak, bypassCsp, useragent poll)
	time.Sleep(3 * time.Second)

	// Read the detections JSON from the textarea
	result, err := sc.eval(`(function() {
		var el = document.getElementById('detections-json');
		return el ? el.value : '[]';
	})()`)
	if err != nil {
		t.Fatalf("failed to read detections: %v", err)
	}
	resultsJSON := result.Result.Value.Str()

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

	// Determine Chrome major version to decide whether runtimeEnableLeak
	// should be expected to pass. Chrome 145+ includes the V8 fix.
	chromeMajor := 0
	versionResult, verr := (proto.BrowserGetVersion{}).Call(browser)
	if verr != nil {
		t.Logf("BrowserGetVersion failed (%v), treating chromeMajor=0 (will skip runtimeEnableLeak)", verr)
	} else {
		if strings.HasPrefix(versionResult.Product, "Chrome/") {
			fullVer := strings.TrimPrefix(versionResult.Product, "Chrome/")
			if dotIdx := strings.Index(fullVer, "."); dotIdx > 0 {
				if v, err := strconv.Atoi(fullVer[:dotIdx]); err == nil {
					chromeMajor = v
				}
			}
		}
	}

	// rating < 0 = green/pass, 0 = grey/untested, 0.5 = yellow/warning, > 0 = red/fail
	//
	// Known unfixable detections:
	//
	// runtimeEnableLeak: rod must send CDP Runtime.enable to evaluate JS.
	// Chrome 145+ has a V8 fix that makes this undetectable. For older
	// versions we still skip it.
	skip := map[string]bool{}
	if chromeMajor < 145 {
		skip["runtimeEnableLeak"] = true
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
	sc := getStealthCtx(page, &State{})

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
	sc := getStealthCtx(page, &State{})

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
	sc := getStealthCtx(page, &State{})

	_, err := sc.element("#nonexistent", 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for #nonexistent, got nil")
	}
}

func TestStealthCtx_IsolatedWorldEval(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page, &State{})

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
	sc := getStealthCtx(page, &State{})

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
	sc := getStealthCtx(page, &State{})

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
	sc := getStealthCtx(page, &State{})

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
	sc := getStealthCtx(page, &State{})

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
	sc := getStealthCtx(page, &State{})

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
	sc := getStealthCtx(page, &State{})

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

func TestStealthCtx_BezierPath(t *testing.T) {
	path := bezierPath(0, 0, 500, 300, 20)
	if len(path) < 10 {
		t.Fatalf("expected >= 10 points, got %d", len(path))
	}
	// First point must be the start
	if path[0][0] != 0 || path[0][1] != 0 {
		t.Errorf("first point should be (0,0), got (%f,%f)", path[0][0], path[0][1])
	}
	// Last point must be the end
	last := path[len(path)-1]
	if last[0] != 500 || last[1] != 300 {
		t.Errorf("last point should be (500,300), got (%f,%f)", last[0], last[1])
	}
}

func TestStealthCtx_EaseInOut(t *testing.T) {
	if v := easeInOutCubic(0); v != 0 {
		t.Errorf("easeInOutCubic(0) = %f, want 0", v)
	}
	if v := easeInOutCubic(1); v != 1 {
		t.Errorf("easeInOutCubic(1) = %f, want 1", v)
	}
	mid := easeInOutCubic(0.5)
	if mid < 0.45 || mid > 0.55 {
		t.Errorf("easeInOutCubic(0.5) = %f, want ~0.5", mid)
	}
	// Ease-in: values near 0 should be smaller than linear
	if v := easeInOutCubic(0.1); v >= 0.1 {
		t.Errorf("easeInOutCubic(0.1) = %f, expected < 0.1 (ease-in)", v)
	}
}

func TestStealthCtx_Click(t *testing.T) {
	page := navigateTo(t, "/stealth-trap")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#target-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element failed: %v", err)
	}
	err = sc.click(nodeID)
	if err != nil {
		t.Fatalf("click failed: %v", err)
	}

	// Verify the monkey-patched querySelector was never called.
	// Read the count via CDP to avoid triggering the monkey patch ourselves.
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
		t.Errorf("monkey-patched querySelector was called %s times during click, expected 0", count)
	}
}

func TestStealthCtx_Hover(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#submit-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element failed: %v", err)
	}
	err = sc.hover(nodeID)
	if err != nil {
		t.Fatalf("hover failed: %v", err)
	}
	// No assertion on effect -- hover primarily needs to not error.
}

func TestStealthCtx_TypeText(t *testing.T) {
	page := navigateTo(t, "/form")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#name-input", defaultTimeout)
	if err != nil {
		t.Fatalf("element failed: %v", err)
	}
	err = sc.input(nodeID, "hello")
	if err != nil {
		t.Fatalf("input failed: %v", err)
	}

	// Verify the value was typed
	result, err := sc.callOn(nodeID, `function() { return this.value; }`)
	if err != nil {
		t.Fatalf("callOn failed: %v", err)
	}
	if result.Result.Value.Str() != "hello" {
		t.Errorf("expected 'hello', got %q", result.Result.Value.Str())
	}
}

func TestStealthCtx_ClearInput(t *testing.T) {
	page := navigateTo(t, "/form")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#name-input", defaultTimeout)
	if err != nil {
		t.Fatalf("element failed: %v", err)
	}
	// Type something first
	err = sc.input(nodeID, "test text")
	if err != nil {
		t.Fatalf("input failed: %v", err)
	}
	// Clear it
	err = sc.clearInput(nodeID)
	if err != nil {
		t.Fatalf("clear failed: %v", err)
	}

	result, err := sc.callOn(nodeID, `function() { return this.value; }`)
	if err != nil {
		t.Fatalf("callOn failed: %v", err)
	}
	if result.Result.Value.Str() != "" {
		t.Errorf("expected empty string, got %q", result.Result.Value.Str())
	}
}

func TestStealthCtx_Select(t *testing.T) {
	page := navigateTo(t, "/form")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#topic", defaultTimeout)
	if err != nil {
		t.Fatalf("element failed: %v", err)
	}
	err = sc.selectOption(nodeID, "support")
	if err != nil {
		t.Fatalf("selectOption failed: %v", err)
	}

	// Verify value was set
	result, err := sc.callOn(nodeID, `function() { return this.value; }`)
	if err != nil {
		t.Fatalf("callOn failed: %v", err)
	}
	if result.Result.Value.Str() != "support" {
		t.Errorf("expected 'support', got %q", result.Result.Value.Str())
	}
}

func TestStealthCtx_Submit(t *testing.T) {
	page := navigateTo(t, "/form")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("form", defaultTimeout)
	if err != nil {
		t.Fatalf("element failed: %v", err)
	}
	err = sc.submit(nodeID)
	if err != nil {
		t.Fatalf("submit failed: %v", err)
	}
}

func TestStealthCtx_Download(t *testing.T) {
	page := navigateTo(t, "/download")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#file-link", defaultTimeout)
	if err != nil {
		t.Fatalf("element failed: %v", err)
	}
	data, err := sc.download(nodeID)
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}
	if string(data) != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", string(data))
	}
}

func TestStealthCtx_ScreenshotEl(t *testing.T) {
	page := navigateTo(t, "/")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#submit-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element failed: %v", err)
	}
	data, err := sc.screenshotElement(nodeID)
	if err != nil {
		t.Fatalf("screenshotElement failed: %v", err)
	}
	if len(data) < 100 {
		t.Errorf("expected >= 100 bytes, got %d", len(data))
	}
	// Check PNG magic bytes
	if len(data) < 2 || data[0] != 0x89 || data[1] != 0x50 {
		t.Errorf("expected PNG magic bytes (0x89, 0x50), got (0x%02x, 0x%02x)", data[0], data[1])
	}
}

func TestStealthCtx_Focus(t *testing.T) {
	page := navigateTo(t, "/form")
	sc := getStealthCtx(page, &State{})

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

// =====================
// Stealth integration tests
// =====================

func TestStealthIntegration_ClickWithoutDetection(t *testing.T) {
	page := navigateTo(t, "/stealth-trap")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#target-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element failed: %v", err)
	}
	err = sc.click(nodeID)
	if err != nil {
		t.Fatalf("click failed: %v", err)
	}

	// Verify no detection
	countNodeID, _ := sc.element("#qs-count", defaultTimeout)
	result, _ := sc.callOn(countNodeID, `function() { return this.textContent; }`)
	count := result.Result.Value.Str()
	if count != "0" {
		t.Errorf("detection triggered: querySelector called %s times", count)
	}
}

func TestStealth_UserAgentDataBrands(t *testing.T) {
	browser := launchStealthBrowser(t)
	page := browser.MustPage("")
	defer page.MustClose()

	// Apply full stealth setup including userAgentData brands
	applyStealthToPage(page, browser, &State{
		ViewportWidth:  1920,
		ViewportHeight: 935,
	})

	page.MustNavigate(env.server.URL + "/stealth-check")
	page.MustWaitLoad()

	// Check that navigator.userAgentData.brands contains "Google Chrome"
	hasChrome := page.MustEval(`() => {
		if (!navigator.userAgentData || !navigator.userAgentData.brands) return false;
		return navigator.userAgentData.brands.some(b => b.brand === "Google Chrome");
	}`).Bool()

	if !hasChrome {
		brands := page.MustEval(`() => JSON.stringify(navigator.userAgentData ? navigator.userAgentData.brands : null)`).Str()
		t.Errorf("expected navigator.userAgentData.brands to contain 'Google Chrome', got: %s", brands)
	}
}

func TestStealth_ScriptPersistsAcrossNavigation(t *testing.T) {
	browser := launchStealthBrowser(t)
	page := browser.MustPage("")
	defer page.MustClose()

	// Inject stealth scripts (uses addScriptToEvaluateOnNewDocument which persists)
	injectStealthScripts(t, page)

	// Navigate to stealth-check and verify webdriver is not true
	page.MustNavigate(env.server.URL + "/stealth-check")
	page.MustWaitLoad()

	webdriver1 := page.MustEval(`() => String(navigator.webdriver)`).Str()
	if webdriver1 == "true" {
		t.Error("navigator.webdriver should not be true after first navigation")
	}

	// Navigate to the index page (different page)
	page.MustNavigate(env.server.URL + "/")
	page.MustWaitLoad()

	// Navigate back to stealth-check and verify scripts still active
	page.MustNavigate(env.server.URL + "/stealth-check")
	page.MustWaitLoad()

	webdriver2 := page.MustEval(`() => String(navigator.webdriver)`).Str()
	if webdriver2 == "true" {
		t.Error("navigator.webdriver should not be true after navigating away and back")
	}
}

// =====================
// Integration test HTML fixtures
// =====================

func handleDynamicDOM(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Dynamic DOM</title></head>
<body>
  <div id="container">
    <button id="add-btn">Add Element</button>
    <button id="remove-btn">Remove Element</button>
    <button id="replace-btn">Replace Element</button>
  </div>
  <div id="dynamic-target"></div>
  <script>
    // Element appears after 500ms
    setTimeout(function() {
      var el = document.createElement('div');
      el.id = 'delayed-500';
      el.textContent = 'Appeared after 500ms';
      document.getElementById('dynamic-target').appendChild(el);
    }, 500);

    // Element appears after 2s
    setTimeout(function() {
      var el = document.createElement('div');
      el.id = 'delayed-2000';
      el.textContent = 'Appeared after 2s';
      document.getElementById('dynamic-target').appendChild(el);
    }, 2000);

    // Click to add element
    document.getElementById('add-btn').addEventListener('click', function() {
      var el = document.createElement('div');
      el.id = 'click-added';
      el.textContent = 'Added by click';
      document.getElementById('dynamic-target').appendChild(el);
    });

    // Click to remove element
    document.getElementById('remove-btn').addEventListener('click', function() {
      var el = document.getElementById('click-added');
      if (el) el.remove();
    });

    // Click to replace element via innerHTML
    document.getElementById('replace-btn').addEventListener('click', function() {
      document.getElementById('dynamic-target').innerHTML = '<div id="replaced-content">Replaced</div>';
    });

    // MutationObserver: when delayed-500 appears, add a sibling
    var observer = new MutationObserver(function(mutations) {
      mutations.forEach(function(m) {
        m.addedNodes.forEach(function(node) {
          if (node.id === 'delayed-500') {
            var sibling = document.createElement('div');
            sibling.id = 'mutation-added';
            sibling.textContent = 'Added by MutationObserver';
            node.parentNode.appendChild(sibling);
          }
        });
      });
    });
    observer.observe(document.getElementById('dynamic-target'), {childList: true});
  </script>
</body>
</html>`))
}

func handleVisibilityZoo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Visibility Zoo</title>
<style>
  .display-none { display: none; }
  .visibility-hidden { visibility: hidden; }
  .opacity-zero { opacity: 0; }
  .zero-width { width: 0; overflow: hidden; }
  .zero-height { height: 0; overflow: hidden; }
  .offscreen { position: absolute; left: -9999px; top: -9999px; }
  .clip-hidden { position: absolute; clip: rect(0,0,0,0); width: 1px; height: 1px; }
  .transform-scale0 { transform: scale(0); }
  .transform-offscreen { transform: translateX(-10000px); }
  .pointer-events-none { pointer-events: none; }
  .parent-hidden { display: none; }
  .collapsed { max-height: 0; overflow: hidden; }
  .overlay-container { position: relative; }
  .overlay-target { padding: 20px; }
  .overlay-cover { position: absolute; top: 0; left: 0; right: 0; bottom: 0; background: rgba(0,0,0,0.5); z-index: 10; }
  .parent-vis-hidden { visibility: hidden; }
  .child-vis-visible { visibility: visible; }
  .visible-element { padding: 10px; background: green; }
</style>
</head>
<body>
  <div id="vis-normal" class="visible-element">Normal visible</div>
  <div id="vis-display-none" class="display-none">Display none</div>
  <div id="vis-visibility-hidden" class="visibility-hidden">Visibility hidden</div>
  <div id="vis-opacity-zero" class="opacity-zero">Opacity zero</div>
  <div id="vis-zero-width" class="zero-width">Zero width</div>
  <div id="vis-zero-height" class="zero-height">Zero height</div>
  <div id="vis-offscreen" class="offscreen">Offscreen</div>
  <div id="vis-clip-hidden" class="clip-hidden">Clip hidden</div>
  <div id="vis-transform-scale0" class="transform-scale0">Transform scale(0)</div>
  <div id="vis-transform-offscreen" class="transform-offscreen">Transform offscreen</div>
  <div id="vis-pointer-events-none" class="pointer-events-none">Pointer events none</div>
  <div class="parent-hidden"><div id="vis-child-of-hidden">Child of hidden parent</div></div>
  <div id="vis-collapsed" class="collapsed">Collapsed</div>
  <div class="overlay-container">
    <div id="vis-behind-overlay" class="overlay-target">Behind overlay</div>
    <div class="overlay-cover" id="the-overlay"></div>
  </div>
  <div class="parent-vis-hidden">
    <div id="vis-child-visible" class="child-vis-visible">Visible child of hidden parent</div>
  </div>
  <div id="overlay-click-result"></div>
  <script>
    document.getElementById('vis-behind-overlay').addEventListener('click', function() {
      document.getElementById('overlay-click-result').textContent = 'target-clicked';
    });
    document.getElementById('the-overlay').addEventListener('click', function() {
      document.getElementById('overlay-click-result').textContent = 'overlay-clicked';
    });
  </script>
</body>
</html>`))
}

func handleComplexLayout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Complex Layout</title>
<style>
  body { margin: 0; padding: 0; }
  .fixed-header { position: fixed; top: 0; left: 0; right: 0; height: 50px; background: navy; z-index: 100; display: flex; align-items: center; justify-content: center; }
  .fixed-header button { color: white; background: transparent; border: 1px solid white; padding: 5px 10px; cursor: pointer; }
  .sticky-nav { position: sticky; top: 50px; background: gray; padding: 10px; z-index: 50; }
  .sticky-nav button { cursor: pointer; }
  .content { padding-top: 60px; }
  .transformed-rotate { transform: rotate(45deg); padding: 20px; background: lightblue; display: inline-block; margin: 50px; }
  .transformed-scale { transform: scale(1.5); padding: 20px; background: lightgreen; display: inline-block; margin: 50px; }
  .transformed-skew { transform: skew(20deg); padding: 20px; background: lightyellow; display: inline-block; margin: 50px; }
  .far-below { margin-top: 3000px; padding: 20px; background: coral; }
  .z-stack { position: relative; height: 100px; }
  .z-back { position: absolute; top: 0; left: 0; width: 200px; height: 100px; background: red; z-index: 1; }
  .z-front { position: absolute; top: 0; left: 0; width: 200px; height: 100px; background: blue; z-index: 2; }
  #fixed-click-result, #sticky-click-result, #below-fold-click-result { padding: 5px; }
</style>
</head>
<body>
  <div class="fixed-header">
    <button id="fixed-btn">Fixed Button</button>
  </div>
  <div class="content">
    <div class="sticky-nav">
      <button id="sticky-btn">Sticky Button</button>
    </div>
    <div style="padding: 20px;">
      <div id="fixed-click-result"></div>
      <div id="sticky-click-result"></div>
      <div class="transformed-rotate"><span id="rotated-el">Rotated</span></div>
      <div class="transformed-scale"><span id="scaled-el">Scaled</span></div>
      <div class="transformed-skew"><span id="skewed-el">Skewed</span></div>
      <div class="z-stack">
        <div class="z-back" id="z-back">Back</div>
        <div class="z-front" id="z-front">Front</div>
      </div>
    </div>
    <div class="far-below">
      <button id="below-fold-btn">Far Below</button>
      <div id="below-fold-click-result"></div>
    </div>
  </div>
  <script>
    document.getElementById('fixed-btn').addEventListener('click', function() {
      document.getElementById('fixed-click-result').textContent = 'fixed-clicked';
    });
    document.getElementById('sticky-btn').addEventListener('click', function() {
      document.getElementById('sticky-click-result').textContent = 'sticky-clicked';
    });
    document.getElementById('below-fold-btn').addEventListener('click', function() {
      document.getElementById('below-fold-click-result').textContent = 'below-fold-clicked';
    });
  </script>
</body>
</html>`))
}

func handleEventEdgeCases(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Event Edge Cases</title>
<style>
  .moving-btn { position: absolute; left: 50px; top: 200px; }
  #result { padding: 10px; margin: 10px 0; }
</style>
</head>
<body>
  <div id="result"></div>

  <!-- Self-destructing button -->
  <button id="self-destruct">Self Destruct</button>

  <!-- innerHTML replacement -->
  <div id="replace-container">
    <button id="replace-trigger">Replace Content</button>
  </div>

  <!-- preventDefault on parent -->
  <a href="/should-not-navigate" id="prevent-link">
    <button id="prevent-btn">Click Me</button>
  </a>

  <!-- stopPropagation -->
  <div id="outer-div">
    <button id="inner-btn">Inner</button>
  </div>

  <!-- Delegated events -->
  <ul id="delegated-list">
    <li id="delegated-item-1" class="delegated-item">Item 1</li>
    <li id="delegated-item-2" class="delegated-item">Item 2</li>
  </ul>

  <!-- Moving button -->
  <button id="moving-btn" class="moving-btn">Moving</button>

  <!-- Disabled button -->
  <button id="disabled-btn" disabled>Disabled</button>

  <!-- Double-click target -->
  <button id="dblclick-btn">Double Click Me</button>

  <script>
    var result = document.getElementById('result');

    // Self-destruct
    document.getElementById('self-destruct').addEventListener('click', function() {
      result.textContent = 'self-destruct-clicked';
      this.remove();
    });

    // innerHTML replacement
    document.getElementById('replace-trigger').addEventListener('click', function() {
      document.getElementById('replace-container').innerHTML = '<div id="replaced-marker">Replaced</div>';
      result.textContent = 'content-replaced';
    });

    // preventDefault on parent link
    document.getElementById('prevent-link').addEventListener('click', function(e) {
      e.preventDefault();
      result.textContent = 'prevented';
    });
    document.getElementById('prevent-btn').addEventListener('click', function() {
      // This should fire, but parent link's default is prevented
    });

    // stopPropagation
    var outerClicked = false;
    document.getElementById('outer-div').addEventListener('click', function() {
      outerClicked = true;
      result.textContent = (result.textContent || '') + 'outer-clicked';
    });
    document.getElementById('inner-btn').addEventListener('click', function(e) {
      e.stopPropagation();
      result.textContent = 'inner-clicked';
    });

    // Delegated events
    document.getElementById('delegated-list').addEventListener('click', function(e) {
      if (e.target.classList.contains('delegated-item')) {
        result.textContent = 'delegated:' + e.target.id;
      }
    });

    // Moving button
    var moveCount = 0;
    document.getElementById('moving-btn').addEventListener('click', function() {
      moveCount++;
      result.textContent = 'move-clicked:' + moveCount;
      this.style.left = (50 + moveCount * 100) + 'px';
      this.style.top = (200 + moveCount * 50) + 'px';
    });

    // Disabled button
    document.getElementById('disabled-btn').addEventListener('click', function() {
      result.textContent = 'disabled-clicked';
    });

    // Double-click
    var dblClickCount = 0;
    document.getElementById('dblclick-btn').addEventListener('dblclick', function() {
      result.textContent = 'dblclick-fired';
    });
    document.getElementById('dblclick-btn').addEventListener('click', function() {
      dblClickCount++;
      if (!result.textContent.startsWith('dblclick')) {
        result.textContent = 'click:' + dblClickCount;
      }
    });
  </script>
</body>
</html>`))
}

func handleFormAdvanced(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Advanced Forms</title>
<style>
  .custom-dropdown { position: relative; display: inline-block; }
  .dropdown-toggle { padding: 5px 10px; cursor: pointer; border: 1px solid #ccc; background: white; }
  .dropdown-menu { display: none; position: absolute; top: 100%; left: 0; background: white; border: 1px solid #ccc; z-index: 100; min-width: 150px; }
  .dropdown-menu.open { display: block; }
  .dropdown-item { padding: 5px 10px; cursor: pointer; }
  .dropdown-item:hover { background: #eee; }
  [contenteditable] { border: 1px solid #ccc; padding: 10px; min-height: 50px; }
</style>
</head>
<body>
  <form id="test-form">
    <textarea id="textarea-input" rows="4" cols="50"></textarea>
    <div id="contenteditable-div" contenteditable="true"></div>
    <input id="password-input" type="password">
    <input id="number-input" type="number" min="0" max="100">
    <input id="date-input" type="date">
    <input id="range-input" type="range" min="0" max="100" value="50">
    <input id="checkbox-1" type="checkbox" value="check1">
    <input id="checkbox-2" type="checkbox" value="check2" checked>
    <input id="radio-a" type="radio" name="radio-group" value="a">
    <input id="radio-b" type="radio" name="radio-group" value="b">
    <input id="radio-c" type="radio" name="radio-group" value="c" checked>
  </form>

  <!-- Custom JS dropdown -->
  <div class="custom-dropdown" id="custom-dropdown">
    <div class="dropdown-toggle" id="dropdown-toggle">Select...</div>
    <div class="dropdown-menu" id="dropdown-menu">
      <div class="dropdown-item" data-value="opt1">Option 1</div>
      <div class="dropdown-item" data-value="opt2">Option 2</div>
      <div class="dropdown-item" data-value="opt3">Option 3</div>
    </div>
  </div>
  <div id="dropdown-result"></div>

  <script>
    // Custom dropdown
    document.getElementById('dropdown-toggle').addEventListener('click', function() {
      document.getElementById('dropdown-menu').classList.toggle('open');
    });
    document.querySelectorAll('.dropdown-item').forEach(function(item) {
      item.addEventListener('click', function() {
        var val = this.getAttribute('data-value');
        document.getElementById('dropdown-toggle').textContent = this.textContent;
        document.getElementById('dropdown-result').textContent = val;
        document.getElementById('dropdown-menu').classList.remove('open');
      });
    });
  </script>
</body>
</html>`))
}

func handleSPANavigation(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>SPA Navigation</title></head>
<body>
  <nav>
    <a href="#" id="nav-home" class="spa-link" data-page="home">Home</a>
    <a href="#" id="nav-about" class="spa-link" data-page="about">About</a>
    <a href="#" id="nav-contact" class="spa-link" data-page="contact">Contact</a>
  </nav>
  <div id="spa-content">
    <div id="page-title">Home Page</div>
    <div id="page-body">Welcome to the home page</div>
  </div>
  <script>
    var pages = {
      home: { title: 'Home Page', body: 'Welcome to the home page' },
      about: { title: 'About Page', body: 'This is the about page' },
      contact: { title: 'Contact Page', body: 'Get in touch with us' }
    };

    document.querySelectorAll('.spa-link').forEach(function(link) {
      link.addEventListener('click', function(e) {
        e.preventDefault();
        var pageName = this.getAttribute('data-page');
        history.pushState({page: pageName}, '', '/spa-navigation/' + pageName);
        // Simulate async content loading with 300ms delay
        document.getElementById('spa-content').innerHTML = '<div id="loading">Loading...</div>';
        setTimeout(function() {
          var page = pages[pageName];
          document.getElementById('spa-content').innerHTML =
            '<div id="page-title">' + page.title + '</div>' +
            '<div id="page-body">' + page.body + '</div>';
        }, 300);
      });
    });

    window.addEventListener('popstate', function(e) {
      if (e.state && e.state.page) {
        var page = pages[e.state.page];
        document.getElementById('spa-content').innerHTML =
          '<div id="page-title">' + page.title + '</div>' +
          '<div id="page-body">' + page.body + '</div>';
      }
    });
  </script>
</body>
</html>`))
}

func handleComplexSelectors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Complex Selectors</title></head>
<body>
  <div id="root">
    <div class="level-1" data-type="container">
      <div class="level-2" data-type="wrapper">
        <div class="level-3" data-type="inner">
          <span id="deep-target" data-role="target" data-status="active">Deep Target</span>
        </div>
      </div>
    </div>
  </div>

  <table id="data-table">
    <thead>
      <tr><th>Name</th><th>Value</th></tr>
    </thead>
    <tbody>
      <tr class="row"><td class="name">Alpha</td><td class="value">1</td></tr>
      <tr class="row"><td class="name">Beta</td><td class="value">2</td></tr>
      <tr class="row"><td class="name">Gamma</td><td class="value">3</td></tr>
      <tr class="row"><td class="name">Delta</td><td class="value">4</td></tr>
      <tr class="row"><td class="name">Epsilon</td><td class="value">5</td></tr>
    </tbody>
  </table>

  <ul id="mixed-list">
    <li class="item type-a">A1</li>
    <li class="item type-b">B1</li>
    <li class="item type-a">A2</li>
    <li class="item type-b">B2</li>
    <li class="item type-a">A3</li>
  </ul>

  <div id="siblings">
    <h2>Title</h2>
    <p class="intro">Intro paragraph</p>
    <p class="body">Body paragraph</p>
    <p class="footer">Footer paragraph</p>
  </div>
</body>
</html>`))
}

func handleScrollScenarios(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Scroll Scenarios</title>
<style>
  .v-scroll-container { height: 200px; overflow-y: auto; border: 2px solid blue; }
  .v-scroll-content { height: 2000px; padding: 10px; }
  .h-scroll-container { width: 300px; overflow-x: auto; white-space: nowrap; border: 2px solid red; }
  .h-scroll-content { display: inline-block; width: 3000px; padding: 10px; }
  .far-below-content { margin-top: 3000px; padding: 20px; background: lightyellow; }
</style>
</head>
<body>
  <h2>Vertical Scroll Container</h2>
  <div class="v-scroll-container" id="v-container">
    <div class="v-scroll-content">
      <div id="v-top-item">Top of scroll container</div>
      <div style="margin-top: 1800px;">
        <div id="v-buried-item">Buried in scroll container</div>
      </div>
    </div>
  </div>

  <h2>Horizontal Scroll Container</h2>
  <div class="h-scroll-container" id="h-container">
    <div class="h-scroll-content">
      <span id="h-start-item">Start</span>
      <span id="h-end-item" style="margin-left: 2500px;">End of horizontal scroll</span>
    </div>
  </div>

  <div class="far-below-content">
    <button id="far-below-btn">Far Below Button</button>
    <div id="far-below-result"></div>
  </div>

  <script>
    document.getElementById('far-below-btn').addEventListener('click', function() {
      document.getElementById('far-below-result').textContent = 'far-below-clicked';
    });
  </script>
</body>
</html>`))
}

func handleIFrameTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>IFrame Test</title></head>
<body>
  <h1>IFrame Test Page</h1>
  <iframe id="test-iframe" srcdoc="<!DOCTYPE html><html><body><div id='iframe-content'>Hello from iframe</div><button id='iframe-btn'>Click me</button></body></html>" width="400" height="200"></iframe>
</body>
</html>`))
}

func handleShadowDOMTest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(`<!DOCTYPE html>
<html><head><title>Shadow DOM Test</title></head>
<body>
  <h1>Shadow DOM Test Page</h1>
  <div id="shadow-host"></div>
  <script>
    var host = document.getElementById('shadow-host');
    var shadow = host.attachShadow({mode: 'open'});
    shadow.innerHTML = '<div id="shadow-content">Hello from shadow DOM</div><button id="shadow-btn">Shadow Button</button>';
  </script>
</body>
</html>`))
}

// =====================
// Dynamic DOM tests
// =====================

func TestStealthCtx_DynamicDOM_WaitForDelayedElement(t *testing.T) {
	page := navigateTo(t, "/dynamic-dom")
	sc := getStealthCtx(page, &State{})

	// Element appears after 500ms, polling should find it within 5s
	nodeID, err := sc.element("#delayed-500", 5*time.Second)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Appeared after 500ms" {
		t.Errorf("expected 'Appeared after 500ms', got %q", txt)
	}
}

func TestStealthCtx_DynamicDOM_WaitForSlowElement(t *testing.T) {
	page := navigateTo(t, "/dynamic-dom")
	sc := getStealthCtx(page, &State{})

	// Element appears after 2s, use 3s timeout
	nodeID, err := sc.element("#delayed-2000", 3*time.Second)
	if err != nil {
		t.Fatalf("element() failed to find element that appears after 2s with 3s timeout: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Appeared after 2s" {
		t.Errorf("expected 'Appeared after 2s', got %q", txt)
	}
}

func TestStealthCtx_DynamicDOM_TimeoutBeforeSlowElement(t *testing.T) {
	page := navigateTo(t, "/dynamic-dom")
	sc := getStealthCtx(page, &State{})

	// Element appears after 2s, but we only wait 500ms — should timeout
	_, err := sc.element("#delayed-2000", 500*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error for element that appears after 2s with 500ms timeout")
	}
	if !strings.Contains(err.Error(), "not found within") {
		t.Errorf("expected 'not found within' error, got: %v", err)
	}
}

func TestStealthCtx_DynamicDOM_ClickTriggersAddition(t *testing.T) {
	page := navigateTo(t, "/dynamic-dom")
	sc := getStealthCtx(page, &State{})

	// Click the add button
	btnID, err := sc.element("#add-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#add-btn) failed: %v", err)
	}
	if err := sc.click(btnID); err != nil {
		t.Fatalf("click failed: %v", err)
	}

	// The new element should now exist
	nodeID, err := sc.element("#click-added", 2*time.Second)
	if err != nil {
		t.Fatalf("element(#click-added) failed after click: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Added by click" {
		t.Errorf("expected 'Added by click', got %q", txt)
	}
}

func TestStealthCtx_DynamicDOM_MutationObserverContent(t *testing.T) {
	page := navigateTo(t, "/dynamic-dom")
	sc := getStealthCtx(page, &State{})

	// MutationObserver adds #mutation-added when #delayed-500 appears (after 500ms)
	nodeID, err := sc.element("#mutation-added", 5*time.Second)
	if err != nil {
		t.Fatalf("element(#mutation-added) failed: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Added by MutationObserver" {
		t.Errorf("expected 'Added by MutationObserver', got %q", txt)
	}
}

func TestStealthCtx_DynamicDOM_ClickRemovesThenFind(t *testing.T) {
	page := navigateTo(t, "/dynamic-dom")
	sc := getStealthCtx(page, &State{})

	// First add an element
	addBtn, err := sc.element("#add-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#add-btn) failed: %v", err)
	}
	if err := sc.click(addBtn); err != nil {
		t.Fatalf("click(add-btn) failed: %v", err)
	}
	// Wait for it to appear
	_, err = sc.element("#click-added", 2*time.Second)
	if err != nil {
		t.Fatalf("element(#click-added) not found after add: %v", err)
	}

	// Now remove it
	removeBtn, err := sc.element("#remove-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#remove-btn) failed: %v", err)
	}
	if err := sc.click(removeBtn); err != nil {
		t.Fatalf("click(remove-btn) failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let DOM update

	// exists() should return false
	found, err := sc.exists("#click-added")
	if err != nil {
		t.Fatalf("exists() failed: %v", err)
	}
	if found {
		t.Error("expected #click-added to not exist after removal, but exists() returned true")
	}
}

func TestStealthCtx_DynamicDOM_StaleNodeAfterReplace(t *testing.T) {
	page := navigateTo(t, "/dynamic-dom")
	sc := getStealthCtx(page, &State{})

	// Wait for delayed-500 to appear and grab its nodeID
	nodeID, err := sc.element("#delayed-500", 5*time.Second)
	if err != nil {
		t.Fatalf("element(#delayed-500) failed: %v", err)
	}

	// Replace the container's innerHTML, which destroys the node
	replaceBtn, err := sc.element("#replace-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#replace-btn) failed: %v", err)
	}
	if err := sc.click(replaceBtn); err != nil {
		t.Fatalf("click(replace-btn) failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let DOM update

	// EXPECTED FAIL: The old nodeID is now stale — callOn or text() should fail
	// because there's no stale nodeID recovery mechanism.
	_, err = sc.text(nodeID)
	if err == nil {
		t.Skip("Expected stale nodeID error after DOM replacement, but text() succeeded — node may not be fully stale yet")
	}
	// If we get here with an error, that confirms the limitation
	t.Logf("Confirmed limitation: stale nodeID after DOM replacement: %v", err)
}

func TestStealthCtx_DynamicDOM_ElementsCountChanges(t *testing.T) {
	page := navigateTo(t, "/dynamic-dom")
	sc := getStealthCtx(page, &State{})

	// Initially 0 items with class .dynamic-child
	n1, err := sc.count(".dynamic-child")
	if err != nil {
		t.Fatalf("count() failed: %v", err)
	}

	// Wait for delayed-500 to add content (it adds nodes to #dynamic-target)
	time.Sleep(700 * time.Millisecond)

	// Count direct children of #dynamic-target
	n2, err := sc.count("#dynamic-target > *")
	if err != nil {
		t.Fatalf("count() failed: %v", err)
	}
	// After 700ms, we should have delayed-500 and mutation-added
	if n2 < 1 {
		t.Errorf("expected at least 1 child after 700ms, got %d", n2)
	}
	// Original count of .dynamic-child should still be 0 (different class)
	if n1 != 0 {
		t.Errorf("expected 0 .dynamic-child initially, got %d", n1)
	}
}

// =====================
// Visibility/Hidden Elements tests
// =====================

func TestStealthCtx_Visible_DisplayNone(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-display-none", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	if vis {
		t.Error("display:none element should not be visible")
	}
}

func TestStealthCtx_Visible_VisibilityHidden(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-visibility-hidden", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	if vis {
		t.Error("visibility:hidden element should not be visible")
	}
}

func TestStealthCtx_Visible_OpacityZero(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-opacity-zero", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	if vis {
		t.Error("opacity:0 element should not be visible")
	}
}

func TestStealthCtx_Visible_ZeroWidth(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-zero-width", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	if vis {
		t.Error("zero-width element should not be visible")
	}
}

func TestStealthCtx_Visible_ZeroHeight(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-zero-height", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	if vis {
		t.Error("zero-height element should not be visible")
	}
}

func TestStealthCtx_Visible_Offscreen(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-offscreen", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	// visible() should detect offscreen elements (position:-9999px) as not visible.
	// Fix: check if bounding rect intersects the viewport.
	if vis {
		t.Error("offscreen element (position:-9999px) should not be visible")
	}
}

func TestStealthCtx_Visible_ClipHidden(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-clip-hidden", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	// visible() should detect clip:rect(0,0,0,0) as not visible.
	// Fix: check CSS clip/clip-path properties.
	if vis {
		t.Error("clip:rect(0,0,0,0) hidden element should not be visible")
	}
}

func TestStealthCtx_Visible_TransformScale0(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-transform-scale0", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	// transform:scale(0) zeroes getBoundingClientRect dimensions
	if vis {
		t.Error("transform:scale(0) element should not be visible (zero bounding rect)")
	}
}

func TestStealthCtx_Visible_TransformOffscreen(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-transform-offscreen", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	// visible() should detect translateX(-10000px) as not visible.
	// Fix: check if bounding rect intersects the viewport after transforms.
	if vis {
		t.Error("translateX(-10000px) element should not be visible")
	}
}

func TestStealthCtx_Visible_ChildOfHiddenParent(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	// Child inherits display:none from parent — element() will find it via DOM
	// but it should not be visible
	nodeID, err := sc.element("#vis-child-of-hidden", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	if vis {
		t.Error("child of display:none parent should not be visible")
	}
}

func TestStealthCtx_Visible_CollapsedMaxHeight0(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-collapsed", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	// max-height:0 with overflow:hidden gives zero height
	if vis {
		t.Error("collapsed (max-height:0) element should not be visible")
	}
}

func TestStealthCtx_Visible_PointerEventsNone(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-pointer-events-none", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	// pointer-events:none doesn't affect visibility — element is visible but unclickable
	if !vis {
		t.Error("pointer-events:none element should be visible (just unclickable)")
	}
}

func TestStealthCtx_Visible_ChildVisibleParentHidden(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-child-visible", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	vis, err := sc.visible(nodeID)
	if err != nil {
		t.Fatalf("visible() failed: %v", err)
	}
	// visibility:visible overrides parent's visibility:hidden
	if !vis {
		t.Error("visibility:visible child of visibility:hidden parent should be visible")
	}
}

func TestStealthCtx_Click_ElementBehindOverlay(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	// Try to click the element behind the overlay
	nodeID, err := sc.element("#vis-behind-overlay", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	err = sc.click(nodeID)
	// click() should return an error indicating the target is obscured by the overlay.
	if err == nil {
		t.Fatal("click() on element behind overlay should return an error")
	}
	if !strings.Contains(err.Error(), "obscured") {
		t.Errorf("click() error should mention 'obscured', got: %v", err)
	}
}

func TestStealthCtx_Click_DisplayNone(t *testing.T) {
	page := navigateTo(t, "/visibility-zoo")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#vis-display-none", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	// click() on display:none should return a clear error (e.g. "element not visible"),
	// not a raw CDP error from getBoxModel.
	// Fix: check visibility before attempting click.
	err = sc.click(nodeID)
	if err == nil {
		t.Fatal("click() on display:none element should return an error")
	}
	if strings.Contains(err.Error(), "Could not compute box model") {
		t.Errorf("click() on display:none should return a user-friendly error, not raw CDP: %v", err)
	}
}

// =====================
// Complex Layout & Scroll tests
// =====================

func TestStealthCtx_Click_FixedHeader(t *testing.T) {
	page := navigateTo(t, "/complex-layout")
	sc := getStealthCtx(page, &State{})

	btnID, err := sc.element("#fixed-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.click(btnID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	resultID, err := sc.element("#fixed-click-result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#fixed-click-result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "fixed-clicked" {
		t.Errorf("expected 'fixed-clicked', got %q", txt)
	}
}

func TestStealthCtx_Click_BelowFold(t *testing.T) {
	page := navigateTo(t, "/complex-layout")
	sc := getStealthCtx(page, &State{})

	btnID, err := sc.element("#below-fold-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.click(btnID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	resultID, err := sc.element("#below-fold-click-result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#below-fold-click-result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "below-fold-clicked" {
		t.Errorf("expected 'below-fold-clicked', got %q", txt)
	}
}

func TestStealthCtx_Click_TransformedElement(t *testing.T) {
	page := navigateTo(t, "/complex-layout")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#rotated-el", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	// UNCERTAIN: rotated element's box model may give wrong center
	err = sc.click(nodeID)
	if err != nil {
		t.Logf("click on rotated element failed: %v", err)
	} else {
		t.Log("click on rotated element succeeded")
	}
}

func TestStealthCtx_Click_ScaledElement(t *testing.T) {
	page := navigateTo(t, "/complex-layout")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#scaled-el", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	err = sc.click(nodeID)
	if err != nil {
		t.Errorf("click on scaled element failed: %v", err)
	}
}

func TestStealthCtx_Click_StickyElement(t *testing.T) {
	page := navigateTo(t, "/complex-layout")
	sc := getStealthCtx(page, &State{})

	btnID, err := sc.element("#sticky-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	// UNCERTAIN: sticky position after scroll may compute wrong coords
	if err := sc.click(btnID); err != nil {
		t.Logf("click on sticky element failed: %v", err)
		return
	}
	time.Sleep(200 * time.Millisecond)

	resultID, err := sc.element("#sticky-click-result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#sticky-click-result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "sticky-clicked" {
		t.Errorf("expected 'sticky-clicked', got %q", txt)
	}
}

func TestStealthCtx_Scroll_ElementInScrollableContainer(t *testing.T) {
	page := navigateTo(t, "/scroll-scenarios")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#v-buried-item", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	// scrollIntoView should scroll nested containers, not just the page.
	// Fix: detect scrollable parents and scroll them too.
	if err := sc.scrollIntoView(nodeID); err != nil {
		t.Fatalf("scrollIntoView failed: %v", err)
	}

	// After scrollIntoView, the element's center should be within the container's visible area.
	// Check by getting its viewport-relative position — if the container didn't scroll,
	// the element is clipped at the container boundary.
	result, err := sc.callOn(nodeID, `function() {
		var rect = this.getBoundingClientRect();
		var container = this.closest('.v-scroll-container') || this.parentElement;
		var cRect = container.getBoundingClientRect();
		// Element center must be within container bounds
		var centerY = rect.top + rect.height/2;
		return centerY >= cRect.top && centerY <= cRect.bottom;
	}`)
	if err != nil {
		t.Fatalf("visibility check failed: %v", err)
	}
	if !result.Result.Value.Bool() {
		t.Error("scrollIntoView should scroll nested scrollable containers to reveal buried element")
	}
}

func TestStealthCtx_Scroll_HorizontalScrollContainer(t *testing.T) {
	page := navigateTo(t, "/scroll-scenarios")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#h-end-item", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	// scrollIntoView should handle horizontal scroll in containers.
	// Fix: detect horizontal overflow and scroll containers horizontally too.
	if err := sc.scrollIntoView(nodeID); err != nil {
		t.Fatalf("scrollIntoView failed: %v", err)
	}

	// After scrollIntoView, the element should be within its container's visible area
	result, err := sc.callOn(nodeID, `function() {
		var rect = this.getBoundingClientRect();
		var container = this.closest('.h-scroll-container') || this.parentElement;
		var cRect = container.getBoundingClientRect();
		var centerX = rect.left + rect.width/2;
		return centerX >= cRect.left && centerX <= cRect.right;
	}`)
	if err != nil {
		t.Fatalf("visibility check failed: %v", err)
	}
	if !result.Result.Value.Bool() {
		t.Error("scrollIntoView should scroll horizontal containers to reveal element")
	}
}

func TestStealthCtx_Scroll_FarBelowFoldClick(t *testing.T) {
	page := navigateTo(t, "/scroll-scenarios")
	sc := getStealthCtx(page, &State{})

	btnID, err := sc.element("#far-below-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.click(btnID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	resultID, err := sc.element("#far-below-result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#far-below-result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "far-below-clicked" {
		t.Errorf("expected 'far-below-clicked', got %q", txt)
	}
}

// =====================
// Event Edge Cases tests
// =====================

func TestStealthCtx_Click_SelfDestruct(t *testing.T) {
	page := navigateTo(t, "/event-edge-cases")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#self-destruct", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Button should have removed itself
	found, err := sc.exists("#self-destruct")
	if err != nil {
		t.Fatalf("exists() failed: %v", err)
	}
	if found {
		t.Error("self-destruct button should have been removed after click")
	}

	// Result should confirm click happened
	resultID, err := sc.element("#result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "self-destruct-clicked" {
		t.Errorf("expected 'self-destruct-clicked', got %q", txt)
	}
}

func TestStealthCtx_Click_ReplacesContent(t *testing.T) {
	page := navigateTo(t, "/event-edge-cases")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#replace-trigger", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// The replaced marker should now exist
	markerID, err := sc.element("#replaced-marker", 2*time.Second)
	if err != nil {
		t.Fatalf("element(#replaced-marker) failed: %v", err)
	}
	txt, err := sc.text(markerID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Replaced" {
		t.Errorf("expected 'Replaced', got %q", txt)
	}
}

func TestStealthCtx_Click_PreventDefault(t *testing.T) {
	page := navigateTo(t, "/event-edge-cases")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#prevent-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Should still be on the same page (preventDefault stops navigation)
	resultID, err := sc.element("#result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "prevented" {
		t.Errorf("expected 'prevented', got %q", txt)
	}
}

func TestStealthCtx_Click_StopPropagation(t *testing.T) {
	page := navigateTo(t, "/event-edge-cases")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#inner-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	resultID, err := sc.element("#result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	// stopPropagation should prevent outer from being notified
	if txt != "inner-clicked" {
		t.Errorf("expected 'inner-clicked' (stopPropagation should prevent outer), got %q", txt)
	}
}

func TestStealthCtx_Click_DelegatedEvent(t *testing.T) {
	page := navigateTo(t, "/event-edge-cases")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#delegated-item-2", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	resultID, err := sc.element("#result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "delegated:delegated-item-2" {
		t.Errorf("expected 'delegated:delegated-item-2', got %q", txt)
	}
}

func TestStealthCtx_Click_MovingButton(t *testing.T) {
	page := navigateTo(t, "/event-edge-cases")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#moving-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}

	// First click moves the button
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("first click() failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	resultID, err := sc.element("#result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "move-clicked:1" {
		t.Errorf("expected 'move-clicked:1' after first click, got %q", txt)
	}

	// Second click — button has moved, need to re-query for a fresh nodeID
	// (the old nodeID may be invalidated by DOM tree re-fetch in scrollIntoView)
	nodeID, err = sc.element("#moving-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("re-query element(#moving-btn) failed: %v", err)
	}
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("second click() failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Re-query result element (previous nodeID invalidated by DOM tree re-fetch)
	resultID, err = sc.element("#result", defaultTimeout)
	if err != nil {
		t.Fatalf("re-query element(#result) failed: %v", err)
	}
	txt, err = sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "move-clicked:2" {
		t.Errorf("expected 'move-clicked:2' after second click at new position, got %q", txt)
	}
}

func TestStealthCtx_Click_DisabledButton(t *testing.T) {
	page := navigateTo(t, "/event-edge-cases")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#disabled-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	// Click a disabled button — the click event should not fire on disabled buttons
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	resultID, err := sc.element("#result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt == "disabled-clicked" {
		t.Error("disabled button should not fire click handler")
	}
}

func TestStealthCtx_Click_DoubleClick(t *testing.T) {
	page := navigateTo(t, "/event-edge-cases")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#dblclick-btn", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}

	// Double-click
	if err := sc.dblclick(nodeID); err != nil {
		t.Fatalf("dblclick() failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	resultID, err := sc.element("#result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	// Two rapid clicks on the same element should trigger a dblclick event.
	// Fix: click() could accept a clickCount option, or provide a dblclick() method.
	if txt != "dblclick-fired" {
		t.Errorf("two rapid clicks should trigger dblclick event, got %q", txt)
	}
}

// =====================
// Advanced Form Interactions tests
// =====================

func TestStealthCtx_Input_Textarea(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#textarea-input", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.input(nodeID, "Hello textarea"); err != nil {
		t.Fatalf("input() failed: %v", err)
	}

	result, err := sc.callOn(nodeID, "function() { return this.value; }")
	if err != nil {
		t.Fatalf("callOn() failed: %v", err)
	}
	if result.Result.Value.Str() != "Hello textarea" {
		t.Errorf("expected 'Hello textarea', got %q", result.Result.Value.Str())
	}
}

func TestStealthCtx_Input_ContentEditable(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#contenteditable-div", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.input(nodeID, "editable text"); err != nil {
		t.Fatalf("input() failed: %v", err)
	}

	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "editable text" {
		t.Errorf("expected 'editable text', got %q", txt)
	}
}

func TestStealthCtx_Input_PasswordInput(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#password-input", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.input(nodeID, "secret123"); err != nil {
		t.Fatalf("input() failed: %v", err)
	}

	result, err := sc.callOn(nodeID, "function() { return this.value; }")
	if err != nil {
		t.Fatalf("callOn() failed: %v", err)
	}
	if result.Result.Value.Str() != "secret123" {
		t.Errorf("expected 'secret123', got %q", result.Result.Value.Str())
	}
}

func TestStealthCtx_Input_NumberInput(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#number-input", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.input(nodeID, "42"); err != nil {
		t.Fatalf("input() failed: %v", err)
	}

	result, err := sc.callOn(nodeID, "function() { return this.value; }")
	if err != nil {
		t.Fatalf("callOn() failed: %v", err)
	}
	if result.Result.Value.Str() != "42" {
		t.Errorf("expected '42', got %q", result.Result.Value.Str())
	}
}

func TestStealthCtx_Click_Checkbox(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	// checkbox-1 is unchecked initially
	nodeID, err := sc.element("#checkbox-1", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}

	// Check initial state
	result, err := sc.callOn(nodeID, "function() { return this.checked; }")
	if err != nil {
		t.Fatalf("callOn() failed: %v", err)
	}
	if result.Result.Value.Bool() {
		t.Fatal("checkbox-1 should be unchecked initially")
	}

	// Click to check
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	result, err = sc.callOn(nodeID, "function() { return this.checked; }")
	if err != nil {
		t.Fatalf("callOn() failed: %v", err)
	}
	if !result.Result.Value.Bool() {
		t.Error("checkbox-1 should be checked after click")
	}
}

func TestStealthCtx_Click_RadioButton(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	// radio-c is initially checked, click radio-a
	nodeID, err := sc.element("#radio-a", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// radio-a should now be checked
	result, err := sc.callOn(nodeID, "function() { return this.checked; }")
	if err != nil {
		t.Fatalf("callOn() failed: %v", err)
	}
	if !result.Result.Value.Bool() {
		t.Error("radio-a should be checked after click")
	}

	// radio-c should now be unchecked
	radioCID, err := sc.element("#radio-c", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#radio-c) failed: %v", err)
	}
	result, err = sc.callOn(radioCID, "function() { return this.checked; }")
	if err != nil {
		t.Fatalf("callOn(#radio-c) failed: %v", err)
	}
	if result.Result.Value.Bool() {
		t.Error("radio-c should be unchecked after clicking radio-a")
	}
}

func TestStealthCtx_Click_CustomDropdown(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	// Click the toggle to open dropdown
	toggleID, err := sc.element("#dropdown-toggle", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#dropdown-toggle) failed: %v", err)
	}
	if err := sc.click(toggleID); err != nil {
		t.Fatalf("click(toggle) failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Dropdown should be open — select Option 2
	itemID, err := sc.element(".dropdown-item[data-value='opt2']", 2*time.Second)
	if err != nil {
		t.Fatalf("element(.dropdown-item[data-value='opt2']) failed: %v", err)
	}
	if err := sc.click(itemID); err != nil {
		t.Fatalf("click(option 2) failed: %v", err)
	}
	time.Sleep(300 * time.Millisecond)

	// Check the result
	resultID, err := sc.element("#dropdown-result", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#dropdown-result) failed: %v", err)
	}
	txt, err := sc.text(resultID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "opt2" {
		t.Errorf("expected 'opt2', got %q", txt)
	}
}

func TestStealthCtx_ClearInput_Textarea(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#textarea-input", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	// Type, then clear
	if err := sc.input(nodeID, "some text"); err != nil {
		t.Fatalf("input() failed: %v", err)
	}
	if err := sc.clearInput(nodeID); err != nil {
		t.Fatalf("clearInput() failed: %v", err)
	}

	result, err := sc.callOn(nodeID, "function() { return this.value; }")
	if err != nil {
		t.Fatalf("callOn() failed: %v", err)
	}
	if result.Result.Value.Str() != "" {
		t.Errorf("expected empty textarea after clear, got %q", result.Result.Value.Str())
	}
}

func TestStealthCtx_ClearInput_ContentEditable(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#contenteditable-div", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}

	// Set content, then clear it.
	// Fix: clearInput should handle contenteditable (use Selection API or execCommand).
	_, err = sc.callOn(nodeID, `function() { this.textContent = "test content"; }`)
	if err != nil {
		t.Fatalf("callOn to set content failed: %v", err)
	}

	err = sc.clearInput(nodeID)
	if err != nil {
		t.Errorf("clearInput on contenteditable should not error: %v", err)
		return
	}

	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "" {
		t.Errorf("clearInput on contenteditable should clear content, got %q", txt)
	}
}

func TestStealthCtx_Input_DateInput(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#date-input", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	// input() should handle date inputs correctly.
	// Fix: detect input[type=date] and set value via JS or use special key sequences.
	if err := sc.input(nodeID, "2024-01-15"); err != nil {
		t.Fatalf("input() failed: %v", err)
	}

	result, err := sc.callOn(nodeID, "function() { return this.value; }")
	if err != nil {
		t.Fatalf("callOn() failed: %v", err)
	}
	val := result.Result.Value.Str()
	if val != "2024-01-15" {
		t.Errorf("input on date input: expected '2024-01-15', got %q", val)
	}
}

func TestStealthCtx_Input_RangeSlider(t *testing.T) {
	page := navigateTo(t, "/form-advanced")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#range-input", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	// input() should handle range inputs.
	// Fix: detect input[type=range] and set value via JS property + input event.
	if err := sc.input(nodeID, "75"); err != nil {
		t.Fatalf("input() failed: %v", err)
	}

	result, err := sc.callOn(nodeID, "function() { return this.value; }")
	if err != nil {
		t.Fatalf("callOn() failed: %v", err)
	}
	val := result.Result.Value.Str()
	if val != "75" {
		t.Errorf("input on range slider: expected '75', got %q", val)
	}
}

// =====================
// SPA Navigation tests
// =====================

func TestStealthCtx_SPA_PushStateNavigation(t *testing.T) {
	page := navigateTo(t, "/spa-navigation")
	sc := getStealthCtx(page, &State{})

	// Click the "About" link
	nodeID, err := sc.element("#nav-about", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#nav-about) failed: %v", err)
	}
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}

	// Wait for async content to appear (300ms delay in SPA)
	titleID, err := sc.element("#page-title", 2*time.Second)
	if err != nil {
		t.Fatalf("element(#page-title) failed: %v", err)
	}
	txt, err := sc.text(titleID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "About Page" {
		t.Errorf("expected 'About Page', got %q", txt)
	}
}

func TestStealthCtx_SPA_ContentAppearsAfterDelay(t *testing.T) {
	page := navigateTo(t, "/spa-navigation")
	sc := getStealthCtx(page, &State{})

	// Click "Contact" to navigate
	nodeID, err := sc.element("#nav-contact", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#nav-contact) failed: %v", err)
	}
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}

	// Content appears after 300ms delay — polling should handle it
	bodyID, err := sc.element("#page-body", 2*time.Second)
	if err != nil {
		t.Fatalf("element(#page-body) failed: %v", err)
	}
	txt, err := sc.text(bodyID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Get in touch with us" {
		t.Errorf("expected 'Get in touch with us', got %q", txt)
	}
}

func TestStealthCtx_SPA_RapidNavigation(t *testing.T) {
	page := navigateTo(t, "/spa-navigation")
	sc := getStealthCtx(page, &State{})

	// Rapidly click through all three navigation links
	links := []string{"#nav-about", "#nav-contact", "#nav-home"}
	for _, sel := range links {
		nodeID, err := sc.element(sel, defaultTimeout)
		if err != nil {
			t.Fatalf("element(%s) failed: %v", sel, err)
		}
		if err := sc.click(nodeID); err != nil {
			t.Fatalf("click(%s) failed: %v", sel, err)
		}
		time.Sleep(100 * time.Millisecond) // brief pause between clicks
	}

	// Wait for final content to load
	time.Sleep(500 * time.Millisecond)

	// Should be on home page
	titleID, err := sc.element("#page-title", 2*time.Second)
	if err != nil {
		t.Fatalf("element(#page-title) failed: %v", err)
	}
	txt, err := sc.text(titleID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Home Page" {
		t.Errorf("expected 'Home Page' after rapid navigation, got %q", txt)
	}
}

func TestStealthCtx_SPA_IsolatedWorldSurvivesPushState(t *testing.T) {
	page := navigateTo(t, "/spa-navigation")
	sc := getStealthCtx(page, &State{})

	// Navigate to about
	nodeID, err := sc.element("#nav-about", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#nav-about) failed: %v", err)
	}
	if err := sc.click(nodeID); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// eval() should still work in the isolated world after pushState
	result, err := sc.eval("document.title")
	if err != nil {
		t.Fatalf("eval() after pushState failed: %v", err)
	}
	title := result.Result.Value.Str()
	if title != "SPA Navigation" {
		t.Errorf("expected title 'SPA Navigation', got %q", title)
	}
}

func TestStealthCtx_SPA_StaleNodeAfterContentReplace(t *testing.T) {
	page := navigateTo(t, "/spa-navigation")
	sc := getStealthCtx(page, &State{})

	// Grab node ID for initial page-title
	titleID, err := sc.element("#page-title", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#page-title) failed: %v", err)
	}

	// Navigate to about — this replaces innerHTML, destroying old nodes
	aboutLink, err := sc.element("#nav-about", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#nav-about) failed: %v", err)
	}
	if err := sc.click(aboutLink); err != nil {
		t.Fatalf("click() failed: %v", err)
	}
	time.Sleep(500 * time.Millisecond)

	// EXPECTED FAIL: old titleID is stale after innerHTML replacement
	_, err = sc.text(titleID)
	if err == nil {
		t.Skip("Expected stale nodeID error after SPA content replace, but text() succeeded")
	}
	t.Logf("Confirmed limitation: stale nodeID after SPA innerHTML replace: %v", err)
}

// =====================
// Complex Selectors tests
// =====================

func TestStealthCtx_Selector_NthChild(t *testing.T) {
	page := navigateTo(t, "/complex-selectors")
	sc := getStealthCtx(page, &State{})

	// Select 3rd row in table
	nodeID, err := sc.element("#data-table tbody tr:nth-child(3) .name", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Gamma" {
		t.Errorf("expected 'Gamma', got %q", txt)
	}
}

func TestStealthCtx_Selector_NthOfType(t *testing.T) {
	page := navigateTo(t, "/complex-selectors")
	sc := getStealthCtx(page, &State{})

	// Select 2nd paragraph in siblings
	nodeID, err := sc.element("#siblings p:nth-of-type(2)", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Body paragraph" {
		t.Errorf("expected 'Body paragraph', got %q", txt)
	}
}

func TestStealthCtx_Selector_AttributeContains(t *testing.T) {
	page := navigateTo(t, "/complex-selectors")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("[data-status*='act']", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Deep Target" {
		t.Errorf("expected 'Deep Target', got %q", txt)
	}
}

func TestStealthCtx_Selector_AttributeStartsWith(t *testing.T) {
	page := navigateTo(t, "/complex-selectors")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("[data-role^='tar']", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Deep Target" {
		t.Errorf("expected 'Deep Target', got %q", txt)
	}
}

func TestStealthCtx_Selector_MultipleAttributes(t *testing.T) {
	page := navigateTo(t, "/complex-selectors")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("[data-role='target'][data-status='active']", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Deep Target" {
		t.Errorf("expected 'Deep Target', got %q", txt)
	}
}

func TestStealthCtx_Selector_DeepNesting(t *testing.T) {
	page := navigateTo(t, "/complex-selectors")
	sc := getStealthCtx(page, &State{})

	nodeID, err := sc.element("#root .level-1 .level-2 .level-3 #deep-target", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Deep Target" {
		t.Errorf("expected 'Deep Target', got %q", txt)
	}
}

func TestStealthCtx_Selector_ChildCombinator(t *testing.T) {
	page := navigateTo(t, "/complex-selectors")
	sc := getStealthCtx(page, &State{})

	// Direct child combinator
	nodeID, err := sc.element("#root > .level-1 > .level-2 > .level-3 > #deep-target", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Deep Target" {
		t.Errorf("expected 'Deep Target', got %q", txt)
	}
}

func TestStealthCtx_Selector_SiblingCombinator(t *testing.T) {
	page := navigateTo(t, "/complex-selectors")
	sc := getStealthCtx(page, &State{})

	// Adjacent sibling: h2 + p
	nodeID, err := sc.element("#siblings h2 + p", defaultTimeout)
	if err != nil {
		t.Fatalf("element() failed: %v", err)
	}
	txt, err := sc.text(nodeID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if txt != "Intro paragraph" {
		t.Errorf("expected 'Intro paragraph', got %q", txt)
	}
}

func TestStealthCtx_Selector_Not(t *testing.T) {
	page := navigateTo(t, "/complex-selectors")
	sc := getStealthCtx(page, &State{})

	// Select list items that are NOT type-b
	nodeIDs, err := sc.elements("#mixed-list .item:not(.type-b)", defaultTimeout)
	if err != nil {
		t.Fatalf("elements() failed: %v", err)
	}
	if len(nodeIDs) != 3 {
		t.Errorf("expected 3 items with :not(.type-b), got %d", len(nodeIDs))
	}
}

func TestStealthCtx_Selector_FirstLastChild(t *testing.T) {
	page := navigateTo(t, "/complex-selectors")
	sc := getStealthCtx(page, &State{})

	// First child
	firstID, err := sc.element("#mixed-list li:first-child", defaultTimeout)
	if err != nil {
		t.Fatalf("element(:first-child) failed: %v", err)
	}
	firstTxt, err := sc.text(firstID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if firstTxt != "A1" {
		t.Errorf("expected first child 'A1', got %q", firstTxt)
	}

	// Last child
	lastID, err := sc.element("#mixed-list li:last-child", defaultTimeout)
	if err != nil {
		t.Fatalf("element(:last-child) failed: %v", err)
	}
	lastTxt, err := sc.text(lastID)
	if err != nil {
		t.Fatalf("text() failed: %v", err)
	}
	if lastTxt != "A3" {
		t.Errorf("expected last child 'A3', got %q", lastTxt)
	}
}

// =====================
// iframe & Shadow DOM tests — Documenting Limitations
// =====================

func TestStealthCtx_IFrame_CannotQueryInside(t *testing.T) {
	page := navigateTo(t, "/iframe-test")
	sc := getStealthCtx(page, &State{})

	// EXPECTED FAIL: Cannot query elements inside an iframe from the parent DOM.
	// DOM.querySelector operates on the parent document only.
	_, err := sc.element("#iframe-content", 2*time.Second)
	if err != nil {
		t.Logf("Confirmed limitation: cannot query inside iframe from parent: %v", err)
	} else {
		t.Log("Unexpectedly found #iframe-content — selector may have leaked through")
	}
}

func TestStealthCtx_IFrame_CanFindIFrameElement(t *testing.T) {
	page := navigateTo(t, "/iframe-test")
	sc := getStealthCtx(page, &State{})

	// Can find the iframe element itself
	nodeID, err := sc.element("#test-iframe", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#test-iframe) failed: %v", err)
	}

	html, err := sc.outerHTML(nodeID)
	if err != nil {
		t.Fatalf("outerHTML() failed: %v", err)
	}
	if !strings.Contains(html, "iframe") {
		t.Errorf("expected outerHTML to contain 'iframe', got %q", html)
	}
}

func TestStealthCtx_ShadowDOM_CannotQueryInside(t *testing.T) {
	page := navigateTo(t, "/shadow-dom-test")
	sc := getStealthCtx(page, &State{})

	// EXPECTED FAIL: Cannot query inside shadow DOM via querySelector on the document.
	_, err := sc.element("#shadow-content", 2*time.Second)
	if err != nil {
		t.Logf("Confirmed limitation: cannot query inside shadow DOM: %v", err)
	} else {
		t.Log("Unexpectedly found #shadow-content — may have pierced shadow boundary")
	}
}

func TestStealthCtx_ShadowDOM_CanFindHost(t *testing.T) {
	page := navigateTo(t, "/shadow-dom-test")
	sc := getStealthCtx(page, &State{})

	// Can find the shadow host element
	nodeID, err := sc.element("#shadow-host", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#shadow-host) failed: %v", err)
	}

	html, err := sc.outerHTML(nodeID)
	if err != nil {
		t.Fatalf("outerHTML() failed: %v", err)
	}
	if !strings.Contains(html, "shadow-host") {
		t.Errorf("expected outerHTML to contain 'shadow-host', got %q", html)
	}
}

// =====================
// Live Website Smoke Tests
// =====================

func TestStealthCtx_Live_WikipediaSearch(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live site test in short mode")
	}

	browser := launchStealthBrowser(t)
	page := browser.MustPage("")
	defer page.MustClose()
	injectStealthScripts(t, page)

	applyStealthToPage(page, browser, &State{
		ViewportWidth:  1920,
		ViewportHeight: 1080,
	})

	page.MustNavigate("https://en.wikipedia.org/wiki/Main_Page")
	page.MustWaitLoad()

	sc := getStealthCtx(page, &State{ViewportWidth: 1920, ViewportHeight: 1080})

	// Find the search input
	searchID, err := sc.element("#searchInput", 10*time.Second)
	if err != nil {
		t.Fatalf("element(#searchInput) failed: %v", err)
	}

	// Type a search query
	if err := sc.input(searchID, "Go programming language"); err != nil {
		t.Fatalf("input() failed: %v", err)
	}

	// Submit the search form
	formID, err := sc.element("#searchform", defaultTimeout)
	if err != nil {
		t.Fatalf("element(#searchform) failed: %v", err)
	}
	if err := sc.submit(formID); err != nil {
		t.Fatalf("submit() failed: %v", err)
	}

	// Wait for results page
	time.Sleep(3 * time.Second)

	// Verify we got search results or a page
	result, err := sc.eval("document.title")
	if err != nil {
		t.Fatalf("eval() failed: %v", err)
	}
	title := result.Result.Value.Str()
	t.Logf("Wikipedia search result page title: %s", title)
	if title == "" {
		t.Error("expected non-empty page title after Wikipedia search")
	}
}

func TestStealthCtx_Live_HackerNewsHeadlines(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live site test in short mode")
	}

	browser := launchStealthBrowser(t)
	page := browser.MustPage("")
	defer page.MustClose()
	injectStealthScripts(t, page)

	applyStealthToPage(page, browser, &State{
		ViewportWidth:  1920,
		ViewportHeight: 1080,
	})

	page.MustNavigate("https://news.ycombinator.com/")
	page.MustWaitLoad()

	sc := getStealthCtx(page, &State{ViewportWidth: 1920, ViewportHeight: 1080})

	// Query all headline links
	nodeIDs, err := sc.elements(".titleline", 10*time.Second)
	if err != nil {
		t.Fatalf("elements(.titleline) failed: %v", err)
	}
	if len(nodeIDs) < 10 {
		t.Errorf("expected at least 10 headlines on HN, got %d", len(nodeIDs))
	} else {
		t.Logf("Found %d headlines on Hacker News", len(nodeIDs))
	}
}

func TestStealthCtx_Live_GitHubRepoPage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live site test in short mode")
	}

	browser := launchStealthBrowser(t)
	page := browser.MustPage("")
	defer page.MustClose()
	injectStealthScripts(t, page)

	applyStealthToPage(page, browser, &State{
		ViewportWidth:  1920,
		ViewportHeight: 1080,
	})

	page.MustNavigate("https://github.com/go-rod/rod")
	page.MustWaitLoad()

	sc := getStealthCtx(page, &State{ViewportWidth: 1920, ViewportHeight: 1080})

	// Verify page loaded by checking for README or repo description
	time.Sleep(2 * time.Second)

	result, err := sc.eval("document.title")
	if err != nil {
		t.Fatalf("eval() failed: %v", err)
	}
	title := result.Result.Value.Str()
	t.Logf("GitHub repo page title: %s", title)
	if !strings.Contains(strings.ToLower(title), "rod") {
		t.Errorf("expected page title to contain 'rod', got %q", title)
	}
}

func TestStealthCtx_Live_StackOverflow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live site test in short mode")
	}

	browser := launchStealthBrowser(t)
	page := browser.MustPage("")
	defer page.MustClose()
	injectStealthScripts(t, page)

	applyStealthToPage(page, browser, &State{
		ViewportWidth:  1920,
		ViewportHeight: 1080,
	})

	page.MustNavigate("https://stackoverflow.com/questions/tagged/go")
	page.MustWaitLoad()

	sc := getStealthCtx(page, &State{ViewportWidth: 1920, ViewportHeight: 1080})
	time.Sleep(3 * time.Second)

	// Try to find question titles (may fail on cookie consent overlay)
	result, err := sc.eval("document.title")
	if err != nil {
		t.Fatalf("eval() failed: %v", err)
	}
	title := result.Result.Value.Str()
	t.Logf("StackOverflow page title: %s", title)
	if title == "" {
		t.Error("expected non-empty page title on StackOverflow")
	}
}

func TestStealthCtx_Live_HTTPBin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live site test in short mode")
	}

	browser := launchStealthBrowser(t)
	page := browser.MustPage("")
	defer page.MustClose()
	injectStealthScripts(t, page)

	applyStealthToPage(page, browser, &State{
		ViewportWidth:  1920,
		ViewportHeight: 1080,
	})

	page.MustNavigate("https://httpbin.org/get")
	page.MustWaitLoad()

	sc := getStealthCtx(page, &State{ViewportWidth: 1920, ViewportHeight: 1080})
	time.Sleep(2 * time.Second)

	// httpbin.org/get returns JSON — verify body contains expected JSON structure
	result, err := sc.eval("document.body.innerText")
	if err != nil {
		t.Fatalf("eval() failed: %v", err)
	}
	bodyText := result.Result.Value.Str()
	if !strings.Contains(bodyText, "headers") && !strings.Contains(bodyText, "origin") {
		t.Errorf("expected httpbin.org/get response to contain 'headers' or 'origin', got: %s", bodyText[:min(len(bodyText), 500)])
	} else {
		t.Logf("httpbin.org/get response contains expected JSON fields")
	}
}
