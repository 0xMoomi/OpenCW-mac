package tunnel

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"opencw/internal/core"
)

// TestDarwinConfig checks that the macOS rules select only the MapleStory
// client's Wine processes and that sing-box accepts the whole configuration,
// including the TUN inbound, without starting it.
func TestDarwinConfig(t *testing.T) {
	server := core.Server{IP: "203.0.113.10", Proxy: &core.Proxy{
		Type: "shadowsocks", Server: "203.0.113.10", Port: 8388,
		Method: "aes-128-gcm", Password: "test",
	}}
	data, err := core.Config(server, true, true)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Inbounds []map[string]any `json:"inbounds"`
		Route    struct {
			Rules []struct {
				ProcessPathRegex []string `json:"process_path_regex"`
			} `json:"rules"`
		} `json:"route"`
	}
	if err = json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Inbounds) != 1 || config.Inbounds[0]["interface_name"] != nil {
		t.Fatalf("macOS TUN must let sing-box pick a utunN name: %v", config.Inbounds)
	}

	game := []string{
		"/Applications/MapleStory Launcher.app/Contents/SharedSupport/maplestoryna/MapleStory Launcher/wineloader",
		"/Applications/MapleStory Launcher.app/Contents/SharedSupport/maplestoryna/MapleStory Classic World.app/Contents/MacOS/MapleStory Classic World",
	}
	other := []string{
		"/Applications/Safari.app/Contents/MacOS/Safari",
		"/Applications/MapleStory Launcher.app/Contents/MacOS/MapleStory Launcher",
		"/Applications/CrossOver.app/Contents/SharedSupport/CrossOver/bin/wineloader",
	}
	// sing-box matches process_path_regex with regexp.Compile and MatchString.
	for i, rule := range config.Route.Rules {
		if len(rule.ProcessPathRegex) == 0 {
			t.Fatalf("rule %d has no process condition", i)
		}
		for _, expr := range rule.ProcessPathRegex {
			re := regexp.MustCompile(expr)
			for _, path := range game {
				if !re.MatchString(path) {
					t.Errorf("rule %d does not match %q", i, path)
				}
			}
			for _, path := range other {
				if re.MatchString(path) {
					t.Errorf("rule %d matches %q", i, path)
				}
			}
		}
	}

	b, err := newBox(context.Background(), data)
	if err != nil {
		t.Fatal("sing-box rejected the configuration: ", err)
	}
	defer b.Close()
	if !b.Router().NeedFindProcess() {
		t.Error("sing-box will not look up connection owners")
	}
	if _, ok := b.Inbound().Get(tunTag); !ok {
		t.Error("missing TUN inbound")
	}
}
