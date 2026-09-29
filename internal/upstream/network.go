package upstream

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Allowance is operator configuration, never profile or API input. Every field
// must match; permanently forbidden address ranges cannot be overridden.
type Allowance struct {
	Host  string
	Port  uint16
	CIDRs []netip.Prefix
}

type Policy struct {
	Allow               []Allowance
	MaxConcurrent       int
	PerOrigin           int
	ReservedInteractive int
	ConnectTimeout      time.Duration
	HeaderTimeout       time.Duration
}

var blockedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("168.63.129.16/32"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
	netip.MustParsePrefix("fec0::/10"),
}

func (c *Client) addressAllowed(host string, port uint16, addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() || addr.Zone() != "" || !addr.IsGlobalUnicast() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range blockedNetworks {
		if prefix.Contains(addr) {
			return false
		}
	}
	if !addr.IsPrivate() && (port == 80 || port == 443) {
		return true
	}
	for _, rule := range c.policy.Allow {
		if rule.Host != host || rule.Port != port {
			continue
		}
		for _, prefix := range rule.CIDRs {
			if prefix.Contains(addr) {
				return true
			}
		}
	}
	return false
}

func (c *Client) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(ctx, c.policy.ConnectTimeout)
	defer cancel()
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrPolicy
	}
	portNumber, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || portNumber == 0 {
		return nil, ErrPolicy
	}
	var addresses []netip.Addr
	if addr, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{addr}
	} else {
		addresses, err = c.lookupIP(ctx, host)
		if err != nil {
			return nil, safeError(ctx, err)
		}
	}
	if len(addresses) == 0 {
		return nil, ErrNetwork
	}
	// Reject mixed public/private answers before dialing any candidate.
	for _, addr := range addresses {
		if !c.addressAllowed(host, uint16(portNumber), addr) {
			return nil, ErrPolicy
		}
	}
	for _, addr := range addresses {
		conn, err := c.dialIP(ctx, network, net.JoinHostPort(addr.Unmap().String(), portText))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, ErrNetwork
}

func copyPolicy(policy Policy) Policy {
	if policy.MaxConcurrent <= 0 {
		policy.MaxConcurrent = 12
	}
	if policy.PerOrigin <= 0 {
		policy.PerOrigin = 6
	}
	if policy.ReservedInteractive <= 0 {
		policy.ReservedInteractive = 1
	}
	if policy.ConnectTimeout <= 0 {
		policy.ConnectTimeout = 10 * time.Second
	}
	if policy.HeaderTimeout <= 0 {
		policy.HeaderTimeout = 15 * time.Second
	}
	allow := make([]Allowance, len(policy.Allow))
	for i, rule := range policy.Allow {
		allow[i] = rule
		allow[i].Host = strings.ToLower(strings.TrimSuffix(rule.Host, "."))
		allow[i].CIDRs = append([]netip.Prefix(nil), rule.CIDRs...)
	}
	policy.Allow = allow
	return policy
}
