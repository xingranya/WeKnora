package sandbox

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
)

func deterministicLookupIP(host string) ([]net.IP, error) {
	switch host {
	case "public.sandbox.test", "parallel.sandbox.test":
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	case "private.sandbox.test":
		return []net.IP{net.ParseIP("10.0.0.8")}, nil
	case "benchmark.sandbox.test":
		return []net.IP{net.ParseIP("198.18.0.8")}, nil
	case "unresolvable.sandbox.test":
		return nil, fmt.Errorf("固定的测试解析失败")
	default:
		return nil, fmt.Errorf("测试未登记主机 %q", host)
	}
}

// denyPrivate is the secure default policy.
var denyPrivate = OutboundURLPolicy{AllowPrivate: false}

// allowPrivate is the self-hosted opt-in policy.
var allowPrivate = OutboundURLPolicy{AllowPrivate: true}

func TestPolicyRejectsAlwaysForbiddenTargets(t *testing.T) {
	// These must be rejected under BOTH policies: link-local carries the cloud
	// metadata service, and the rest are not routable to a sandbox.
	cases := []struct{ name, raw string }{
		{"cloud metadata", "http://169.254.169.254/latest/meta-data/"},
		{"link local", "http://169.254.1.1"},
		{"unspecified", "http://0.0.0.0"},
		{"mdns suffix", "http://cube.local"},
		{"bad scheme file", "file:///etc/passwd"},
		{"bad scheme gopher", "gopher://evil"},
		{"empty", ""},
		{"no host", "http://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for label, policy := range map[string]OutboundURLPolicy{
				"deny-private":  denyPrivate,
				"allow-private": allowPrivate,
			} {
				err := policy.Validate(tc.raw)
				if err == nil {
					t.Fatalf("[%s] Validate(%q) = nil, want error", label, tc.raw)
				}
				if !errors.Is(err, ErrUnsafeOutboundURL) {
					t.Fatalf("[%s] Validate(%q) error = %v, want ErrUnsafeOutboundURL",
						label, tc.raw, err)
				}
			}
		})
	}
}

func TestPolicyRejectsPrivateTargetsByDefault(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:8080",
		"http://localhost:8080",
		"http://[::1]:8080",
		"http://10.0.0.5",
		"http://172.16.3.4",
		"http://192.168.1.1",
		"http://100.64.0.1",
	} {
		if err := denyPrivate.Validate(raw); err == nil {
			t.Fatalf("denyPrivate.Validate(%q) = nil, want error", raw)
		}
	}
}

func TestPolicyAllowsPrivateTargetsWhenOptedIn(t *testing.T) {
	// Self-hosted Cube listens on 127.0.0.1:33000 by default, so this opt-in
	// is what makes per-tenant Cube configuration possible at all.
	for _, raw := range []string{
		"http://127.0.0.1:33000",
		"http://localhost:33000",
		"http://10.0.0.5",
		"http://192.168.1.1:80",
	} {
		if err := allowPrivate.Validate(raw); err != nil {
			t.Fatalf("allowPrivate.Validate(%q) = %v, want nil", raw, err)
		}
	}
}

func TestPolicyAllowsPublicLiteralAddresses(t *testing.T) {
	// Literal addresses keep this hermetic: no DNS required.
	for _, raw := range []string{
		"http://203.0.113.10:8080",
		"https://203.0.113.10",
		"https://[2001:db8::1]:443/v1",
	} {
		if err := denyPrivate.Validate(raw); err != nil {
			t.Fatalf("denyPrivate.Validate(%q) = %v, want nil", raw, err)
		}
	}
}

func TestPolicyAllowsPublicHostname(t *testing.T) {
	const host = "public.sandbox.test"
	if err := denyPrivate.validateWithLookup("https://"+host, deterministicLookupIP); err != nil {
		t.Fatalf("denyPrivate.Validate(%q) = %v, want nil", host, err)
	}
}

func TestPolicyValidateRejectsUnresolvableHost(t *testing.T) {
	// Fail closed: if we cannot verify where a host points, we refuse it. This
	// also gives the admin an early "that hostname does not exist" signal.
	const host = "unresolvable.sandbox.test"
	err := denyPrivate.validateWithLookup("https://"+host, deterministicLookupIP)
	if err == nil {
		t.Fatal("Validate on an unresolvable host = nil, want error")
	}
}

func TestPolicyRejectsResolvedPrivateAndBenchmarkTargets(t *testing.T) {
	for _, raw := range []string{
		"https://private.sandbox.test",
		"https://benchmark.sandbox.test",
	} {
		for label, policy := range map[string]OutboundURLPolicy{
			"deny-private":  denyPrivate,
			"allow-private": allowPrivate,
		} {
			err := policy.validateWithLookup(raw, deterministicLookupIP)
			if raw == "https://private.sandbox.test" && label == "allow-private" {
				if err != nil {
					t.Fatalf("%s.Validate(%q) = %v, want nil", label, raw, err)
				}
				continue
			}
			if err == nil {
				t.Fatalf("%s.Validate(%q) = nil, want error", label, raw)
			}
		}
	}
}

func TestPolicyInjectedResolverSupportsConcurrentValidation(t *testing.T) {
	const goroutineCount = 16
	ready := sync.WaitGroup{}
	ready.Add(goroutineCount)
	start := make(chan struct{})
	errorsFound := make(chan error, goroutineCount)

	for range goroutineCount {
		go func() {
			ready.Done()
			<-start
			errorsFound <- denyPrivate.validateWithLookup(
				"https://parallel.sandbox.test",
				deterministicLookupIP,
			)
		}()
	}
	ready.Wait()
	close(start)
	for range goroutineCount {
		if err := <-errorsFound; err != nil {
			t.Fatalf("并行固定解析失败：%v", err)
		}
	}
}

func TestPolicyDialControlMirrorsValidation(t *testing.T) {
	// The dialer must forbid exactly what validation forbids, otherwise a
	// saved config would fail mysteriously at first use.
	alwaysBlocked := []string{"169.254.169.254:80", "0.0.0.0:80", "198.18.0.8:443"}
	privateOnly := []string{"127.0.0.1:8080", "10.1.2.3:443", "[::1]:8080"}

	for _, address := range alwaysBlocked {
		if err := denyPrivate.DialControl("tcp", address, nil); err == nil {
			t.Fatalf("denyPrivate.DialControl(%q) = nil, want error", address)
		}
		if err := allowPrivate.DialControl("tcp", address, nil); err == nil {
			t.Fatalf("allowPrivate.DialControl(%q) = nil, want error", address)
		}
	}
	for _, address := range privateOnly {
		if err := denyPrivate.DialControl("tcp", address, nil); err == nil {
			t.Fatalf("denyPrivate.DialControl(%q) = nil, want error", address)
		}
		if err := allowPrivate.DialControl("tcp", address, nil); err != nil {
			t.Fatalf("allowPrivate.DialControl(%q) = %v, want nil", address, err)
		}
	}
	for _, address := range []string{"203.0.113.10:443", "[2001:db8::1]:443"} {
		if err := denyPrivate.DialControl("tcp", address, nil); err != nil {
			t.Fatalf("denyPrivate.DialControl(%q) = %v, want nil", address, err)
		}
	}
}

func TestDefaultOutboundURLPolicyFailsClosed(t *testing.T) {
	if DefaultOutboundURLPolicy().AllowPrivate {
		t.Fatal("callers without a workspace config must fail closed")
	}
}
