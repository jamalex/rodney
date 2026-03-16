package main

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

// homeDirFlag is set when --home-dir is explicitly passed on the command line.
// Used by cmdStop to clean up the session directory.
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

// resolveTempHomeDir handles the special --home-dir value "tmp" by creating
// a temporary directory. For any other value, it returns the path unchanged.
func resolveTempHomeDir(dir string) (string, error) {
	if dir == "tmp" {
		return os.MkdirTemp("/tmp", "rodney-session-")
	}
	return dir, nil
}

// cleanupSessionDir removes a session directory. Only called for directories
// created via --home-dir (never for default or env-var-based directories).
func cleanupSessionDir(dir string) {
	if dir == "" {
		return
	}
	os.RemoveAll(dir)
}

// extractScopeArgs scans args for --local/--global/--home-dir, removes them, and returns the mode and home dir.
// If both --local and --global appear, the last one wins. --home-dir takes a path argument.
func extractScopeArgs(args []string) (scopeMode, string, []string) {
	mode := scopeAuto
	homeDir := ""
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
		default:
			filtered = append(filtered, args[i])
		}
	}
	return mode, homeDir, filtered
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
		if _, err := os.Stat(filepath.Join(localDir, "state.json")); err == nil {
			return localDir
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".rodney")
	}
}

// State persisted between CLI invocations
type State struct {
	DebugURL       string `json:"debug_url"`
	ChromePID      int    `json:"chrome_pid"`
	ActivePage     int    `json:"active_page"`  // index into pages list
	DataDir        string `json:"data_dir"`
	ProxyPID       int    `json:"proxy_pid,omitempty"`  // PID of auth proxy helper
	ProxyPort      int    `json:"proxy_port,omitempty"` // local port of auth proxy
	Stealth        bool   `json:"stealth,omitempty"`
	ViewportWidth  int    `json:"viewport_width,omitempty"`
	ViewportHeight int    `json:"viewport_height,omitempty"`
}

func stateDir() string {
	if dir := os.Getenv("RODNEY_HOME"); dir != "" {
		return dir
	}
	if activeStateDir != "" {
		return activeStateDir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".rodney")
}

func statePath() string {
	return filepath.Join(stateDir(), "state.json")
}

func loadState() (*State, error) {
	data, err := os.ReadFile(statePath())
	if err != nil {
		return nil, fmt.Errorf("no browser session (run 'rodney start' first)")
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("corrupt state file: %w", err)
	}
	return &s, nil
}

func saveState(s *State) error {
	if err := os.MkdirAll(stateDir(), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(statePath(), data, 0644)
}

func removeState() {
	os.Remove(statePath())
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

// getActivePage returns the currently active page
func getActivePage(browser *rod.Browser, s *State) (*rod.Page, error) {
	pages, err := browser.Pages()
	if err != nil {
		return nil, fmt.Errorf("failed to list pages: %w", err)
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("no pages open")
	}
	idx := s.ActivePage
	if idx < 0 || idx >= len(pages) {
		idx = 0
	}
	return pages[idx], nil
}

func printUsage() {
	fmt.Print(helpText)
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

	// Extract --local/--global/--home-dir from all args before dispatching
	mode, homeDir, cleanedArgs := extractScopeArgs(os.Args[1:])
	if len(cleanedArgs) == 0 {
		printUsage()
		os.Exit(1)
	}

	if homeDir != "" {
		resolved, err := resolveTempHomeDir(homeDir)
		if err != nil {
			fatal("failed to create temp session directory: %v", err)
		}
		homeDir = resolved
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
	case "start":
		cmdStart(args)
	case "connect":
		cmdConnect(args)
	case "stop":
		cmdStop(args)
	case "status":
		cmdStatus(args)
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
	case "pages":
		cmdPages(args)
	case "page":
		cmdPage(args)
	case "newpage":
		cmdNewPage(args)
	case "closepage":
		cmdClosePage(args)
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
func applyStealthToPage(page *rod.Page, browser *rod.Browser, s *State) {
	// 1. Inject stealth scripts via CDP before any navigation occurs.
	// addScriptToEvaluateOnNewDocument persists across navigations.
	proto.PageAddScriptToEvaluateOnNewDocument{Source: stealth.JS}.Call(page)
	proto.PageAddScriptToEvaluateOnNewDocument{Source: workerFixJS}.Call(page)

	// 2. Set viewport
	if s.ViewportWidth > 0 && s.ViewportHeight > 0 {
		proto.EmulationSetDeviceMetricsOverride{
			Width:             s.ViewportWidth,
			Height:            s.ViewportHeight,
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

// withPage loads state, connects, and returns the active page.
// Caller should NOT close the browser (we just disconnect).
func withPage() (*State, *rod.Browser, *rod.Page) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	page, err := getActivePage(browser, s)
	if err != nil {
		fatal("%v", err)
	}
	// Apply default timeout so element queries don't hang forever
	page = page.Timeout(defaultTimeout)
	return s, browser, page
}

// --- Commands ---

type startFlags struct {
	headless         bool
	ignoreCertErrors bool
	stealth          bool
	viewport         string
	profile          string
}

// parseStartFlags parses the arguments to "rodney start".
func parseStartFlags(args []string) (startFlags, error) {
	f := startFlags{headless: true}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--show":
			f.headless = false
		case "--insecure", "-k":
			f.ignoreCertErrors = true
		case "--stealth":
			f.stealth = true
		case "--viewport":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("missing value for --viewport\nusage: rodney start [--show] [--stealth] [--viewport WxH] [--profile NAME] [--insecure | -k]")
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
				return f, fmt.Errorf("missing value for --profile\nusage: rodney start [--show] [--stealth] [--viewport WxH] [--profile NAME] [--insecure | -k]")
			}
			f.profile = args[i]
		default:
			return f, fmt.Errorf("unknown flag: %s\nusage: rodney start [--show] [--stealth] [--viewport WxH] [--profile NAME] [--insecure | -k]", args[i])
		}
	}
	return f, nil
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

func cmdStart(args []string) {
	flags, err := parseStartFlags(args)
	if err != nil {
		fatal("%s", err)
	}
	ignoreCertErrors := flags.ignoreCertErrors

	// Check if already running
	if s, err := loadState(); err == nil {
		// Try connecting
		if b, err := connectBrowser(s); err == nil {
			b.MustClose()
			// It was actually running, warn
			removeState()
		}
	}

	headless := flags.headless

	dataDir := filepath.Join(stateDir(), "chrome-data")
	os.MkdirAll(dataDir, 0755)
	var profileDir string

	if flags.profile != "" {
		profileDir, _ = resolveProfile(dataDir, flags.profile)
	}

	l := launcher.New().
		Leakless(false).        // Keep Chrome alive after CLI exits
		UserDataDir(dataDir).
		Headless(headless)

	if profileDir != "" {
		l = l.Set("profile-directory", profileDir)
	}

	// --no-sandbox is only needed when running as root (e.g., Docker
	// containers). On normal desktops Chrome's sandbox provides important
	// process isolation for security.
	if os.Getuid() == 0 {
		l = l.Set("no-sandbox")
	}

	// --disable-gpu avoids GPU-related crashes in headless/container
	// environments. In visible mode we leave GPU enabled since software
	// rendering can mishandle HiDPI scaling and cause viewport glitches.
	if headless {
		l = l.Set("disable-gpu")
	}

	// --single-process is required for screenshots in gVisor/container
	// environments, but crashes mainline Chrome in non-headless mode.
	// Only set it when we're NOT in stealth visible mode (which uses
	// mainline Chrome).
	if !flags.stealth || headless {
		l = l.Set("single-process")
	}

	// When in non-headless mode, make sure that we show the startup window immediately
	// (instead of showing a window only after calling "rodney open")
	if !headless {
		l = l.Delete("no-startup-window")
	}

	if bin := os.Getenv("ROD_CHROME_BIN"); bin != "" {
		l = l.Bin(bin)
	}

	// In stealth mode, prefer a system-installed Chrome over rod's bundled
	// Chromium. Mainline Chrome has navigator.userAgentData and other
	// features that make it harder to fingerprint as automation.
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
	if server, user, pass, needed := detectProxy(); needed {
		authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))

		// Find a free port for the local proxy
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			fatal("failed to find free port for proxy: %v", err)
		}
		proxyPort = ln.Addr().(*net.TCPAddr).Port
		ln.Close()

		// Launch ourselves as the proxy helper in the background
		exe, _ := os.Executable()
		cmd := exec.Command(exe, "_proxy",
			strconv.Itoa(proxyPort), server, authHeader)
		setSysProcAttr(cmd)
		if err := cmd.Start(); err != nil {
			fatal("failed to start proxy helper: %v", err)
		}
		proxyPID = cmd.Process.Pid
		// Detach so it survives after we exit
		cmd.Process.Release()

		// Wait for the proxy to be ready
		time.Sleep(500 * time.Millisecond)

		l.Set("proxy-server", fmt.Sprintf("http://127.0.0.1:%d", proxyPort))
		ignoreCertErrors = true // Proxy requires ignoring cert errors
		fmt.Printf("Auth proxy started (PID %d, port %d) -> %s\n", proxyPID, proxyPort, server)
	}

	if ignoreCertErrors {
		l.Set("ignore-certificate-errors")
	}

	if flags.stealth {
		l.Set("disable-blink-features", "AutomationControlled")
		l.Delete("enable-automation")

		// Use --headless=new instead of rod's default --headless when in
		// stealth mode. The new headless mode properly supports
		// addScriptToEvaluateOnNewDocument and doesn't add "HeadlessChrome"
		// to the user agent string.
		if headless {
			l.Headless(false)       // remove rod's default --headless flag
			l.Set("headless", "new") // use new headless mode
		}
	}

	debugURL := l.MustLaunch()

	// Get Chrome PID from the launcher
	pid := l.PID()

	var vpWidth, vpHeight int
	if flags.stealth && headless {
		// Only override viewport in headless mode. In visible mode the
		// window controls the viewport — EmulationSetDeviceMetricsOverride
		// would shrink the content area and fight with the window size.
		vp := flags.viewport
		if vp == "" {
			vp = "1920x935"
		}
		parts := strings.SplitN(vp, "x", 2)
		vpWidth, _ = strconv.Atoi(parts[0])
		vpHeight, _ = strconv.Atoi(parts[1])
	}

	state := &State{
		DebugURL:       debugURL,
		ChromePID:      pid,
		ActivePage:     0,
		DataDir:        dataDir,
		ProxyPID:       proxyPID,
		ProxyPort:      proxyPort,
		Stealth:        flags.stealth,
		ViewportWidth:  vpWidth,
		ViewportHeight: vpHeight,
	}

	if err := saveState(state); err != nil {
		fatal("failed to save state: %v", err)
	}

	if flags.stealth {
		browser, err := connectBrowser(state)
		if err == nil {
			pages, err := browser.Pages()
			if err == nil {
				for _, p := range pages {
					applyStealthToPage(p, browser, state)
				}
			}
		}
	}

	fmt.Printf("Chrome started (PID %d)\n", pid)
	fmt.Printf("Debug URL: %s\n", debugURL)
	if flags.stealth {
		fmt.Println("Stealth mode enabled")
	}
	if homeDirFlag != "" {
		fmt.Printf("Session: %s\n", homeDirFlag)
	}
}

func cmdConnect(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney connect <host:port>")
	}
	hostport := args[0]
	if _, _, err := net.SplitHostPort(hostport); err != nil {
		fatal("argument must be host:port (e.g. localhost:9222): %s", hostport)
	}

	// Fetch the WebSocket debugger URL from Chrome's /json/version endpoint
	resp, err := http.Get("http://" + hostport + "/json/version")
	if err != nil {
		fatal("could not reach browser at %s: %v", hostport, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fatal("failed to read response: %v", err)
	}
	var info struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &info); err != nil || info.WebSocketDebuggerURL == "" {
		fatal("unexpected response from browser at %s", hostport)
	}

	// Verify the connection works
	browser := rod.New().ControlURL(info.WebSocketDebuggerURL)
	if err := browser.Connect(); err != nil {
		fatal("could not connect to browser: %v", err)
	}

	// ChromePID=0 signals that we don't own this browser (stop won't kill it)
	state := &State{
		DebugURL:   info.WebSocketDebuggerURL,
		ChromePID:  0,
		ActivePage: 0,
	}
	if err := saveState(state); err != nil {
		fatal("failed to save state: %v", err)
	}

	fmt.Printf("Connected to browser at %s\n", hostport)
	fmt.Printf("Debug URL: %s\n", info.WebSocketDebuggerURL)
}

func cmdStop(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		// Try to kill by PID only if we launched the browser
		if s.ChromePID > 0 {
			proc, err := os.FindProcess(s.ChromePID)
			if err == nil {
				proc.Signal(syscall.SIGTERM)
			}
		}
	} else if s.ChromePID > 0 {
		// Only close (and kill) the browser if we launched it
		browser.MustClose()
	}
	// If ChromePID==0 we connected to an external browser; just clear state without closing it
	// Also kill the proxy helper if running
	if s.ProxyPID > 0 {
		if proc, err := os.FindProcess(s.ProxyPID); err == nil {
			proc.Signal(syscall.SIGTERM)
		}
	}
	removeState()
	fmt.Println("Chrome stopped")
	if homeDirFlag != "" {
		// Wait for Chrome to fully exit before removing the data directory
		if s.ChromePID > 0 {
			waitForProcessExit(s.ChromePID, 5*time.Second)
		}
		cleanupSessionDir(homeDirFlag)
	}
}

func cmdStatus(args []string) {
	s, err := loadState()
	if err != nil {
		fmt.Println("No active browser session")
		return
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fmt.Printf("Browser not responding (PID %d, state may be stale)\n", s.ChromePID)
		return
	}
	pages, _ := browser.Pages()
	fmt.Printf("Browser running (PID %d)\n", s.ChromePID)
	fmt.Printf("Debug URL: %s\n", s.DebugURL)
	fmt.Printf("Pages: %d\n", len(pages))
	fmt.Printf("Active page: %d\n", s.ActivePage)
	if page, err := getActivePage(browser, s); err == nil {
		info, _ := page.Info()
		if info != nil {
			fmt.Printf("Current: %s - %s\n", info.Title, info.URL)
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

	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	// If no pages exist, create one
	pages, _ := browser.Pages()
	var page *rod.Page
	if len(pages) == 0 {
		if s.Stealth {
			// In stealth mode, create blank page first so stealth scripts
			// are injected before any navigation occurs.
			page = browser.MustPage("")
			applyStealthToPage(page, browser, s)
			if err := page.Navigate(url); err != nil {
				fatal("navigation failed: %v", err)
			}
		} else {
			page = browser.MustPage(url)
		}
		s.ActivePage = 0
		saveState(s)
	} else {
		page, err = getActivePage(browser, s)
		if err != nil {
			fatal("%v", err)
		}
		// Re-apply stealth on each navigation. The CDP session from the
		// previous CLI invocation has disconnected, so session-scoped
		// state like Emulation.setUserAgentOverride is lost.
		if s.Stealth {
			applyStealthToPage(page, browser, s)
		}
		if err := page.Navigate(url); err != nil {
			fatal("navigation failed: %v", err)
		}
	}
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.Title)
	}
}

func cmdBack(args []string) {
	_, _, page := withPage()
	page.MustNavigateBack()
	page.MustWaitLoad()
	info, _ := page.Info()
	if info != nil {
		fmt.Println(info.URL)
	}
}

func cmdForward(args []string) {
	_, _, page := withPage()
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

func cmdPages(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := browser.Pages()
	if err != nil {
		fatal("failed to list pages: %v", err)
	}
	for i, p := range pages {
		marker := " "
		if i == s.ActivePage {
			marker = "*"
		}
		info, _ := p.Info()
		if info != nil {
			fmt.Printf("%s [%d] %s - %s\n", marker, i, info.Title, info.URL)
		} else {
			fmt.Printf("%s [%d] (unknown)\n", marker, i)
		}
	}
}

func cmdPage(args []string) {
	if len(args) < 1 {
		fatal("usage: rodney page <index>")
	}
	idx, err := strconv.Atoi(args[0])
	if err != nil {
		fatal("invalid index: %v", err)
	}
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := browser.Pages()
	if err != nil {
		fatal("failed to list pages: %v", err)
	}
	if idx < 0 || idx >= len(pages) {
		fatal("page index %d out of range (0-%d)", idx, len(pages)-1)
	}
	s.ActivePage = idx
	if err := saveState(s); err != nil {
		fatal("failed to save state: %v", err)
	}
	info, _ := pages[idx].Info()
	if info != nil {
		fmt.Printf("Switched to [%d] %s - %s\n", idx, info.Title, info.URL)
	}
}

func cmdNewPage(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}

	url := ""
	if len(args) > 0 {
		url = args[0]
		if !strings.Contains(url, "://") {
			url = "http://" + url
		}
	}

	var page *rod.Page
	if s.Stealth {
		// In stealth mode, create a blank page first so we can inject
		// stealth scripts via addScriptToEvaluateOnNewDocument BEFORE
		// any navigation occurs.
		page = browser.MustPage("")
		applyStealthToPage(page, browser, s)
		if url != "" {
			page.MustNavigate(url)
			page.MustWaitLoad()
		}
	} else if url != "" {
		page = browser.MustPage(url)
		page.MustWaitLoad()
	} else {
		page = browser.MustPage("")
	}

	// Switch active to the new page
	pages, _ := browser.Pages()
	for i, p := range pages {
		if p.TargetID == page.TargetID {
			s.ActivePage = i
			break
		}
	}
	saveState(s)

	info, _ := page.Info()
	if info != nil {
		fmt.Printf("Opened [%d] %s\n", s.ActivePage, info.URL)
	}
}

func cmdClosePage(args []string) {
	s, err := loadState()
	if err != nil {
		fatal("%v", err)
	}
	browser, err := connectBrowser(s)
	if err != nil {
		fatal("%v", err)
	}
	pages, err := browser.Pages()
	if err != nil {
		fatal("failed to list pages: %v", err)
	}
	if len(pages) <= 1 {
		fatal("cannot close the last page")
	}

	idx := s.ActivePage
	if len(args) > 0 {
		idx, err = strconv.Atoi(args[0])
		if err != nil {
			fatal("invalid index: %v", err)
		}
	}
	if idx < 0 || idx >= len(pages) {
		fatal("page index %d out of range", idx)
	}

	pages[idx].MustClose()

	// Adjust active page
	if s.ActivePage >= len(pages)-1 {
		s.ActivePage = len(pages) - 2
	}
	if s.ActivePage < 0 {
		s.ActivePage = 0
	}
	saveState(s)
	fmt.Printf("Closed page %d\n", idx)
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
