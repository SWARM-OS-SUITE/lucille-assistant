package main

// The gaming rig: live numbers from the GAMING device only (not every node), what
// they mean in plain words, and an optional model-written settings review.
//
//   GET  /api/rig                 the gaming device's telemetry + findings + the active gaming session
//   POST /api/rig/advice {"game"} a short settings review (model if one answers, the rules otherwise)
//
// "The gaming device" is this PC unless config.json names another telemetry node in
// "gaming_node". GPU numbers come from nvidia-smi (NVIDIA) or sysfs (AMD); a device
// with neither just has no GPU rows, the rest still works.

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type GPUTelemetry struct {
	Name        string  `json:"name,omitempty"`
	UtilPct     float64 `json:"util_percent"`
	TempC       float64 `json:"temp_c"`
	VRAMUsedMB  int64   `json:"vram_used_mb"`
	VRAMTotalMB int64   `json:"vram_total_mb"`
	PowerW      float64 `json:"power_w"` // -1 when the driver does not say
}

// runNvidiaSMI is a variable so tests never need a GPU.
var runNvidiaSMI = func(ctx context.Context) (string, error) {
	path, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, path,
		"--query-gpu=name,utilization.gpu,temperature.gpu,memory.used,memory.total,power.draw",
		"--format=csv,noheader,nounits").Output()
	return string(out), err
}

// parseNvidiaSMI reads the first GPU line: "NVIDIA GeForce RTX 3070, 97, 71, 6144, 8192, 215.50".
// The name can contain commas, so the five numbers are taken from the right.
func parseNvidiaSMI(out string) *GPUTelemetry {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
	parts := strings.Split(line, ",")
	if len(parts) < 6 {
		return nil
	}
	n := len(parts)
	num := func(s string) (float64, bool) {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		return v, err == nil
	}
	util, ok1 := num(parts[n-5])
	temp, ok2 := num(parts[n-4])
	used, ok3 := num(parts[n-3])
	total, ok4 := num(parts[n-2])
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil
	}
	power, okp := num(parts[n-1])
	if !okp {
		power = -1 // "[N/A]" on some laptops and older cards
	}
	return &GPUTelemetry{
		Name: strings.TrimSpace(strings.Join(parts[:n-5], ",")), UtilPct: util, TempC: temp,
		VRAMUsedMB: int64(used), VRAMTotalMB: int64(total), PowerW: power,
	}
}

// readAMDGPU reads the amdgpu driver's sysfs files (busy percent, VRAM bytes, hwmon temp).
func readAMDGPU(sysRoot string) *GPUTelemetry {
	cards, _ := filepath.Glob(filepath.Join(sysRoot, "class/drm/card*/device/gpu_busy_percent"))
	for _, busyPath := range cards {
		dev := filepath.Dir(busyPath)
		busy, err := readText(busyPath)
		if err != nil {
			continue
		}
		util, err := strconv.ParseFloat(strings.TrimSpace(busy), 64)
		if err != nil {
			continue
		}
		g := &GPUTelemetry{Name: "AMD GPU", UtilPct: util, TempC: -1, PowerW: -1}
		if s, err := readText(filepath.Join(dev, "mem_info_vram_used")); err == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				g.VRAMUsedMB = v / (1 << 20)
			}
		}
		if s, err := readText(filepath.Join(dev, "mem_info_vram_total")); err == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				g.VRAMTotalMB = v / (1 << 20)
			}
		}
		if temps, _ := filepath.Glob(filepath.Join(dev, "hwmon/hwmon*/temp1_input")); len(temps) > 0 {
			if s, err := readText(temps[0]); err == nil {
				if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && v > 0 {
					g.TempC = round1(v / 1000)
				}
			}
		}
		if p, err := readText(filepath.Join(dev, "product_name")); err == nil && strings.TrimSpace(p) != "" {
			g.Name = strings.TrimSpace(p)
		}
		return g
	}
	return nil
}

var gpuCache struct {
	sync.Mutex
	at   time.Time
	last *GPUTelemetry
}

// gpuSample is cached for a few seconds: telemetry samples every 2 s and
// nvidia-smi is a process launch each time.
func (c *Collector) gpuSample() *GPUTelemetry {
	if !c.GPU {
		return nil
	}
	gpuCache.Lock()
	defer gpuCache.Unlock()
	if time.Since(gpuCache.at) < 4*time.Second {
		return gpuCache.last
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var g *GPUTelemetry
	if out, err := runNvidiaSMI(ctx); err == nil {
		g = parseNvidiaSMI(out)
	}
	if g == nil {
		g = readAMDGPU(c.SysRoot)
	}
	gpuCache.at, gpuCache.last = time.Now(), g
	return g
}

// ── What the numbers mean ───────────────────────────────────────

type RigFinding struct {
	Level string `json:"level"` // ok | warn | bad | info
	Area  string `json:"area"`
	Text  string `json:"text"`
}

// rigFindings turns one telemetry sample into plain advice. Rules only, no model:
// it works offline, instantly, and says exactly which number triggered it.
func rigFindings(t NodeTelemetry) []RigFinding {
	var out []RigFinding
	add := func(level, area, format string, args ...any) {
		out = append(out, RigFinding{level, area, fmt.Sprintf(format, args...)})
	}
	g := t.GPU
	if g != nil {
		vramPct := 0.0
		if g.VRAMTotalMB > 0 {
			vramPct = float64(g.VRAMUsedMB) / float64(g.VRAMTotalMB) * 100
		}
		switch {
		case g.TempC >= 88:
			add("bad", "GPU temperature", "GPU is at %.0f°C. Raise the fan curve, clear the case airflow, or cap the frame rate / power limit before it throttles.", g.TempC)
		case g.TempC >= 80:
			add("warn", "GPU temperature", "GPU is warm at %.0f°C. Fine for now; a frame-rate cap or a fan curve bump would bring it down.", g.TempC)
		}
		if vramPct >= 92 {
			add("bad", "VRAM", "Video memory is %.0f%% full (%d of %d MB). Lower texture quality one step; stutter and pop-in come next.", vramPct, g.VRAMUsedMB, g.VRAMTotalMB)
		} else if vramPct >= 85 {
			add("warn", "VRAM", "Video memory is %.0f%% used. Close overlays and browsers, or drop textures a notch if you see stutter.", vramPct)
		}
		switch {
		case g.UtilPct >= 97 && t.CPUPercent >= 0 && t.CPUPercent < 85:
			add("info", "Bottleneck", "GPU-bound (GPU %.0f%%, CPU %.0f%%). Lower resolution scale or turn on DLSS/FSR; shadows and ray tracing cost the most here.", g.UtilPct, t.CPUPercent)
		case g.UtilPct < 70 && t.CPUPercent >= 90:
			add("info", "Bottleneck", "CPU-bound (CPU %.0f%%, GPU only %.0f%%). Lower view distance, crowd/physics and simulation settings; resolution won't help.", t.CPUPercent, g.UtilPct)
		}
	} else {
		add("info", "GPU", "No GPU numbers on this device (needs nvidia-smi or an AMD GPU). CPU, memory and temperature still work.")
	}
	if t.TempC != nil && *t.TempC >= 90 {
		add("bad", "System temperature", "Hottest sensor is at %.0f°C. Check cooling before a long session.", *t.TempC)
	}
	if t.RAMPercent >= 92 {
		add("bad", "Memory", "RAM is %.0f%% used. Close the browser and launchers; the game will start swapping.", t.RAMPercent)
	} else if t.RAMPercent >= 85 {
		add("warn", "Memory", "RAM is %.0f%% used. Background apps are crowding the game.", t.RAMPercent)
	}
	if t.SwapPct >= 25 {
		add("warn", "Swap", "%.0f%% of swap is in use, so the system is already paging. Expect hitches until memory is freed.", t.SwapPct)
	}
	if t.StorageFreeGB >= 0 && t.StorageFreeGB < 15 {
		add("warn", "Storage", "Only %.0f GB free on the system drive. Shader caches and updates need room.", t.StorageFreeGB)
	}
	if len(out) == 0 || allInfo(out) && g != nil {
		add("ok", "Overall", "Nothing is pushing a limit right now.")
	}
	return out
}

func allInfo(f []RigFinding) bool {
	for _, x := range f {
		if x.Level != "info" {
			return false
		}
	}
	return true
}

// ── HTTP ────────────────────────────────────────────────────────

// gamingNode picks the device to show: the node named in config "gaming_node", else this PC.
func (a *App) gamingNode() (NodeView, bool) {
	want := strings.TrimSpace(a.cfg.GamingNode)
	for _, n := range a.hub.Snapshot() {
		if want == "" && n.Kind == "local" || want != "" && strings.EqualFold(n.Name, want) {
			return n, true
		}
	}
	return NodeView{}, false
}

func (a *App) activeGame(now time.Time) (string, float64) {
	list, _ := a.store.Sessions()
	for _, s := range list {
		if s.End == nil && s.Kind == "gaming" {
			return s.Project, s.Minutes(now)
		}
	}
	return "", 0
}

func (a *App) handleRig(w http.ResponseWriter, r *http.Request) {
	n, ok := a.gamingNode()
	if !ok {
		writeJSON(w, map[string]any{"ok": false, "error": fmt.Sprintf("The gaming device %q isn't one of the telemetry nodes.", a.cfg.GamingNode)})
		return
	}
	out := map[string]any{"ok": true, "device": n.Name, "kind": n.Kind, "online": n.Online, "last_seen": n.LastSeen, "telemetry": n.Telemetry}
	if n.Telemetry != nil && n.Online {
		out["findings"] = rigFindings(*n.Telemetry)
	}
	game, minutes := a.activeGame(time.Now())
	out["gaming"] = map[string]any{"active": game != "", "game": game, "minutes": round1(minutes)}
	writeJSON(w, out)
}

var rigAdvice struct {
	sync.Mutex
	last time.Time
}

// rigSummary is the telemetry as a few plain lines, for the model and for the rules fallback.
func rigSummary(t NodeTelemetry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "CPU %.0f%% (%d cores)", t.CPUPercent, t.Cores)
	if t.TempC != nil {
		fmt.Fprintf(&b, ", hottest sensor %.0f°C", *t.TempC)
	}
	fmt.Fprintf(&b, "; RAM %.0f%% of %d MB; swap %.0f%%", t.RAMPercent, t.RAMTotalMB, t.SwapPct)
	if g := t.GPU; g != nil {
		fmt.Fprintf(&b, "; GPU %s %.0f%%, %.0f°C, VRAM %d/%d MB", g.Name, g.UtilPct, g.TempC, g.VRAMUsedMB, g.VRAMTotalMB)
	}
	return b.String()
}

func (a *App) handleRigAdvice(w http.ResponseWriter, r *http.Request) {
	var req struct{ Game string }
	_ = readJSON(r, &req)
	game := strings.TrimSpace(req.Game)
	if len(game) > 80 {
		game = game[:80]
	}
	n, ok := a.gamingNode()
	if !ok || n.Telemetry == nil || !n.Online {
		writeErr(w, http.StatusServiceUnavailable, "The gaming device isn't reporting right now.")
		return
	}
	if game == "" {
		game, _ = a.activeGame(time.Now())
	}
	t := *n.Telemetry
	findings := rigFindings(t)

	var rules strings.Builder
	for _, f := range findings {
		rules.WriteString("- " + f.Text + "\n")
	}

	rigAdvice.Lock()
	tooSoon := time.Since(rigAdvice.last) < 10*time.Second
	if !tooSoon {
		rigAdvice.last = time.Now()
	}
	rigAdvice.Unlock()
	if tooSoon {
		writeJSON(w, map[string]any{"ok": true, "source": "rules", "advice": strings.TrimSpace(rules.String()), "note": "Asked again too quickly; here are the rule-based findings."})
		return
	}

	subject := "this PC's current load"
	if game != "" {
		subject = game + " on this PC"
	}
	prompt := fmt.Sprintf(`Review %s.
Live numbers: %s.
What the rules already flagged:
%s
In under 150 words, plain sentences, no markdown: say what to change first and why, naming real in-game setting names for the game if you know them (resolution scale, upscaler, shadows, textures, ray tracing, frame cap). If you don't know the game's menu, say what to look for instead. Don't invent numbers beyond the ones given.`,
		subject, rigSummary(t), strings.TrimSpace(rules.String()))

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	text, model, _, err := a.generateChatReply(ctx, ChatRequest{
		Message: prompt, maxTokens: 400, noLocal: true,
		system: "You are Lucille, a calm, practical gaming assistant. You give specific, honest PC-gaming settings advice from real telemetry.",
	})
	if err != nil || strings.TrimSpace(text) == "" {
		writeJSON(w, map[string]any{"ok": true, "source": "rules", "advice": strings.TrimSpace(rules.String()),
			"note": "No model answered (offline or no key set), so these are the rule-based findings."})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "source": "model", "model": model, "game": game, "advice": strings.TrimSpace(stripThink(text))})
}
