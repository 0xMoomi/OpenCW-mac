package core

import (
	"regexp"
	"strings"
)

// tunInterfaceName names the Windows adapter OpenCW creates.
const tunInterfaceName = "OpenCW"

// NexonProcesses are the Nexon launcher and runtime executables. Except on
// ports 80 and 853, their traffic uses the relay.
var NexonProcesses = []string{
	"nexon_client.exe", "nexon_runtime.exe", "nexon_updater.exe", "nxsteam.exe", "nexon_launcher.exe",
}

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
