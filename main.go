package main

import (
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
	// In macOS, Amperage is signed int64, but represented as uint64 when negative in some ioreg fields
	// If amperage > 0x7FFFFFFF, it is negative (discharging)
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
	// SystemEnergyConsumed in PowerTelemetryData reflects true instantaneous consumption
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
`, adapterPowerWatts, systemLoadWatts, batteryChargeWatts, adapterWatts, isChargingVal)

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