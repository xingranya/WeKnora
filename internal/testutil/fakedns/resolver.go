// Package fakedns 为单元测试提供不访问公网的固定 DNS 解析器。
package fakedns

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/dns/dnsmessage"
)

var defaultResolverInstallMu sync.Mutex

// InstallDefault 在当前测试期间用本地固定 DNS 替换 net.DefaultResolver。
//
// records 的键是主机名，值是需要返回的 IPv4/IPv6 地址。未登记主机返回
// NXDOMAIN。函数持有进程级安装锁直到测试清理完成，因此调用它的测试不得先
// 调用 t.Parallel；同一个测试也只应安装一次。
func InstallDefault(t testing.TB, records map[string][]string) {
	t.Helper()
	defaultResolverInstallMu.Lock()

	server, err := newServer(records)
	if err != nil {
		defaultResolverInstallMu.Unlock()
		t.Fatalf("启动固定 DNS 失败：%v", err)
	}

	previous := net.DefaultResolver
	net.DefaultResolver = server.resolver()
	t.Cleanup(func() {
		net.DefaultResolver = previous
		server.close()
		defaultResolverInstallMu.Unlock()
	})
}

type dnsServer struct {
	conn    *net.UDPConn
	records map[string][]net.IP
	wg      sync.WaitGroup
	once    sync.Once
}

func newServer(rawRecords map[string][]string) (*dnsServer, error) {
	records := make(map[string][]net.IP, len(rawRecords))
	for host, rawAddresses := range rawRecords {
		normalizedHost := normalizeHost(host)
		if normalizedHost == "" {
			return nil, fmt.Errorf("主机名不能为空")
		}
		addresses := make([]net.IP, 0, len(rawAddresses))
		for _, rawAddress := range rawAddresses {
			ip := net.ParseIP(strings.TrimSpace(rawAddress))
			if ip == nil {
				return nil, fmt.Errorf("主机 %q 的地址 %q 无效", host, rawAddress)
			}
			addresses = append(addresses, cloneIP(ip))
		}
		records[normalizedHost] = addresses
	}

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	server := &dnsServer{conn: conn, records: records}
	server.wg.Add(1)
	go server.serve()
	return server, nil
}

func (s *dnsServer) resolver() *net.Resolver {
	address := s.conn.LocalAddr().String()
	return &net.Resolver{
		PreferGo:     true,
		StrictErrors: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "udp4", address)
		},
	}
}

func (s *dnsServer) close() {
	s.once.Do(func() {
		_ = s.conn.Close()
		s.wg.Wait()
	})
}

func (s *dnsServer) serve() {
	defer s.wg.Done()
	buffer := make([]byte, 4096)
	for {
		n, remote, err := s.conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		response, err := s.buildResponse(buffer[:n])
		if err != nil {
			continue
		}
		_, _ = s.conn.WriteToUDP(response, remote)
	}
}

func (s *dnsServer) buildResponse(query []byte) ([]byte, error) {
	var parser dnsmessage.Parser
	header, err := parser.Start(query)
	if err != nil {
		return nil, err
	}
	questions, err := parser.AllQuestions()
	if err != nil {
		return nil, err
	}

	rCode := dnsmessage.RCodeSuccess
	for _, question := range questions {
		if _, ok := s.records[normalizeHost(question.Name.String())]; !ok {
			rCode = dnsmessage.RCodeNameError
			break
		}
	}

	builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 header.ID,
		Response:           true,
		Authoritative:      true,
		RecursionDesired:   header.RecursionDesired,
		RecursionAvailable: true,
		RCode:              rCode,
	})
	builder.EnableCompression()
	if err := builder.StartQuestions(); err != nil {
		return nil, err
	}
	for _, question := range questions {
		if err := builder.Question(question); err != nil {
			return nil, err
		}
	}
	if rCode != dnsmessage.RCodeSuccess {
		return builder.Finish()
	}
	if err := builder.StartAnswers(); err != nil {
		return nil, err
	}
	for _, question := range questions {
		for _, ip := range s.records[normalizeHost(question.Name.String())] {
			resourceHeader := dnsmessage.ResourceHeader{
				Name:  question.Name,
				Class: dnsmessage.ClassINET,
				TTL:   0,
			}
			switch question.Type {
			case dnsmessage.TypeA:
				ipv4 := ip.To4()
				if ipv4 == nil {
					continue
				}
				var address [4]byte
				copy(address[:], ipv4)
				if err := builder.AResource(resourceHeader, dnsmessage.AResource{A: address}); err != nil {
					return nil, err
				}
			case dnsmessage.TypeAAAA:
				if ip.To4() != nil {
					continue
				}
				ipv6 := ip.To16()
				if ipv6 == nil {
					continue
				}
				var address [16]byte
				copy(address[:], ipv6)
				if err := builder.AAAAResource(resourceHeader, dnsmessage.AAAAResource{AAAA: address}); err != nil {
					return nil, err
				}
			}
		}
	}
	return builder.Finish()
}

func normalizeHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func cloneIP(ip net.IP) net.IP {
	clone := make(net.IP, len(ip))
	copy(clone, ip)
	return clone
}
