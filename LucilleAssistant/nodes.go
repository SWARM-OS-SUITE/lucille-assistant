package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Nodes are other machines running the I30LTZ node agent. This service pairs with
// each agent once, keeps its token in nodes.json (mode 0600, on this PC only),
// follows its SSE stream and re-serves every node through one stream. A phone
// therefore needs a single connection and never holds a node token.

var (
	sampleEvery = 2 * time.Second
	staleAfter  = 10 * time.Second // a node that sent nothing for this long is shown offline
	silenceCap  = 25 * time.Second // drop and reconnect a node stream that went quiet
	reloadEvery = 10 * time.Second
)

type nodeCfg struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token"`
}

// NodeView is what the app sees. It never carries a token.
type NodeView struct {
	Name      string         `json:"name"`
	Kind      string         `json:"kind"` // "local" (this PC) or "node"
	URL       string         `json:"url,omitempty"`
	Online    bool           `json:"online"`
	LastSeen  *time.Time     `json:"last_seen,omitempty"`
	Error     string         `json:"error,omitempty"`
	Telemetry *NodeTelemetry `json:"telemetry,omitempty"`
}

type nodeState struct {
	cfg      nodeCfg
	cancel   context.CancelFunc
	last     NodeTelemetry
	lastSeen time.Time
	err      string
}

type Hub struct {
	path string
	col  *Collector

	mu    sync.RWMutex
	local NodeTelemetry
	have  bool
	nodes map[string]*nodeState
	mtime time.Time
}

func newHub(nodesFile string) *Hub {
	return &Hub{path: nodesFile, col: NewCollector(), nodes: map[string]*nodeState{}}
}

// nodesPath is nodes.json next to the config file (or LUCILLE_ASSISTANT_NODES).
func nodesPath() string {
	if p := os.Getenv("LUCILLE_ASSISTANT_NODES"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	cfg := envOr("LUCILLE_ASSISTANT_CONFIG", filepath.Join(home, ".config/lucille-assistant/config.json"))
	return filepath.Join(filepath.Dir(cfg), "nodes.json")
}

// ── nodes.json ──────────────────────────────────────────────────

var nodeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,39}$`)

func validNodeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("the node address must look like http://100.82.1.20:7437")
	}
	return u.Scheme + "://" + u.Host, nil
}

func readNodes(path string) ([]nodeCfg, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Nodes []nodeCfg `json:"nodes"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return f.Nodes, nil
}

func writeNodes(path string, list []nodeCfg) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(map[string]any{"nodes": list}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// nodeClient is shared so idle connections are reused; each request carries its own context.
var nodeClient = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 8 * time.Second}}

// ── Running ─────────────────────────────────────────────────────

// Run samples this PC and keeps one follower per paired node until ctx ends.
func (h *Hub) Run(ctx context.Context) {
	h.sampleLocal()
	h.reload(ctx)
	sample := time.NewTicker(sampleEvery)
	reload := time.NewTicker(reloadEvery)
	defer sample.Stop()
	defer reload.Stop()
	for {
		select {
		case <-ctx.Done():
			h.mu.Lock()
			for _, n := range h.nodes {
				n.cancel()
			}
			h.mu.Unlock()
			return
		case <-sample.C:
			h.sampleLocal()
		case <-reload.C:
			h.reload(ctx)
		}
	}
}

func (h *Hub) sampleLocal() {
	t := h.col.Sample()
	h.mu.Lock()
	h.local, h.have = t, true
	h.mu.Unlock()
}

// reload applies nodes.json when it changed: new nodes get a follower, removed ones lose theirs.
func (h *Hub) reload(ctx context.Context) {
	fi, err := os.Stat(h.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
	var mt time.Time
	if err == nil {
		mt = fi.ModTime()
	}
	h.mu.RLock()
	unchanged := mt.Equal(h.mtime)
	h.mu.RUnlock()
	if unchanged {
		return
	}
	list, err := readNodes(h.path)
	if err != nil {
		log.Printf("nodes: %v", err)
		return
	}
	want := map[string]nodeCfg{}
	for _, n := range list {
		if nodeName.MatchString(n.Name) && n.Token != "" {
			if u, err := validNodeURL(n.URL); err == nil {
				n.URL = u
				want[n.Name] = n
			}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mtime = mt
	for name, st := range h.nodes {
		if w, ok := want[name]; !ok || w != st.cfg {
			st.cancel()
			delete(h.nodes, name)
		}
	}
	for name, cfg := range want {
		if _, ok := h.nodes[name]; ok {
			continue
		}
		nctx, cancel := context.WithCancel(ctx)
		st := &nodeState{cfg: cfg, cancel: cancel}
		h.nodes[name] = st
		go h.follow(nctx, st)
		log.Printf("nodes: following %q at %s", cfg.Name, cfg.URL)
	}
}

func (h *Hub) record(st *nodeState, t NodeTelemetry) {
	h.mu.Lock()
	wasOffline := st.err != "" || st.lastSeen.IsZero()
	st.last, st.lastSeen, st.err = t, time.Now(), ""
	h.mu.Unlock()
	if wasOffline {
		log.Printf("nodes: %q is online", st.cfg.Name)
	}
}

func (h *Hub) fail(st *nodeState, err error) {
	h.mu.Lock()
	changed := st.err == ""
	st.err = err.Error()
	h.mu.Unlock()
	if changed {
		log.Printf("nodes: %q offline: %v", st.cfg.Name, err)
	}
}

// follow keeps one node's SSE stream open, reconnecting with a growing pause.
func (h *Hub) follow(ctx context.Context, st *nodeState) {
	wait := 3 * time.Second
	for ctx.Err() == nil {
		got, err := streamNode(ctx, st.cfg, func(t NodeTelemetry) { h.record(st, t) })
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			h.fail(st, err)
		}
		if got {
			wait = 3 * time.Second
		} else if wait < 30*time.Second {
			wait *= 2
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// streamNode reads one /v1/events connection. It reports whether any telemetry arrived.
func streamNode(parent context.Context, n nodeCfg, onTelemetry func(NodeTelemetry)) (bool, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.URL+"/v1/events", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+n.Token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := nodeClient.Do(req)
	if err != nil {
		return false, nodeNetError(err, n.Token)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("the node answered HTTP %d (re-pair it if the token was revoked)", resp.StatusCode)
	}
	// A stream that goes silent is dropped so the node shows offline and we reconnect.
	watchdog := time.AfterFunc(silenceCap, cancel)
	defer watchdog.Stop()
	got := false
	err = readSSE(resp.Body, func(event string, data []byte) {
		watchdog.Reset(silenceCap)
		if event != "telemetry" {
			return
		}
		t := blankTelemetry()
		if json.Unmarshal(data, &t) == nil {
			got = true
			onTelemetry(t)
		}
	})
	switch {
	case parent.Err() == nil && ctx.Err() != nil:
		err = fmt.Errorf("the node sent nothing for %s", silenceCap)
	case err == nil || errors.Is(err, io.EOF):
		err = errors.New("the node closed the stream")
	}
	return got, err
}

// nodeNetError turns a Go network error into a short message for the phone: no URL, no
// "dial tcp ...", and never the token.
func nodeNetError(err error, token string) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	msg := strings.ReplaceAll(err.Error(), token, "***")
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		msg = "connection refused (is i30ltz-node running there?)"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout") || strings.Contains(msg, "timed out"):
		msg = "timed out (is the machine on and on the tailnet?)"
	case errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) || strings.Contains(msg, "no route"):
		msg = "no route to the machine (is it on the tailnet?)"
	}
	return errors.New(msg)
}

// blankTelemetry has every measurable field at -1, so a node that omits a field
// shows "unknown" instead of a made-up zero.
func blankTelemetry() NodeTelemetry {
	return NodeTelemetry{CPUPercent: -1, RAMPercent: -1, RAMTotalMB: -1, RAMUsedMB: -1, StorageTotalGB: -1,
		StorageFreeGB: -1, Load1: -1, UptimeSeconds: -1, SwapPct: -1, NetRxKBps: -1, NetTxKBps: -1}
}

// readSSE parses text/event-stream and calls fn once per event.
func readSSE(r io.Reader, fn func(event string, data []byte)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var event string
	var data bytes.Buffer
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data.Len() > 0 || event != "" {
				fn(event, data.Bytes())
			}
			event = ""
			data.Reset()
		case strings.HasPrefix(line, ":"): // comment / keep-alive
			fn("", nil)
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(line[len("event:"):])
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(line[len("data:"):], " "))
		}
	}
	return sc.Err()
}

// ── Snapshot ────────────────────────────────────────────────────

func (h *Hub) Snapshot() []NodeView {
	h.mu.RLock()
	have, local := h.have, h.local
	h.mu.RUnlock()
	if !have {
		h.sampleLocal()
		h.mu.RLock()
		local = h.local
		h.mu.RUnlock()
	}
	now := time.Now()
	lt := local.Time
	name := local.Hostname
	if name == "" {
		name = "this PC"
	}
	out := []NodeView{{Name: name, Kind: "local", Online: true, LastSeen: &lt, Telemetry: &local}}

	h.mu.RLock()
	var rest []NodeView
	for _, st := range h.nodes {
		v := NodeView{Name: st.cfg.Name, Kind: "node", URL: st.cfg.URL, Error: st.err}
		if !st.lastSeen.IsZero() {
			seen, tel := st.lastSeen, st.last
			v.LastSeen = &seen
			v.Telemetry = &tel
			v.Online = st.err == "" && now.Sub(seen) < staleAfter
		}
		if v.LastSeen == nil && v.Error == "" {
			v.Error = "connecting"
		}
		rest = append(rest, v)
	}
	h.mu.RUnlock()
	sort.Slice(rest, func(i, j int) bool { return strings.ToLower(rest[i].Name) < strings.ToLower(rest[j].Name) })
	return append(out, rest...)
}

// ── HTTP ────────────────────────────────────────────────────────

func (a *App) telemetryPayload() map[string]any {
	return map[string]any{"ok": true, "generated_at": time.Now().UTC(), "nodes": a.hub.Snapshot()}
}

func (a *App) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, a.telemetryPayload())
}

// handleTelemetryStream is one SSE stream carrying every node, a new "nodes" event each sample.
func (a *App) handleTelemetryStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming is not supported here")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	send := func() bool {
		raw, err := json.Marshal(a.telemetryPayload())
		if err != nil {
			return false
		}
		_, err = fmt.Fprintf(w, "event: nodes\ndata: %s\n\n", raw)
		flusher.Flush()
		return err == nil
	}
	if !send() {
		return
	}
	tick := time.NewTicker(sampleEvery)
	ping := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if !send() {
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ── `lucille-assistant nodes ...` (run on this PC) ──────────────

// PairNode asks a node agent to pair using the one-time code shown on that machine
// (`i30ltz-node pair`), then stores the token it hands back in nodes.json.
func PairNode(ctx context.Context, path, name, rawURL, code string) error {
	if !nodeName.MatchString(name) {
		return errors.New("the name must be 1-40 letters, digits, spaces, dots, dashes or underscores")
	}
	base, err := validNodeURL(rawURL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(code) == "" {
		return errors.New("a pairing code is needed (run `i30ltz-node pair` on that machine)")
	}
	host, _ := os.Hostname()
	body, _ := json.Marshal(map[string]string{"code": strings.TrimSpace(code), "label": "Lucille Assistant on " + host})
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/pair", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %w", base, err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	if resp.StatusCode != http.StatusOK || out.Token == "" {
		msg := out.Error
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("pairing was refused: %s", msg)
	}
	list, err := readNodes(path)
	if err != nil {
		return err
	}
	replaced := false
	for i := range list {
		if list[i].Name == name {
			list[i], replaced = nodeCfg{Name: name, URL: base, Token: out.Token}, true
		}
	}
	if !replaced {
		list = append(list, nodeCfg{Name: name, URL: base, Token: out.Token})
	}
	return writeNodes(path, list)
}

// runNodesCLI handles `nodes list | add NAME URL CODE | remove NAME`. Tokens are never printed.
func runNodesCLI(args []string, out io.Writer) int {
	path := nodesPath()
	usage := func() int {
		fmt.Fprintln(out, "usage: lucille-assistant nodes list | add NAME URL CODE | remove NAME")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "list":
		list, err := readNodes(path)
		if err != nil {
			fmt.Fprintln(out, "error:", err)
			return 1
		}
		if len(list) == 0 {
			fmt.Fprintln(out, "no nodes paired yet (nodes file:", path+")")
		}
		for _, n := range list {
			fmt.Fprintf(out, "%-24s %s\n", n.Name, n.URL)
		}
		return 0
	case "add":
		if len(args) != 4 {
			return usage()
		}
		if err := PairNode(context.Background(), path, args[1], args[2], args[3]); err != nil {
			fmt.Fprintln(out, "error:", err)
			return 1
		}
		fmt.Fprintf(out, "paired %q; the running service picks it up within %s\n", args[1], reloadEvery)
		return 0
	case "remove":
		if len(args) != 2 {
			return usage()
		}
		list, err := readNodes(path)
		if err != nil {
			fmt.Fprintln(out, "error:", err)
			return 1
		}
		kept := list[:0]
		for _, n := range list {
			if n.Name != args[1] {
				kept = append(kept, n)
			}
		}
		if len(kept) == len(list) {
			fmt.Fprintf(out, "no node named %q\n", args[1])
			return 1
		}
		if err := writeNodes(path, kept); err != nil {
			fmt.Fprintln(out, "error:", err)
			return 1
		}
		fmt.Fprintf(out, "removed %q (also revoke it on that machine: i30ltz-node revoke ...)\n", args[1])
		return 0
	}
	return usage()
}
