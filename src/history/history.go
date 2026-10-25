package history

// Package: history
// Author: Nikola Jurkovic
// License: GPL-3.0 or later
//
// In-memory sensor history for the Xeneon Edge sensor chart widget (roadmap G4 S1).
// A single sampler goroutine records point-in-time readings from the same getters
// the /api/cpuTemp, /api/cpuLoad, /api/gpuTemp, /api/gpuLoad and /api/storageTemp
// routes use, into a circular buffer per sensor key. There is no persistence and
// a single tier: capacity 900 samples at a 2 s tick = 30 minutes of retention.
//
// Sensor keys (see Sensors()):
//   - cpu-temp           CPU temperature (°C), temperatures.GetCpuTemperature
//   - cpu-load           CPU utilization (%), systeminfo.GetCpuUtilization
//   - gpu-<i>-temp       GPU temperature (°C) per GPU, temperatures.GetGpuTemperatureIndex
//   - gpu-<i>-load       GPU utilization (%) per GPU, systeminfo.GetGPUUtilizationIndex
//   - storage-<i>-temp   storage temperature (°C), systeminfo.GetStorageTemperatureIndex
//
// <i> is the GPU hwmon/device index from systeminfo.GetInfo().GPU (the same value
// /api/gpuTemp/clean/ resolves) and the Nth storage device in hwmon order (the
// same index space /api/storageTemp/clean/ serves).
//
// Locking discipline: the package mutex is never held across sensor reads. The
// getters do file I/O and spawn helper processes (nvidia-smi / amd-smi), so their
// latency is out of our control; all reads happen lock-free and only the buffer
// append takes the lock (mirrors the unlocked-I/O discipline of src/spotify).

import (
	"OpenLinkHub/src/systeminfo"
	"OpenLinkHub/src/temperatures"
	"sort"
	"strconv"
	"sync"
	"time"
)

const (
	// sampleInterval is the sampler tick. 2 s keeps the chart lively and gives
	// exactly 30 minutes of retention at the full buffer capacity.
	sampleInterval = 2 * time.Second

	// historyCapacity is the per-sensor ring size: 900 samples * 2 s = 30 min.
	historyCapacity = 900

	// storageRefreshTicks re-derives the storage device count every N ticks
	// (30 ticks = 60 s) so hotplugged or removed drives are picked up without
	// rescanning hwmon on every tick.
	storageRefreshTicks = 30
)

// Point is a single recorded sample. T is a Unix timestamp in seconds, V is the
// sensor value (°C for temperatures, % 0-100 for utilization).
type Point struct {
	T int64   `json:"t"`
	V float32 `json:"v"`
}

// reading is one value produced by a sampler tick, waiting to be buffered.
type reading struct {
	key string
	v   float32
}

// ring is a fixed-capacity circular buffer of Points, oldest first.
type ring struct {
	buf       []Point
	next      int   // next write position
	count     int   // number of valid samples, capped at len(buf)
	lastWrite int64 // unix seconds of the most recent sample
}

func newRing() *ring {
	return &ring{buf: make([]Point, historyCapacity)}
}

// append stores a sample, overwriting the oldest one once full.
func (r *ring) append(p Point) {
	r.lastWrite = p.T
	r.buf[r.next] = p
	r.next = (r.next + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
}

// snapshot returns buffered points oldest-first, keeping only samples with
// t >= cutoff when cutoff > 0. The result is a copy, safe to use after the
// caller releases the package mutex.
func (r *ring) snapshot(cutoff int64) []Point {
	out := make([]Point, 0, r.count)
	start := (r.next - r.count + len(r.buf)) % len(r.buf)
	for i := range r.count {
		p := r.buf[(start+i)%len(r.buf)]
		if cutoff > 0 && p.T < cutoff {
			continue
		}
		out = append(out, p)
	}
	return out
}

var (
	mu      sync.RWMutex
	sensors = make(map[string]*ring)

	samplerMu sync.Mutex // serializes Init/Stop lifecycle only, never held during ticks
	stopCh    chan struct{}
	running   bool
)

// Init starts the sampler goroutine. It is idempotent; later calls while the
// sampler is running do nothing.
func Init() {
	samplerMu.Lock()
	defer samplerMu.Unlock()
	if running {
		return
	}
	stopCh = make(chan struct{})
	running = true
	go sampler(stopCh)
}

// Stop stops the sampler goroutine. It is idempotent and does not wait for an
// in-flight tick to finish (sensor reads spawn helper processes and can take a
// moment; buffered data is lost with the process either way).
func Stop() {
	samplerMu.Lock()
	defer samplerMu.Unlock()
	if !running {
		return
	}
	close(stopCh)
	running = false
}

// Snapshot returns the recorded history for the given sensor key, oldest sample
// first, limited to the last windowMinutes minutes. windowMinutes <= 0 returns
// the entire retained buffer (up to 30 minutes). Unknown sensor keys yield an
// empty (non-nil) slice, so JSON responses marshal to [].
func Snapshot(sensor string, windowMinutes int) []Point {
	mu.RLock()
	defer mu.RUnlock()
	r, ok := sensors[sensor]
	if !ok {
		return []Point{}
	}
	var cutoff int64
	if windowMinutes > 0 {
		cutoff = time.Now().Unix() - int64(windowMinutes)*60
	}
	return r.snapshot(cutoff)
}

// Sensors returns the sorted list of sensor keys that have recorded at least one
// sample, suitable for validating a widget's configured sensor. Keys for missing
// hardware (a GPU that is not present, a storage index beyond the device count)
// never appear here; the sampler simply records nothing for them.
func Sensors() []string {
	mu.RLock()
	keys := make([]string, 0, len(sensors))
	for key := range sensors {
		keys = append(keys, key)
	}
	mu.RUnlock()
	sort.Strings(keys)
	return keys
}

// sampler ticks every sampleInterval until stop is closed. The tick counter and
// storage device count are owned by this goroutine, so they need no locking.
func sampler(stop <-chan struct{}) {
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()

	tick := 0
	storageCount := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if tick%storageRefreshTicks == 0 {
				// Same source the /api/storageTemp handler uses; the length of
				// this list is the number of indexable storage devices.
				storageCount = len(temperatures.GetStorageTemperatures())
			}
			tick++
			record(time.Now().Unix(), storageCount)
			if tick%storageRefreshTicks == 0 {
				pruneIdle(time.Now().Unix())
			}
		}
	}
}

// pruneIdle drops rings whose sensor stopped reporting (hotplugged-away GPU
// or drive): an idle ring would otherwise linger forever and make Sensors()
// advertise hardware that no longer exists. 5 minutes of silence is far
// longer than any transient read failure.
func pruneIdle(now int64) {
	mu.Lock()
	defer mu.Unlock()
	for key, r := range sensors {
		if now-r.lastWrite > 300 {
			delete(sensors, key)
		}
	}
}

// record samples every known sensor and buffers the results. All sensor reads
// run without holding mu: they walk sysfs and spawn nvidia-smi/amd-smi, and a
// slow read must never block Snapshot/Sensors. Getters signal failure by
// returning 0 (temperature getters) or a negative value (storage index), and a
// failed read simply records nothing for that tick.
func record(now int64, storageCount int) {
	readings := make([]reading, 0, 4+storageCount*2)

	// CPU. A 0 °C reading means the hwmon lookup failed, so skip it; 0% load is
	// a valid sample and is always recorded.
	if temp := temperatures.GetCpuTemperature(); temp > 0 {
		readings = append(readings, reading{"cpu-temp", temp})
	}
	readings = append(readings, reading{"cpu-load", float32(systeminfo.GetCpuUtilization())})

	// GPUs. The list is re-read every tick (a plain map read, no I/O), so GPUs
	// that appear in or vanish from the system info are picked up automatically
	// and missing GPUs are skipped without error.
	for _, gpu := range gpuList() {
		index := strconv.Itoa(gpu.Index)
		// GetGpuTemperatureIndex falls back from NVIDIA to AMD and returns 0
		// when neither probe succeeds; skip 0 °C like the CPU temperature.
		if temp := temperatures.GetGpuTemperatureIndex(gpu.Index); temp > 0 {
			readings = append(readings, reading{"gpu-" + index + "-temp", temp})
		}
		// 0% is a valid utilization; GetGPUUtilizationIndex already guards the
		// index lookup internally.
		readings = append(readings, reading{"gpu-" + index + "-load", float32(systeminfo.GetGPUUtilizationIndex(gpu.Index))})
	}

	// Storage. GetStorageTemperatureIndex returns -1 when the index is beyond
	// the current device list (e.g. a drive was unplugged), matching the
	// /api/storageTemp/clean/ validity check; such ticks record nothing.
	for i := range storageCount {
		if temp := systeminfo.GetStorageTemperatureIndex(i); temp >= 0 {
			readings = append(readings, reading{"storage-" + strconv.Itoa(i) + "-temp", float32(temp)})
		}
	}

	mu.Lock()
	for _, r := range readings {
		ringBuf := sensors[r.key]
		if ringBuf == nil {
			ringBuf = newRing()
			sensors[r.key] = ringBuf
		}
		ringBuf.append(Point{T: now, V: r.v})
	}
	mu.Unlock()
}

// gpuList returns the configured GPUs by device index, tolerating system info
// that has not been initialized yet.
func gpuList() []systeminfo.GpuData {
	info := systeminfo.GetInfo()
	if info == nil || info.GPU == nil {
		return nil
	}
	gpus := make([]systeminfo.GpuData, 0, len(info.GPU))
	for _, gpu := range info.GPU {
		gpus = append(gpus, gpu)
	}
	return gpus
}
