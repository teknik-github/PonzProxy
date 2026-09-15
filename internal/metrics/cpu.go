package metrics

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// cpuSampler reports this process's CPU usage as a percentage of one core's
// worth of time, averaged over the gap between calls.
//
// It reads /proc/self/stat, which means it reports 0 on platforms without
// procfs. That is a deliberate trade: the dashboard degrades to hiding one
// tile, and ponzproxy avoids a cgo dependency for a cosmetic number.
type cpuSampler struct {
	mu       sync.Mutex
	lastTick float64
	lastTime time.Time
	lastVal  float64
}

// clockTicks is the kernel's USER_HZ. It is 100 on every mainstream Linux
// configuration, and getconf CLK_TCK is not reachable without cgo.
const clockTicks = 100.0

func (c *cpuSampler) sample() float64 {
	ticks, ok := readProcessCPUTicks()
	if !ok {
		return 0
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	if c.lastTime.IsZero() {
		// The first call has no interval to average over; establish the
		// baseline and report nothing rather than a meaningless figure.
		c.lastTick, c.lastTime = ticks, now
		return 0
	}

	elapsed := now.Sub(c.lastTime).Seconds()
	if elapsed < 0.2 {
		// Too short an interval turns tick quantisation into noise, so
		// repeat the previous reading instead.
		return c.lastVal
	}

	used := (ticks - c.lastTick) / clockTicks
	c.lastTick, c.lastTime = ticks, now
	c.lastVal = used / elapsed * 100
	return c.lastVal
}

// readProcessCPUTicks returns utime+stime from /proc/self/stat.
func readProcessCPUTicks() (float64, bool) {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}

	// Field 2 is the executable name in parentheses and may itself contain
	// spaces, so parsing starts after the final ')'.
	close := strings.LastIndexByte(string(data), ')')
	if close < 0 {
		return 0, false
	}
	fields := strings.Fields(string(data[close+1:]))

	// After the name, fields are numbered from 3. utime is 14 and stime 15,
	// which are indices 11 and 12 of this slice.
	const utimeIdx, stimeIdx = 11, 12
	if len(fields) <= stimeIdx {
		return 0, false
	}
	utime, err1 := strconv.ParseFloat(fields[utimeIdx], 64)
	stime, err2 := strconv.ParseFloat(fields[stimeIdx], 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return utime + stime, true
}
