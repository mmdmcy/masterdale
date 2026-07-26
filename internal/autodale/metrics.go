package autodale

import (
	"bufio"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type MetricsSample struct {
	Timestamp      string        `json:"timestamp"`
	HostName       string        `json:"hostname"`
	CPUPercent     float64       `json:"cpu_percent"`
	LoadAverage    string        `json:"load_average,omitempty"`
	MemoryTotalMB  uint64        `json:"memory_total_mb,omitempty"`
	MemoryFreeMB   uint64        `json:"memory_free_mb,omitempty"`
	DiskTotalGB    float64       `json:"disk_total_gb,omitempty"`
	DiskFreeGB     float64       `json:"disk_free_gb,omitempty"`
	BatteryPercent float64       `json:"battery_percent,omitempty"`
	PowerWatts     float64       `json:"power_watts,omitempty"`
	PowerSource    string        `json:"power_source"`
	EnergySource   string        `json:"energy_source"`
	EstimatedWatts float64       `json:"estimated_watts,omitempty"`
	Network        NetworkSample `json:"network"`
}

type NetworkSample struct {
	Timestamp        string  `json:"timestamp"`
	Interface        string  `json:"interface,omitempty"`
	Kind             string  `json:"kind,omitempty"`
	Source           string  `json:"source"`
	RXBytes          uint64  `json:"rx_bytes,omitempty"`
	TXBytes          uint64  `json:"tx_bytes,omitempty"`
	RXBytesPerSecond float64 `json:"rx_bytes_per_second,omitempty"`
	TXBytesPerSecond float64 `json:"tx_bytes_per_second,omitempty"`
	WiFiSignalDBm    float64 `json:"wifi_signal_dbm,omitempty"`
	WiFiQuality      float64 `json:"wifi_quality_percent,omitempty"`
	SampleSeconds    float64 `json:"sample_seconds,omitempty"`
}

type EnergyReport struct {
	Day             string  `json:"day"`
	Samples         int     `json:"samples"`
	EnergyKWh       float64 `json:"energy_kwh"`
	CostPerKWh      float64 `json:"cost_per_kwh"`
	EstimatedCost   float64 `json:"estimated_cost"`
	EnergySource    string  `json:"energy_source"`
	FirstSampleTime string  `json:"first_sample_time,omitempty"`
	LastSampleTime  string  `json:"last_sample_time,omitempty"`
}

func SampleMetrics(idleWatts, maxWatts float64) MetricsSample {
	host, _ := os.Hostname()
	cpu := sampleCPUPercent()
	mem := readMeminfo()
	sample := MetricsSample{
		Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
		HostName:     host,
		CPUPercent:   round(cpu, 2),
		PowerSource:  "unknown",
		EnergySource: "estimated",
	}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		sample.LoadAverage = strings.TrimSpace(string(b))
	}
	if len(mem) > 0 {
		sample.MemoryTotalMB = mem["MemTotal"] / 1024
		sample.MemoryFreeMB = mem["MemAvailable"] / 1024
	}
	if usage, ok := rootDiskUsage(); ok {
		sample.DiskTotalGB = round(float64(usage.totalBytes)/(1024*1024*1024), 2)
		sample.DiskFreeGB = round(float64(usage.freeBytes)/(1024*1024*1024), 2)
	}
	enrichBattery(&sample)
	sample.Network = SampleNetwork(250 * time.Millisecond)
	if sample.PowerWatts <= 0 {
		if idleWatts <= 0 {
			idleWatts = 8
		}
		if maxWatts <= idleWatts {
			maxWatts = 35
		}
		sample.EstimatedWatts = round(idleWatts+(maxWatts-idleWatts)*(sample.CPUPercent/100), 2)
	} else {
		sample.EnergySource = "battery_sensor"
	}
	return sample
}

type networkCounters struct {
	rxBytes uint64
	txBytes uint64
}

func SampleNetwork(interval time.Duration) NetworkSample {
	sample := NetworkSample{
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Source:    "unavailable",
	}
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	iface := defaultNetworkInterface()
	if iface == "" {
		return sample
	}
	before, ok := readNetworkCounters(iface)
	if !ok {
		return sample
	}
	started := time.Now()
	time.Sleep(interval)
	after, ok := readNetworkCounters(iface)
	if !ok {
		return sample
	}
	elapsed := time.Since(started).Seconds()
	sample = networkSampleFromCounters(iface, before, after, elapsed)
	if signal, quality, ok := wirelessSignal(iface); ok {
		sample.Kind = "wifi"
		sample.WiFiSignalDBm = signal
		sample.WiFiQuality = quality
	}
	return sample
}

func networkSampleFromCounters(
	iface string,
	before networkCounters,
	after networkCounters,
	elapsed float64,
) NetworkSample {
	sample := NetworkSample{
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Interface:     iface,
		Kind:          "network",
		Source:        "default_route_sysfs",
		RXBytes:       after.rxBytes,
		TXBytes:       after.txBytes,
		SampleSeconds: round(elapsed, 3),
	}
	if elapsed <= 0 || after.rxBytes < before.rxBytes || after.txBytes < before.txBytes {
		return sample
	}
	sample.RXBytesPerSecond = round(float64(after.rxBytes-before.rxBytes)/elapsed, 2)
	sample.TXBytesPerSecond = round(float64(after.txBytes-before.txBytes)/elapsed, 2)
	return sample
}

func defaultNetworkInterface() string {
	file, err := os.Open("/proc/net/route")
	if err != nil {
		return ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 || fields[0] == "Iface" || fields[1] != "00000000" {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 64)
		if err == nil && flags&1 == 1 && fields[0] != "lo" {
			return fields[0]
		}
	}
	return ""
}

func readNetworkCounters(iface string) (networkCounters, bool) {
	base := filepath.Join("/sys/class/net", iface, "statistics")
	rx, errRX := strconv.ParseUint(strings.TrimSpace(readSmall(filepath.Join(base, "rx_bytes"))), 10, 64)
	tx, errTX := strconv.ParseUint(strings.TrimSpace(readSmall(filepath.Join(base, "tx_bytes"))), 10, 64)
	if errRX != nil || errTX != nil {
		return networkCounters{}, false
	}
	return networkCounters{rxBytes: rx, txBytes: tx}, true
}

func wirelessSignal(iface string) (float64, float64, bool) {
	file, err := os.Open("/proc/net/wireless")
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		name, values, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) != iface {
			continue
		}
		fields := strings.Fields(values)
		if len(fields) < 4 {
			return 0, 0, false
		}
		link := parseFloat(strings.TrimSuffix(fields[1], "."))
		level := parseFloat(strings.TrimSuffix(fields[2], "."))
		return level, round(math.Max(0, math.Min(100, link/70*100)), 1), true
	}
	return 0, 0, false
}

func AppendMetric(sink Sink, sample MetricsSample) error {
	if err := os.MkdirAll(sink.DataDir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(sink.DataDir, "metrics.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(sample)
	_, err = f.Write(append(b, '\n'))
	return err
}

func ReadMetrics(sink Sink) ([]MetricsSample, error) {
	f, err := os.Open(filepath.Join(sink.DataDir, "metrics.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var samples []MetricsSample
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var sample MetricsSample
		if err := json.Unmarshal(scanner.Bytes(), &sample); err == nil {
			samples = append(samples, sample)
		}
	}
	return samples, scanner.Err()
}

func DailyEnergyReport(samples []MetricsSample, day string, costPerKWh float64) EnergyReport {
	if day == "" {
		day = time.Now().Format("2006-01-02")
	}
	report := EnergyReport{Day: day, CostPerKWh: costPerKWh, EnergySource: "estimated"}
	var previous *MetricsSample
	for i := range samples {
		sample := samples[i]
		sampleTime, err := time.Parse(time.RFC3339Nano, sample.Timestamp)
		if err != nil || sampleTime.Local().Format("2006-01-02") != day {
			continue
		}
		report.Samples++
		if report.FirstSampleTime == "" {
			report.FirstSampleTime = sample.Timestamp
		}
		report.LastSampleTime = sample.Timestamp
		if previous != nil {
			prevTime, err1 := time.Parse(time.RFC3339Nano, previous.Timestamp)
			if err1 == nil {
				hours := sampleTime.Sub(prevTime).Hours()
				if hours > 0 && hours < 2 {
					watts := sample.PowerWatts
					if watts <= 0 {
						watts = sample.EstimatedWatts
					} else {
						report.EnergySource = "battery_sensor"
					}
					report.EnergyKWh += (watts * hours) / 1000
				}
			}
		}
		previous = &samples[i]
	}
	report.EnergyKWh = round(report.EnergyKWh, 4)
	report.EstimatedCost = round(report.EnergyKWh*costPerKWh, 4)
	return report
}

func sampleCPUPercent() float64 {
	a, ok := readCPUStat()
	if !ok {
		return 0
	}
	time.Sleep(250 * time.Millisecond)
	b, ok := readCPUStat()
	if !ok {
		return 0
	}
	idleDelta := float64((b.idle + b.iowait) - (a.idle + a.iowait))
	totalDelta := float64(b.total - a.total)
	if totalDelta <= 0 {
		return 0
	}
	return math.Max(0, math.Min(100, 100*(1-idleDelta/totalDelta)))
}

type cpuStat struct {
	idle   uint64
	iowait uint64
	total  uint64
}

func readCPUStat() (cpuStat, bool) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuStat{}, false
	}
	line := strings.SplitN(string(b), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 8 || fields[0] != "cpu" {
		return cpuStat{}, false
	}
	var values []uint64
	for _, field := range fields[1:] {
		value, _ := strconv.ParseUint(field, 10, 64)
		values = append(values, value)
	}
	var total uint64
	for _, value := range values {
		total += value
	}
	return cpuStat{idle: values[3], iowait: values[4], total: total}, true
}

func enrichBattery(sample *MetricsSample) {
	entries, err := os.ReadDir("/sys/class/power_supply")
	if err != nil {
		return
	}
	for _, entry := range entries {
		path := filepath.Join("/sys/class/power_supply", entry.Name())
		typ := strings.TrimSpace(readSmall(filepath.Join(path, "type")))
		if typ == "Mains" || typ == "AC" {
			online := strings.TrimSpace(readSmall(filepath.Join(path, "online")))
			if online == "1" {
				sample.PowerSource = "AC"
			}
		}
		if typ != "Battery" {
			continue
		}
		if capacity := parseFloat(readSmall(filepath.Join(path, "capacity"))); capacity > 0 {
			sample.BatteryPercent = capacity
		}
		if power := parseMicro(readSmall(filepath.Join(path, "power_now"))); power > 0 {
			sample.PowerWatts = round(power, 2)
		} else {
			current := parseMicro(readSmall(filepath.Join(path, "current_now")))
			voltage := parseMicro(readSmall(filepath.Join(path, "voltage_now")))
			if current > 0 && voltage > 0 {
				sample.PowerWatts = round(current*voltage, 2)
			}
		}
		status := strings.TrimSpace(readSmall(filepath.Join(path, "status")))
		if status != "" && sample.PowerSource == "unknown" {
			sample.PowerSource = status
		}
	}
}

func readSmall(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return v
}

func parseMicro(s string) float64 {
	v := parseFloat(s)
	if v <= 0 {
		return 0
	}
	return v / 1000000
}

func round(v float64, places int) float64 {
	scale := math.Pow10(places)
	return math.Round(v*scale) / scale
}
