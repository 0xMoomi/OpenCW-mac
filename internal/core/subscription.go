package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Proxy holds only connection fields. Downloaded routes, DNS, inbounds, files,
// detours, plugins, and scripts can never become application configuration.
type Proxy struct {
	Type           string          `json:"type"`
	Server         string          `json:"server"`
	Port           uint16          `json:"server_port"`
	UUID           string          `json:"uuid,omitempty"`
	Password       string          `json:"password,omitempty"`
	Method         string          `json:"method,omitempty"`
	Flow           string          `json:"flow,omitempty"`
	PacketEncoding *string         `json:"packet_encoding,omitempty"`
	TLS            *ProxyTLS       `json:"tls,omitempty"`
	Transport      *ProxyTransport `json:"transport,omitempty"`
	Obfs           *ProxyObfs      `json:"obfs,omitempty"`
}

// ProxyTLS is the relay's TLS client settings.
type ProxyTLS struct {
	Enabled    bool          `json:"enabled"`
	ServerName string        `json:"server_name,omitempty"`
	Insecure   bool          `json:"insecure,omitempty"`
	ALPN       []string      `json:"alpn,omitempty"`
	UTLS       *ProxyUTLS    `json:"utls,omitempty"`
	Reality    *ProxyReality `json:"reality,omitempty"`
}

// ProxyUTLS selects a uTLS client fingerprint.
type ProxyUTLS struct {
	Enabled     bool   `json:"enabled"`
	Fingerprint string `json:"fingerprint"`
}

// ProxyReality is the VLESS Reality server identity.
type ProxyReality struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"public_key"`
	ShortID   string `json:"short_id,omitempty"`
}

// ProxyTransport is a V2Ray transport; only WebSocket is accepted.
type ProxyTransport struct {
	Type                string            `json:"type"`
	Path                string            `json:"path,omitempty"`
	Headers             map[string]string `json:"headers,omitempty"`
	MaxEarlyData        uint32            `json:"max_early_data,omitempty"`
	EarlyDataHeaderName string            `json:"early_data_header_name,omitempty"`
}

// ProxyObfs is Hysteria2 obfuscation; only Salamander is accepted.
type ProxyObfs struct {
	Type     string `json:"type"`
	Password string `json:"password"`
}

// Feed size and count limits.
const (
	subscriptionLimit    = 2 << 20
	uriListLimit         = 3 << 20
	maxSubscriptionNodes = 5000
)

// Feed sources. The names appear in logs and in the selection tier logic.
const (
	sourceAu1rxx       = "Au1rxx"
	sourceMorpheusadam = "morpheusadam"
	source0xRadikal    = "0xRadikal"

	subscriptionURL = "https://raw.githubusercontent.com/Au1rxx/free-vpn-subscriptions/main/output/by-country/singbox-US.json"
	morpheusadamURL = "https://raw.githubusercontent.com/morpheusadam/v2ray-config/main/subs/bundles/hysteria2.txt"
	radikalURL      = "https://raw.githubusercontent.com/0xRadikal/Free-v2ray-Configs/main/verified/configs_base64.txt"
)

var shadowsocksCiphers = []string{
	"aes-128-gcm", "aes-256-gcm", "chacha20-ietf-poly1305",
	"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305",
}

// utlsFingerprints are the client fingerprint names sing-box accepts.
var utlsFingerprints = []string{
	"", "chrome", "chrome_psk", "chrome_psk_shuffle", "chrome_padding_psk_shuffle", "chrome_pq",
	"chrome_pq_psk", "firefox", "edge", "safari", "360", "qq", "ios", "android", "random", "randomized",
}

var hostPattern = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)

func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return PublicIPv4(host)
	}
	return hostPattern.MatchString(host) &&
		strings.Contains(host, ".") &&
		!strings.HasSuffix(strings.ToLower(host), ".local")
}

// hasControl reports a control character in any text setting. No valid
// setting contains one, and feed text reaches the logs and the screen.
func hasControl(p *Proxy) bool {
	fields := []string{p.Type, p.Server, p.UUID, p.Password, p.Method, p.Flow}
	if p.PacketEncoding != nil {
		fields = append(fields, *p.PacketEncoding)
	}
	if t := p.TLS; t != nil {
		fields = append(fields, t.ServerName)
		fields = append(fields, t.ALPN...)
		if t.UTLS != nil {
			fields = append(fields, t.UTLS.Fingerprint)
		}
		if t.Reality != nil {
			fields = append(fields, t.Reality.PublicKey, t.Reality.ShortID)
		}
	}
	if t := p.Transport; t != nil {
		fields = append(fields, t.Type, t.Path, t.EarlyDataHeaderName)
		for k, v := range t.Headers {
			fields = append(fields, k, v)
		}
	}
	if o := p.Obfs; o != nil {
		fields = append(fields, o.Type, o.Password)
	}
	return slices.ContainsFunc(fields, func(f string) bool { return strings.ContainsFunc(f, unicode.IsControl) })
}

func validateProxy(p *Proxy) error {
	if !validHost(p.Server) || p.Port == 0 {
		return errors.New("invalid public endpoint")
	}
	if hasControl(p) {
		return errors.New("control character in settings")
	}
	switch p.Type {
	case "shadowsocks":
		if !slices.Contains(shadowsocksCiphers, p.Method) || p.Password == "" {
			return errors.New("unsupported Shadowsocks cipher")
		}
	case "vless":
		if p.UUID == "" || (p.Flow != "" && p.Flow != "xtls-rprx-vision") {
			return errors.New("invalid VLESS parameters")
		}
		if p.PacketEncoding != nil && !slices.Contains([]string{"", "xudp", "packetaddr"}, *p.PacketEncoding) {
			return errors.New("invalid UDP encoding")
		}
		if p.TLS == nil || !p.TLS.Enabled {
			return errors.New("VLESS requires authenticated TLS")
		}
	case "hysteria2":
		if p.Password == "" || p.TLS == nil || !p.TLS.Enabled {
			return errors.New("invalid Hysteria2 parameters")
		}
		if p.Obfs != nil && (p.Obfs.Type != "salamander" || p.Obfs.Password == "") {
			return errors.New("unsupported obfuscation")
		}
	default:
		return errors.New("unsupported protocol")
	}
	if p.TLS != nil {
		if p.TLS.Insecure {
			return errors.New("unverified TLS certificate")
		}
		if u := p.TLS.UTLS; u != nil && !slices.Contains(utlsFingerprints, u.Fingerprint) {
			return errors.New("unsupported uTLS fingerprint")
		}
		if r := p.TLS.Reality; r != nil && r.Enabled {
			if r.PublicKey == "" {
				return errors.New("missing Reality public key")
			}
			// sing-box rejects a Reality client without uTLS.
			if p.TLS.UTLS == nil || !p.TLS.UTLS.Enabled {
				p.TLS.UTLS = &ProxyUTLS{Enabled: true, Fingerprint: "chrome"}
			}
		}
	}
	if p.Transport != nil && p.Transport.Type != "ws" {
		return errors.New("unsupported transport")
	}
	return nil
}

func newServer(p *Proxy, source string) Server {
	s := Server{IP: p.Server, Host: p.Server, Source: source, Proxy: p}
	s.Identity = s.Key()
	return s
}

// addUnique appends s unless a server with the same connection settings is
// already in seen.
func (d *Directory) addUnique(seen map[string]bool, s Server) {
	key := s.Key()
	if seen[key] {
		d.Duplicates++
		return
	}
	seen[key] = true
	d.Servers = append(d.Servers, s)
}

// ParseSubscription parses a sing-box JSON subscription whose feed contains
// only US nodes.
func ParseSubscription(data []byte, source string) (Directory, error) {
	var doc struct {
		Outbounds []json.RawMessage `json:"outbounds"`
	}
	var result Directory
	if len(data) > subscriptionLimit {
		return result, errors.New("subscription too large")
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return result, err
	}
	if len(doc.Outbounds) > maxSubscriptionNodes {
		return result, errors.New("too many subscription nodes")
	}
	seen := map[string]bool{}
	for _, raw := range doc.Outbounds {
		var p Proxy
		if json.Unmarshal(raw, &p) != nil || p.Server == "" {
			continue
		}
		if err := validateProxy(&p); err != nil {
			result.Rejected++
			continue
		}
		result.addUnique(seen, newServer(&p, source))
	}
	return result, nil
}

// FetchSubscription downloads and parses the Au1rxx US feed.
func FetchSubscription(ctx context.Context) (Directory, error) {
	data, err := fetchFeed(ctx, subscriptionURL, 3*time.Second, subscriptionLimit)
	if err != nil {
		return Directory{}, err
	}
	return ParseSubscription(data, sourceAu1rxx)
}

// Mixed-country URI lists carry the country only as a label in the fragment.
var usLabel = regexp.MustCompile(`(?i)(^|[^a-z])us([^a-z]|$)|united[ _-]?states|🇺🇸|美国`)

// decodeBase64 accepts every alphabet and padding variant feeds use.
func decodeBase64(s string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if b, e := enc.DecodeString(s); e == nil {
			return b, nil
		}
	}
	return nil, errors.New("invalid base64")
}

// ParseURIList parses a plain or base64-encoded list of share URIs, keeping
// only entries labeled US.
func ParseURIList(data []byte, source string) (Directory, error) {
	var result Directory
	if len(data) > uriListLimit {
		return result, errors.New("subscription too large")
	}
	if !strings.Contains(string(data), "://") {
		b, e := decodeBase64(strings.TrimSpace(string(data)))
		if e != nil {
			return result, e
		}
		data = b
	}
	seen := map[string]bool{}
	nodes := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		u, e := url.Parse(line)
		if e != nil || !usLabel.MatchString(u.Fragment) {
			continue
		}
		if nodes++; nodes > maxSubscriptionNodes {
			return Directory{}, errors.New("too many subscription nodes")
		}
		p, e := parseProxyURI(u)
		if e != nil {
			result.Rejected++
			continue
		}
		result.addUnique(seen, newServer(p, source))
	}
	return result, nil
}

func parseProxyURI(u *url.URL) (*Proxy, error) {
	port, e := strconv.ParseUint(u.Port(), 10, 16)
	if e != nil || u.User == nil {
		return nil, errors.New("invalid proxy URI")
	}
	p := &Proxy{Server: u.Hostname(), Port: uint16(port), Type: u.Scheme}
	q := u.Query()
	// SIP003 plugins are external programs.
	if q.Get("plugin") != "" {
		return nil, errors.New("external proxy plugin")
	}
	switch p.Type {
	case "shadowsocks", "ss":
		p.Type = "shadowsocks"
		p.Method = u.User.Username()
		p.Password, _ = u.User.Password()
		// SIP002 form: base64("method:password") as the user name.
		if p.Password == "" {
			b, err := decodeBase64(p.Method)
			if err != nil {
				return nil, err
			}
			p.Method, p.Password, _ = strings.Cut(string(b), ":")
		}
	case "vless":
		p.UUID = u.User.Username()
		p.Flow = q.Get("flow")
		p.TLS = uriTLS(q)
		security := q.Get("security")
		if security != "tls" && security != "reality" {
			return nil, errors.New("missing TLS")
		}
		if security == "reality" {
			p.TLS.Reality = &ProxyReality{Enabled: true, PublicKey: q.Get("pbk"), ShortID: q.Get("sid")}
		}
		if fp := q.Get("fp"); fp != "" {
			p.TLS.UTLS = &ProxyUTLS{Enabled: true, Fingerprint: fp}
		}
		if typ := q.Get("type"); typ != "" && typ != "tcp" {
			p.Transport = &ProxyTransport{Type: typ, Path: q.Get("path")}
			if h := q.Get("host"); h != "" {
				p.Transport.Headers = map[string]string{"Host": h}
			}
		}
	case "hysteria2", "hy2":
		p.Type = "hysteria2"
		// A "user:pass" userinfo is one password containing a colon.
		p.Password = u.User.Username()
		if pass, ok := u.User.Password(); ok {
			p.Password += ":" + pass
		}
		p.TLS = uriTLS(q)
		if obfs := q.Get("obfs"); obfs != "" {
			p.Obfs = &ProxyObfs{Type: obfs, Password: q.Get("obfs-password")}
		}
	default:
		return nil, errors.New("unsupported protocol")
	}
	if err := validateProxy(p); err != nil {
		return nil, err
	}
	return p, nil
}

// uriTLS reads the share-URI TLS parameters. Insecure flags are recorded only
// so that validateProxy rejects the entry.
func uriTLS(q url.Values) *ProxyTLS {
	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("peer")
	}
	t := &ProxyTLS{Enabled: true, ServerName: sni}
	t.Insecure = q.Get("insecure") == "1" || q.Get("insecure") == "true" || q.Get("allowInsecure") == "1"
	if a := q.Get("alpn"); a != "" {
		t.ALPN = strings.Split(a, ",")
	}
	return t
}

func fetchURIList(ctx context.Context, address, source string) (Directory, error) {
	b, e := fetchFeed(ctx, address, 2*time.Second, uriListLimit)
	if e != nil {
		return Directory{}, e
	}
	return ParseURIList(b, source)
}

// fetchFeed downloads one feed with a hard size cap. It never uses a system
// proxy and refuses redirects, so a feed is only ever read from its own URL.
func fetchFeed(ctx context.Context, address string, connectTimeout time.Duration, limit int64) ([]byte, error) {
	tr := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: connectTimeout}).DialContext,
		TLSHandshakeTimeout: connectTimeout,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("unexpected subscription redirect")
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cache-Control", "no-cache, no-store")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("response too large")
	}
	return data, nil
}

// FetchCatalog fetches all feeds in parallel under one bounded acquisition
// budget. There is no retry chain, and one slow or unavailable source cannot
// discard the other feeds.
func FetchCatalog(parent context.Context) (Directory, error) {
	ctx, cancel := context.WithTimeout(parent, 4*time.Second)
	defer cancel()
	type item struct {
		index int
		name  string
		d     Directory
		err   error
	}
	uriList := func(address, source string) func(context.Context) (Directory, error) {
		return func(ctx context.Context) (Directory, error) { return fetchURIList(ctx, address, source) }
	}
	feeds := []struct {
		name string
		get  func(context.Context) (Directory, error)
	}{
		{sourceAu1rxx, FetchSubscription},
		{sourceMorpheusadam, uriList(morpheusadamURL, sourceMorpheusadam)},
		{source0xRadikal, uriList(radikalURL, source0xRadikal)},
	}
	ch := make(chan item, len(feeds))
	for i, f := range feeds {
		go func() {
			d, e := f.get(ctx)
			ch <- item{i, f.name, d, e}
		}()
	}
	// Keep source precedence stable when duplicate settings arrive in a
	// different order. The network requests themselves remain concurrent.
	items := make([]item, len(feeds))
	for range feeds {
		v := <-ch
		items[v.index] = v
	}
	var result Directory
	seen := map[string]bool{}
	for _, v := range items {
		if v.err != nil {
			result.Sources = append(result.Sources, SourceResult{Name: v.name, Err: v.err})
			continue
		}
		result.Sources = append(result.Sources, SourceResult{Name: v.name, Candidates: len(v.d.Servers)})
		result.Rejected += v.d.Rejected
		result.Duplicates += v.d.Duplicates
		for _, s := range v.d.Servers {
			if s.Source == "" {
				s.Source = v.name
			}
			result.addUnique(seen, s)
		}
	}
	if parent.Err() != nil {
		return result, parent.Err()
	}
	if len(result.Servers) == 0 {
		return result, errors.New("no servers from any source")
	}
	return result, nil
}

// ResolveServer resolves the relay once, rejects non-public destinations, and
// pins the chosen IPv4 for the connection. The subscription's hostname stays
// the TLS server name for certificate validation.
func ResolveServer(ctx context.Context, s Server) (Server, error) {
	if s.Proxy == nil {
		return s, nil
	}
	ip := s.Proxy.Server
	if !PublicIPv4(ip) {
		var err error
		if ip, err = LookupPublicIPv4(ctx, ip); err != nil {
			return s, err
		}
		if ip == "" {
			return s, fmt.Errorf("no public IPv4 for %s", s.Host)
		}
	}
	return s.PinnedTo(ip), nil
}
