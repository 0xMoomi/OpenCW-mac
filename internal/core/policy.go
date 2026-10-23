package core

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// NexonProcesses are the Nexon launcher and runtime executables. Except on
// ports 80 and 853, their traffic uses the relay.
var NexonProcesses = []string{
	"nexon_client.exe", "nexon_runtime.exe", "nexon_updater.exe", "nxsteam.exe", "nexon_launcher.exe",
}

// LoginDomains are MapleStory login hosts, matched by sniffed domain so they
// use the relay on any port.
var LoginDomains = []string{"config.na.nexon.com", "login.maplestory.nexon.net"}

// Engine tags and TUN settings. The tunnel package looks up the "vpn" outbound
// and the "apps" inbound by tag.
const (
	relayTag            = "vpn"
	directTag           = "direct"
	tunIPv4             = "172.31.255.1/30"
	tunIPv6             = "fdfe:dcba:9876::1/126"
	relayConnectTimeout = "4s"
)

// process matches any of names as the final path component, case-insensitively
// as Windows does.
func process(names ...string) []string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = regexp.QuoteMeta(n)
	}
	return []string{`(?i)(^|[\\/])(` + strings.Join(parts, "|") + `)$`}
}

// Rules returns the engine's process routing rules, evaluated in order.
// Anything unmatched goes direct.
func Rules() []map[string]any {
	nexon := process(NexonProcesses...)
	maple := process("MapleStory.exe")
	return []map[string]any{
		{"process_path_regex": nexon, "port": []int{80, 853}, "action": "route", "outbound": directTag},
		{"process_path_regex": nexon, "action": "route", "outbound": relayTag},
		{"process_path_regex": maple, "port_range": []string{"8500:8599"}, "action": "route", "outbound": relayTag},
		{
			"process_path_regex": maple, "network": "tcp", "port_range": []string{"1024:65535"},
			"action": "route", "outbound": relayTag,
		},
		{
			"process_path_regex": process("MapleStory.exe", "ms-launcher.exe"), "port": 443,
			"action": "route", "outbound": relayTag,
		},
		{
			"process_path_regex": maple, "network": "tcp",
			"action": "sniff", "sniffer": []string{"http", "tls"}, "timeout": "300ms",
		},
		{"process_path_regex": maple, "domain": LoginDomains, "action": "route", "outbound": relayTag},
	}
}

// Config builds the engine configuration for one relay. ipv6 adds an IPv6
// address (and so an IPv6 default route) to the TUN; pass NativeIPv6() so a
// PC without IPv6 internet keeps behaving as if OpenCW were not there.
func Config(s Server, enableTun, ipv6 bool) ([]byte, error) {
	if s.Proxy == nil {
		return nil, errors.New("server has no relay settings")
	}
	data, err := json.Marshal(s.Proxy)
	if err != nil {
		return nil, err
	}
	var outbound map[string]any
	if err = json.Unmarshal(data, &outbound); err != nil {
		return nil, err
	}
	outbound["tag"] = relayTag
	outbound["connect_timeout"] = relayConnectTimeout
	config := map[string]any{
		"log": map[string]any{"disabled": true},
		"dns": map[string]any{
			"servers": []map[string]any{{"type": "local", "tag": "local"}},
			"final":   "local",
		},
		"outbounds": []map[string]any{{"type": "direct", "tag": directTag}, outbound},
		"route": map[string]any{
			"rules":                   Rules(),
			"final":                   directTag,
			"auto_detect_interface":   true,
			"default_domain_resolver": "local",
		},
	}
	if enableTun {
		addresses := []string{tunIPv4}
		if ipv6 {
			addresses = append(addresses, tunIPv6)
		}
		// With the "system" stack the OS terminates both TCP and UDP flows from
		// the TUN; no userspace TCP/IP stack is involved.
		in := map[string]any{
			"type":           "tun",
			"tag":            "apps",
			"interface_name": "OpenCW",
			"address":        addresses,
			"mtu":            1400,
			"auto_route":     true,
			"strict_route":   false,
			"stack":          "system",
		}
		if PublicIPv4(s.IP) {
			in["route_exclude_address"] = []string{s.IP + "/32"}
		}
		config["inbounds"] = []map[string]any{in}
	}
	return json.Marshal(config)
}
