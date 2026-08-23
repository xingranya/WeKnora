package fakedns

import (
	"net"
	"testing"
)

func TestInstallDefaultUsesOnlyFixedRecords(t *testing.T) {
	InstallDefault(t, map[string][]string{
		"public.test": {"8.8.8.8", "2001:4860:4860::8888"},
	})

	addresses, err := net.LookupIP("public.test")
	if err != nil {
		t.Fatalf("固定主机解析失败：%v", err)
	}
	if len(addresses) != 2 {
		t.Fatalf("固定主机地址数 = %d，期望 2", len(addresses))
	}
	if _, err := net.LookupIP("missing.test"); err == nil {
		t.Fatal("未登记主机应返回 NXDOMAIN")
	}
}
