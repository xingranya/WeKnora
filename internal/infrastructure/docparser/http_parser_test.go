package docparser

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/utils"
)

func TestHTTPDocumentReaderFailsClosedInReleaseMode(t *testing.T) {
	t.Setenv("GIN_MODE", "release")
	reader, err := NewHTTPDocumentReader("https://example.com")
	if reader != nil || err == nil || !strings.Contains(err.Error(), "disabled in release mode") {
		t.Fatalf("NewHTTPDocumentReader() = (%v, %v), want release-mode rejection", reader, err)
	}
}

func TestHTTPDocumentReaderFailedReleaseReconnectKeepsPreviousAddress(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("example.com,docreader.example.com")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	t.Setenv("GIN_MODE", "debug")
	reader, err := NewHTTPDocumentReader("https://example.com")
	if err != nil {
		t.Fatalf("NewHTTPDocumentReader() error: %v", err)
	}
	if got := reader.base(); got != "https://example.com" {
		t.Fatalf("initial base URL = %q", got)
	}

	t.Setenv("GIN_MODE", "release")
	if reader.IsConnected() {
		t.Fatal("release-mode HTTP reader must not report connected")
	}
	if err := reader.Reconnect("https://docreader.example.com"); err == nil {
		t.Fatal("Reconnect() unexpectedly enabled HTTP transport in release mode")
	}
	if got := reader.base(); got != "https://example.com" {
		t.Fatalf("failed reconnect changed base URL to %q", got)
	}
}

func TestHTTPDocumentReaderCloseClearsConnectionState(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("example.com")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	t.Setenv("GIN_MODE", "debug")
	reader, err := NewHTTPDocumentReader("https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if reader.IsConnected() || reader.base() != "" {
		t.Fatal("Close() did not clear HTTP docreader connection state")
	}
}
