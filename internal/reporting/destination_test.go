package reporting

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestDestinationPolicy(t *testing.T) {
	for _, test := range []struct {
		name                            string
		cidrs, hosts, sockets           []string
		network, host, socket, resolved string
		allow                           bool
	}{
		{name: "allowed IP", cidrs: []string{"10.20.0.0/16"}, host: "10.20.1.2", allow: true},
		{name: "rejected IP", cidrs: []string{"10.20.0.0/16"}, host: "10.21.1.2"},
		{name: "IPv6 CIDR", cidrs: []string{"2001:db8::/32"}, host: "2001:db8::1", allow: true},
		{name: "exact IP", hosts: []string{"127.0.0.1"}, host: "127.0.0.1", allow: true},
		{name: "empty fail closed", host: "8.8.8.8"},
		{name: "public IP not implicitly allowed", hosts: []string{"mysql.example"}, host: "8.8.8.8"},
		{name: "exact hostname", hosts: []string{"mysql.example"}, host: "MySQL.Example", resolved: "8.8.8.8", allow: true},
		{name: "different hostname", hosts: []string{"mysql.example"}, host: "other.example", resolved: "8.8.8.8"},
		{name: "exact public hostname with unrelated CIDR", cidrs: []string{"10.20.0.0/16"}, hosts: []string{"mysql.example"}, host: "mysql.example", resolved: "8.8.8.8", allow: true},
		{name: "exact private hostname outside CIDR", cidrs: []string{"10.20.0.0/16"}, hosts: []string{"mysql.example"}, host: "mysql.example", resolved: "192.168.1.1"},
		{name: "exact private hostname inside CIDR", cidrs: []string{"10.20.0.0/16"}, hosts: []string{"mysql.example"}, host: "mysql.example", resolved: "10.20.1.2", allow: true},
		{name: "non-allowlisted hostname outside CIDR", cidrs: []string{"10.20.0.0/16"}, host: "other.example", resolved: "8.8.8.8"},
		{name: "DNS inside CIDR", cidrs: []string{"10.20.0.0/16"}, host: "mysql.example", resolved: "10.20.1.2", allow: true},
		{name: "DNS rebinding loopback", hosts: []string{"mysql.example"}, host: "mysql.example", resolved: "127.0.0.1"},
		{name: "DNS rebinding private", hosts: []string{"mysql.example"}, host: "mysql.example", resolved: "192.168.1.1"},
		{name: "DNS rebinding link local", hosts: []string{"mysql.example"}, host: "mysql.example", resolved: "169.254.169.254"},
		{name: "explicit loopback CIDR", cidrs: []string{"127.0.0.0/8"}, host: "localhost", resolved: "127.0.0.1", allow: true},
		{name: "loopback rejected", cidrs: []string{"10.20.0.0/16"}, host: "127.0.0.1"},
		{name: "socket exact", network: "unix", sockets: []string{"/run/mysql/mysql.sock"}, socket: "/run/mysql/mysql.sock", allow: true},
		{name: "socket other", network: "unix", sockets: []string{"/run/mysql/mysql.sock"}, socket: "/tmp/mysql.sock"},
		{name: "socket traversal", network: "unix", sockets: []string{"/run/mysql/mysql.sock"}, socket: "/run/mysql/../mysql/mysql.sock"},
		{name: "socket alternate", network: "unix", sockets: []string{"/run/mysql/mysql.sock"}, socket: "/run//mysql/mysql.sock"},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := NewDestinationPolicy(test.cidrs, test.hosts, test.sockets)
			if err != nil {
				t.Fatal(err)
			}
			policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
				return []net.IPAddr{{IP: net.ParseIP(test.resolved)}}, nil
			}
			err = policy.Validate(context.Background(), test.network, test.host, test.socket)
			if (err == nil) != test.allow {
				t.Fatalf("allowed=%v error=%v", test.allow, err)
			}
		})
	}
}

func TestDestinationPolicyChecksEveryDNSAddress(t *testing.T) {
	for _, test := range []struct {
		name      string
		addresses []string
		allow     bool
	}{
		{"all public", []string{"8.8.8.8", "1.1.1.1"}, true},
		{"public and disallowed private", []string{"8.8.8.8", "192.168.1.1"}, false},
		{"public and explicitly allowed private", []string{"8.8.8.8", "10.20.1.2"}, true},
		{"public and disallowed loopback", []string{"8.8.8.8", "127.0.0.1"}, false},
		{"public and disallowed link local", []string{"8.8.8.8", "169.254.169.254"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, _ := NewDestinationPolicy([]string{"10.20.0.0/16"}, []string{"mysql.example"}, nil)
			policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
				var addresses []net.IPAddr
				for _, address := range test.addresses {
					addresses = append(addresses, net.IPAddr{IP: net.ParseIP(address)})
				}
				return addresses, nil
			}
			if err := policy.Validate(context.Background(), "tcp", "mysql.example", ""); (err == nil) != test.allow {
				t.Fatalf("allowed=%v error=%v", test.allow, err)
			}
		})
	}
	policy, _ := NewDestinationPolicy([]string{"10.20.0.0/16"}, nil, nil)
	policy.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("10.20.1.2")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if err := policy.Validate(context.Background(), "tcp", "mysql.example", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	policy.lookup = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("DNS failed") }
	if err := policy.Validate(context.Background(), "tcp", "mysql.example", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}

func TestDestinationDialUsesValidatedIPWithoutSecondLookup(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	policy, _ := NewDestinationPolicy([]string{"10.0.0.0/8"}, []string{"does-not-resolve.invalid", "127.0.0.1"}, nil)
	lookups := 0
	policy.lookup = func(_ context.Context, host string) ([]net.IPAddr, error) {
		lookups++
		if host != "does-not-resolve.invalid" || lookups != 1 {
			t.Errorf("unexpected DNS lookup: host=%q count=%d", host, lookups)
			return nil, errors.New("second lookup")
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	connection, err := policy.dial(ctx, "tcp", net.JoinHostPort("does-not-resolve.invalid", port))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if connection.RemoteAddr().String() != listener.Addr().String() || lookups != 1 {
		t.Fatalf("dialed %s after %d lookups", connection.RemoteAddr(), lookups)
	}
}

func TestStoredDatasourceRejectedUnderNewPolicyBeforeConnection(t *testing.T) {
	policy, _ := NewDestinationPolicy([]string{"10.20.0.0/16"}, nil, nil)
	manager, err := NewPoolManager(NewCipher([32]byte{}), PoolConfig{ConnectTimeout: time.Second, MySQLMaxPacketBytes: 1 << 20, Destinations: policy})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	// No password decryption or outbound dial may occur for this stale record.
	_, err = manager.Database(context.Background(), Datasource{ID: 1, Revision: 1, Host: "127.0.0.1", Port: 3306, Status: StatusActive}, false)
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
