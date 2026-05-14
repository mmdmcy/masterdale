package dale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type FleetDevice struct {
	HostName string   `json:"hostname"`
	DNSName  string   `json:"dns_name,omitempty"`
	OS       string   `json:"os"`
	Online   bool     `json:"online"`
	Active   bool     `json:"active"`
	IPs      []string `json:"ips"`
}

type FleetProbe struct {
	Device FleetDevice `json:"device"`
	URL    string      `json:"url"`
	OK     bool        `json:"ok"`
	Error  string      `json:"error,omitempty"`
}

func TailscaleDevices(ctx context.Context) ([]FleetDevice, error) {
	if _, err := exec.LookPath("tailscale"); err != nil {
		return nil, errors.New("tailscale command not found")
	}
	cctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "tailscale", "status", "--json").Output()
	if err != nil {
		return nil, err
	}
	var status struct {
		Self struct {
			HostName string   `json:"HostName"`
			DNSName  string   `json:"DNSName"`
			OS       string   `json:"OS"`
			Online   bool     `json:"Online"`
			Active   bool     `json:"Active"`
			IPs      []string `json:"TailscaleIPs"`
		} `json:"Self"`
		Peer map[string]struct {
			HostName string   `json:"HostName"`
			DNSName  string   `json:"DNSName"`
			OS       string   `json:"OS"`
			Online   bool     `json:"Online"`
			Active   bool     `json:"Active"`
			IPs      []string `json:"TailscaleIPs"`
		} `json:"Peer"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return nil, err
	}
	devices := []FleetDevice{{
		HostName: status.Self.HostName,
		DNSName:  status.Self.DNSName,
		OS:       status.Self.OS,
		Online:   status.Self.Online,
		Active:   status.Self.Active,
		IPs:      status.Self.IPs,
	}}
	for _, peer := range status.Peer {
		devices = append(devices, FleetDevice{
			HostName: peer.HostName,
			DNSName:  peer.DNSName,
			OS:       peer.OS,
			Online:   peer.Online,
			Active:   peer.Active,
			IPs:      peer.IPs,
		})
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].HostName < devices[j].HostName
	})
	return devices, nil
}

func ProbeFleet(ctx context.Context, port int) ([]FleetProbe, error) {
	devices, err := TailscaleDevices(ctx)
	if err != nil {
		return nil, err
	}
	if port == 0 {
		port = 7345
	}
	client := http.Client{Timeout: 2 * time.Second}
	var probes []FleetProbe
	for _, device := range devices {
		ip := firstIPv4(device.IPs)
		if ip == "" {
			ip = firstIPAny(device.IPs)
		}
		if ip == "" {
			probes = append(probes, FleetProbe{Device: device, Error: "no tailscale IP"})
			continue
		}
		url := "http://" + net.JoinHostPort(ip, strconv.Itoa(port)) + "/healthz"
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		probe := FleetProbe{Device: device, URL: url}
		if err != nil {
			probe.Error = err.Error()
		} else {
			probe.OK = resp.StatusCode < 300
			if !probe.OK {
				probe.Error = fmt.Sprintf("status %d", resp.StatusCode)
			}
			_ = resp.Body.Close()
		}
		probes = append(probes, probe)
	}
	return probes, nil
}

func FindFleetDevice(ctx context.Context, name string) (FleetDevice, error) {
	devices, err := TailscaleDevices(ctx)
	if err != nil {
		return FleetDevice{}, err
	}
	var matches []FleetDevice
	for _, device := range devices {
		if name == "" || device.HostName == name || device.DNSName == name {
			matches = append(matches, device)
			continue
		}
		if equalFold(device.HostName, name) || equalFold(device.DNSName, name) {
			matches = append(matches, device)
		}
	}
	if len(matches) == 0 {
		return FleetDevice{}, fmt.Errorf("device %q not found in tailscale status", name)
	}
	if len(matches) > 1 {
		return FleetDevice{}, fmt.Errorf("multiple devices match %q", name)
	}
	return matches[0], nil
}

func ProbeDevice(ctx context.Context, device FleetDevice, port int) FleetProbe {
	if port == 0 {
		port = 7345
	}
	ip := firstIPv4(device.IPs)
	if ip == "" {
		ip = firstIPAny(device.IPs)
	}
	probe := FleetProbe{Device: device}
	if ip == "" {
		probe.Error = "no tailscale IP"
		return probe
	}
	probe.URL = "http://" + net.JoinHostPort(ip, strconv.Itoa(port)) + "/healthz"
	client := http.Client{Timeout: 2 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, probe.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	defer resp.Body.Close()
	probe.OK = resp.StatusCode < 300
	if !probe.OK {
		probe.Error = fmt.Sprintf("status %d", resp.StatusCode)
	}
	return probe
}

func firstIPv4(ips []string) string {
	for _, raw := range ips {
		ip := net.ParseIP(raw)
		if ip != nil && ip.To4() != nil {
			return raw
		}
	}
	return ""
}

func firstIPAny(ips []string) string {
	if len(ips) == 0 {
		return ""
	}
	return ips[0]
}

func equalFold(a string, b string) bool {
	if b == "" {
		return false
	}
	return len(a) == len(b) && (a == b || strings.EqualFold(a, b))
}
