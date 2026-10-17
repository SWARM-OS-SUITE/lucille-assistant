package main

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Node telemetry. The field names and rules match the I30LTZ node agent
// (/v1/telemetry), so this PC and every paired node look the same to the app:
// a value that could not be measured is -1 (or absent), never a plausible default.

type NodeTelemetry struct {
	Hostname       string    `json:"hostname"`
	OS             string    `json:"os"`
	CPUPercent     float64   `json:"cpu_percent"`
	RAMPercent     float64   `json:"ram_percent"`
	RAMTotalMB     int64     `json:"ram_total_mb"`
	RAMUsedMB      int64     `json:"ram_used_mb"`
	StorageTotalGB float64   `json:"storage_total_gb"`
	StorageFreeGB  float64   `json:"storage_free_gb"`
	Load1          float64   `json:"load1"`
	UptimeSeconds  int64     `json:"uptime_seconds"`
	TempC          *float64  `json:"temp_c,omitempty"`
	Time           time.Time `json:"time"`
	// Extras only this service measures; other nodes leave them at -1.
	Cores     int     `json:"cores,omitempty"`
	SwapPct   float64 `json:"swap_percent"`
	NetRxKBps float64 `json:"net_rx_kbps"`
	NetTxKBps float64 `json:"net_tx_kbps"`
	// GPU is absent when the device has no nvidia-smi / amdgpu to read (rig.go).
	GPU *GPUTelemetry `json:"gpu,omitempty"`
}

// Collector reads from configurable roots so tests can point it at fixtures.
type Collector struct {
	ProcRoot      string // default /proc
	SysRoot       string // default /sys
	OSReleasePath string // default /etc/os-release
	StatfsPath    string // default /
	GPU           bool   // read GPU numbers (nvidia-smi / amdgpu); off in tests so they never touch real hardware

	mu                  sync.Mutex
	prevIdle, prevTotal uint64
	haveCPU             bool
	prevRx, prevTx      uint64
	prevNetAt           time.Time
	haveNet             bool
}

func NewCollector() *Collector {
	return &Collector{ProcRoot: "/proc", SysRoot: "/sys", OSReleasePath: "/etc/os-release", StatfsPath: "/", GPU: true}
}

func readText(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

func round1(v float64) float64 { return float64(int64(v*10+0.5)) / 10 }

// parseCPUStat returns idle and total jiffies from the aggregate "cpu " line.
func parseCPUStat(stat string) (idle, total uint64, err error) {
	for _, line := range strings.Split(stat, "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		if len(fields) < 4 {
			return 0, 0, errors.New("short cpu line")
		}
		for i, f := range fields {
			if i >= 8 { // guest times are already counted in user/nice
				break
			}
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return 0, 0, err
			}
			total += v
			if i == 3 || i == 4 { // idle + iowait
				idle += v
			}
		}
		return idle, total, nil
	}
	return 0, 0, errors.New("no aggregate cpu line in stat")
}

// parseMeminfo returns the values of the named fields in kB (-1 when absent).
func parseMeminfo(mem string, names ...string) map[string]int64 {
	out := map[string]int64{}
	for _, n := range names {
		out[n] = -1
	}
	for _, line := range strings.Split(mem, "\n") {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if _, want := out[name]; !want {
			continue
		}
		if f := strings.Fields(rest); len(f) > 0 {
			if v, err := strconv.ParseInt(f[0], 10, 64); err == nil {
				out[name] = v
			}
		}
	}
	return out
}

// parseNetDev sums received and transmitted bytes over every interface except loopback.
func parseNetDev(raw string) (rx, tx uint64, ok bool) {
	for _, line := range strings.Split(raw, "\n") {
		name, rest, found := strings.Cut(line, ":")
		if !found || strings.TrimSpace(name) == "lo" {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		r, e1 := strconv.ParseUint(f[0], 10, 64)
		t, e2 := strconv.ParseUint(f[8], 10, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		rx, tx, ok = rx+r, tx+t, true
	}
	return
}

func parseOSName(release string) string {
	for _, line := range strings.Split(release, "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(v, `"'`)
		}
	}
	return "Linux"
}

func (c *Collector) cpuPercent() float64 {
	raw, err := readText(filepath.Join(c.ProcRoot, "stat"))
	if err != nil {
		return -1
	}
	idle, total, err := parseCPUStat(raw)
	if err != nil {
		return -1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pi, pt, have := c.prevIdle, c.prevTotal, c.haveCPU
	c.prevIdle, c.prevTotal, c.haveCPU = idle, total, true
	if !have || total <= pt || idle < pi {
		return -1 // the first sample has nothing to compare with
	}
	return round1(float64((total-pt)-(idle-pi)) / float64(total-pt) * 100)
}

// netRates returns KB/s received and sent since the previous sample, or -1, -1.
func (c *Collector) netRates(now time.Time) (float64, float64) {
	raw, err := readText(filepath.Join(c.ProcRoot, "net/dev"))
	if err != nil {
		return -1, -1
	}
	rx, tx, ok := parseNetDev(raw)
	if !ok {
		return -1, -1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pr, pt, pat, have := c.prevRx, c.prevTx, c.prevNetAt, c.haveNet
	c.prevRx, c.prevTx, c.prevNetAt, c.haveNet = rx, tx, now, true
	dt := now.Sub(pat).Seconds()
	if !have || dt <= 0 || rx < pr || tx < pt { // first sample, or a counter reset
		return -1, -1
	}
	return round1(float64(rx-pr) / 1024 / dt), round1(float64(tx-pt) / 1024 / dt)
}

func (c *Collector) maxTempC() *float64 {
	matches, _ := filepath.Glob(filepath.Join(c.SysRoot, "class/thermal/thermal_zone*/temp"))
	var best float64
	found := false
	for _, m := range matches {
		raw, err := readText(m)
		if err != nil {
			continue
		}
		milli, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil || milli <= 0 || milli > 150000 { // ignore sentinel values
			continue
		}
		if !found || milli/1000 > best {
			best, found = milli/1000, true
		}
	}
	if !found {
		return nil
	}
	best = round1(best)
	return &best
}

// Sample takes one snapshot. CPU and network rates are -1 until a second sample exists.
func (c *Collector) Sample() NodeTelemetry {
	now := time.Now().UTC()
	t := NodeTelemetry{CPUPercent: c.cpuPercent(), RAMPercent: -1, RAMTotalMB: -1, RAMUsedMB: -1,
		StorageTotalGB: -1, StorageFreeGB: -1, Load1: -1, UptimeSeconds: -1, SwapPct: -1, Time: now}
	t.NetRxKBps, t.NetTxKBps = c.netRates(now)
	t.Hostname, _ = os.Hostname()
	t.OS = "Linux"
	if raw, err := readText(c.OSReleasePath); err == nil {
		t.OS = parseOSName(raw)
	}
	if raw, err := readText(filepath.Join(c.ProcRoot, "meminfo")); err == nil {
		m := parseMeminfo(raw, "MemTotal", "MemAvailable", "SwapTotal", "SwapFree")
		if total, avail := m["MemTotal"], m["MemAvailable"]; total > 0 && avail >= 0 {
			t.RAMTotalMB = total / 1024
			t.RAMUsedMB = (total - avail) / 1024
			t.RAMPercent = round1(float64(total-avail) / float64(total) * 100)
		}
		if st, sf := m["SwapTotal"], m["SwapFree"]; st > 0 && sf >= 0 {
			t.SwapPct = round1(float64(st-sf) / float64(st) * 100)
		}
	}
	var fs syscall.Statfs_t
	if err := syscall.Statfs(c.StatfsPath, &fs); err == nil {
		bs := float64(fs.Bsize)
		t.StorageTotalGB = round1(float64(fs.Blocks) * bs / (1 << 30))
		t.StorageFreeGB = round1(float64(fs.Bavail) * bs / (1 << 30))
	}
	if raw, err := readText(filepath.Join(c.ProcRoot, "loadavg")); err == nil {
		if f := strings.Fields(raw); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				t.Load1 = v
			}
		}
	}
	if raw, err := readText(filepath.Join(c.ProcRoot, "uptime")); err == nil {
		if f := strings.Fields(raw); len(f) > 0 {
			if v, err := strconv.ParseFloat(f[0], 64); err == nil {
				t.UptimeSeconds = int64(v)
			}
		}
	}
	if raw, err := readText(filepath.Join(c.ProcRoot, "cpuinfo")); err == nil {
		t.Cores = strings.Count(raw, "\nprocessor") + btoi(strings.HasPrefix(raw, "processor"))
	}
	t.TempC = c.maxTempC()
	t.GPU = c.gpuSample()
	return t
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
