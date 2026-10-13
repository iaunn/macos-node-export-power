package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

var (
	rePowerIn        = regexp.MustCompile(`"SystemPowerIn"\s*=\s*(\d+)`)
	reEnergyConsumed = regexp.MustCompile(`"SystemEnergyConsumed"\s*=\s*(\d+)`)
	reLoad           = regexp.MustCompile(`"SystemLoad"\s*=\s*(\d+)`)
	reWatts          = regexp.MustCompile(`"Watts"\s*=\s*(\d+)`)
	reVoltageIn      = regexp.MustCompile(`"SystemVoltageIn"\s*=\s*(\d+)`)
	reCurrentIn      = regexp.MustCompile(`"SystemCurrentIn"\s*=\s*(\d+)`)
	reAmperage       = regexp.MustCompile(`"Amperage"\s*=\s*(\d+)`)
	reVoltage        = regexp.MustCompile(`"Voltage"\s*=\s*(\d+)`)
	reIsCharging     = regexp.MustCompile(`"IsCharging"\s*=\s*(Yes|No)`)
	reExternalConn   = regexp.MustCompile(`"ExternalConnected"\s*=\s*(Yes|No)`)
	reBatteryTemp    = regexp.MustCompile(`"Temperature"\s*=\s*(\d+)`)
	reVirtualTemp    = regexp.MustCompile(`"VirtualTemperature"\s*=\s*(\d+)`)
)

func extractMetric(re *regexp.Regexp, text string) float64 {
	matches := re.FindStringSubmatch(text)
	if len(matches) > 1 {
		val, err := strconv.ParseFloat(matches[1], 64)
		if err == nil {
			return val
		}
	}
	return 0
}

func extractBool(re *regexp.Regexp, text string) bool {
	matches := re.FindStringSubmatch(text)
	if len(matches) > 1 && matches[1] == "Yes" {
		return true
	}
	return false
}

type MacmonData struct {
	Temp struct {
		CPUTempAvg float64 `json:"cpu_temp_avg"`
		GPUTempAvg float64 `json:"gpu_temp_avg"`
	} `json:"temp"`
	Fans []struct {
		Name string  `json:"name"`
		RPM  float64 `json:"rpm"`
	} `json:"fans"`
	CPUPower       float64 `json:"cpu_power"`
	GPUPower       float64 `json:"gpu_power"`
	ANEPower       float64 `json:"ane_power"`
	RAMPower       float64 `json:"ram_power"`
	AllPower       float64 `json:"all_power"`
	SysPower       float64 `json:"sys_power"`
	ECPUFreqMHz    float64 `json:"ecpu_freq_mhz"`
	PCPUFreqMHz    float64 `json:"pcpu_freq_mhz"`
	GPUFreqMHz     float64 `json:"gpu_freq_mhz"`
	GPUActiveRatio float64 `json:"gpu_active_ratio"`
}

// findMacmon locates the macmon binary. PM2/launchd often run with a minimal
// PATH that lacks Homebrew's bin directory, so check common locations too.
func findMacmon() string {
	if p, err := exec.LookPath("macmon"); err == nil {
		return p
	}
	for _, p := range []string{"/opt/homebrew/bin/macmon", "/usr/local/bin/macmon"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func collectMacmon() *MacmonData {
	bin := findMacmon()
	if bin == "" {
		log.Printf("macmon not found; skipping thermal/SoC metrics")
		return nil
	}
	out, err := exec.Command(bin, "pipe", "-s", "1").Output()
	if err != nil {
		log.Printf("macmon error: %v", err)
		return nil
	}
	var data MacmonData
	if err := json.Unmarshal(out, &data); err != nil {
		log.Printf("macmon parse error: %v", err)
		return nil
	}
	return &data
}

func collectAndWrite(outDir string) error {
	out, err := exec.Command("ioreg", "-r", "-n", "AppleSmartBattery").Output()
	if err != nil {
		return fmt.Errorf("ioreg error: %w", err)
	}
	text := string(out)

	sysPowerMW := extractMetric(rePowerIn, text)
	energyConsumedMW := extractMetric(reEnergyConsumed, text)
	sysLoadMW := extractMetric(reLoad, text)
	adapterWatts := extractMetric(reWatts, text)
	amperageRaw := extractMetric(reAmperage, text)
	voltageMV := extractMetric(reVoltage, text)
	isCharging := extractBool(reIsCharging, text)
	externalConnected := extractBool(reExternalConn, text)

	// Adapter power in Watts
	var adapterPowerWatts float64
	if externalConnected {
		if sysPowerMW > 0 {
			adapterPowerWatts = sysPowerMW / 1000.0
		} else {
			vIn := extractMetric(reVoltageIn, text)
			cIn := extractMetric(reCurrentIn, text)
			adapterPowerWatts = (vIn * cIn) / 1000000.0
		}
	}

	// Calculate battery charge/discharge watts
	var batteryChargeWatts float64
	var amperageMA float64
	if amperageRaw > 0x7FFFFFFFFFFFFFFF {
		amperageMA = float64(int64(uint64(amperageRaw)))
	} else if amperageRaw > 0x7FFFFFFF {
		amperageMA = float64(int32(uint32(amperageRaw)))
	} else {
		amperageMA = amperageRaw
	}

	if voltageMV > 0 && amperageMA != 0 {
		batteryChargeWatts = (amperageMA * voltageMV) / 1000000.0
	}

	// System load (actual machine power consumed)
	var systemLoadWatts float64
	if energyConsumedMW > 0 {
		systemLoadWatts = energyConsumedMW / 1000.0
	} else if sysLoadMW > 0 {
		systemLoadWatts = sysLoadMW / 1000.0
	} else if !externalConnected && batteryChargeWatts < 0 {
		systemLoadWatts = -batteryChargeWatts
	}

	var isChargingVal float64
	if isCharging {
		isChargingVal = 1
	}

	batteryTempRaw := extractMetric(reBatteryTemp, text)
	virtualTempRaw := extractMetric(reVirtualTemp, text)
	batteryTempCelsius := batteryTempRaw / 100.0
	virtualTempCelsius := virtualTempRaw / 100.0

	content := fmt.Sprintf(`# HELP macos_adapter_power_watts System power drawn from adapter in Watts
# TYPE macos_adapter_power_watts gauge
macos_adapter_power_watts %.3f

# HELP macos_system_load_watts Total system load in Watts
# TYPE macos_system_load_watts gauge
macos_system_load_watts %.3f

# HELP macos_battery_charge_watts Battery charging power in Watts (positive=charging, negative=discharging)
# TYPE macos_battery_charge_watts gauge
macos_battery_charge_watts %.3f

# HELP macos_adapter_max_watts Negotiated adapter maximum capacity in Watts
# TYPE macos_adapter_max_watts gauge
macos_adapter_max_watts %.0f

# HELP macos_battery_charging Whether battery is currently charging (1=yes, 0=no)
# TYPE macos_battery_charging gauge
macos_battery_charging %.0f

# HELP macos_battery_temperature_celsius Battery temperature in degrees Celsius
# TYPE macos_battery_temperature_celsius gauge
macos_battery_temperature_celsius %.2f

# HELP macos_virtual_temperature_celsius PMU virtual enclosure temperature in degrees Celsius
# TYPE macos_virtual_temperature_celsius gauge
macos_virtual_temperature_celsius %.2f
`, adapterPowerWatts, systemLoadWatts, batteryChargeWatts, adapterWatts, isChargingVal, batteryTempCelsius, virtualTempCelsius)

	// Fetch macmon metrics if available
	if macmon := collectMacmon(); macmon != nil {
		content += fmt.Sprintf(`
# HELP macos_cpu_temp_celsius Average CPU temperature in Celsius
# TYPE macos_cpu_temp_celsius gauge
macos_cpu_temp_celsius %.2f

# HELP macos_gpu_temp_celsius Average GPU temperature in Celsius
# TYPE macos_gpu_temp_celsius gauge
macos_gpu_temp_celsius %.2f

# HELP macos_cpu_power_watts CPU core power consumption in Watts
# TYPE macos_cpu_power_watts gauge
macos_cpu_power_watts %.3f

# HELP macos_gpu_power_watts GPU power consumption in Watts
# TYPE macos_gpu_power_watts gauge
macos_gpu_power_watts %.3f

# HELP macos_ane_power_watts Apple Neural Engine power consumption in Watts
# TYPE macos_ane_power_watts gauge
macos_ane_power_watts %.3f

# HELP macos_ram_power_watts Unified RAM power consumption in Watts
# TYPE macos_ram_power_watts gauge
macos_ram_power_watts %.3f

# HELP macos_gpu_active_ratio GPU utilization active ratio (0.0 to 1.0)
# TYPE macos_gpu_active_ratio gauge
macos_gpu_active_ratio %.4f

# HELP macos_ecpu_freq_mhz Efficiency CPU cluster frequency in MHz
# TYPE macos_ecpu_freq_mhz gauge
macos_ecpu_freq_mhz %.0f

# HELP macos_pcpu_freq_mhz Performance CPU cluster frequency in MHz
# TYPE macos_pcpu_freq_mhz gauge
macos_pcpu_freq_mhz %.0f

# HELP macos_gpu_freq_mhz GPU frequency in MHz
# TYPE macos_gpu_freq_mhz gauge
macos_gpu_freq_mhz %.0f
`,
			macmon.Temp.CPUTempAvg,
			macmon.Temp.GPUTempAvg,
			macmon.CPUPower,
			macmon.GPUPower,
			macmon.ANEPower,
			macmon.RAMPower,
			macmon.GPUActiveRatio,
			macmon.ECPUFreqMHz,
			macmon.PCPUFreqMHz,
			macmon.GPUFreqMHz,
		)

		var maxFan float64
		for _, fan := range macmon.Fans {
			if fan.RPM > maxFan {
				maxFan = fan.RPM
			}
		}
		content += fmt.Sprintf(`
# HELP macos_fan_speed_rpm Maximum fan speed in RPM
# TYPE macos_fan_speed_rpm gauge
macos_fan_speed_rpm %.0f
`, maxFan)
	}

	// Atomic write using a temp file in the same directory
	tmpFile := filepath.Join(outDir, fmt.Sprintf("macos_power.prom.tmp-%d", os.Getpid()))
	targetFile := filepath.Join(outDir, "macos_power.prom")

	if err := os.WriteFile(tmpFile, []byte(content), 0644); err != nil {
		return fmt.Errorf("write temp file error: %w", err)
	}

	return os.Rename(tmpFile, targetFile)
}

func main() {
	intervalFlag := flag.Duration("interval", 10*time.Second, "Collection interval (e.g. 10s, 30s, 1m)")
	outDirFlag := flag.String("out-dir", "/var/log/node_exporter/textfile", "Directory for node_exporter textfiles")
	flag.Parse()

	if err := os.MkdirAll(*outDirFlag, 0755); err != nil {
		log.Fatalf("Failed to create target directory: %v", err)
	}

	log.Printf("Starting macOS power collector (Interval: %v, OutDir: %s)", *intervalFlag, *outDirFlag)

	// Collect once immediately on launch
	if err := collectAndWrite(*outDirFlag); err != nil {
		log.Printf("Collection error: %v", err)
	}

	ticker := time.NewTicker(*intervalFlag)
	defer ticker.Stop()

	for range ticker.C {
		if err := collectAndWrite(*outDirFlag); err != nil {
			log.Printf("Collection error: %v", err)
		}
	}
}