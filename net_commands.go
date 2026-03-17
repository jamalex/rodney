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
	"time"
)

type netLogFlags struct {
	since      string
	id         string
	method     string
	pathPrefix string
	domain     string
	eventType  string
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
	if f.since != "" {
		anchorIdx := -1
		switch f.since {
		case "nav", "render", "interaction":
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
	}

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
			suffix := p[1:]
			if strings.HasSuffix(hostname, suffix) {
				return true
			}
		} else if hostname == p {
			return true
		}
	}
	return false
}

func readJSONLFile(path string) ([]NetEvent, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []NetEvent
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
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
			return
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

func tailFollow(path string, flags netLogFlags) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "waiting for network log to appear...")
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
			filtered := filterNetEvents([]NetEvent{e}, flags)
			for _, fe := range filtered {
				fe = stripNetEvent(fe, flags.headers, flags.timestamps)
				data, _ := json.Marshal(fe)
				fmt.Println(string(data))
			}
		}
		time.Sleep(200 * time.Millisecond)
		scanner = bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	}
}

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

func cmdNetClear(args []string) {
	dataDir, _ := resolveSessionDataDir()
	sockPath := filepath.Join(dataDir, "net", "monitor.sock")

	err := ipcSend(sockPath, IPCMessage{Session: activeSessionID, Type: "clear"})
	if err != nil {
		sessionDir := filepath.Join(dataDir, "net", activeSessionID)
		os.Remove(filepath.Join(sessionDir, "index.jsonl"))
		os.RemoveAll(filepath.Join(sessionDir, "bodies"))
		os.MkdirAll(sessionDir, 0755)
		os.WriteFile(filepath.Join(sessionDir, "index.jsonl"), nil, 0644)
	}
}
