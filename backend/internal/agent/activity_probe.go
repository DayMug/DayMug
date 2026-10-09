package agent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ActivityProbe reports whether a stdout-silent child still appears to be
// doing useful work.
type ActivityProbe func() (active bool, reason string)

// NewProcessActivityProbe combines two low-cost activity signals:
// process-group CPU time and adapter session-log growth. It is intentionally
// best-effort; unreadable /proc or missing logs simply remove that signal.
func NewProcessActivityProbe(proc RunningProcess, logPaths func() []string) ActivityProbe {
	info, _ := proc.(ProcessInfo)
	p := &processActivityProbe{
		info:     info,
		logPaths: logPaths,
	}
	p.prevCPU = p.sampleCPU()
	p.prevLogs = p.sampleLogs()
	return p.probe
}

type processActivityProbe struct {
	mu       sync.Mutex
	info     ProcessInfo
	logPaths func() []string

	prevCPU  uint64
	prevLogs map[string]logSample
}

type logSample struct {
	size    int64
	modNSec int64
}

func (p *processActivityProbe) probe() (bool, string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if cpu := p.sampleCPU(); cpu > p.prevCPU {
		p.prevCPU = cpu
		p.prevLogs = p.sampleLogs()
		return true, "process tree CPU advanced"
	} else {
		p.prevCPU = cpu
	}

	logs := p.sampleLogs()
	for path, cur := range logs {
		prev, ok := p.prevLogs[path]
		if !ok {
			if cur.size > 0 {
				p.prevLogs = logs
				return true, "session log appeared"
			}
			continue
		}
		if cur.size > prev.size || cur.modNSec > prev.modNSec {
			p.prevLogs = logs
			return true, "session log advanced"
		}
	}
	p.prevLogs = logs
	return false, ""
}

func (p *processActivityProbe) sampleCPU() uint64 {
	if p.info == nil {
		return 0
	}
	pgid := p.info.PGID()
	if pgid <= 0 {
		return 0
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	var total uint64
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		entryPGID, ticks, ok := parseProcStat(string(data))
		if ok && entryPGID == pgid {
			total += ticks
		}
	}
	return total
}

func (p *processActivityProbe) sampleLogs() map[string]logSample {
	out := map[string]logSample{}
	if p.logPaths == nil {
		return out
	}
	for _, path := range p.logPaths() {
		if strings.TrimSpace(path) == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		out[path] = logSample{size: info.Size(), modNSec: info.ModTime().UnixNano()}
	}
	return out
}

func parseProcStat(data string) (pgid int, cpuTicks uint64, ok bool) {
	end := strings.LastIndex(data, ")")
	if end < 0 || end+2 >= len(data) {
		return 0, 0, false
	}
	fields := strings.Fields(data[end+2:])
	if len(fields) < 13 {
		return 0, 0, false
	}
	pgrp, err := strconv.Atoi(fields[2])
	if err != nil {
		return 0, 0, false
	}
	utime, err := strconv.ParseUint(fields[11], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	stime, err := strconv.ParseUint(fields[12], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return pgrp, utime + stime, true
}
