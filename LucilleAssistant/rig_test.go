package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseNvidiaSMI(t *testing.T) {
	g := parseNvidiaSMI("NVIDIA GeForce RTX 3070, 97, 71, 6144, 8192, 215.50\n")
	if g == nil || g.Name != "NVIDIA GeForce RTX 3070" || g.UtilPct != 97 || g.TempC != 71 ||
		g.VRAMUsedMB != 6144 || g.VRAMTotalMB != 8192 || g.PowerW != 215.5 {
		t.Fatalf("bad parse: %+v", g)
	}
	// a name with a comma in it, and a driver that reports power as [N/A]
	g = parseNvidiaSMI("NVIDIA RTX A2000, 12GB, 40, 55, 1000, 12000, [N/A]")
	if g == nil || g.Name != "NVIDIA RTX A2000, 12GB" || g.UtilPct != 40 || g.PowerW != -1 {
		t.Fatalf("comma name / N/A power: %+v", g)
	}
	for _, junk := range []string{"", "No devices were found", "a, b, c"} {
		if parseNvidiaSMI(junk) != nil {
			t.Fatalf("%q should not parse", junk)
		}
	}
}

func TestReadAMDGPU(t *testing.T) {
	sys := t.TempDir()
	dev := filepath.Join(sys, "class/drm/card0/device")
	writeFile(t, filepath.Join(dev, "gpu_busy_percent"), "63\n")
	writeFile(t, filepath.Join(dev, "mem_info_vram_used"), "2147483648\n")  // 2048 MB
	writeFile(t, filepath.Join(dev, "mem_info_vram_total"), "8589934592\n") // 8192 MB
	writeFile(t, filepath.Join(dev, "hwmon/hwmon3/temp1_input"), "67000\n")
	g := readAMDGPU(sys)
	if g == nil || g.UtilPct != 63 || g.VRAMUsedMB != 2048 || g.VRAMTotalMB != 8192 || g.TempC != 67 {
		t.Fatalf("amd parse: %+v", g)
	}
	if readAMDGPU(t.TempDir()) != nil {
		t.Fatal("no card should be nil")
	}
}

func rigTelemetry(gpu *GPUTelemetry) NodeTelemetry {
	temp := 60.0
	return NodeTelemetry{CPUPercent: 40, RAMPercent: 50, SwapPct: 0, StorageFreeGB: 200, TempC: &temp, GPU: gpu}
}

func hasFinding(fs []RigFinding, level, area string) bool {
	for _, f := range fs {
		if f.Level == level && f.Area == area {
			return true
		}
	}
	return false
}

func TestRigFindingsRules(t *testing.T) {
	cool := &GPUTelemetry{UtilPct: 50, TempC: 60, VRAMUsedMB: 2000, VRAMTotalMB: 8000}
	if fs := rigFindings(rigTelemetry(cool)); !hasFinding(fs, "ok", "Overall") {
		t.Fatalf("healthy rig should say so: %+v", fs)
	}

	hot := &GPUTelemetry{UtilPct: 99, TempC: 90, VRAMUsedMB: 7600, VRAMTotalMB: 8000}
	fs := rigFindings(rigTelemetry(hot))
	for _, want := range [][2]string{{"bad", "GPU temperature"}, {"bad", "VRAM"}, {"info", "Bottleneck"}} {
		if !hasFinding(fs, want[0], want[1]) {
			t.Fatalf("missing %v in %+v", want, fs)
		}
	}
	if hasFinding(fs, "ok", "Overall") {
		t.Fatal("a struggling rig must not also say everything is fine")
	}

	cpuBound := rigTelemetry(&GPUTelemetry{UtilPct: 55, TempC: 60, VRAMUsedMB: 1000, VRAMTotalMB: 8000})
	cpuBound.CPUPercent = 96
	fs = rigFindings(cpuBound)
	found := false
	for _, f := range fs {
		if f.Area == "Bottleneck" && strings.Contains(f.Text, "CPU-bound") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected CPU-bound: %+v", fs)
	}

	tight := rigTelemetry(nil)
	tight.RAMPercent, tight.SwapPct, tight.StorageFreeGB = 95, 40, 5
	fs = rigFindings(tight)
	for _, want := range [][2]string{{"bad", "Memory"}, {"warn", "Swap"}, {"warn", "Storage"}, {"info", "GPU"}} {
		if !hasFinding(fs, want[0], want[1]) {
			t.Fatalf("missing %v in %+v", want, fs)
		}
	}
}

func rigApp(t *testing.T, tel NodeTelemetry, gamingNode string) *App {
	t.Helper()
	cfg := Config{DataDir: t.TempDir(), GamingNode: gamingNode}
	st, err := openStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	h := newHub(filepath.Join(t.TempDir(), "nodes.json"))
	h.local, h.have = tel, true
	tel.Hostname = "gamerig"
	h.local = tel
	return &App{cfg: cfg, store: st, hub: h, cache: newCache()}
}

func TestRigEndpointShowsOnlyTheGamingDevice(t *testing.T) {
	tel := rigTelemetry(&GPUTelemetry{Name: "RTX", UtilPct: 90, TempC: 70, VRAMUsedMB: 1000, VRAMTotalMB: 8000})
	tel.Time = time.Now()
	a := rigApp(t, tel, "")
	rec := httptest.NewRecorder()
	a.handleRig(rec, httptest.NewRequest("GET", "/api/rig", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out struct {
		OK        bool           `json:"ok"`
		Device    string         `json:"device"`
		Kind      string         `json:"kind"`
		Telemetry NodeTelemetry  `json:"telemetry"`
		Findings  []RigFinding   `json:"findings"`
		Gaming    map[string]any `json:"gaming"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Kind != "local" || out.Telemetry.GPU == nil || len(out.Findings) == 0 || out.Gaming["active"] != false {
		t.Fatalf("unexpected /api/rig: %s", rec.Body.String())
	}

	// naming a node that doesn't exist is an honest error, not a silent fallback to this PC
	b := rigApp(t, tel, "no-such-box")
	rec = httptest.NewRecorder()
	b.handleRig(rec, httptest.NewRequest("GET", "/api/rig", nil))
	if !strings.Contains(rec.Body.String(), `"ok":false`) {
		t.Fatalf("expected an error for an unknown gaming_node: %s", rec.Body.String())
	}
}

func TestRigAdviceFallsBackToRulesWithoutAModel(t *testing.T) {
	tel := rigTelemetry(&GPUTelemetry{UtilPct: 99, TempC: 91, VRAMUsedMB: 7800, VRAMTotalMB: 8000})
	tel.Time = time.Now()
	a := rigApp(t, tel, "")
	rigAdvice.Lock()
	rigAdvice.last = time.Time{}
	rigAdvice.Unlock()
	req := httptest.NewRequest("POST", "/api/rig/advice", strings.NewReader(`{"game":"Hades"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.handleRigAdvice(rec, req)
	var out struct {
		OK     bool   `json:"ok"`
		Source string `json:"source"`
		Advice string `json:"advice"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if !out.OK || out.Advice == "" || !strings.Contains(out.Advice, "GPU") {
		t.Fatalf("expected rule-based advice mentioning the GPU: %s", rec.Body.String())
	}
}
