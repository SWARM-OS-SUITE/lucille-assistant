package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fixtureRoots builds a fake /proc and /sys for the collector.
func fixtureRoots(t *testing.T) (proc, sys string) {
	t.Helper()
	dir := t.TempDir()
	proc, sys = filepath.Join(dir, "proc"), filepath.Join(dir, "sys")
	writeFile(t, filepath.Join(proc, "stat"), "cpu  100 0 100 800 0 0 0 0 0 0\n")
	writeFile(t, filepath.Join(proc, "meminfo"), "MemTotal:       8000000 kB\nMemFree: 100 kB\nMemAvailable:   2000000 kB\nSwapTotal: 1000000 kB\nSwapFree:  750000 kB\n")
	writeFile(t, filepath.Join(proc, "loadavg"), "1.50 1.00 0.50 1/100 1234\n")
	writeFile(t, filepath.Join(proc, "uptime"), "3600.55 7000.00\n")
	writeFile(t, filepath.Join(proc, "cpuinfo"), "processor\t: 0\nmodel name: x\n\nprocessor\t: 1\nmodel name: x\n")
	writeFile(t, filepath.Join(proc, "net/dev"), "Inter-|   Receive\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets\n"+
		"    lo: 999999 1 0 0 0 0 0 0 999999 1 0 0 0 0 0 0\n  eth0: 1048576 10 0 0 0 0 0 0 524288 10 0 0 0 0 0 0\n")
	writeFile(t, filepath.Join(sys, "class/thermal/thermal_zone0/temp"), "41000\n")
	writeFile(t, filepath.Join(sys, "class/thermal/thermal_zone1/temp"), "55500\n")
	writeFile(t, filepath.Join(sys, "class/thermal/thermal_zone2/temp"), "-274000\n") // sentinel, ignored
	return proc, sys
}

func TestCollectorMeasuresAndNeverInvents(t *testing.T) {
	proc, sys := fixtureRoots(t)
	osr := filepath.Join(t.TempDir(), "os-release")
	writeFile(t, osr, "NAME=Debian\nPRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\n")
	c := &Collector{ProcRoot: proc, SysRoot: sys, OSReleasePath: osr, StatfsPath: t.TempDir()}

	first := c.Sample()
	if first.CPUPercent != -1 || first.NetRxKBps != -1 || first.NetTxKBps != -1 {
		t.Fatalf("first sample must not invent rates: cpu=%v rx=%v tx=%v", first.CPUPercent, first.NetRxKBps, first.NetTxKBps)
	}
	if first.OS != "Debian GNU/Linux 13 (trixie)" || first.Cores != 2 {
		t.Errorf("os=%q cores=%d", first.OS, first.Cores)
	}
	if first.RAMTotalMB != 7812 || first.RAMUsedMB != 5859 || first.RAMPercent != 75 {
		t.Errorf("ram total=%d used=%d pct=%v", first.RAMTotalMB, first.RAMUsedMB, first.RAMPercent)
	}
	if first.SwapPct != 25 || first.Load1 != 1.5 || first.UptimeSeconds != 3600 {
		t.Errorf("swap=%v load=%v uptime=%d", first.SwapPct, first.Load1, first.UptimeSeconds)
	}
	if first.TempC == nil || *first.TempC != 55.5 {
		t.Errorf("temp should be the hottest valid zone, got %v", first.TempC)
	}
	if first.StorageTotalGB <= 0 {
		t.Errorf("storage total = %v", first.StorageTotalGB)
	}

	// Second sample: +100 busy, +100 idle jiffies = 50%; network grew by 1 MiB rx / 0.5 MiB tx.
	writeFile(t, filepath.Join(proc, "stat"), "cpu  150 0 150 900 0 0 0 0 0 0\n")
	writeFile(t, filepath.Join(proc, "net/dev"), "    lo: 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n  eth0: 2097152 10 0 0 0 0 0 0 1048576 10 0 0 0 0 0 0\n")
	c.mu.Lock()
	c.prevNetAt = c.prevNetAt.Add(-time.Second) // exactly one second between samples
	c.mu.Unlock()
	second := c.Sample()
	if second.CPUPercent != 50 {
		t.Errorf("cpu = %v, want 50", second.CPUPercent)
	}
	if second.NetRxKBps < 900 || second.NetRxKBps > 1100 || second.NetTxKBps < 450 || second.NetTxKBps > 560 {
		t.Errorf("net rates rx=%v tx=%v KB/s, want about 1024 / 512", second.NetRxKBps, second.NetTxKBps)
	}

	// Missing sources become -1 / nil, never zero.
	empty := &Collector{ProcRoot: t.TempDir(), SysRoot: t.TempDir(), OSReleasePath: "/nonexistent", StatfsPath: "/nonexistent"}
	e := empty.Sample()
	if e.RAMPercent != -1 || e.RAMTotalMB != -1 || e.Load1 != -1 || e.UptimeSeconds != -1 || e.SwapPct != -1 ||
		e.StorageTotalGB != -1 || e.TempC != nil || e.CPUPercent != -1 {
		t.Errorf("an unreadable system must report -1/nil, got %+v", e)
	}
}

func TestReadSSE(t *testing.T) {
	stream := ": ping\n\nevent: telemetry\ndata: {\"a\":1}\n\nevent: other\ndata: x\ndata: y\n\ndata: bare\n\n"
	type ev struct{ name, data string }
	var got []ev
	if err := readSSE(strings.NewReader(stream), func(e string, d []byte) {
		if e != "" || len(d) > 0 {
			got = append(got, ev{e, string(d)})
		}
	}); err != nil {
		t.Fatal(err)
	}
	want := []ev{{"telemetry", `{"a":1}`}, {"other", "x\ny"}, {"", "bare"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// fakeAgent is a minimal I30LTZ node agent: /v1/pair and an SSE /v1/events behind a Bearer token.
func fakeAgent(t *testing.T, token string, frames func() string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/pair", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Code, Label string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Code != "123456" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"wrong code"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"` + token + `"}`))
	})
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl := w.(http.Flusher)
		for i := 0; i < 50 && r.Context().Err() == nil; i++ {
			fmt.Fprint(w, frames())
			fl.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func fastHub(t *testing.T) *Hub {
	t.Helper()
	oldS, oldR, oldStale := sampleEvery, reloadEvery, staleAfter
	sampleEvery, reloadEvery, staleAfter = 30*time.Millisecond, 30*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { sampleEvery, reloadEvery, staleAfter = oldS, oldR, oldStale })
	return newHub(filepath.Join(t.TempDir(), "nodes.json"))
}

// runHub starts the hub and, on cleanup, stops it and waits until it has finished. Cleanups run
// last-in first-out, so this completes before fastHub restores the package timing variables.
func runHub(t *testing.T, h *Hub) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func nodeByName(h *Hub, name string) *NodeView {
	for _, n := range h.Snapshot() {
		if n.Name == name {
			n := n
			return &n
		}
	}
	return nil
}

func TestHubFollowsPairedNodes(t *testing.T) {
	const token = "tok-very-secret-123"
	// This agent predates the extra fields, so it never sends net_rx_kbps / swap_percent.
	agent := fakeAgent(t, token, func() string {
		return `event: telemetry` + "\n" + `data: {"hostname":"swarm-os","os":"Arch","cpu_percent":12.5,"ram_percent":40,"ram_total_mb":16000,"ram_used_mb":6400,"storage_total_gb":500,"storage_free_gb":300,"load1":0.4,"uptime_seconds":99}` + "\n\n"
	})
	h := fastHub(t)
	if err := writeNodes(h.path, []nodeCfg{{Name: "swarm", URL: agent.URL, Token: token}}); err != nil {
		t.Fatal(err)
	}
	runHub(t, h)

	waitFor(t, "swarm to come online", func() bool { n := nodeByName(h, "swarm"); return n != nil && n.Online })
	n := nodeByName(h, "swarm")
	if n.Kind != "node" || n.Telemetry == nil || n.Telemetry.CPUPercent != 12.5 || n.Telemetry.RAMTotalMB != 16000 {
		t.Fatalf("telemetry not carried through: %+v", n)
	}
	if n.Telemetry.NetRxKBps != -1 || n.Telemetry.SwapPct != -1 || n.Telemetry.TempC != nil {
		t.Errorf("fields the node did not send must stay unknown, got rx=%v swap=%v temp=%v", n.Telemetry.NetRxKBps, n.Telemetry.SwapPct, n.Telemetry.TempC)
	}

	snap := h.Snapshot()
	if snap[0].Kind != "local" || !snap[0].Online || snap[0].Telemetry == nil {
		t.Errorf("this PC must come first and be online: %+v", snap[0])
	}
	raw, _ := json.Marshal(snap)
	if bytes.Contains(raw, []byte(token)) || bytes.Contains(raw, []byte(`"token"`)) {
		t.Fatal("a node token leaked into what the app receives")
	}

	// Removing the node from nodes.json stops following it.
	if err := writeNodes(h.path, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "swarm to disappear", func() bool { return nodeByName(h, "swarm") == nil })
}

func TestHubShowsAWrongTokenAsOfflineWithoutLeakingIt(t *testing.T) {
	agent := fakeAgent(t, "right-token", func() string { return "" })
	h := fastHub(t)
	if err := writeNodes(h.path, []nodeCfg{{Name: "bad", URL: agent.URL, Token: "revoked-token-zzz"}}); err != nil {
		t.Fatal(err)
	}
	runHub(t, h)
	waitFor(t, "an HTTP 401 error", func() bool { n := nodeByName(h, "bad"); return n != nil && strings.Contains(n.Error, "401") })
	n := nodeByName(h, "bad")
	if n.Online || n.Telemetry != nil {
		t.Errorf("a refused node must be offline with no data: %+v", n)
	}
	raw, _ := json.Marshal(n)
	if bytes.Contains(raw, []byte("revoked-token-zzz")) {
		t.Fatal("token leaked into the node view")
	}
}

func TestNodeNetErrorsAreShortAndTokenFree(t *testing.T) {
	// A port nothing listens on gives a real "connection refused".
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	_, err := streamNode(context.Background(), nodeCfg{Name: "x", URL: "http://" + addr, Token: "tok-secret"}, func(NodeTelemetry) {})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v", err)
	}
	for _, bad := range []string{"http://", addr, "dial tcp", "tok-secret"} {
		if strings.Contains(err.Error(), bad) {
			t.Errorf("message %q should not contain %q", err.Error(), bad)
		}
	}
	if e := nodeNetError(fmt.Errorf("boom tok-secret boom"), "tok-secret"); strings.Contains(e.Error(), "tok-secret") {
		t.Errorf("token leaked: %v", e)
	}
}

func TestHubMarksASilentNodeOffline(t *testing.T) {
	sent := 0
	agent := fakeAgent(t, "tok", func() string {
		sent++
		if sent > 3 { // then nothing but keep-alives, which carry no telemetry
			return ": ping\n\n"
		}
		return "event: telemetry\ndata: {\"hostname\":\"quiet\",\"cpu_percent\":1}\n\n"
	})
	h := fastHub(t)
	_ = writeNodes(h.path, []nodeCfg{{Name: "quiet", URL: agent.URL, Token: "tok"}})
	runHub(t, h)
	waitFor(t, "online", func() bool { n := nodeByName(h, "quiet"); return n != nil && n.Online })
	waitFor(t, "offline once telemetry stops", func() bool { n := nodeByName(h, "quiet"); return n != nil && !n.Online })
	if n := nodeByName(h, "quiet"); n.LastSeen == nil || n.Telemetry == nil {
		t.Errorf("an offline node should still show its last known data: %+v", n)
	}
}

func TestTelemetryStreamEndpoint(t *testing.T) {
	app, dir := setupTestApp(t)
	defer os.RemoveAll(dir)
	app.hub = fastHub(t)
	runHub(t, app.hub)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/telemetry", app.handleTelemetry)
	mux.HandleFunc("GET /api/telemetry/stream", app.handleTelemetryStream)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// snapshot
	resp, err := http.Get(srv.URL + "/api/telemetry")
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		OK    bool       `json:"ok"`
		Nodes []NodeView `json:"nodes"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&snap)
	resp.Body.Close()
	if !snap.OK || len(snap.Nodes) == 0 || snap.Nodes[0].Kind != "local" {
		t.Fatalf("snapshot = %+v", snap)
	}

	// stream: two "nodes" events arrive, each a full snapshot
	sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer scancel()
	req, _ := http.NewRequestWithContext(sctx, "GET", srv.URL+"/api/telemetry/stream", nil)
	sresp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	if ct := sresp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	events := 0
	sc := bufio.NewScanner(sresp.Body)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() && events < 2 {
		line := sc.Text()
		if rest, ok := strings.CutPrefix(line, "data: "); ok {
			var p struct {
				OK    bool       `json:"ok"`
				Nodes []NodeView `json:"nodes"`
			}
			if err := json.Unmarshal([]byte(rest), &p); err != nil || !p.OK || len(p.Nodes) == 0 {
				t.Fatalf("bad event %q: %v", rest, err)
			}
			events++
		}
	}
	if events < 2 {
		t.Fatalf("got %d events, want a new one every sample", events)
	}
}

func TestNodesFileIsPrivateAndPairingWorks(t *testing.T) {
	agent := fakeAgent(t, "paired-token-777", func() string { return "" })
	path := filepath.Join(t.TempDir(), "sub", "nodes.json")
	t.Setenv("LUCILLE_ASSISTANT_NODES", path)

	if err := PairNode(context.Background(), path, "swarm", agent.URL, "123456"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("nodes.json must be 0600, got %v (%v)", fi.Mode().Perm(), err)
	}
	list, _ := readNodes(path)
	if len(list) != 1 || list[0].Token != "paired-token-777" || list[0].URL != agent.URL {
		t.Fatalf("stored = %+v", list)
	}

	// Pairing again replaces rather than duplicates.
	if err := PairNode(context.Background(), path, "swarm", agent.URL, "123456"); err != nil {
		t.Fatal(err)
	}
	if list, _ = readNodes(path); len(list) != 1 {
		t.Fatalf("duplicate node: %+v", list)
	}

	// Refusals change nothing.
	for name, c := range map[string][3]string{
		"wrong code": {"other", agent.URL, "000000"},
		"bad name":   {"../x", agent.URL, "123456"},
		"bad url":    {"x", "ftp://host", "123456"},
		"url path":   {"x", agent.URL + "/v1/pair", "123456"},
		"no code":    {"x", agent.URL, " "},
	} {
		if err := PairNode(context.Background(), path, c[0], c[1], c[2]); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if list, _ = readNodes(path); len(list) != 1 {
		t.Fatalf("a refused pairing changed the file: %+v", list)
	}

	// The CLI never prints a token.
	var out bytes.Buffer
	if rc := runNodesCLI([]string{"list"}, &out); rc != 0 || !strings.Contains(out.String(), "swarm") || strings.Contains(out.String(), "paired-token-777") {
		t.Fatalf("list rc=%d out=%q", rc, out.String())
	}
	out.Reset()
	if rc := runNodesCLI([]string{"remove", "swarm"}, &out); rc != 0 {
		t.Fatalf("remove rc=%d %q", rc, out.String())
	}
	if list, _ = readNodes(path); len(list) != 0 {
		t.Fatalf("remove left %+v", list)
	}
	if rc := runNodesCLI([]string{"remove", "swarm"}, &out); rc == 0 {
		t.Error("removing a missing node should fail")
	}
}
