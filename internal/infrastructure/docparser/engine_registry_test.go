package docparser

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestListAllEnginesBuiltinIncludesHTML(t *testing.T) {
	engines := ListAllEngines(true, nil, nil)
	for _, engine := range engines {
		if engine.Name != "builtin" {
			continue
		}
		if !engine.Available {
			t.Fatalf("builtin engine is unavailable: %s", engine.UnavailableReason)
		}

		fileTypes := make(map[string]bool, len(engine.FileTypes))
		for _, fileType := range engine.FileTypes {
			fileTypes[fileType] = true
		}
		for _, want := range []string{"html", "htm"} {
			if !fileTypes[want] {
				t.Errorf("builtin engine file types do not include %q: %v", want, engine.FileTypes)
			}
		}
		return
	}

	t.Fatal("builtin engine not found")
}

func TestParserMaxFileSizeMatchesDocReaderPayloadContract(t *testing.T) {
	t.Setenv("DOCREADER_GRPC_MAX_FILE_SIZE_MB", "50")
	t.Setenv("MAX_FILE_SIZE_MB", "50")
	want := int64(49 * 1024 * 1024)

	for _, engine := range []string{"", BuiltinEngineName, AnydocEngineName, "remote-only"} {
		if got := ParserMaxFileSizeBytes(engine); got != want {
			t.Errorf("ParserMaxFileSizeBytes(%q) = %d, want %d", engine, got, want)
		}
	}

	engines := ListAllEngines(true, nil, []types.ParserEngineInfo{{
		Name:      "remote-only",
		Available: true,
	}})
	for _, engine := range engines {
		if engine.Name == "remote-only" && engine.MaxFileSizeBytes != want {
			t.Fatalf("remote engine max file size = %d, want %d", engine.MaxFileSizeBytes, want)
		}
	}
}

func TestUsesRemoteDocReaderMatchesRegistryRouting(t *testing.T) {
	tests := []struct {
		name     string
		engine   string
		fileType string
		isURL    bool
		want     bool
	}{
		{name: "builtin", engine: BuiltinEngineName, fileType: "pdf", want: true},
		{name: "remote-only", engine: "markitdown", fileType: "pdf", want: true},
		{name: "url default", fileType: "url", isURL: true, want: true},
		{name: "simple default", fileType: "txt", want: false},
		{name: "explicit simple", engine: SimpleEngineName, fileType: "txt", want: false},
		{name: "mineru", engine: MinerUEngineName, fileType: "pdf", want: false},
		{name: "anydoc fallback", engine: AnydocEngineName, fileType: "docx", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := UsesRemoteDocReader(test.engine, test.fileType, test.isURL); got != test.want {
				t.Fatalf("UsesRemoteDocReader() = %v, want %v", got, test.want)
			}
		})
	}
}
