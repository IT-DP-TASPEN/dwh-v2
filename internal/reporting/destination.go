package reporting

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
)

// DestinationPolicy is immutable after construction. Pools and datasource
// writes share it. DNS is checked again on every physical connection dial.
type DestinationPolicy struct {
	cidrs   []netip.Prefix
	hosts   map[string]bool
	sockets map[string]bool
	lookup  func(context.Context, string) ([]net.IPAddr, error)
}

func NewDestinationPolicy(cidrs, hosts, sockets []string) (*DestinationPolicy, error) {
	p := &DestinationPolicy{hosts: make(map[string]bool), sockets: make(map[string]bool), lookup: net.DefaultResolver.LookupIPAddr}
	for _, value := range cidrs {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid report destination CIDR")
		}
		p.cidrs = append(p.cidrs, prefix.Masked())
	}
	for _, host := range hosts {
		if host == "" || strings.ContainsAny(host, "/\\: \t\r\n\x00") && net.ParseIP(host) == nil || strings.Contains(host, "*") {
			return nil, fmt.Errorf("invalid exact report destination host")
		}
		if ip := net.ParseIP(host); ip != nil {
			host = ip.String()
		}
		p.hosts[strings.ToLower(host)] = true
	}
	for _, path := range sockets {
		if !cleanSocket(path) {
			return nil, fmt.Errorf("report destination socket must be a cleaned absolute path")
		}
		p.sockets[path] = true
	}
	return p, nil
}

func cleanSocket(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}

func (p *DestinationPolicy) Validate(ctx context.Context, network, host, socket string) error {
	if network == "unix" {
		if !cleanSocket(socket) || p != nil && !p.sockets[socket] {
			return ErrInvalid
		}
		return nil
	}
	_, err := p.addresses(ctx, host)
	return err
}

func (p *DestinationPolicy) addresses(ctx context.Context, host string) ([]net.IPAddr, error) {
	if p == nil {
		return nil, nil
	} // Explicitly unconfigured library/test callers.
	if ip := net.ParseIP(host); ip != nil {
		if !p.allowedIP(ip) {
			return nil, ErrInvalid
		}
		return []net.IPAddr{{IP: ip}}, nil
	}
	if host == "" || strings.ContainsAny(host, "/\\: \t\r\n\x00") || strings.Contains(host, "*") {
		return nil, ErrInvalid
	}
	addresses, err := p.lookup(ctx, host)
	if err != nil || len(addresses) == 0 {
		return nil, ErrInvalid
	}
	exact := p.hosts[strings.ToLower(host)]
	for _, address := range addresses {
		// CIDRs constrain every resolved address when configured. An exact host
		// alone permits public destinations; private/local addresses need a CIDR
		// or exact literal IP permission, preventing DNS rebinding into local DBs.
		if !p.allowedIP(address.IP) && !(exact && len(p.cidrs) == 0 && publicDestination(address.IP)) {
			return nil, ErrInvalid
		}
	}
	return addresses, nil
}

func (p *DestinationPolicy) allowedIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if p.hosts[address.String()] {
		return true
	}
	for _, prefix := range p.cidrs {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func publicDestination(ip net.IP) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

func (p *DestinationPolicy) dial(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := net.Dialer{}
	if network == "unix" {
		if err := p.Validate(ctx, network, "", address); err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, network, address)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrInvalid
	}
	addresses, err := p.addresses(ctx, host)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return dialer.DialContext(ctx, network, address)
	}
	// Dial the already validated address, never resolve the hostname again.
	var last error
	for _, ip := range addresses {
		connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return connection, nil
		}
		last = err
	}
	return nil, last
}
