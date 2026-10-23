package tunnel

import (
	"context"
	"errors"
	"net"
	"sync"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/certificate"
	"github.com/sagernet/sing-box/adapter/endpoint"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/adapter/outbound"
	boxservice "github.com/sagernet/sing-box/adapter/service"
	"github.com/sagernet/sing-box/dns"
	"github.com/sagernet/sing-box/dns/transport/local"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/protocol/direct"
	"github.com/sagernet/sing-box/protocol/hysteria2"
	"github.com/sagernet/sing-box/protocol/shadowsocks"
	"github.com/sagernet/sing-box/protocol/tun"
	"github.com/sagernet/sing-box/protocol/vless"
	E "github.com/sagernet/sing/common/exceptions"
	singjson "github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"

	"opencw/internal/core"
)

// Tags assigned by core.Config.
const (
	relayTag = "vpn"
	tunTag   = "apps"
)

// Session is one running engine for one relay. Its TUN stays inactive until
// EnableTun.
type Session struct {
	box      *box.Box
	outbound adapter.Outbound
	cancel   context.CancelFunc
	once     sync.Once
	closeErr error
}

// The engine constructs the inbound once, but no Windows interface or route is
// created until a real TCP/UDP exit check succeeds. Activation keeps the same
// outbound and authenticated tunnel, avoiding a second relay handshake.
type deferredTun struct {
	adapter.Inbound
	scope  *adapter.Scope
	mu     sync.Mutex
	active bool
}

func (t *deferredTun) Start(_ adapter.StartStage, scope *adapter.Scope) error {
	t.scope = scope
	return nil
}

func (t *deferredTun) activate() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.active {
		return nil
	}
	if t.scope == nil {
		return errors.New("TUN not initialized")
	}
	if err := t.scope.Context().Err(); err != nil {
		return err
	}
	for _, stage := range adapter.ListStartStages {
		if err := t.Inbound.Start(stage, t.scope); err != nil {
			return err
		}
	}
	t.active = true
	t.scope.Add(func() error {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.active = false
		return nil
	})
	return nil
}

func (t *deferredTun) InterfaceUpdated(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active || t.scope.Context().Err() != nil {
		return
	}
	if listener, ok := t.Inbound.(adapter.InterfaceUpdateListener); ok {
		listener.InterfaceUpdated(ctx)
	}
}

// newBox builds an engine with only the protocols OpenCW supports registered,
// and the TUN inbound wrapped in deferredTun.
func newBox(ctx context.Context, data []byte) (*box.Box, error) {
	ins := inbound.NewRegistry()
	inbound.Register[option.TunInboundOptions](ins, "tun", func(
		ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.TunInboundOptions,
	) (adapter.Inbound, error) {
		in, err := tun.NewInbound(ctx, router, logger, tag, options)
		if err != nil {
			return nil, err
		}
		return &deferredTun{Inbound: in}, nil
	})
	outs := outbound.NewRegistry()
	direct.RegisterOutbound(outs)
	shadowsocks.RegisterOutbound(outs)
	vless.RegisterOutbound(outs)
	hysteria2.RegisterOutbound(outs)
	ends := endpoint.NewRegistry()
	dnses := dns.NewTransportRegistry()
	local.RegisterTransport(dnses)
	ctx = box.Context(ctx, ins, outs, ends, dnses, boxservice.NewRegistry(), certificate.NewRegistry())
	options, err := singjson.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		return nil, err
	}
	return box.New(box.Options{Context: ctx, Options: options})
}

// Start resolves server and starts an engine for it. With enableTun the TUN
// is activated immediately; otherwise only DialContext carries traffic until
// EnableTun.
func Start(ctx context.Context, server core.Server, enableTun bool) (*Session, error) {
	ctx, cancel := context.WithCancel(ctx)
	server, err := core.ResolveServer(ctx, server)
	if err != nil {
		cancel()
		return nil, err
	}
	// The TUN inbound is always configured; it stays deferred until EnableTun.
	data, err := core.Config(server, true, core.NativeIPv6())
	if err != nil {
		cancel()
		return nil, err
	}
	b, err := newBox(ctx, data)
	if err != nil {
		cancel()
		return nil, err
	}
	s := &Session{box: b, cancel: cancel}
	e, ok := b.Outbound().Outbound(relayTag)
	if !ok {
		s.Close()
		return nil, errors.New("missing relay outbound")
	}
	s.outbound = e
	if err = b.Start(); err != nil {
		s.Close()
		return nil, err
	}
	if enableTun {
		if err := s.EnableTun(); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

// EnableTun creates the Windows interface and routes on the running engine.
func (s *Session) EnableTun() error {
	in, ok := s.box.Inbound().Get(tunTag)
	if !ok {
		return errors.New("missing application TUN")
	}
	if err := in.(*deferredTun).activate(); err != nil {
		return err
	}
	// The router was started before the interface existed. Process lookup only
	// accepts sources found in this cache; publish the new address before apps
	// can send their first packet, without waiting for a Windows notification.
	return s.box.Network().UpdateInterfaces()
}

// Close stops the engine once; later calls return the same result. A nil
// Session is already closed.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.cancel()
		s.closeErr = withoutClosed(s.box.Close())
	})
	return s.closeErr
}

// withoutClosed drops shutdown errors about sockets the engine had already
// closed itself (the direct outbound's listener after the TUN stack stops).
// The session is down either way; only real failures are reported.
func withoutClosed(err error) error {
	var kept []error
	for _, e := range E.Expand(err) {
		if !errors.Is(e, net.ErrClosed) {
			kept = append(kept, e)
		}
	}
	return errors.Join(kept...)
}

// DialContext dials address through the relay.
func (s *Session) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return s.outbound.DialContext(ctx, network, M.ParseSocksaddr(address))
}
