package systeminfo

// Package: systeminfo
// Author: Nikola Jurkovic
// License: GPL-3.0 or later

import (
	"OpenLinkHub/src/common"
	"OpenLinkHub/src/config"
	"OpenLinkHub/src/dashboard"
	"OpenLinkHub/src/logger"
	"OpenLinkHub/src/temperatures"
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type AmdGfxActivity struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

type AmdGpuUsage struct {
	GfxActivity AmdGfxActivity `json:"gfx_activity"`
}

type AmdGPUUsageData struct {
	GPU   int         `json:"gpu"`
	Usage AmdGpuUsage `json:"usage"`
}

type AmdSMIUsage struct {
	GPUData []AmdGPUUsageData `json:"gpu_data"`
}

type AmdSMIModel struct {
	GPUData []AMDGPUModelData `json:"gpu_data"`
}

type CpuData struct {
	Model   string
	Cores   int
	Threads int
}

type GpuData struct {
	Index             int
	Model             string
	Temperature       float32
	TemperatureString string
}

// GpuStat is a full per-GPU telemetry snapshot for the GPU Monitor widget.
type GpuStat struct {
	Index         int     `json:"index"`
	Name          string  `json:"name"`
	Utilization   int     `json:"utilization"`
	MemoryUsed    int     `json:"memoryUsed"`
	MemoryTotal   int     `json:"memoryTotal"`
	Temperature   int     `json:"temperature"`
	PowerDraw     float64 `json:"powerDraw"`
	PowerLimit    float64 `json:"powerLimit"`
	FanSpeed      int     `json:"fanSpeed"`
	ClockGraphics int     `json:"clockGraphics"`
	ClockMemory   int     `json:"clockMemory"`
	PcieGen       int     `json:"pcieGen"`
	PcieWidth     int     `json:"pcieWidth"`
}

// GpuProcess is a single GPU process row for the GPU Monitor widget.
type GpuProcess struct {
	GpuIndex int     `json:"gpuIndex"`
	Pid      int     `json:"pid"`
	Type     string  `json:"type"`
	Name     string  `json:"name"`
	Gpu      int     `json:"gpu"`     // per-process GPU % (sm), -1 when unavailable
	Memory   int     `json:"memory"`  // GPU memory (MiB)
	User     string  `json:"user"`    // process owner
	Cpu      float64 `json:"cpu"`     // host CPU %
	HostMem  int     `json:"hostMem"` // host RSS (MiB)
	Command  string  `json:"command"` // full command line
}

// GpuThroughput is per-GPU PCIe RX/TX (MB/s).
type GpuThroughput struct {
	Index int `json:"index"`
	Rx    int `json:"rx"`
	Tx    int `json:"tx"`
}

// GpuExtended bundles the slower-changing GPU data (throughput + process table)
// polled separately from the fast telemetry so the widget stays cheap.
type GpuExtended struct {
	Throughput []GpuThroughput `json:"throughput"`
	Processes  []GpuProcess    `json:"processes"`
}

// GpuTelemetry is the fast-poll response for the GPU Monitor widget.
type GpuTelemetry struct {
	Gpus []GpuStat `json:"gpus"`
}

type StorageData struct {
	Model             string
	Temperature       float32
	TemperatureString string
	Key               string
}

type KernelData struct {
	OsType       string
	Architecture string
}

type MotherboardData struct {
	Model    string
	BIOS     string
	BIOSDate string
}

type SystemInfo struct {
	CPU         *CpuData
	GPU         map[int]GpuData
	Kernel      *KernelData
	Storage     *[]StorageData
	Motherboard *MotherboardData
}

type Asic struct {
	MarketName string `json:"market_name"`
	VendorID   string `json:"vendor_id"`
	VendorName string `json:"vendor_name"`
}

type AMDGPUModelData struct {
	GPU  int  `json:"gpu"`
	Asic Asic `json:"asic"`
}

var (
	info             *SystemInfo
	prevTotal        = 0
	prevIdle         = 0
	gpuIndex         = 0
	amdsmi           = "amd-smi"
	isAmdsmiFound    = true
	isNvidiaSmiFound = true
)

// Init will initialize and store system info
func Init() {
	gpuIndex = config.GetConfig().AMDGpuIndex
	if len(config.GetConfig().AMDSmiPath) > 0 {
		amdsmi = config.GetConfig().AMDSmiPath
	}

	_, err := exec.LookPath(amdsmi)
	if err != nil {
		isAmdsmiFound = false
		logger.Log(logger.Fields{"warn": err}).Warn("amd-smi not found")
	}

	_, err = exec.LookPath("nvidia-smi")
	if err != nil {
		isNvidiaSmiFound = false
		logger.Log(logger.Fields{"warn": err}).Warn("nvidia-smi not found")
	}

	info = &SystemInfo{}
	info.getCpuData()
	info.getKernelData()
	info.getGpuData()
	info.GetStorageData()
	info.GetBoardData()
}

// GetInfo will return currently stored system info
func GetInfo() *SystemInfo {
	return info
}

// getKernelData will return Kernel data
func (si *SystemInfo) getKernelData() {
	f, err := os.ReadFile("/proc/sys/kernel/ostype")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to read kernel ostype")
		return
	}

	cmd := exec.Command("uname", "-m")
	var out bytes.Buffer
	cmd.Stdout = &out

	err = cmd.Run()
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to read kernel architecture")
		return
	}

	si.Kernel = &KernelData{
		OsType:       strings.TrimSpace(string(f)),
		Architecture: out.String(),
	}
}

// getGpuData will return GPU data
func (si *SystemInfo) getGpuData() {
	cmd := exec.Command("lspci")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Error running lspci")
		return
	}

	// Parse the output to find NVIDIA GPUs
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		line := scanner.Text()
		gpus := make(map[int]GpuData)
		if (strings.Contains(line, "VGA compatible controller") || strings.Contains(line, "3D controller")) && strings.Contains(line, "NVIDIA") && config.GetConfig().DefaultNvidiaGPU != -1 {
			// NVIDIA
			for key := range config.GetConfig().NvidiaGpuIndex {
				gpuModel := GetNVIDIAGpuModel(key)
				if len(gpuModel) > 0 {
					temp := temperatures.GetGpuTemperatureIndex(key)
					model := &GpuData{
						Index:             key,
						Model:             gpuModel,
						Temperature:       temp,
						TemperatureString: dashboard.GetDashboard().TemperatureToString(temp),
					}
					gpus[key] = *model
				}
			}
			si.GPU = gpus
			return
		} else if strings.Contains(line, "VGA compatible controller") && strings.Contains(line, "Advanced Micro Devices") {
			gpuModel := GetAMDGpuModel()
			if len(gpuModel) == 0 {
				lineSplit := strings.Split(line, "[")
				// if lspci gives [my]i[gpu]... return "myigpu"
				// otherwise if gives [my gpu] returns "my gpu"
				if len(lineSplit) >= 3 {
					before, after, found := strings.Cut(lineSplit[1], "]")
					if !found {
						continue
					}
					gpuModel = before + after + lineSplit[2]
				} else {
					gpuModel = lineSplit[1]
				}
				gpuModel = strings.Split(gpuModel, "]")[0]
			}
			temp := temperatures.GetAMDGpuTemperature()
			model := &GpuData{
				Index:             0,
				Model:             gpuModel,
				Temperature:       temp,
				TemperatureString: dashboard.GetDashboard().TemperatureToString(temp),
			}
			gpus[0] = *model
			si.GPU = gpus
			return
		} else {
			si.GPU = nil
		}
	}

	if err = scanner.Err(); err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Error reading lspci data")
	} else {
		fmt.Println("No compatible VGA controllers found")
	}
}

// getCpuData will return CPU data
func (si *SystemInfo) getCpuData() {
	cores := 0
	threads := 0
	model := ""
	// Open cpuinfo
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to get CPU info")
		return
	}

	// Close it
	defer func(f *os.File) {
		err = f.Close()
		if err != nil {
			logger.Log(logger.Fields{"error": err}).Error("Unable to close file handle")
		}
	}(f)

	// Scan it
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "model name") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 && len(model) == 0 {
				model = strings.TrimSpace(parts[1])
			}
		}

		if strings.Contains(line, "cpu cores") {
			parts := strings.Split(line, ":")
			if len(parts) > 1 && cores == 0 {
				cores, err = strconv.Atoi(strings.TrimSpace(parts[1]))
				if err != nil {
					logger.Log(logger.Fields{"error": err}).Error("Unable to process CPU cores")
				}
			}
		}

		if strings.Contains(line, "processor") {
			threads++
		}
	}

	if err = scanner.Err(); err != nil {
		return
	}

	si.CPU = &CpuData{
		Model:   model,
		Cores:   cores,
		Threads: threads,
	}
}

// GetNVIDIAGpuModel will return NVIDIA gpu model
func GetNVIDIAGpuModel(index int) string {
	model := ""
	cmd := exec.Command("nvidia-smi", "-i", strconv.Itoa(index), "--query-gpu=gpu_name", "--format=csv,noheader,nounits")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	model = strings.TrimSpace(string(output))
	return model
}

func GetAMDGpuModel() string {
	if !isAmdsmiFound {
		return ""
	}
	cmd := exec.Command(amdsmi, "static", "-g", strconv.Itoa(gpuIndex), "--asic", "--json")
	jsonOutput, err := cmd.Output()
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to process amd-smi")
		return ""
	}

	var gpuInfo AmdSMIModel
	err = json.Unmarshal(jsonOutput, &gpuInfo)
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to unmarshal JSON data for AmdSMIModel")
		return ""
	}

	return gpuInfo.GPUData[0].Asic.MarketName
}

// GetGpuStats returns full per-GPU telemetry (NVIDIA) for the GPU Monitor widget.
func GetGpuStats() []GpuStat {
	stats := make([]GpuStat, 0)
	if !isNvidiaSmiFound {
		return stats
	}

	cmd := exec.Command("nvidia-smi",
		"--query-gpu=index,name,utilization.gpu,memory.used,memory.total,temperature.gpu,power.draw,power.limit,fan.speed,clocks.gr,clocks.mem,pcie.link.gen.gpucurrent,pcie.link.width.current",
		"--format=csv,noheader,nounits")
	output, err := cmd.Output()
	if err != nil {
		return stats
	}

	// nvidia-smi prints "[N/A]" / "[Not Supported]" for fields a GPU cannot
	// report; return -1 so the widget shows "—" instead of a fake 0.
	atoi := func(s string) int {
		v, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			return -1
		}
		return v
	}
	atof := func(s string) float64 {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return -1
		}
		return v
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ", ")
		if len(fields) < 13 {
			continue
		}
		stats = append(stats, GpuStat{
			Index:         atoi(fields[0]),
			Name:          strings.TrimSpace(fields[1]),
			Utilization:   atoi(fields[2]),
			MemoryUsed:    atoi(fields[3]),
			MemoryTotal:   atoi(fields[4]),
			Temperature:   atoi(fields[5]),
			PowerDraw:     atof(fields[6]),
			PowerLimit:    atof(fields[7]),
			FanSpeed:      atoi(fields[8]),
			ClockGraphics: atoi(fields[9]),
			ClockMemory:   atoi(fields[10]),
			PcieGen:       atoi(fields[11]),
			PcieWidth:     atoi(fields[12]),
		})
	}
	return stats
}

// GetGpuThroughput samples per-GPU PCIe RX/TX (MB/s) via nvidia-smi dmon.
func GetGpuThroughput() []GpuThroughput {
	result := make([]GpuThroughput, 0)
	if !isNvidiaSmiFound {
		return result
	}
	output, err := exec.Command("nvidia-smi", "dmon", "-c", "1", "-s", "t").Output()
	if err != nil {
		return result
	}
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		idx, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		rx, _ := strconv.Atoi(fields[1])
		tx, _ := strconv.Atoi(fields[2])
		result = append(result, GpuThroughput{Index: idx, Rx: rx, Tx: tx})
	}
	return result
}

// gpuProcessRegex parses a process row from default nvidia-smi output:
// |    1   N/A  N/A   10373      G   /usr/bin/ghostty            118MiB |
// The GI/CI columns (MIG) are optional so pre-MIG nvidia-smi layouts still parse.
var gpuProcessRegex = regexp.MustCompile(`^\|\s+(\d+)\s+(?:\S+\s+\S+\s+)?(\d+)\s+(\S+)\s+(.+?)\s+(\d+)MiB\s*\|`)

// GetGpuProcesses returns the GPU process table (NVIDIA) for the GPU Monitor widget.
func GetGpuProcesses() []GpuProcess {
	procs := make([]GpuProcess, 0)
	if !isNvidiaSmiFound {
		return procs
	}

	output, err := exec.Command("nvidia-smi").Output()
	if err != nil {
		return procs
	}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		m := gpuProcessRegex.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		pid, _ := strconv.Atoi(m[2])
		memory, _ := strconv.Atoi(m[5])
		procs = append(procs, GpuProcess{
			GpuIndex: idx,
			Pid:      pid,
			Type:     gpuProcessType(m[3]),
			Name:     strings.TrimSpace(m[4]),
			Gpu:      -1,
			Memory:   memory,
		})
	}

	enrichGpuProcessUtilization(procs) // per-process GPU % via pmon
	enrichHostProcessInfo(procs)       // user / cpu% / host mem / command via ps
	return procs
}

// gpuProcessType expands nvidia-smi's terse process type into a readable label.
func gpuProcessType(t string) string {
	switch t {
	case "C":
		return "Compute"
	case "G", "C+G", "G+C":
		return "Graphic"
	}
	return t
}

// enrichGpuProcessUtilization fills per-process GPU % (sm) from nvidia-smi pmon.
func enrichGpuProcessUtilization(procs []GpuProcess) {
	output, err := exec.Command("nvidia-smi", "pmon", "-c", "1").Output()
	if err != nil {
		return
	}
	sm := make(map[string]int)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		v, err := strconv.Atoi(f[3])
		if err != nil {
			continue // "-" when unsupported
		}
		sm[f[0]+":"+f[1]] = v
	}
	for i := range procs {
		if v, ok := sm[strconv.Itoa(procs[i].GpuIndex)+":"+strconv.Itoa(procs[i].Pid)]; ok {
			procs[i].Gpu = v
		}
	}
}

// enrichHostProcessInfo fills user, cpu %, host memory and full command via ps.
// Note: the full command line (and owner) of every GPU process is then served
// over the unauthenticated /api/gpuExtended endpoint, matching nvtop's process
// table. Command lines can contain secrets passed as arguments; this stays
// within the app's existing no-auth/LAN posture but exposes more than the older
// hardware-only telemetry endpoints.
func enrichHostProcessInfo(procs []GpuProcess) {
	if len(procs) == 0 {
		return
	}
	seen := make(map[int]bool)
	pids := make([]string, 0, len(procs))
	for _, p := range procs {
		if !seen[p.Pid] {
			seen[p.Pid] = true
			pids = append(pids, strconv.Itoa(p.Pid))
		}
	}

	output, err := exec.Command("ps", "-o", "pid=,user=,pcpu=,rss=,args=", "-p", strings.Join(pids, ",")).Output()
	if err != nil {
		return
	}

	type hostInfo struct {
		user    string
		cpu     float64
		hostMem int
		command string
	}
	byPid := make(map[int]hostInfo)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		f := strings.Fields(scanner.Text())
		if len(f) < 5 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(f[2], 64)
		rss, _ := strconv.Atoi(f[3])
		byPid[pid] = hostInfo{
			user: f[1],
			cpu:  cpu,
			// Executable basename only — the full argv can carry secrets
			// (e.g. --password=, API tokens) and this is served unauthenticated.
			command: filepath.Base(f[4]),
			hostMem: rss / 1024,
		}
	}
	for i := range procs {
		if h, ok := byPid[procs[i].Pid]; ok {
			procs[i].User = h.user
			procs[i].Cpu = h.cpu
			procs[i].HostMem = h.hostMem
			procs[i].Command = h.command
		}
	}
}

// GetNVIDIAUtilization will return NVIDIA gpu utilization
func getNVIDIAUtilization(index int) int {
	cmd := exec.Command("nvidia-smi", "-i", strconv.Itoa(index), "--query-gpu=utilization.gpu", "--format=csv,noheader,nounits")
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	utilization := strings.TrimSpace(string(output))
	util, e := strconv.Atoi(utilization)
	if e != nil {
		logger.Log(logger.Fields{"error": e}).Error("Unable to convert GPU utilization")
		return 0
	}
	return util
}

// GetStorageData will return storage information
func (si *SystemInfo) GetStorageData() {
	hwmonDir := "/sys/class/hwmon"
	entries, err := os.ReadDir(hwmonDir)
	if err != nil {
		logger.Log(logger.Fields{"dir": hwmonDir, "error": err}).Error("Unable to read hwmon directory")
		return
	}

	var storageList []StorageData

	for _, entry := range entries {
		nameFile := filepath.Join(hwmonDir, entry.Name(), "name")
		nameBytes, e := os.ReadFile(nameFile)
		if e != nil {
			continue
		}

		name := strings.TrimSpace(string(nameBytes))

		if string(name) == "nvme" || string(name) == "drivetemp" {
			var temperature float32 = 0.0
			tempFile := filepath.Join(hwmonDir, entry.Name(), "temp1_input")
			temp, e := os.ReadFile(tempFile)
			if e != nil {
				logger.Log(logger.Fields{"dir": entry.Name(), "file": tempFile, "error": e}).Error("Unable to read hwmon file")
				continue
			}

			// Convert the temperature from milli-Celsius to Celsius
			tempMilliC, e := strconv.Atoi(strings.TrimSpace(string(temp)))
			if e != nil {
				logger.Log(logger.Fields{"dir": entry.Name(), "file": tempFile, "error": e}).Error("Unable to read nvme temperature file")
				continue
			}
			temperature = float32(tempMilliC / 1000)

			modelFile := filepath.Join(hwmonDir, entry.Name(), "device/model")
			deviceModel, e := os.ReadFile(modelFile)
			if e != nil {
				logger.Log(logger.Fields{"dir": entry.Name(), "file": tempFile, "error": e}).Error("Unable to read hwmon file")
				continue
			}

			model := strings.TrimSpace(string(deviceModel))

			storage := StorageData{
				Key:               entry.Name(),
				Temperature:       temperature,
				TemperatureString: dashboard.GetDashboard().TemperatureToString(temperature),
				Model:             model,
			}
			storageList = append(storageList, storage)
		}
	}

	si.Storage = &storageList
}

// GetStorageTemperatureIndex returns the live temperature (°C) of the Nth storage
// device in hwmon order, or -1 when unavailable. Read fresh each call so Edge ring
// gauges stay current.
func GetStorageTemperatureIndex(index int) int {
	si := &SystemInfo{}
	si.GetStorageData()
	if si.Storage == nil || index < 0 || index >= len(*si.Storage) {
		return -1
	}
	return int((*si.Storage)[index].Temperature)
}

// GetBoardData will return motherboard details
func (si *SystemInfo) GetBoardData() {
	board := &MotherboardData{}

	// Motherboard model
	f, err := os.ReadFile("/sys/class/dmi/id/product_name")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to read kernel ostype")
		return
	}
	board.Model = strings.TrimSpace(string(f))

	// BIOS version
	f, err = os.ReadFile("/sys/class/dmi/id/bios_version")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to read kernel ostype")
		return
	}
	board.BIOS = strings.TrimSpace(string(f))

	// BIOS release date
	f, err = os.ReadFile("/sys/class/dmi/id/bios_date")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to read kernel ostype")
		return
	}
	board.BIOSDate = strings.TrimSpace(string(f))
	si.Motherboard = board
}

// GetCpuUtilization will return CPU utilization
func GetCpuUtilization() float64 {
	file, err := os.Open("/proc/stat")
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Failed to open /proc/stat")
		return 0
	}
	defer func(file *os.File) {
		err = file.Close()
		if err != nil {

		}
	}(file)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "cpu") {
			fields := strings.Fields(line)
			var (
				user    = common.Atoi(fields[1])
				nice    = common.Atoi(fields[2])
				system  = common.Atoi(fields[3])
				idle    = common.Atoi(fields[4])
				iowait  = common.Atoi(fields[5])
				irq     = common.Atoi(fields[6])
				softirq = common.Atoi(fields[7])
			)

			total := user + nice + system + idle + iowait + irq + softirq
			idleTime := idle + iowait

			totalDiff := total - prevTotal
			idleDiff := idleTime - prevIdle

			prevTotal = total
			prevIdle = idleTime

			cpuUsage := float64(totalDiff-idleDiff) / float64(totalDiff) * 100
			return cpuUsage
		}
	}
	return 0
}

// getAMDUtilization fetches the GPU utilization using amd-smi
func getAMDUtilization() float64 {
	if !isAmdsmiFound {
		return 0
	}
	cmd := exec.Command(amdsmi, "metric", "-g", strconv.Itoa(gpuIndex), "-u", "--json")
	jsonOutput, err := cmd.Output()
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to process amd-smi")
		return 0
	}

	var gpuUsage AmdSMIUsage
	err = json.Unmarshal(jsonOutput, &gpuUsage)
	if err != nil {
		logger.Log(logger.Fields{"error": err}).Error("Unable to unmarshal JSON data for AmdSMIUsage")
		return 0
	}

	if len(gpuUsage.GPUData) > 0 {
		return gpuUsage.GPUData[0].Usage.GfxActivity.Value
	}
	return 0
}

func GetGPUUtilization() int {
	utilization := 0
	if info.GPU != nil {
		index := config.GetConfig().DefaultNvidiaGPU
		if index != -1 && strings.Contains(strings.ToLower(info.GPU[index].Model), "nvidia") {
			// NVIDIA
			utilization = getNVIDIAUtilization(index)
		} else {
			// AMD
			util := getAMDUtilization()
			utilization = int(util)
		}
	}
	return utilization
}

// GetGPUUtilizationIndex will return gpu utilization for a specific gpu index
func GetGPUUtilizationIndex(index int) int {
	if info.GPU != nil {
		if gpu, ok := info.GPU[index]; ok {
			if strings.Contains(strings.ToLower(gpu.Model), "nvidia") {
				// NVIDIA
				return getNVIDIAUtilization(index)
			}
			// AMD
			return int(getAMDUtilization())
		}
	}
	return 0
}
