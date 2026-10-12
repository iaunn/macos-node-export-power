# macOS Power Exporter

A lightweight macOS power and battery telemetry collector for Prometheus & `node_exporter`. It extracts real-time power metrics directly from Apple Silicon / macOS PMU via `AppleSmartBattery` IORegistry and writes them out in Prometheus exposition format.

---

## Metrics Collected

| Metric | Type | Description |
|---|---|---|
| `macos_adapter_power_watts` | Gauge | Real-time power drawn from AC/USB-PD adapter in Watts |
| `macos_system_load_watts` | Gauge | Instantaneous system power consumption by the Mac hardware in Watts |
| `macos_battery_charge_watts` | Gauge | Battery charging power in Watts (`+` charging, `-` discharging) |
| `macos_adapter_max_watts` | Gauge | Negotiated adapter maximum capacity rating (e.g., 60W, 96W, 140W) |
| `macos_battery_charging` | Gauge | `1` if battery is actively charging, `0` otherwise |

---

## 1. Build the Collector

```bash
cd export-power
go build -o macos-power-collector main.go
```

---

## 2. Running `node_exporter` with Textfile Collector

`node_exporter` needs the `--collector.textfile.directory` flag pointed to the folder where `macos-power-collector` outputs its `.prom` files (e.g. `./textfile` or `/opt/node_exporter/textfile`).

### Step 2.1: Prepare Output Directory
```bash
mkdir -p ./textfile
```

### Step 2.2: Run `node_exporter`

#### Option A: Direct Command Line / Testing
```bash
node_exporter --collector.textfile.directory=$(pwd)/textfile
```
*(By default, `node_exporter` listens on `http://localhost:9100/metrics`)*

#### Option B: If Installed via Homebrew (`brew services`)
If you run `node_exporter` via Homebrew, edit its LaunchAgent plist:
```bash
# Path to Homebrew's node_exporter plist
~/Library/LaunchAgents/homebrew.mxcl.node_exporter.plist
```
Add the argument under `<array>`:
```xml
<string>--collector.textfile.directory=/path/to/export-power/textfile</string>
```
Then restart the service:
```bash
brew services restart node_exporter
```

---

## 3. Running `macos-power-collector`

### Option A: Run via PM2 (Recommended)
You can run `macos-power-collector` continuously in the background using PM2:

```bash
pm2 start ./macos-power-collector \
  --name "macos-power" \
  -- \
  -interval=10s \
  -out-dir=./textfile
```

Manage with:
```bash
pm2 logs macos-power
pm2 restart macos-power
pm2 stop macos-power
```

### Option B: Run via macOS `launchd` (Auto-start on Boot)
Create `~/Library/LaunchAgents/com.user.macos-power-collector.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.user.macos-power-collector</string>
    <key>ProgramArguments</key>
    <array>
        <string>/path/to/export-power/macos-power-collector</string>
        <string>-interval=10s</string>
        <string>-out-dir=/path/to/export-power/textfile</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
```

Load and start it:
```bash
launchctl load ~/Library/LaunchAgents/com.user.macos-power-collector.plist
```

---

## 4. Verify Metrics

Check `node_exporter` output to verify metrics are being served:
```bash
curl -s http://localhost:9100/metrics | grep macos_
```

Example response:
```promql
# HELP macos_adapter_max_watts Negotiated adapter maximum capacity in Watts
# TYPE macos_adapter_max_watts gauge
macos_adapter_max_watts 60
# HELP macos_adapter_power_watts System power drawn from adapter in Watts
# TYPE macos_adapter_power_watts gauge
macos_adapter_power_watts 57.474
# HELP macos_battery_charge_watts Battery charging power in Watts (positive=charging, negative=discharging)
# TYPE macos_battery_charge_watts gauge
macos_battery_charge_watts 33.982
# HELP macos_battery_charging Whether battery is currently charging (1=yes, 0=no)
# TYPE macos_battery_charging gauge
macos_battery_charging 1
# HELP macos_system_load_watts Total system load in Watts
# TYPE macos_system_load_watts gauge
macos_system_load_watts 15.965
```

---

## 5. Grafana Dashboard

Import [grafana/dashboard.json](grafana/dashboard.json) into your Grafana instance. It includes the **macOS Power & Energy Consumption** row with real-time stat gauges and a full-width time series graph.
