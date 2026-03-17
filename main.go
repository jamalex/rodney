package main

import (
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/go-rod/stealth"
)

//go:embed help.txt
var helpText string

var version = "dev"

// scopeMode determines whether to use a local or global state directory.
type scopeMode int

const (
	scopeAuto   scopeMode = iota // auto-detect: local if .rodney/state.json exists in cwd, else global
	scopeLocal                   // force local (./.rodney/)
	scopeGlobal                  // force global (~/.rodney/)
)

// activeStateDir is set once at startup based on --local/--global/--home-dir flags.
var activeStateDir string

// activeScopeMode is set once in main() from extractScopeArgs result.
var activeScopeMode scopeMode

// homeDirFlag is set when --home-dir is explicitly passed on the command line.
// Used by cmdNewSession to resolve the session data directory.
var homeDirFlag string

// waitForProcessExit polls until the given PID is no longer running, or timeout.
func waitForProcessExit(pid int, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		proc, err := os.FindProcess(pid)
		if err != nil {
			return
		}
		// On Unix, FindProcess always succeeds; use Signal(0) to check if alive
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			return // process exited
		}
		time.Sleep(50 * time.Millisecond)
	}
}


// extractScopeArgs scans args for --local/--global/--home-dir, removes them, and returns the mode and home dir.
// If both --local and --global appear, the last one wins. --home-dir takes a path argument.
func extractScopeArgs(args []string) (scopeMode, string, string, []string) {
	mode := scopeAuto
	homeDir := ""
	sessionID := ""
	var filtered []string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--local":
			mode = scopeLocal
		case args[i] == "--global":
			mode = scopeGlobal
		case args[i] == "--home-dir" && i+1 < len(args):
			homeDir = args[i+1]
			i++ // skip the value
		case strings.HasPrefix(args[i], "--home-dir="):
			homeDir = args[i][len("--home-dir="):]
		case args[i] == "--session" && i+1 < len(args):
			sessionID = args[i+1]
			i++ // skip the value
		case strings.HasPrefix(args[i], "--session="):
			sessionID = args[i][len("--session="):]
		default:
			filtered = append(filtered, args[i])
		}
	}
	return mode, homeDir, sessionID, filtered
}

// resolveStateDir determines the state directory based on scope mode and working directory.
func resolveStateDir(mode scopeMode, workingDir string) string {
	switch mode {
	case scopeLocal:
		return filepath.Join(workingDir, ".rodney")
	case scopeGlobal:
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".rodney")
	default: // scopeAuto
		localDir := filepath.Join(workingDir, ".rodney")
		if _, err := os.Stat(localDir); err == nil {
			return localDir
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".rodney")
	}
}

// resolveNewSessionDir determines the data directory for a new session.
// If no .rodney/ exists locally and no explicit scope is given, creates a temp dir.
func resolveNewSessionDir(mode scopeMode, workingDir string, homeDir string) string {
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

// SessionInfo holds per-session state within a browser instance.
type SessionInfo struct {
	TargetID       string `json:"target_id"`
	ViewportWidth  int    `json:"viewport_width,omitempty"`
	ViewportHeight int    `json:"viewport_height,omitempty"`
	NoCapture      bool   `json:"no_capture,omitempty"`
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
	MonitorPID      int                    `json:"monitor_pid,omitempty"`
	Sessions        map[string]SessionInfo `json:"sessions,omitempty"`
}

// activeSessionID is set by --session <id> flag or RODNEY_SESSION env var.
var activeSessionID string

// resolveSessionID determines the session ID from available sources.
// Priority: positionalArg > flagValue (--session) > RODNEY_SESSION env var.
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

func stateDir() string {
	if activeStateDir != "" {
		return activeStateDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rodney")
}

func statePath() string {
	return filepath.Join(stateDir(), "state.json")
}

func stateLockPath() string {
	return filepath.Join(stateDir(), "state.lock")
}

func loadState() (*State, error) {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return nil, fmt.Errorf("no browser session (run 'rodney newsession' first)")
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("corrupt state file: %w", err)
	}
	return &s, nil
}

// saveStateAt writes state to statePath using atomic temp+rename under an exclusive file lock.
func saveStateAt(statePath, lockPath string, s *State) error {
	return withFileLock(lockPath, func() error {
		return atomicWriteJSON(statePath, s)
	})
}

// saveState writes state to the default state path with locking and atomic writes.
func saveState(s *State) error {
	return saveStateAt(statePath(), stateLockPath(), s)
}

func removeState() {
	os.Remove(statePath())
}

// ---------------------------------------------------------------------------
// Global session registry (~/.rodney/sessions.json)
// ---------------------------------------------------------------------------

// registryDir returns the global config directory (~/.rodney/).
func registryDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rodney")
}

// registryPath returns the path to the global session registry file.
func registryPath() string {
	return filepath.Join(registryDir(), "sessions.json")
}

// registryLockPath returns the path to the registry lock file.
func registryLockPath() string {
	return filepath.Join(registryDir(), "sessions.lock")
}

// withFileLock acquires an exclusive flock on lockPath, runs fn, then releases.
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

// atomicWriteJSON writes v as indented JSON to path via a temp file + rename.
func atomicWriteJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// registryLoadAll reads the entire registry map from regPath.
// Returns an empty map (not error) if the file does not exist.
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
		return nil, fmt.Errorf("corrupt registry: %w", err)
	}
	return m, nil
}

// registryLookup returns the data directory for sessionID, or an error if not found.
func registryLookup(regPath, sessionID string) (string, error) {
	m, err := registryLoadAll(regPath)
	if err != nil {
		return "", err
	}
	dir, ok := m[sessionID]
	if !ok {
		return "", fmt.Errorf("session %q not found in registry", sessionID)
	}
	return dir, nil
}

// registryAdd adds a sessionID -> dataDir mapping under an exclusive file lock.
func registryAdd(regPath, lockPath, sessionID, dataDir string) error {
	return withFileLock(lockPath, func() error {
		m, err := registryLoadAll(regPath)
		if err != nil {
			return err
		}
		m[sessionID] = dataDir
		return atomicWriteJSON(regPath, m)
	})
}

// registryRemove deletes a sessionID from the registry under an exclusive file lock.
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

// proxyConfigHash returns a hex-encoded SHA-256 hash of the proxy URL.
func proxyConfigHash(proxyURL string) string {
	h := sha256.Sum256([]byte(proxyURL))
	return "sha256:" + hex.EncodeToString(h[:])
}

// connectBrowser connects to the running Chrome instance
func connectBrowser(s *State) (*rod.Browser, error) {
	// NoDefaultDevice prevents rod from sending
	// Emulation.setDeviceMetricsOverride (with its LaptopWithMDPIScreen
	// default of 1280x800) every time it attaches to a page target.
	// In visible mode this would shrink the viewport; in stealth headless
	// mode we set our own viewport via applyStealthToPage instead.
	browser := rod.New().ControlURL(s.DebugURL).NoDefaultDevice()
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to browser (is it still running?): %w", err)
	}
	return browser, nil
}

// getActivePage returns the page for the current session.
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
		return nil, fmt.Errorf("failed to list pages: %w", err)
	}
	for _, p := range pages {
		if string(p.TargetID) == si.TargetID {
			return p, nil
		}
	}
	return nil, fmt.Errorf("page for session %q no longer exists", activeSessionID)
}

func printUsage() {
	fmt.Print(helpText)
}

// shortID generates a 6-character lowercase alphanumeric ID.
func shortID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 6)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[n.Int64()]
	}
	return string(b)
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(2)
	}

	// Extract --local/--global/--home-dir/--session from all args before dispatching
	mode, homeDir, sessionID, cleanedArgs := extractScopeArgs(os.Args[1:])
	activeScopeMode = mode
	if sessionID == "" {
		sessionID = os.Getenv("RODNEY_SESSION")
	}
	activeSessionID = sessionID
	if len(cleanedArgs) == 0 {
		printUsage()
		os.Exit(1)
	}

	if homeDir != "" {
		activeStateDir = homeDir
		homeDirFlag = homeDir
	} else {
		wd, _ := os.Getwd()
		activeStateDir = resolveStateDir(mode, wd)
	}

	cmd := cleanedArgs[0]
	args := cleanedArgs[1:]

	if cmd == "--version" {
		fmt.Println(version)
		os.Exit(0)
	}

	switch cmd {
	case "_proxy":
		cmdInternalProxy(args) // hidden: runs the auth proxy helper
	case "_netmonitor":
		cmdNetMonitor(args) // hidden: runs the network monitor
	case "newsession":
		cmdNewSession(args)
	case "endsession":
		cmdEndSession(args)
	case "sessions":
		cmdSessions(args)
	case "open":
		cmdOpen(args)
	case "back":
		cmdBack(args)
	case "forward":
		cmdForward(args)
	case "reload":
		cmdReload(args)
	case "clear-cache":
		cmdClearCache(args)
	case "url":
		cmdURL(args)
	case "title":
		cmdTitle(args)
	case "html":
		cmdHTML(args)
	case "text":
		cmdText(args)
	case "attr":
		cmdAttr(args)
	case "pdf":
		cmdPDF(args)
	case "js":
		cmdJS(args)
	case "click":
		cmdClick(args)
	case "input":
		cmdInput(args)
	case "clear":
		cmdClear(args)
	case "select":
		cmdSelect(args)
	case "submit":
		cmdSubmit(args)
	case "hover":
		cmdHover(args)
	case "file":
		cmdFile(args)
	case "download":
		cmdDownload(args)
	case "focus":
		cmdFocus(args)
	case "wait":
		cmdWait(args)
	case "waitload":
		cmdWaitLoad(args)
	case "waitstable":
		cmdWaitStable(args)
	case "waitidle":
		cmdWaitIdle(args)
	case "sleep":
		cmdSleep(args)
	case "screenshot":
		cmdScreenshot(args)
	case "screenshot-el":
		cmdScreenshotEl(args)
	case "exists":
		cmdExists(args)
	case "count":
		cmdCount(args)
	case "visible":
		cmdVisible(args)
	case "assert":
		cmdAssert(args)
	case "ax-tree":
		cmdAXTree(args)
	case "ax-find":
		cmdAXFind(args)
	case "ax-node":
		cmdAXNode(args)
	case "net-log":
		cmdNetLog(args)
	case "net-body":
		cmdNetBody(args)
	case "net-clear":
		cmdNetClear(args)
	// Removed commands with helpful error messages
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
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(2)
	}
}

// Default timeout for element queries (seconds)
var defaultTimeout = 30 * time.Second

func init() {
	if t := os.Getenv("ROD_TIMEOUT"); t != "" {
		if secs, err := strconv.ParseFloat(t, 64); err == nil {
			defaultTimeout = time.Duration(secs * float64(time.Second))
		}
	}
}

// findSystemChrome looks for an installed Chrome/Chromium binary on the system.
// Returns the path if found, empty string otherwise.

// workerFixJS is a content script that makes navigator.webdriver consistent
// between the main thread and Web Workers.
// --disable-blink-features=AutomationControlled sets the main thread to false
// but removes the property entirely from workers (undefined). Bot-detection
// sites compare navigator properties between contexts to detect automation.
//
// This script intercepts:
// 1. URL.createObjectURL — patches blob-URL workers
// 2. Worker constructor — patches URL-based workers (via sync XHR fetch + blob rewrite)
const workerFixJS = `(function() {
  'use strict';
  // Build a patch that syncs navigator properties from main thread into workers.
  // --disable-blink-features=AutomationControlled makes main thread webdriver=false
  // but workers may differ in webdriver, language, languages, and vendor.
  var props = {
    webdriver: {get: function() { return false }, configurable: true},
  };
  // Sync language/languages/vendor so workers match the main thread exactly
  if (typeof navigator.language === 'string') {
    var lang = navigator.language;
    props.language = {get: function() { return lang }, configurable: true};
  }
  if (navigator.languages) {
    var langs = Array.prototype.slice.call(navigator.languages);
    props.languages = {get: function() { return langs }, configurable: true};
  }
  if (typeof navigator.vendor === 'string') {
    var vendor = navigator.vendor;
    props.vendor = {get: function() { return vendor }, configurable: true};
  }
  // Serialize the patch as self-contained JS for injection into workers
  var patch = 'try{';
  patch += 'var p=' + JSON.stringify({
    webdriver: false,
    language: navigator.language,
    languages: navigator.languages ? Array.prototype.slice.call(navigator.languages) : undefined,
    vendor: navigator.vendor || undefined,
  }) + ';';
  patch += 'Object.keys(p).forEach(function(k){';
  patch += 'if(p[k]!==undefined)Object.defineProperty(navigator,k,{get:function(){return p[k]},configurable:true});';
  patch += '});';
  patch += '}catch(e){}\n';

  // 1. Intercept blob URL creation to patch blob workers
  var origCreateObjectURL = URL.createObjectURL;
  URL.createObjectURL = function(obj) {
    if (obj instanceof Blob && (obj.type === '' || /javascript/i.test(obj.type))) {
      obj = new Blob([patch, obj], {type: obj.type || 'application/javascript'});
    }
    return origCreateObjectURL(obj);
  };

  // 2. Intercept Worker constructor to patch URL-based workers
  var OrigWorker = Worker;
  Worker = function(url, opts) {
    if (typeof url === 'string' && !url.startsWith('blob:') && !url.startsWith('data:')) {
      try {
        if (opts && opts.type === 'module') {
          return new OrigWorker(url, opts);
        }
        var xhr = new XMLHttpRequest();
        xhr.open('GET', url, false);
        xhr.send();
        if (xhr.status === 200) {
          var blob = new Blob([patch + xhr.responseText], {type: 'application/javascript'});
          url = origCreateObjectURL(blob);
        }
      } catch(e) {}
    }
    return new OrigWorker(url, opts);
  };
  Worker.prototype = OrigWorker.prototype;
  Object.keys(OrigWorker).forEach(function(k) { try { Worker[k] = OrigWorker[k]; } catch(e) {} });
})();
`

// platformName returns the CDP platform name for the current OS.
func platformName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "windows":
		return "Windows"
	default:
		return "Linux"
	}
}

// archName returns the CDP architecture name for the current GOARCH.
func archName() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm"
	default:
		return "x86"
	}
}

// userAgentDataScript builds a JS snippet that overrides navigator.userAgentData
// to report the given Chrome version and platform. This is injected via
// addScriptToEvaluateOnNewDocument so it persists even after the CDP session
// disconnects (unlike Emulation.setUserAgentOverride which is session-scoped).
func userAgentDataScript(majorVer, fullVer, platform, arch string) string {
	const tpl = `(function() {
  'use strict';
  var brands = Object.freeze([
    Object.freeze({brand: 'Chromium', version: 'MAJOR_VER'}),
    Object.freeze({brand: 'Google Chrome', version: 'MAJOR_VER'}),
    Object.freeze({brand: 'Not_A Brand', version: '24'})
  ]);
  var fullVersionList = Object.freeze([
    Object.freeze({brand: 'Chromium', version: 'FULL_VER'}),
    Object.freeze({brand: 'Google Chrome', version: 'FULL_VER'})
  ]);
  var uaData = {
    brands: brands,
    mobile: false,
    platform: 'PLAT_NAME',
    getHighEntropyValues: function() {
      return Promise.resolve({
        brands: brands,
        fullVersionList: fullVersionList,
        mobile: false,
        model: '',
        platform: 'PLAT_NAME',
        platformVersion: '',
        architecture: 'ARCH_NAME',
        bitness: '64',
        uaFullVersion: 'FULL_VER',
        wow64: false
      });
    },
    toJSON: function() {
      return {brands: brands, mobile: false, platform: 'PLAT_NAME'};
    }
  };
  if (typeof NavigatorUAData !== 'undefined') {
    try { Object.setPrototypeOf(uaData, NavigatorUAData.prototype); } catch(e) {}
  }
  Object.defineProperty(Navigator.prototype, 'userAgentData', {
    get: function() { return uaData; },
    configurable: true,
    enumerable: true
  });
})();`
	s := strings.ReplaceAll(tpl, "MAJOR_VER", majorVer)
	s = strings.ReplaceAll(s, "FULL_VER", fullVer)
	s = strings.ReplaceAll(s, "PLAT_NAME", platform)
	s = strings.ReplaceAll(s, "ARCH_NAME", arch)
	return s
}

// applyStealthToPage injects stealth scripts, sets viewport, and configures
// user agent metadata for a page. Called for both initial and new pages.
// vpWidth/vpHeight of 0 means no viewport override (used in visible/non-headless mode).
func applyStealthToPage(page *rod.Page, browser *rod.Browser, vpWidth, vpHeight int) {
	// 1. Inject stealth scripts via CDP before any navigation occurs.
	// addScriptToEvaluateOnNewDocument persists across navigations.
	proto.PageAddScriptToEvaluateOnNewDocument{Source: stealth.JS}.Call(page)
	proto.PageAddScriptToEvaluateOnNewDocument{Source: workerFixJS}.Call(page)

	// 2. Set viewport
	if vpWidth > 0 && vpHeight > 0 {
		proto.EmulationSetDeviceMetricsOverride{
			Width:             vpWidth,
			Height:            vpHeight,
			DeviceScaleFactor: 1,
		}.Call(page)
	}

	// 3. Set user agent metadata to populate navigator.userAgentData.
	// We use BOTH the CDP Emulation.setUserAgentOverride (which sets HTTP
	// request headers like Sec-CH-UA) AND a JS-based override injected via
	// addScriptToEvaluateOnNewDocument (which persists after CDP session
	// disconnect). Belt and suspenders.
	versionResult, err := proto.BrowserGetVersion{}.Call(browser)
	if err == nil {
		majorVer := ""
		fullVer := ""
		// Parse Chrome version from Product field (e.g. "Chrome/145.0.7632.109")
		if strings.HasPrefix(versionResult.Product, "Chrome/") {
			fullVer = strings.TrimPrefix(versionResult.Product, "Chrome/")
			if dotIdx := strings.Index(fullVer, "."); dotIdx > 0 {
				majorVer = fullVer[:dotIdx]
			} else {
				majorVer = fullVer
			}
		}
		if majorVer != "" {
			// CDP override — sets Sec-CH-UA request headers
			proto.EmulationSetUserAgentOverride{
				UserAgent: versionResult.UserAgent,
				UserAgentMetadata: &proto.EmulationUserAgentMetadata{
					Brands: []*proto.EmulationUserAgentBrandVersion{
						{Brand: "Chromium", Version: majorVer},
						{Brand: "Google Chrome", Version: majorVer},
						{Brand: "Not_A Brand", Version: "24"},
					},
					FullVersionList: []*proto.EmulationUserAgentBrandVersion{
						{Brand: "Chromium", Version: fullVer},
						{Brand: "Google Chrome", Version: fullVer},
					},
					Platform:     platformName(),
					Architecture: archName(),
					Mobile:       false,
				},
			}.Call(page)

			// JS override — patches navigator.userAgentData directly so
			// it survives after the CLI disconnects from the browser.
			proto.PageAddScriptToEvaluateOnNewDocument{
				Source: userAgentDataScript(majorVer, fullVer, platformName(), archName()),
			}.Call(page)
		}
	}
}

// withPage resolves the session from the registry, loads state, connects to
// the browser, and returns the active page. Caller should NOT close the browser.
func withPage() (*State, *rod.Browser, *rod.Page) {
	sid := resolveSessionID(activeSessionID, "")
	if sid == "" {
		fatal("session ID required; pass --session <id>")
	}
	activeSessionID = sid

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

	// Check network monitor health and re-launch if needed
	if s.MonitorPID > 0 && !isProcessAlive(s.MonitorPID) {
		hasCapture := false
		for _, si := range s.Sessions {
			if !si.NoCapture {
				hasCapture = true
				break
			}
		}
		if hasCapture {
			withFileLock(stateLockPath(), func() error {
				fresh, err := loadState()
				if err != nil {
					return err
				}
				if fresh.MonitorPID > 0 && isProcessAlive(fresh.MonitorPID) {
					return nil
				}
				pid := launchNetMonitor(dataDir)
				fresh.MonitorPID = pid
				// Write directly since we already hold the lock
				return atomicWriteJSON(statePath(), fresh)
			})
			s, err = loadState()
			if err != nil {
				fatal("reload state: %v", err)
			}
		}
	}

	browser, err := connectBrowser(s)
	if err != nil {
		fatal("browser for session %q is no longer running: %v", sid, err)
	}
	page, err := getActivePage(browser, s)
	if err != nil {
		fatal("%v", err)
	}
	// Apply default timeout so element queries don't hang forever
	page = page.Timeout(defaultTimeout)
	return s, browser, page
}

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
}

// --- Commands ---

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

// parseStartFlags parses the arguments to "rodney newsession".
func parseStartFlags(args []string) (startFlags, error) {
	f := startFlags{headless: false, stealth: true}
	f.explicitFlags = make(map[string]bool)
	usage := "usage: rodney newsession [--headless] [--no-stealth] [--viewport WxH] [--profile NAME] [--insecure | -k]"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--show":
			// accepted for backwards compat, already the default
		case "--headless":
			f.headless = true
			f.explicitFlags["headless"] = true
		case "--insecure", "-k":
			f.ignoreCertErrors = true
			f.explicitFlags["insecure"] = true
		case "--stealth":
			// accepted for backwards compat, already the default
		case "--no-stealth":
			f.stealth = false
			f.explicitFlags["stealth"] = true
		case "--no-capture":
			f.noCapture = true
		case "--viewport":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("missing value for --viewport\n%s", usage)
			}
			parts := strings.SplitN(args[i], "x", 2)
			if len(parts) != 2 {
				return f, fmt.Errorf("invalid viewport format %q: expected WxH (e.g. 1920x935)", args[i])
			}
			if _, err := strconv.Atoi(parts[0]); err != nil {
				return f, fmt.Errorf("invalid viewport width %q: %v", parts[0], err)
			}
			if _, err := strconv.Atoi(parts[1]); err != nil {
				return f, fmt.Errorf("invalid viewport height %q: %v", parts[1], err)
			}
			f.viewport = args[i]
		case "--profile":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("missing value for --profile\n%s", usage)
			}
			f.profile = args[i]
			f.explicitFlags["profile"] = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return f, fmt.Errorf("unknown flag: %s\n%s", args[i], usage)
			}
			// First non-flag positional arg is the URL
			if f.url == "" {
				f.url = args[i]
			} else {
				return f, fmt.Errorf("unexpected argument: %s\n%s", args[i], usage)
			}
		}
	}
	return f, nil
}

// checkBrowserCompat verifies that explicitly-passed flags are compatible with
// the running browser. Returns nil if compatible, descriptive error if not.
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

// resolveProfile resolves a --profile value to a Chrome profile directory name
// and the Chrome data directory that contains it. It accepts any of:
//   - A directory name directly (e.g. "Default", "Profile 1")
//   - A display name from Chrome's UI (e.g. "Archie", "Jamie")
//   - An email address (e.g. "archie@learningequality.org")
//
// Resolution reads Chrome's Local State JSON file, checking multiple locations:
//  1. The rodney chrome-data directory (dataDir)
//  2. The rodney state/home directory (parent of dataDir)
//  3. Standard system Chrome locations (~/.config/google-chrome, etc.)
//
// Returns (profileDirName, chromeDataDir). If the profile was found in a
// system Chrome installation, chromeDataDir points there so the caller can
// use it as --user-data-dir. If not found, returns (value, "") and the
// caller should use its default data directory.
func resolveProfile(dataDir, value string) (string, string) {
	// Build candidate paths for Local State, in priority order
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(dataDir, "Local State"),
		filepath.Join(filepath.Dir(dataDir), "Local State"),
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".config", "google-chrome", "Local State"),
			filepath.Join(home, ".config", "chromium", "Local State"),
		)
		if runtime.GOOS == "darwin" {
			candidates = append(candidates,
				filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Local State"),
			)
		}
	}

	type profileInfo struct {
		Name     string `json:"name"`
		UserName string `json:"user_name"`
	}
	type localState struct {
		Profile struct {
			InfoCache map[string]profileInfo `json:"info_cache"`
		} `json:"profile"`
	}

	valueLower := strings.ToLower(value)

	// Search each Local State file until we find a match. We don't stop
	// at the first readable file because rodney's own chrome-data may have
	// a bare-bones Local State with only a generic "Default" profile.
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var state localState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		chromeDir := filepath.Dir(path) // directory containing Local State
		for dirName, info := range state.Profile.InfoCache {
			// Exact directory name match (case-insensitive)
			if strings.ToLower(dirName) == valueLower {
				return dirName, chromeDir
			}
			// Display name match (case-insensitive)
			if strings.ToLower(info.Name) == valueLower {
				fmt.Printf("Resolved profile %q to directory %q (display name: %q)\n", value, dirName, info.Name)
				return dirName, chromeDir
			}
			// Email match (case-insensitive)
			if info.UserName != "" && strings.ToLower(info.UserName) == valueLower {
				fmt.Printf("Resolved profile %q to directory %q (email: %s, display name: %q)\n", value, dirName, info.UserName, info.Name)
				return dirName, chromeDir
			}
		}
	}

	return value, "" // No match found; use value as-is with default data dir
}

// getProxyEnvVar returns the raw proxy environment variable value (for hashing).
func getProxyEnvVar() string {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

// launchResult holds the results of launching a Chrome browser.
type launchResult struct {
	debugURL      string
	pid           int
	chromeDataDir string
	proxyPID      int
	proxyPort     int
	proxyServer   string
	proxyHash     string
	vpWidth       int
	vpHeight      int
}

// launchChrome starts a new Chrome process with the given flags and data directory.
// It configures the launcher, handles proxy detection, stealth flags, viewport
// parsing, and returns all info needed to populate the State.
func launchChrome(flags *startFlags, dataDir string) launchResult {
	ignoreCertErrors := flags.ignoreCertErrors
	headless := flags.headless

	chromeDataDir := filepath.Join(dataDir, "chrome-data")
	os.MkdirAll(chromeDataDir, 0755)
	var profileDir string

	if flags.profile != "" {
		profileDir, _ = resolveProfile(chromeDataDir, flags.profile)
	}

	l := launcher.New().
		Leakless(false).
		UserDataDir(chromeDataDir).
		Headless(headless)

	if profileDir != "" {
		l = l.Set("profile-directory", profileDir)
	}

	if os.Getuid() == 0 {
		l = l.Set("no-sandbox")
	}

	if headless {
		l = l.Set("disable-gpu")
	}

	if !flags.stealth || headless {
		l = l.Set("single-process")
	}

	if !headless {
		l = l.Delete("no-startup-window")
	}

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	if flags.stealth && os.Getenv("ROD_CHROME_BIN") == "" {
		for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium-browser", "chromium"} {
			if path, err := exec.LookPath(name); err == nil {
				l = l.Bin(path)
				break
			}
		}
	}

	// Detect authenticated proxy and launch helper if needed
	var proxyPID, proxyPort int
	var proxyServer, proxyHash string
	if server, user, pass, needed := detectProxy(); needed {
		proxyServer = server
		proxyHash = proxyConfigHash(getProxyEnvVar())

		authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal("failed to find free port for proxy: %v", err)
		}
		proxyPort = ln.Addr().(*net.TCPAddr).Port
		ln.Close()

		exe, _ := os.Executable()
		cmd := exec.Command(exe, "_proxy",
			strconv.Itoa(proxyPort), server, authHeader)
		setSysProcAttr(cmd)
		if err := cmd.Start(); err != nil {
			fatal("failed to start proxy helper: %v", err)
		}
		proxyPID = cmd.Process.Pid
		cmd.Process.Release()

		time.Sleep(500 * time.Millisecond)

		l.Set("proxy-server", fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
		ignoreCertErrors = true
		fmt.Fprintf(os.Stderr, "Auth proxy started (PID %d, port %d) -> %s\n", proxyPID, proxyPort, server)
	}

	if ignoreCertErrors {
		l.Set("ignore-certificate-errors")
	}

	if flags.stealth {
		l.Set("disable-blink-features", "AutomationControlled")
		l.Delete("enable-automation")

		if headless {
			l.Headless(false)
			l.Set("headless", "new")
		}
	}

	debugURL := l.MustLaunch()
	pid := l.PID()

	var vpWidth, vpHeight int
	if flags.stealth && headless {
		vp := flags.viewport
		if vp == "" {
			vp = "1920x935"
		}
		parts := strings.SplitN(vp, "x", 2)
		vpWidth, _ = strconv.Atoi(parts[0])
		vpHeight, _ = strconv.Atoi(parts[1])
	}

	return launchResult{
		debugURL:      debugURL,
		pid:           pid,
		chromeDataDir: chromeDataDir,
		proxyPID:      proxyPID,
		proxyPort:     proxyPort,
		proxyServer:   proxyServer,
		proxyHash:     proxyHash,
		vpWidth:       vpWidth,
		vpHeight:      vpHeight,
	}
}

// cmdNewSession is the session-centric entry point: it lazily launches Chrome
// (or reuses an existing browser), creates a new page/window, assigns a unique
// session ID, persists everything to state.json and the global registry, and
// prints ONLY the session ID to stdout.
func cmdNewSession(args []string) {
	flags, err := parseStartFlags(args)
	if err != nil {
		fatal("%s", err)
	}

	wd, _ := os.Getwd()
	dataDir := resolveNewSessionDir(activeScopeMode, wd, homeDirFlag)
	activeStateDir = dataDir

	s, loadErr := loadState()
	var browser *rod.Browser
	browserAlreadyRunning := false

	if loadErr == nil && s.ChromePID > 0 {
		if proc, findErr := os.FindProcess(s.ChromePID); findErr == nil {
			if proc.Signal(syscall.Signal(0)) == nil {
				// PID alive: check compatibility
				currentProxyHash := ""
				if proxyEnv := getProxyEnvVar(); proxyEnv != "" {
					currentProxyHash = proxyConfigHash(proxyEnv)
				}
				if compatErr := checkBrowserCompat(s, &flags, currentProxyHash); compatErr != nil {
					fatal("%v", compatErr)
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
		removeState()
		result := launchChrome(&flags, dataDir)

		s = &State{
			DebugURL:        result.debugURL,
			ChromePID:       result.pid,
			DataDir:         result.chromeDataDir,
			Headless:        flags.headless,
			Stealth:         flags.stealth,
			Insecure:        flags.ignoreCertErrors,
			Profile:         flags.profile,
			ProxyPID:        result.proxyPID,
			ProxyPort:       result.proxyPort,
			ProxyServer:     result.proxyServer,
			ProxyConfigHash: result.proxyHash,
			Sessions:        make(map[string]SessionInfo),
		}

		browser, err = connectBrowser(s)
		if err != nil {
			fatal("cannot connect to launched browser: %v", err)
		}

		if s.Stealth {
			pages, _ := browser.Pages()
			for _, p := range pages {
				applyStealthToPage(p, browser,result.vpWidth, result.vpHeight)
			}
		}

		if err := saveState(s); err != nil {
			fatal("save state: %v", err)
		}
	}

	// Create page: claim the blank tab for the first session, new window otherwise.
	var page *rod.Page
	if s.Sessions == nil {
		s.Sessions = make(map[string]SessionInfo)
	}
	if len(s.Sessions) == 0 {
		pages, _ := browser.Pages()
		if len(pages) > 0 {
			page = pages[0]
		} else {
			page = browser.MustPage("")
		}
	} else {
		t, createErr := proto.TargetCreateTarget{URL: "", NewWindow: true}.Call(browser)
		if createErr != nil {
			fatal("create window: %v", createErr)
		}
		page = browser.MustPageFromTargetID(t.TargetID)
		if s.Stealth {
			applyStealthToPage(page, browser,0, 0)
		}
	}

	// Navigate to URL if provided
	if flags.url != "" {
		u := flags.url
		if !strings.Contains(u, "://") {
			u = "http://" + u
		}
		page.MustNavigate(u).MustWaitLoad()
	}

	// Generate unique session ID, avoiding collisions with the global registry
	sessionID := shortID()
	reg, _ := registryLoadAll(registryPath())
	for {
		if _, exists := reg[sessionID]; !exists {
			break
		}
		sessionID = shortID()
	}

	// Parse viewport
	vw, vh := 0, 0
	if flags.viewport != "" {
		parts := strings.SplitN(flags.viewport, "x", 2)
		if len(parts) == 2 {
			vw, _ = strconv.Atoi(parts[0])
			vh, _ = strconv.Atoi(parts[1])
		}
	}

	// Apply viewport via CDP if specified
	if vw > 0 && vh > 0 {
		proto.EmulationSetDeviceMetricsOverride{
			Width: vw, Height: vh, DeviceScaleFactor: 1,
		}.Call(page)
	}

	// Persist session info
	s.Sessions[sessionID] = SessionInfo{
		TargetID:       string(page.TargetID),
		ViewportWidth:  vw,
		ViewportHeight: vh,
		NoCapture:      flags.noCapture,
	}
	if err := saveState(s); err != nil {
		fatal("save state: %v", err)
	}
	if err := registryAdd(registryPath(), registryLockPath(), sessionID, dataDir); err != nil {
		fatal("registry add: %v", err)
	}

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

	fmt.Println(sessionID)
}

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
		// Session not in state but in registry; clean up registry
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
		// Clean up stealthCtxMap entry
		stealthCtxMap.Delete(si.TargetID)
	}

	// 5. Remove session from state (under lock) and check if last session
	slp := filepath.Join(dataDir, "state.lock")
	wasLastSession := false
	withFileLock(slp, func() error {
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

	// Clean up network capture data for this session
	netSessionDir := filepath.Join(dataDir, "net", sid)
	os.RemoveAll(netSessionDir)

	// 7. If no sessions remain, shut down browser
	if wasLastSession && browser != nil {
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
		if s.ChromePID > 0 {
			waitForProcessExit(s.ChromePID, 3*time.Second)
			if proc, err := os.FindProcess(s.ChromePID); err == nil {
				proc.Signal(syscall.SIGTERM)
				waitForProcessExit(s.ChromePID, 2*time.Second)
			}
		}

		// Kill proxy helper
		if s.ProxyPID > 0 {
			if proc, err := os.FindProcess(s.ProxyPID); err == nil {
				proc.Signal(syscall.SIGTERM)
			}
		}

		// Kill network monitor
		if s.MonitorPID > 0 {
			if proc, err := os.FindProcess(s.MonitorPID); err == nil {
				proc.Signal(syscall.SIGTERM)
			}
		}

		// Clean up net directory
		os.RemoveAll(filepath.Join(dataDir, "net"))

		// Remove state files
		os.Remove(sp)
		os.Remove(slp)

		// Remove temp dir if applicable
		if strings.HasPrefix(dataDir, filepath.Join(os.TempDir(), "rodney-")) {
			os.RemoveAll(dataDir)
		}
	}
}

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
	registry, err := registryLoadAll(regPath)
	if err != nil {
		fatal("cannot read registry: %v", err)
	}
	if len(registry) == 0 {
		fmt.Fprintln(os.Stderr, "no sessions")
		return
	}

	// Resolve active session for marker
	activeSID := resolveSessionID(activeSessionID, "")

	// If session context is set and --all not passed, filter to that session's data dir
	filterDir := ""
	if !showAll && activeSID != "" {
		if dd, ok := registry[activeSID]; ok {
			filterDir = dd
		}
	}

	// Group sessions by data dir
	groups := make(map[string][]string) // dataDir -> []sessionID
	var groupOrder []string
	for sid, dd := range registry {
		if filterDir != "" && dd != filterDir {
			continue
		}
		if _, seen := groups[dd]; !seen {
			groupOrder = append(groupOrder, dd)
		}
		groups[dd] = append(groups[dd], sid)
	}

	lockPath := registryLockPath()
	home, _ := os.UserHomeDir()
	wd, _ := os.Getwd()

	for _, dataDir := range groupOrder {
		sids := groups[dataDir]

		// Format header
		header := dataDir
		localDir := filepath.Join(wd, ".rodney")
		globalDir := filepath.Join(home, ".rodney")
		switch dataDir {
		case localDir:
			header = ".rodney/"
		case globalDir:
			header = "~/.rodney/"
		default:
			if strings.HasPrefix(dataDir, home+"/") {
				header = "~/" + dataDir[len(home)+1:] + "/"
			}
		}

		// Read state.json for this data dir
		sp := filepath.Join(dataDir, "state.json")
		data, readErr := os.ReadFile(sp)
		if readErr != nil {
			// state.json missing: prune all sessions for this dir from registry
			for _, sid := range sids {
				registryRemove(regPath, lockPath, sid)
				os.RemoveAll(filepath.Join(dataDir, "net", sid))
			}
			continue
		}

		var s State
		if err := json.Unmarshal(data, &s); err != nil {
			// Corrupt state: prune
			for _, sid := range sids {
				registryRemove(regPath, lockPath, sid)
				os.RemoveAll(filepath.Join(dataDir, "net", sid))
			}
			continue
		}

		// Check if Chrome PID is alive
		pidAlive := false
		if s.ChromePID > 0 {
			if proc, findErr := os.FindProcess(s.ChromePID); findErr == nil {
				if proc.Signal(syscall.Signal(0)) == nil {
					pidAlive = true
				}
			}
		}

		if !pidAlive {
			// PID dead: mark sessions as stale
			var entries []sessionEntry
			for _, sid := range sids {
				si, ok := s.Sessions[sid]
				if !ok {
					registryRemove(regPath, lockPath, sid)
					os.RemoveAll(filepath.Join(dataDir, "net", sid))
					continue
				}
				_ = si
				entries = append(entries, sessionEntry{
					ID:    sid,
					Title: "(stale)",
					URL:   "",
				})
			}
			if len(entries) > 0 {
				fmt.Print(formatSessionsGroup(header, entries, activeSID))
			}
			continue
		}

		// PID alive: connect and get page info
		browser, connErr := connectBrowser(&s)
		if connErr != nil {
			// Cannot connect but PID alive: treat as stale
			var entries []sessionEntry
			for _, sid := range sids {
				entries = append(entries, sessionEntry{
					ID:    sid,
					Title: "(stale)",
					URL:   "",
				})
			}
			fmt.Print(formatSessionsGroup(header, entries, activeSID))
			continue
		}

		pages, _ := browser.Pages()
		pageMap := make(map[string]*rod.Page)
		for _, p := range pages {
			pageMap[string(p.TargetID)] = p
		}

		var entries []sessionEntry
		for _, sid := range sids {
			si, ok := s.Sessions[sid]
			if !ok {
				registryRemove(regPath, lockPath, sid)
				os.RemoveAll(filepath.Join(dataDir, "net", sid))
				continue
			}
			title := ""
			pageURL := ""
			if p, found := pageMap[si.TargetID]; found {
				info, infoErr := p.Info()
				if infoErr == nil {
					title = info.Title
					pageURL = info.URL
				}
			} else {
				title = "(closed)"
			}
			entries = append(entries, sessionEntry{
				ID:    sid,
				Title: title,
				URL:   pageURL,
			})
		}
		if len(entries) > 0 {
			fmt.Print(formatSessionsGroup(header, entries, activeSID))
		}
	}
}

func cmdOpen(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney open <url>")
	}
	url := args[0]
	// Add scheme if missing
	if !strings.Contains(url, "://") {
		url = "http://" + url
	}

	s, browser, page := withPage()
	sendInteractionMarker("open", []string{url})

	// Re-apply stealth on each navigation. The CDP session from the
	// previous CLI invocation has disconnected, so session-scoped
	// state like Emulation.setUserAgentOverride is lost.
	if s.Stealth {
		vpW, vpH := 0, 0
		if si, ok := s.Sessions[activeSessionID]; ok {
			vpW, vpH = si.ViewportWidth, si.ViewportHeight
		}
		applyStealthToPage(page, browser,vpW, vpH)
	}
	if err := page.Navigate(url); err != nil {
		fatal("navigation failed: %v", err)
	}
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.Title)
	}
}

func cmdBack(args []string) {
	_, _, page := withPage()
	sendInteractionMarker("back", nil)
	page.MustNavigateBack()
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.URL)
	}
}

func cmdForward(args []string) {
	_, _, page := withPage()
	sendInteractionMarker("forward", nil)
	page.MustNavigateForward()
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.URL)
	}
}

func cmdReload(args []string) {
	hard := false
	for _, a := range args {
		if a == "--hard" {
			hard = true
		}
	}
	_, _, page := withPage()
	sendInteractionMarker("reload", args)
	if hard {
		// CDP Page.reload with ignoreCache (equivalent to Shift+Refresh)
		err := (proto.PageReload{IgnoreCache: true}).Call(page)
		if err != nil {
			fatal("reload failed: %v", err)
		}
	} else {
		page.MustReload()
	}
	page.MustWaitLoad()
	fmt.Println("Reloaded")
}

func cmdClearCache(args []string) {
	_, _, page := withPage()
	err := (proto.NetworkClearBrowserCache{}).Call(page)
	if err != nil {
		fatal("clear cache failed: %v", err)
	}
	fmt.Println("Browser cache cleared")
}

func cmdURL(args []string) {
	_, _, page := withPage()
	info, err := page.Info()
	if err != nil {
		fatal("failed to get page info: %v", err)
	}
	fmt.Println(info.URL)
}

func cmdTitle(args []string) {
	_, _, page := withPage()
	info, err := page.Info()
	if err != nil {
		fatal("failed to get page info: %v", err)
	}
	fmt.Println(info.Title)
}

func cmdHTML(args []string) {
	s, _, page := withPage()
	if s.Stealth {
		sc := getStealthCtx(page, s)
		if len(args) > 0 {
			nodeID, err := sc.element(args[0], defaultTimeout)
			if err != nil {
				fatal("element not found: %v", err)
			}
			html, err := sc.outerHTML(nodeID)
			if err != nil {
				fatal("failed to get HTML: %v", err)
			}
			fmt.Println(html)
		} else {
			result, err := sc.eval("document.documentElement.outerHTML")
			if err != nil {
				fatal("failed to get HTML: %v", err)
			}
			fmt.Println(result.Result.Value.Str())
		}
		return
	}
	if len(args) > 0 {
		el, err := page.Element(args[0])
		if err != nil {
			fatal("element not found: %v", err)
		}
		html, err := el.HTML()
		if err != nil {
			fatal("failed to get HTML: %v", err)
		}
		fmt.Println(html)
	} else {
		html := page.MustEval(`() => document.documentElement.outerHTML`).Str()
		fmt.Println(html)
	}
}

func cmdText(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney text <selector>")
	}
	s, _, page := withPage()
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		text, err := sc.text(nodeID)
		if err != nil {
			fatal("failed to get text: %v", err)
		}
		fmt.Println(text)
		return
	}
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	text, err := el.Text()
	if err != nil {
		fatal("failed to get text: %v", err)
	}
	fmt.Println(text)
}

func cmdAttr(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney attr <selector> <attribute>")
	}
	s, _, page := withPage()
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		val, err := sc.attr(nodeID, args[1])
		if err != nil {
			fatal("attribute %q not found", args[1])
		}
		fmt.Println(val)
		return
	}
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	val := el.MustAttribute(args[1])
	if val == nil {
		fatal("attribute %q not found", args[1])
	}
	fmt.Println(*val)
}

func cmdPDF(args []string) {
	file := "page.pdf"
	if len(args) > 0 {
		file = args[0]
	}
	_, _, page := withPage()
	req := proto.PagePrintToPDF{}
	r, err := page.PDF(&req)
	if err != nil {
		fatal("failed to generate PDF: %v", err)
	}
	buf := make([]byte, 0)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	if err := os.WriteFile(file, buf, 0644); err != nil {
		fatal("failed to write PDF: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", file, len(buf))
}

func cmdJS(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney js <expression>")
	}
	expr := strings.Join(args, " ")
	s, _, page := withPage()
	sendInteractionMarker("js", args)

	if s.Stealth {
		sc := getStealthCtx(page, s)
		result, err := sc.eval(expr)
		if err != nil {
			fatal("JS error: %v", err)
		}
		v := result.Result.Value
		raw := v.JSON("", "")
		switch {
		case raw == "null" || raw == "undefined":
			fmt.Println(raw)
		case raw == "true" || raw == "false":
			fmt.Println(raw)
		case len(raw) > 0 && raw[0] == '"':
			fmt.Println(v.Str())
		case len(raw) > 0 && (raw[0] == '{' || raw[0] == '['):
			fmt.Println(v.JSON("", "  "))
		default:
			fmt.Println(raw)
		}
		return
	}

	// Wrap bare expressions in a function
	js := fmt.Sprintf(`() => { return (%s); }`, expr)
	result, err := page.Eval(js)
	if err != nil {
		fatal("JS error: %v", err)
	}
	// Print the value based on its JSON type
	v := result.Value
	raw := v.JSON("", "")
	// For simple types, print cleanly; for objects/arrays, pretty-print
	switch {
	case raw == "null" || raw == "undefined":
		fmt.Println(raw)
	case raw == "true" || raw == "false":
		fmt.Println(raw)
	case len(raw) > 0 && raw[0] == '"':
		// String value - print unquoted
		fmt.Println(v.Str())
	case len(raw) > 0 && (raw[0] == '{' || raw[0] == '['):
		// Object or array - pretty print
		fmt.Println(v.JSON("", "  "))
	default:
		// Numbers and other primitives
		fmt.Println(raw)
	}
}

func cmdClick(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney click <selector>")
	}
	s, _, page := withPage()
	sendInteractionMarker("click", args)
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		if err := sc.click(nodeID); err != nil {
			fatal("click failed: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
		fmt.Println("Clicked")
		return
	}
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	if err := el.Click(proto.InputMouseButtonLeft, 1); err != nil {
		fatal("click failed: %v", err)
	}
	// Brief pause for click handlers to execute
	time.Sleep(100 * time.Millisecond)
	fmt.Println("Clicked")
}

func cmdInput(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney input <selector> <text>")
	}
	s, _, page := withPage()
	sendInteractionMarker("input", args)
	text := strings.Join(args[1:], " ")
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		if err := sc.input(nodeID, text); err != nil {
			fatal("input failed: %v", err)
		}
		fmt.Printf("Typed: %s\n", text)
		return
	}
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustSelectAllText().MustInput(text)
	fmt.Printf("Typed: %s\n", text)
}

func cmdClear(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney clear <selector>")
	}
	s, _, page := withPage()
	sendInteractionMarker("clear", args)
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		if err := sc.clearInput(nodeID); err != nil {
			fatal("clear failed: %v", err)
		}
		fmt.Println("Cleared")
		return
	}
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustSelectAllText().MustInput("")
	fmt.Println("Cleared")
}

func cmdFile(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney file <selector> <path|->")
	}
	selector := args[0]
	filePath := args[1]

	_, _, page := withPage()
	sendInteractionMarker("file", args)
	el, err := page.Element(selector)
	if err != nil {
		fatal("element not found: %v", err)
	}

	if filePath == "-" {
		// Read from stdin to a temp file
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fatal("failed to read stdin: %v", err)
		}
		tmp, err := os.CreateTemp("", "rodney-upload-*")
		if err != nil {
			fatal("failed to create temp file: %v", err)
		}
		if _, err := tmp.Write(data); err != nil {
			tmp.Close()
			fatal("failed to write temp file: %v", err)
		}
		tmp.Close()
		filePath = tmp.Name()
	} else {
		if _, err := os.Stat(filePath); err != nil {
			fatal("file not found: %v", err)
		}
	}

	if err := el.SetFiles([]string{filePath}); err != nil {
		fatal("failed to set file: %v", err)
	}
	fmt.Printf("Set file: %s\n", args[1])
}

func cmdDownload(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney download <selector> [file|-]")
	}
	selector := args[0]
	outFile := ""
	if len(args) > 1 {
		outFile = args[1]
	}

	s, _, page := withPage()

	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(selector, defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		data, err := sc.download(nodeID)
		if err != nil {
			fatal("download failed: %v", err)
		}

		if outFile == "-" {
			os.Stdout.Write(data)
			return
		}

		// Infer filename from the element's href or src attribute
		if outFile == "" {
			urlStr, err := sc.attr(nodeID, "href")
			if err != nil {
				urlStr, _ = sc.attr(nodeID, "src")
			}
			if urlStr != "" {
				outFile = inferDownloadFilename(urlStr)
			} else {
				outFile = nextAvailableFile("download", "")
			}
		}

		if err := os.WriteFile(outFile, data, 0644); err != nil {
			fatal("failed to write file: %v", err)
		}
		fmt.Printf("Saved %s (%d bytes)\n", outFile, len(data))
		return
	}

	el, err := page.Element(selector)
	if err != nil {
		fatal("element not found: %v", err)
	}

	// Get the URL from the element's href or src attribute
	urlStr := ""
	if v := el.MustAttribute("href"); v != nil {
		urlStr = *v
	} else if v := el.MustAttribute("src"); v != nil {
		urlStr = *v
	} else {
		fatal("element has no href or src attribute")
	}

	var data []byte

	if strings.HasPrefix(urlStr, "data:") {
		data, err = decodeDataURL(urlStr)
		if err != nil {
			fatal("failed to decode data URL: %v", err)
		}
	} else {
		// Use fetch() in the page context so it has cookies/session
		// Also resolves relative URLs automatically
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
		}`, urlStr)
		result, err := page.Eval(js)
		if err != nil {
			fatal("download failed: %v", err)
		}
		data, err = base64.StdEncoding.DecodeString(result.Value.Str())
		if err != nil {
			fatal("failed to decode response: %v", err)
		}
	}

	if outFile == "-" {
		os.Stdout.Write(data)
		return
	}

	if outFile == "" {
		outFile = inferDownloadFilename(urlStr)
	}

	if err := os.WriteFile(outFile, data, 0644); err != nil {
		fatal("failed to write file: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", outFile, len(data))
}

// decodeDataURL decodes a data:[<mediatype>][;base64],<data> URL.
func decodeDataURL(dataURL string) ([]byte, error) {
	// Find the comma separating metadata from data
	commaIdx := strings.Index(dataURL, ",")
	if commaIdx < 0 {
		return nil, fmt.Errorf("invalid data URL: no comma found")
	}
	meta := dataURL[5:commaIdx] // skip "data:"
	encoded := dataURL[commaIdx+1:]

	if strings.HasSuffix(meta, ";base64") {
		return base64.StdEncoding.DecodeString(encoded)
	}
	// URL-encoded text
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		return nil, err
	}
	return []byte(decoded), nil
}

// inferDownloadFilename tries to extract a reasonable filename from a URL.
func inferDownloadFilename(urlStr string) string {
	if strings.HasPrefix(urlStr, "data:") {
		// Extract MIME type for extension
		commaIdx := strings.Index(urlStr, ",")
		if commaIdx > 0 {
			meta := urlStr[5:commaIdx]
			meta = strings.TrimSuffix(meta, ";base64")
			ext := mimeToExt(meta)
			return nextAvailableFile("download", ext)
		}
		return nextAvailableFile("download", "")
	}

	parsed, err := url.Parse(urlStr)
	if err == nil && parsed.Path != "" && parsed.Path != "/" {
		base := filepath.Base(parsed.Path)
		if base != "." && base != "/" {
			return nextAvailableFile(
				strings.TrimSuffix(base, filepath.Ext(base)),
				filepath.Ext(base),
			)
		}
	}
	return nextAvailableFile("download", "")
}

// mimeToExt returns a file extension for common MIME types.
func mimeToExt(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/html":
		return ".html"
	case "text/css":
		return ".css"
	case "application/json":
		return ".json"
	case "application/javascript":
		return ".js"
	case "application/octet-stream":
		return ".bin"
	default:
		return ""
	}
}

func cmdSelect(args []string) {
	if len(args) < 2 {
		fatal("usage: rodney select <selector> <value>")
	}
	s, _, page := withPage()
	sendInteractionMarker("select", args)
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		if err := sc.selectOption(nodeID, args[1]); err != nil {
			fatal("select failed: %v", err)
		}
		fmt.Printf("Selected: %s\n", args[1])
		return
	}
	// Use JavaScript to set the value, as rod's Select matches by text
	js := fmt.Sprintf(`() => {
		const el = document.querySelector(%q);
		if (!el) throw new Error('element not found');
		el.value = %q;
		el.dispatchEvent(new Event('change', {bubbles: true}));
		return el.value;
	}`, args[0], args[1])
	result, err := page.Eval(js)
	if err != nil {
		fatal("select failed: %v", err)
	}
	fmt.Printf("Selected: %s\n", result.Value.Str())
}

func cmdSubmit(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney submit <selector>")
	}
	s, _, page := withPage()
	sendInteractionMarker("submit", args)
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("form not found: %v", err)
		}
		if err := sc.submit(nodeID); err != nil {
			fatal("submit failed: %v", err)
		}
		fmt.Println("Submitted")
		return
	}
	_, err := page.Element(args[0])
	if err != nil {
		fatal("form not found: %v", err)
	}
	page.MustEval(fmt.Sprintf(`() => document.querySelector(%q).submit()`, args[0]))
	fmt.Println("Submitted")
}

func cmdHover(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney hover <selector>")
	}
	s, _, page := withPage()
	sendInteractionMarker("hover", args)
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		if err := sc.hover(nodeID); err != nil {
			fatal("hover failed: %v", err)
		}
		fmt.Println("Hovered")
		return
	}
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustHover()
	fmt.Println("Hovered")
}

func cmdFocus(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney focus <selector>")
	}
	s, _, page := withPage()
	sendInteractionMarker("focus", args)
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		if err := sc.focus(nodeID); err != nil {
			fatal("focus failed: %v", err)
		}
		fmt.Println("Focused")
		return
	}
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustFocus()
	fmt.Println("Focused")
}

func cmdWait(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney wait <selector>")
	}
	s, _, page := withPage()
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		// Poll visible until true or timeout
		deadline := time.Now().Add(defaultTimeout)
		for {
			vis, err := sc.visible(nodeID)
			if err == nil && vis {
				break
			}
			if time.Now().After(deadline) {
				fatal("element %q not visible within %v", args[0], defaultTimeout)
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Println("Element visible")
		return
	}
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	el.MustWaitVisible()
	fmt.Println("Element visible")
}

func cmdWaitLoad(args []string) {
	_, _, page := withPage()
	page.MustWaitLoad()
	fmt.Println("Page loaded")
}

func cmdWaitStable(args []string) {
	_, _, page := withPage()
	page.MustWaitStable()
	fmt.Println("DOM stable")
}

func cmdWaitIdle(args []string) {
	_, _, page := withPage()
	page.MustWaitIdle()
	fmt.Println("Network idle")
}

func cmdSleep(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney sleep <seconds>")
	}
	secs, err := strconv.ParseFloat(args[0], 64)
	if err != nil {
		fatal("invalid seconds: %v", err)
	}
	time.Sleep(time.Duration(secs * float64(time.Second)))
}

// nextAvailableFile returns "base+ext" if it doesn't exist,
// otherwise "base-2+ext", "base-3+ext", etc.
func nextAvailableFile(base, ext string) string {
	name := base + ext
	if _, err := os.Stat(name); os.IsNotExist(err) {
		return name
	}
	for i := 2; ; i++ {
		name = fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(name); os.IsNotExist(err) {
			return name
		}
	}
}

func cmdScreenshot(args []string) {
	var file string
	width := 1280
	height := 0
	fullPage := true

	// Parse flags and positional args
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-w", "--width":
			i++
			if i >= len(args) {
				fatal("missing value for %s", args[i-1])
			}
			v, err := strconv.Atoi(args[i])
			if err != nil {
				fatal("invalid width: %v", err)
			}
			width = v
		case "-h", "--height":
			i++
			if i >= len(args) {
				fatal("missing value for %s", args[i-1])
			}
			v, err := strconv.Atoi(args[i])
			if err != nil {
				fatal("invalid height: %v", err)
			}
			height = v
			fullPage = false
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) > 0 {
		file = positional[0]
	} else {
		file = nextAvailableFile("screenshot", ".png")
	}

	_, _, page := withPage()

	// Set viewport size
	viewportHeight := height
	if viewportHeight == 0 {
		viewportHeight = 720
	}
	err := proto.EmulationSetDeviceMetricsOverride{
		Width:             width,
		Height:            viewportHeight,
		DeviceScaleFactor: 1,
	}.Call(page)
	if err != nil {
		fatal("failed to set viewport: %v", err)
	}

	data, err := page.Screenshot(fullPage, nil)
	if err != nil {
		fatal("screenshot failed: %v", err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write screenshot: %v", err)
	}
	fmt.Println(file)
}

func cmdScreenshotEl(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney screenshot-el <selector> [file]")
	}
	file := "element.png"
	if len(args) > 1 {
		file = args[1]
	}
	s, _, page := withPage()
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fatal("element not found: %v", err)
		}
		data, err := sc.screenshotElement(nodeID)
		if err != nil {
			fatal("screenshot failed: %v", err)
		}
		if err := os.WriteFile(file, data, 0644); err != nil {
			fatal("failed to write screenshot: %v", err)
		}
		fmt.Printf("Saved %s (%d bytes)\n", file, len(data))
		return
	}
	el, err := page.Element(args[0])
	if err != nil {
		fatal("element not found: %v", err)
	}
	data, err := el.Screenshot(proto.PageCaptureScreenshotFormatPng, 0)
	if err != nil {
		fatal("screenshot failed: %v", err)
	}
	if err := os.WriteFile(file, data, 0644); err != nil {
		fatal("failed to write screenshot: %v", err)
	}
	fmt.Printf("Saved %s (%d bytes)\n", file, len(data))
}

func cmdExists(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney exists <selector>")
	}
	s, _, page := withPage()
	if s.Stealth {
		sc := getStealthCtx(page, s)
		found, err := sc.exists(args[0])
		if err != nil {
			fatal("query failed: %v", err)
		}
		if found {
			fmt.Println("true")
			os.Exit(0)
		} else {
			fmt.Println("false")
			os.Exit(1)
		}
		return
	}
	has, _, err := page.Has(args[0])
	if err != nil {
		fatal("query failed: %v", err)
	}
	if has {
		fmt.Println("true")
		os.Exit(0)
	} else {
		fmt.Println("false")
		os.Exit(1)
	}
}

func cmdCount(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney count <selector>")
	}
	s, _, page := withPage()
	if s.Stealth {
		sc := getStealthCtx(page, s)
		n, err := sc.count(args[0])
		if err != nil {
			fatal("query failed: %v", err)
		}
		fmt.Println(n)
		return
	}
	els, err := page.Elements(args[0])
	if err != nil {
		fatal("query failed: %v", err)
	}
	fmt.Println(len(els))
}

func cmdVisible(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney visible <selector>")
	}
	s, _, page := withPage()
	if s.Stealth {
		sc := getStealthCtx(page, s)
		nodeID, err := sc.element(args[0], defaultTimeout)
		if err != nil {
			fmt.Println("false")
			os.Exit(1)
			return
		}
		vis, err := sc.visible(nodeID)
		if err != nil || !vis {
			fmt.Println("false")
			os.Exit(1)
			return
		}
		fmt.Println("true")
		os.Exit(0)
		return
	}
	el, err := page.Element(args[0])
	if err != nil {
		fmt.Println("false")
		os.Exit(1)
	}
	visible, err := el.Visible()
	if err != nil {
		fmt.Println("false")
		os.Exit(1)
	}
	if visible {
		fmt.Println("true")
		os.Exit(0)
	} else {
		fmt.Println("false")
		os.Exit(1)
	}
}

// parseAssertArgs separates flags (--message/-m) from positional args.
// Returns (expression, expected, message). expected is nil for truthy mode.
func parseAssertArgs(args []string) (expr string, expected *string, message string) {
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--message", "-m":
			i++
			if i < len(args) {
				message = args[i]
			}
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) >= 1 {
		expr = positional[0]
	}
	if len(positional) >= 2 {
		expected = &positional[1]
	}
	return
}

// formatAssertFail builds the failure output line.
// For truthy failures expected is nil; for equality failures it points to the expected string.
func formatAssertFail(actual string, expected *string, message string) string {
	if expected != nil {
		// Equality mode
		detail := fmt.Sprintf("got %q, expected %q", actual, *expected)
		if message != "" {
			return fmt.Sprintf("fail: %s (%s)", message, detail)
		}
		return fmt.Sprintf("fail: %s", detail)
	}
	// Truthy mode
	if message != "" {
		return fmt.Sprintf("fail: %s (got %s)", message, actual)
	}
	return fmt.Sprintf("fail: got %s", actual)
}

func cmdAssert(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney assert <js-expression> [expected] [--message msg]")
	}

	expr, expected, message := parseAssertArgs(args)
	if expr == "" {
		fatal("usage: rodney assert <js-expression> [expected] [--message msg]")
	}

	s, _, page := withPage()

	var raw, actual string

	if s.Stealth {
		sc := getStealthCtx(page, s)
		result, err := sc.eval(expr)
		if err != nil {
			fatal("JS error: %v", err)
		}
		v := result.Result.Value
		raw = v.JSON("", "")
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
	} else {
		js := fmt.Sprintf(`() => { return (%s); }`, expr)
		result, err := page.Eval(js)
		if err != nil {
			fatal("JS error: %v", err)
		}
		v := result.Value
		raw = v.JSON("", "")
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
	}

	if expected != nil {
		// Equality mode: compare string representation to expected
		if actual == *expected {
			fmt.Println("pass")
			os.Exit(0)
		} else {
			fmt.Println(formatAssertFail(actual, expected, message))
			os.Exit(1)
		}
	} else {
		// Truthy mode: check if the JS value is truthy
		switch raw {
		case "false", "0", "null", "undefined", `""`:
			fmt.Println(formatAssertFail(actual, nil, message))
			os.Exit(1)
		default:
			fmt.Println("pass")
			os.Exit(0)
		}
	}
}

// Ignore SIGPIPE for piped output
func init() {
	signal.Ignore(syscall.SIGPIPE)
}

// --- Accessibility commands ---

func cmdAXTree(args []string) {
	var depth *int
	jsonOutput := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--depth":
			i++
			if i >= len(args) {
				fatal("missing value for --depth")
			}
			v, err := strconv.Atoi(args[i])
			if err != nil {
				fatal("invalid depth: %v", err)
			}
			depth = &v
		case "--json":
			jsonOutput = true
		default:
			fatal("unknown flag: %s\nusage: rodney ax-tree [--depth N] [--json]", args[i])
		}
	}

	_, _, page := withPage()
	result, err := proto.AccessibilityGetFullAXTree{Depth: depth}.Call(page)
	if err != nil {
		fatal("failed to get accessibility tree: %v", err)
	}

	if jsonOutput {
		fmt.Println(formatAXTreeJSON(result.Nodes))
	} else {
		fmt.Print(formatAXTree(result.Nodes))
	}
}

func cmdAXFind(args []string) {
	var name, role string
	jsonOutput := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name":
			i++
			if i >= len(args) {
				fatal("missing value for --name")
			}
			name = args[i]
		case "--role":
			i++
			if i >= len(args) {
				fatal("missing value for --role")
			}
			role = args[i]
		case "--json":
			jsonOutput = true
		default:
			fatal("unknown flag: %s\nusage: rodney ax-find [--name N] [--role R] [--json]", args[i])
		}
	}

	_, _, page := withPage()
	nodes, err := queryAXNodes(page, name, role)
	if err != nil {
		fatal("query failed: %v", err)
	}

	if len(nodes) == 0 {
		fmt.Fprintln(os.Stderr, "No matching nodes")
		os.Exit(1)
	}

	if jsonOutput {
		data, _ := json.MarshalIndent(nodes, "", "  ")
		fmt.Println(string(data))
	} else {
		fmt.Print(formatAXNodeList(nodes))
	}
}

func cmdAXNode(args []string) {
	jsonOutput := false
	var positional []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		default:
			positional = append(positional, args[i])
		}
	}

	if len(positional) < 1 {
		fatal("usage: rodney ax-node <selector> [--json]")
	}
	selector := positional[0]

	_, _, page := withPage()
	node, err := getAXNode(page, selector)
	if err != nil {
		fatal("%v", err)
	}

	if jsonOutput {
		fmt.Println(formatAXNodeDetailJSON(node))
	} else {
		fmt.Print(formatAXNodeDetail(node))
	}
}

// queryAXNodes uses Accessibility.queryAXTree to find nodes by name and/or role.
func queryAXNodes(page *rod.Page, name, role string) ([]*proto.AccessibilityAXNode, error) {
	// Get the document node to use as query root
	zero := 0
	doc, err := proto.DOMGetDocument{Depth: &zero}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to get document: %w", err)
	}

	result, err := proto.AccessibilityQueryAXTree{
		BackendNodeID: doc.Root.BackendNodeID,
		AccessibleName: name,
		Role:           role,
	}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("accessibility query failed: %w", err)
	}

	return result.Nodes, nil
}

// getAXNode gets the accessibility node for a DOM element identified by CSS selector.
func getAXNode(page *rod.Page, selector string) (*proto.AccessibilityAXNode, error) {
	el, err := page.Element(selector)
	if err != nil {
		return nil, fmt.Errorf("element not found: %w", err)
	}

	// Describe the DOM node to get its backend node ID
	node, err := proto.DOMDescribeNode{ObjectID: el.Object.ObjectID}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to describe DOM node: %w", err)
	}

	result, err := proto.AccessibilityGetPartialAXTree{
		BackendNodeID:  node.Node.BackendNodeID,
		FetchRelatives: false,
	}.Call(page)
	if err != nil {
		return nil, fmt.Errorf("failed to get accessibility info: %w", err)
	}

	// Find the non-ignored node (the first non-ignored node is typically our target)
	for _, n := range result.Nodes {
		if !n.Ignored {
			return n, nil
		}
	}

	// Fall back to first node if all are ignored
	if len(result.Nodes) > 0 {
		return result.Nodes[0], nil
	}

	return nil, fmt.Errorf("no accessibility node found for selector %q", selector)
}

// axValueStr extracts a printable string from an AccessibilityAXValue.
func axValueStr(v *proto.AccessibilityAXValue) string {
	if v == nil {
		return ""
	}
	raw := v.Value.JSON("", "")
	// Unquote JSON strings
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err == nil {
			return s
		}
	}
	return raw
}

// formatAXTree formats a flat list of AX nodes as an indented text tree.
// Ignored nodes are skipped.
func formatAXTree(nodes []*proto.AccessibilityAXNode) string {
	if len(nodes) == 0 {
		return ""
	}

	// Build lookup maps
	nodeByID := make(map[proto.AccessibilityAXNodeID]*proto.AccessibilityAXNode)
	for _, n := range nodes {
		nodeByID[n.NodeID] = n
	}

	// Find root (node with no parent or first node)
	var rootID proto.AccessibilityAXNodeID
	for _, n := range nodes {
		if n.ParentID == "" {
			rootID = n.NodeID
			break
		}
	}
	if rootID == "" && len(nodes) > 0 {
		rootID = nodes[0].NodeID
	}

	var sb strings.Builder
	var walk func(id proto.AccessibilityAXNodeID, depth int)
	walk = func(id proto.AccessibilityAXNodeID, depth int) {
		node, ok := nodeByID[id]
		if !ok {
			return
		}
		// Skip ignored nodes but still recurse into their children
		if !node.Ignored {
			indent := strings.Repeat("  ", depth)
			role := axValueStr(node.Role)
			name := axValueStr(node.Name)

			line := fmt.Sprintf("%s[%s]", indent, role)
			if name != "" {
				line += fmt.Sprintf(" %q", name)
			}

			// Append interesting properties
			props := formatProperties(node.Properties)
			if props != "" {
				line += " (" + props + ")"
			}

			sb.WriteString(line + "\n")
			// Children at depth+1
			for _, childID := range node.ChildIDs {
				walk(childID, depth+1)
			}
		} else {
			// Ignored node: pass through to children at same depth
			for _, childID := range node.ChildIDs {
				walk(childID, depth)
			}
		}
	}

	walk(rootID, 0)
	return sb.String()
}

// formatProperties formats the interesting AX properties into a comma-separated string.
func formatProperties(props []*proto.AccessibilityAXProperty) string {
	if len(props) == 0 {
		return ""
	}
	var parts []string
	for _, p := range props {
		val := axValueStr(p.Value)
		switch string(p.Name) {
		case "focusable", "disabled", "editable", "hidden", "required",
			"checked", "expanded", "selected", "modal", "multiline",
			"multiselectable", "readonly", "focused", "settable":
			// Boolean-ish properties: only show if true
			if val == "true" {
				parts = append(parts, string(p.Name))
			}
		case "level":
			parts = append(parts, fmt.Sprintf("level=%s", val))
		case "autocomplete", "hasPopup", "orientation", "live",
			"relevant", "valuemin", "valuemax", "valuetext",
			"roledescription", "keyshortcuts":
			if val != "" {
				parts = append(parts, fmt.Sprintf("%s=%s", p.Name, val))
			}
		}
	}
	return strings.Join(parts, ", ")
}

// formatAXTreeJSON formats nodes as a JSON array.
func formatAXTreeJSON(nodes []*proto.AccessibilityAXNode) string {
	data, err := json.MarshalIndent(nodes, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(data)
}

// formatAXNodeList formats a list of nodes as single-line summaries.
func formatAXNodeList(nodes []*proto.AccessibilityAXNode) string {
	var sb strings.Builder
	for _, node := range nodes {
		role := axValueStr(node.Role)
		name := axValueStr(node.Name)
		line := fmt.Sprintf("[%s]", role)
		if name != "" {
			line += fmt.Sprintf(" %q", name)
		}
		if node.BackendDOMNodeID != 0 {
			line += fmt.Sprintf(" backendNodeId=%d", node.BackendDOMNodeID)
		}
		props := formatProperties(node.Properties)
		if props != "" {
			line += " (" + props + ")"
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

// formatAXNodeDetail formats a single node with all its properties in key: value format.
func formatAXNodeDetail(node *proto.AccessibilityAXNode) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("role: %s\n", axValueStr(node.Role)))
	if name := axValueStr(node.Name); name != "" {
		sb.WriteString(fmt.Sprintf("name: %s\n", name))
	}
	if desc := axValueStr(node.Description); desc != "" {
		sb.WriteString(fmt.Sprintf("description: %s\n", desc))
	}
	if val := axValueStr(node.Value); val != "" {
		sb.WriteString(fmt.Sprintf("value: %s\n", val))
	}
	for _, p := range node.Properties {
		val := axValueStr(p.Value)
		sb.WriteString(fmt.Sprintf("%s: %s\n", p.Name, val))
	}
	return sb.String()
}

// formatAXNodeDetailJSON formats a single node as JSON.
func formatAXNodeDetailJSON(node *proto.AccessibilityAXNode) string {
	data, err := json.MarshalIndent(node, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// --- Auth proxy for environments with authenticated HTTP proxies ---

// detectProxy checks for HTTPS_PROXY/HTTP_PROXY with credentials.
// Returns (proxyServer, username, password, true) if auth proxy is needed.
func detectProxy() (server, user, pass string, needed bool) {
	proxyEnv := os.Getenv("HTTPS_PROXY")
	if proxyEnv == "" {
		proxyEnv = os.Getenv("https_proxy")
	}
	if proxyEnv == "" {
		proxyEnv = os.Getenv("HTTP_PROXY")
	}
	if proxyEnv == "" {
		proxyEnv = os.Getenv("http_proxy")
	}
	if proxyEnv == "" {
		return "", "", "", false
	}
	parsed, err := url.Parse(proxyEnv)
	if err != nil || parsed.User == nil {
		return "", "", "", false
	}
	user = parsed.User.Username()
	pass, _ = parsed.User.Password()
	if user == "" {
		return "", "", "", false
	}
	server = parsed.Hostname() + ":" + parsed.Port()
	return server, user, pass, true
}

// cmdInternalProxy is a hidden subcommand: rodney _proxy <port> <upstream> <authHeader>
// It runs a local auth proxy that forwards to the upstream proxy with credentials.
func cmdInternalProxy(args []string) {
	if len(args) < 3 {
		fatal("usage: rodney _proxy <port> <upstream> <authHeader>")
	}
	port := args[0]
	upstream := args[1]
	authHeader := args[2]

	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		fatal("proxy listen failed: %v", err)
	}

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodConnect {
				proxyConnect(w, r, upstream, authHeader)
			} else {
				proxyHTTP(w, r, upstream, authHeader)
			}
		}),
	}
	server.Serve(listener) // blocks forever
}

func proxyConnect(w http.ResponseWriter, r *http.Request, upstream, authHeader string) {
	upstreamConn, err := net.DialTimeout("tcp", upstream, 30*time.Second)
	if err != nil {
		http.Error(w, "upstream dial failed", http.StatusBadGateway)
		return
	}

	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n",
		r.Host, r.Host, authHeader)
	if _, err := upstreamConn.Write([]byte(connectReq)); err != nil {
		upstreamConn.Close()
		http.Error(w, "upstream write failed", http.StatusBadGateway)
		return
	}

	buf := make([]byte, 4096)
	n, err := upstreamConn.Read(buf)
	if err != nil {
		upstreamConn.Close()
		http.Error(w, "upstream read failed", http.StatusBadGateway)
		return
	}
	response := string(buf[:n])
	if len(response) < 12 || response[9:12] != "200" {
		upstreamConn.Close()
		http.Error(w, "upstream rejected CONNECT", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstreamConn.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		upstreamConn.Close()
		return
	}

	clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go func() {
		io.Copy(upstreamConn, clientConn)
		upstreamConn.Close()
	}()
	go func() {
		io.Copy(clientConn, upstreamConn)
		clientConn.Close()
	}()
}

func proxyHTTP(w http.ResponseWriter, r *http.Request, upstream, authHeader string) {
	proxyURL, _ := url.Parse("http://" + upstream)
	transport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		ProxyConnectHeader: http.Header{
			"Proxy-Authorization": {authHeader},
		},
	}
	r.Header.Set("Proxy-Authorization", authHeader)

	resp, err := transport.RoundTrip(r)
	if err != nil {
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
