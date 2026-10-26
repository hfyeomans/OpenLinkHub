package config

// Package: config
// Author: Nikola Jurkovic
// License: GPL-3.0 or later

import (
	"OpenLinkHub/src/common"
	"encoding/json"
	"log"
	"os"
	"os/user"
	"slices"
	"strings"
	"sync"
)

type Configuration struct {
	Debug                     bool     `json:"debug"`
	ListenPort                int      `json:"listenPort"`
	ListenAddress             string   `json:"listenAddress"`
	CPUSensorChip             string   `json:"cpuSensorChip"`
	Manual                    bool     `json:"manual"`
	Frontend                  bool     `json:"frontend"`
	Metrics                   bool     `json:"metrics"`
	Memory                    bool     `json:"memory"`
	MemorySmBus               string   `json:"memorySmBus"`
	MemoryType                int      `json:"memoryType"`
	Exclude                   []uint16 `json:"exclude"`
	MemorySku                 string   `json:"memorySku"`
	ConfigPath                string   `json:",omitempty"`
	ResumeDelay               int      `json:"resumeDelay"`
	LogFile                   string   `json:"logFile"`
	LogLevel                  string   `json:"logLevel"`
	EnhancementKits           []byte   `json:"enhancementKits"`
	TemperatureOffset         int      `json:"temperatureOffset"`
	AMDGpuIndex               int      `json:"amdGpuIndex"`
	AMDSmiPath                string   `json:"amdsmiPath"`
	CheckDevicePermission     bool     `json:"checkDevicePermission"`
	GraphProfiles             bool     `json:"graphProfiles"`
	CpuTempFile               string   `json:"cpuTempFile"`
	RamTempViaHwmon           bool     `json:"ramTempViaHwmon"`
	NvidiaGpuIndex            []int    `json:"nvidiaGpuIndex"`
	DefaultNvidiaGPU          int      `json:"defaultNvidiaGPU"`
	OpenRGBPort               int      `json:"openRGBPort"`
	EnableOpenRGBTargetServer bool     `json:"enableOpenRGBTargetServer"`
	EnableGamepad             bool     `json:"enableGamepad"`
	EnableMotherboard         bool     `json:"enableMotherboard"`
	MotherboardBiosOnExit     bool     `json:"motherboardBiosOnExit"`
	MemoryRegisterOverride    []byte   `json:"memoryRegisterOverride"`
}

var (
	location      = ""
	configuration Configuration
	configMu      sync.Mutex
	upgrade       = map[string]any{
		"memorySku":                 "",
		"resumeDelay":               15000,
		"logLevel":                  "info",
		"logFile":                   "",
		"enhancementKits":           make([]byte, 0),
		"temperatureOffset":         0,
		"amdGpuIndex":               0,
		"amdsmiPath":                "",
		"checkDevicePermission":     false,
		"cpuTempFile":               "",
		"graphProfiles":             false,
		"ramTempViaHwmon":           false,
		"nvidiaGpuIndex":            []int{0},
		"defaultNvidiaGPU":          0,
		"openRGBPort":               6743,
		"enableOpenRGBTargetServer": false,
		"enableGamepad":             true,
		"enableMotherboard":         false,
		"motherboardBiosOnExit":     false,
		"memoryRegisterOverride":    make([]byte, 0),
	}
	systemService = true
)

// Init will initialize a new config object
func Init() {
	setSystemService()

	var configPath = ""

	pwd, _ := os.Getwd()
	isAtomic := common.FileExists(pwd + "/atomic")
	if isAtomic {
		pwd = "/etc/OpenLinkHub"
		configPath = "/etc/OpenLinkHub"
	} else {
		configPath = pwd
	}
	location = pwd + "/config.json"

	// Create or upgrade
	upgradeFile(location)

	f, err := os.Open(location)
	if err != nil {
		log.Printf("Unable to open config file %s: %v, falling back to defaults", location, err)
		configuration = *defaultConfiguration()
		configuration.ConfigPath = configPath
		return
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			log.Printf("Unable to close config file %s: %v", location, closeErr)
		}
	}()
	if err = json.NewDecoder(f).Decode(&configuration); err != nil {
		log.Printf("Unable to decode config file %s: %v, falling back to defaults", location, err)
		configuration = *defaultConfiguration()
	}
	configuration.ConfigPath = configPath
}

// GetConfig will return structs.Configuration struct
func GetConfig() Configuration {
	configMu.Lock()
	defer configMu.Unlock()

	cfg := configuration
	if cfg.Exclude != nil {
		cfg.Exclude = append([]uint16(nil), cfg.Exclude...)
	}
	return cfg
}

// UpdateSupportedDevices will update the Exclude slice based on the enabled flag for each product ID
func UpdateSupportedDevices(productIds map[uint16]bool) uint8 {
	configMu.Lock()
	defer configMu.Unlock()

	for productId, enabled := range productIds {
		if enabled {
			if i := slices.Index(configuration.Exclude, productId); i != -1 {
				configuration.Exclude = append(configuration.Exclude[:i], configuration.Exclude[i+1:]...)
			}
		} else {
			if !slices.Contains(configuration.Exclude, productId) {
				configuration.Exclude = append(configuration.Exclude, productId)
			}
		}
	}
	if err := saveConfigSettings(configuration); err != nil {
		log.Printf("Unable to save config file %s: %v", location, err)
		return 0
	}
	return 1
}

// IsSystemService will return true if service runs under system context
func IsSystemService() bool {
	return systemService
}

// defaultConfiguration returns the configuration used when the config file is
// missing, corrupt, or truncated.
func defaultConfiguration() *Configuration {
	return &Configuration{
		Debug:                     false,
		ListenPort:                27003,
		ListenAddress:             "127.0.0.1",
		CPUSensorChip:             "",
		Manual:                    false,
		Frontend:                  true,
		Metrics:                   false,
		Memory:                    false,
		MemorySmBus:               "i2c-0",
		MemoryType:                5,
		Exclude:                   make([]uint16, 0),
		MemorySku:                 "",
		ResumeDelay:               15000,
		LogLevel:                  "info",
		LogFile:                   "",
		EnhancementKits:           make([]byte, 0),
		TemperatureOffset:         0,
		AMDGpuIndex:               0,
		AMDSmiPath:                "",
		CheckDevicePermission:     false,
		CpuTempFile:               "",
		GraphProfiles:             true,
		RamTempViaHwmon:           true,
		NvidiaGpuIndex:            []int{0},
		DefaultNvidiaGPU:          0,
		OpenRGBPort:               6743,
		EnableOpenRGBTargetServer: false,
		EnableGamepad:             true,
		EnableMotherboard:         false,
		MotherboardBiosOnExit:     false,
		MemoryRegisterOverride:    make([]byte, 0),
	}
}

// upgradeFile will create or upgrade config file
func upgradeFile(cfg string) {
	if !common.FileExists(cfg) {
		if err := saveConfigSettings(defaultConfiguration()); err != nil {
			log.Printf("Unable to create config file %s: %v", cfg, err)
		}
		return
	}

	save := false
	var data map[string]interface{}
	file, err := os.Open(cfg)
	if err != nil {
		log.Printf("Unable to open config file %s for upgrade: %v", cfg, err)
		return
	}
	if err = json.NewDecoder(file).Decode(&data); err != nil {
		_ = file.Close()
		log.Printf("Unable to decode config file %s for upgrade: %v", cfg, err)
		return
	}
	if err = file.Close(); err != nil {
		log.Printf("Unable to close config file %s: %v", cfg, err)
	}

	// Loop thru upgrade value
	for key, value := range upgrade {
		if _, ok := data[key]; !ok {
			data[key] = value
			save = true
		}
	}
	if save {
		if err = saveConfigSettings(data); err != nil {
			log.Printf("Unable to upgrade config file %s: %v", cfg, err)
		}
	}
}

// SaveConfigSettings will save dashboard settings
func saveConfigSettings(data any) error {
	return common.SaveJsonData(location, data)
}

// setSystemService will check and set systemService state
func setSystemService() {
	uid := os.Getuid()
	if uid < 1000 {
		systemService = true
		return
	}

	if os.Getenv("DISPLAY") != "" ||
		os.Getenv("WAYLAND_DISPLAY") != "" ||
		os.Getenv("XDG_SESSION_TYPE") != "" {
		systemService = false
	}

	u, err := user.Current()
	if err != nil {
		systemService = true
		return
	}

	systemService = !strings.HasPrefix(u.HomeDir, "/home/")
}
