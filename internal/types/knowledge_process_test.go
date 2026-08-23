package types

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolPtr(v bool) *bool {
	return &v
}

func TestKnowledgeProcessOverridesRoundtrip(t *testing.T) {
	k := &Knowledge{}
	overrides := &KnowledgeProcessOverrides{
		EnableMultimodel:      boolPtr(true),
		ChunkingConfig:        &ChunkingConfig{ChunkSize: 1024},
		ParserEngineOverrides: map[string]string{"pdf_force_scanned": "true"},
	}
	require.NoError(t, k.SetProcessOverrides(overrides))
	got, err := k.ProcessOverrides()
	require.NoError(t, err)
	require.NotNil(t, got)
	require.True(t, *got.EnableMultimodel)
	require.Equal(t, 1024, got.ChunkingConfig.ChunkSize)
	require.Equal(t, "true", got.ParserEngineOverrides["pdf_force_scanned"])
}

func TestSetProcessOverridesPreservesOtherMetadata(t *testing.T) {
	k := &Knowledge{}
	manualMeta := NewManualKnowledgeMetadata("# hello", ManualKnowledgeStatusDraft, 1)
	require.NoError(t, k.SetManualMetadata(manualMeta))

	overrides := &KnowledgeProcessOverrides{
		EnableMultimodel: boolPtr(false),
	}
	require.NoError(t, k.SetProcessOverrides(overrides))

	gotManual, err := k.ManualMetadata()
	require.NoError(t, err)
	require.NotNil(t, gotManual)
	require.Equal(t, "# hello", gotManual.Content)
	require.Equal(t, ManualKnowledgeFormatMarkdown, gotManual.Format)
	require.Equal(t, ManualKnowledgeStatusDraft, gotManual.Status)

	gotOverrides, err := k.ProcessOverrides()
	require.NoError(t, err)
	require.NotNil(t, gotOverrides)
	require.False(t, *gotOverrides.EnableMultimodel)
}

func TestSetManualMetadataPreservesInternalMetadata(t *testing.T) {
	k := &Knowledge{Metadata: JSON(`{
		"_weknora_move_claim":{"task_id":"move-1","stage":"active"},
		"process_overrides":{"parser_engine":"mineru"}
	}`)}
	require.NoError(t, k.SetManualMetadata(NewManualKnowledgeMetadata("正文", ManualKnowledgeStatusPublish, 2)))

	metadata, err := k.Metadata.Map()
	require.NoError(t, err)
	require.Contains(t, metadata, "_weknora_move_claim")
	require.Contains(t, metadata, metadataKeyProcessOverrides)
	assert.Equal(t, "正文", metadata["content"])
	assert.Equal(t, float64(2), metadata["version"])

	require.NoError(t, k.SetManualMetadata(nil))
	metadata, err = k.Metadata.Map()
	require.NoError(t, err)
	require.Contains(t, metadata, "_weknora_move_claim")
	require.Contains(t, metadata, metadataKeyProcessOverrides)
	assert.NotContains(t, metadata, "content")
	manual, err := k.ManualMetadata()
	require.NoError(t, err)
	assert.Nil(t, manual)
}

func TestSourceFileQuotaBytesPreservesStorageSizeSemantics(t *testing.T) {
	knowledge := &Knowledge{StorageSize: 25}
	require.NoError(t, knowledge.SetSourceFileQuotaBytes(75))
	require.Equal(t, int64(75), knowledge.SourceFileQuotaBytes())
	require.Equal(t, int64(25), knowledge.StorageSize)
	require.Equal(t, int64(100), knowledge.QuotaStorageBytes())

	require.NoError(t, knowledge.SetProcessOverrides(&KnowledgeProcessOverrides{
		ParserEngineOverrides: map[string]string{"pdf_force_scanned": "true"},
	}))
	require.Equal(t, int64(75), knowledge.SourceFileQuotaBytes())
	require.NotContains(t, string(knowledge.Metadata), "source_file_quota_bytes")
}
