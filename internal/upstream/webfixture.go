//go:build webfixture

package upstream

import (
	"context"
	"net"
	"net/netip"
	"strconv"
)

// NewWebFixtureClient is compiled only into the browser fixture binary. The
// checked transport sees a synthetic public address; its dial is pinned to the
// local fixture listener. No DNS lookup or external connection is possible.
func NewWebFixtureClient(listenerAddress string) (*Client, error) {
	host, port, err := net.SplitHostPort(listenerAddress)
	if err != nil || host != "127.0.0.1" {
		return nil, ErrPolicy
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return nil, ErrPolicy
	}
	c := New(Policy{})
	c.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "fixture.example.test:80" {
			return nil, ErrPolicy
		}
		return c.dialContext(ctx, network, address)
	}
	c.lookupIP = func(ctx context.Context, host string) ([]netip.Addr, error) {
		if host != "fixture.example.test" {
			return nil, ErrPolicy
		}
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}
	c.dialIP = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:80" {
			return nil, ErrPolicy
		}
		return (&net.Dialer{}).DialContext(ctx, network, listenerAddress)
	}
	return c, nil
}
