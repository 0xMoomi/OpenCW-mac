package core

// tunInterfaceName is empty on macOS: utun interfaces must be named utunN, so
// sing-box picks the next free one.
const tunInterfaceName = ""

// mapleProcesses matches every process of the official MapleStory Mac client.
// It is a CrossOver (Wine) wrapper: MapleStory.exe and the Nexon launcher and
// runtime all run as Wine host processes whose executable (wineloader, or the
// per-app stub such as "MapleStory Classic World.app/Contents/MacOS/...") lies
// in the bundle's SharedSupport folder, so macOS cannot tell them apart. That
// Wine copy runs only Nexon software, so other apps are unaffected.
var mapleProcesses = []string{`(?i)/MapleStory Launcher\.app/Contents/SharedSupport/`}

// Rules returns the engine's process routing rules, evaluated in order.
// Anything unmatched goes direct.
//
// It merges the Windows rules for the Nexon processes (relay everything but
// ports 80 and 853) and MapleStory.exe (relay its game, high and 443 ports,
// and the login hosts on any port). Their union relays every port except 80
// and 853, so launcher downloads and DNS-over-TLS stay direct; port 80 is
// sniffed first so the login hosts still use the relay there.
func Rules() []map[string]any {
	maple := mapleProcesses
	return []map[string]any{
		{
			"process_path_regex": maple, "network": "tcp", "port": 80,
			"action": "sniff", "sniffer": []string{"http"}, "timeout": "300ms",
		},
		{"process_path_regex": maple, "domain": LoginDomains, "action": "route", "outbound": relayTag},
		{"process_path_regex": maple, "port": []int{80, 853}, "action": "route", "outbound": directTag},
		{"process_path_regex": maple, "action": "route", "outbound": relayTag},
	}
}
