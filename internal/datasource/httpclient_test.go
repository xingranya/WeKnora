package datasource

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/testutil/fakedns"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

func TestValidateConnectorBaseURLBlocksLoopback(t *testing.T) {
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)

	err := ValidateConnectorBaseURL("http://127.0.0.1:8000")
	if err == nil {
		t.Fatal("expected loopback base_url to be rejected")
	}
}

func TestValidateConnectorBaseURLAllowsPublicHTTPS(t *testing.T) {
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)
	fakedns.InstallDefault(t, map[string][]string{"connector-public.test": {"8.8.8.8"}})

	if err := ValidateConnectorBaseURL("https://connector-public.test"); err != nil {
		t.Fatalf("expected public base_url to pass: %v", err)
	}
}
