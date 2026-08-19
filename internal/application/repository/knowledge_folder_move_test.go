package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// folderPathOf reads a row's stored folder_path directly so the assertions do
// not depend on the list filters.
func folderPathOf(t *testing.T, db *gorm.DB, id string) string {
	t.Helper()
	var path string
	require.NoError(t, db.Raw(`SELECT folder_path FROM knowledges WHERE id = ?`, id).Scan(&path).Error)
	return path
}

func TestUpdateKnowledgeFolderPath(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := NewKnowledgeRepository(db).(*knowledgeRepository)
	ctx := context.Background()

	const tenantID = uint64(1)
	kbID := uuid.New().String()
	otherKBID := uuid.New().String()

	loose := insertKnowledgeInFolder(t, db, tenantID, kbID, "", "readme.md")
	filed := insertKnowledgeInFolder(t, db, tenantID, kbID, "docs", "intro.md")
	foreign := insertKnowledgeInFolder(t, db, tenantID, otherKBID, "docs", "other.md")

	affected, err := repo.UpdateKnowledgeFolderPath(ctx, tenantID, kbID, []string{loose, filed}, "archive/2026", "user-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), affected)
	assert.Equal(t, "archive/2026", folderPathOf(t, db, loose))
	assert.Equal(t, "archive/2026", folderPathOf(t, db, filed))

	// Moving back out of every folder is expressed as the empty path.
	affected, err = repo.UpdateKnowledgeFolderPath(ctx, tenantID, kbID, []string{filed}, "", "user-1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected)
	assert.Equal(t, "", folderPathOf(t, db, filed))

	// A row in another knowledge base is never touched, even when its ID is passed.
	affected, err = repo.UpdateKnowledgeFolderPath(ctx, tenantID, kbID, []string{foreign}, "leaked", "user-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), affected)
	assert.Equal(t, "docs", folderPathOf(t, db, foreign))

	affected, err = repo.UpdateKnowledgeFolderPath(ctx, tenantID, kbID, nil, "archive", "user-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), affected)
}

func TestRenameKnowledgeFolderPath(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := NewKnowledgeRepository(db).(*knowledgeRepository)
	ctx := context.Background()

	const tenantID = uint64(1)
	kbID := uuid.New().String()

	root := insertKnowledgeInFolder(t, db, tenantID, kbID, "", "readme.md")
	docs := insertKnowledgeInFolder(t, db, tenantID, kbID, "docs", "intro.md")
	spec := insertKnowledgeInFolder(t, db, tenantID, kbID, "docs/spec", "design.md")
	deep := insertKnowledgeInFolder(t, db, tenantID, kbID, "docs/spec/v2", "draft.md")
	sibling := insertKnowledgeInFolder(t, db, tenantID, kbID, "docsets", "unrelated.md")

	// The folder and its whole subtree move; siblings whose name merely shares a
	// prefix ("docsets") must not be dragged along.
	affected, err := repo.RenameKnowledgeFolderPath(ctx, tenantID, kbID, "docs", "handbook", "user-1")
	require.NoError(t, err)
	assert.Equal(t, int64(3), affected)
	assert.Equal(t, "handbook", folderPathOf(t, db, docs))
	assert.Equal(t, "handbook/spec", folderPathOf(t, db, spec))
	assert.Equal(t, "handbook/spec/v2", folderPathOf(t, db, deep))
	assert.Equal(t, "docsets", folderPathOf(t, db, sibling))
	assert.Equal(t, "", folderPathOf(t, db, root))

	// Renaming a nested folder onto an existing one merges them.
	_, err = repo.RenameKnowledgeFolderPath(ctx, tenantID, kbID, "handbook/spec", "docsets", "user-1")
	require.NoError(t, err)
	assert.Equal(t, "docsets", folderPathOf(t, db, spec))
	assert.Equal(t, "docsets/v2", folderPathOf(t, db, deep))
	assert.Equal(t, "docsets", folderPathOf(t, db, sibling))

	// An unknown folder is a no-op rather than an error.
	affected, err = repo.RenameKnowledgeFolderPath(ctx, tenantID, kbID, "missing", "whatever", "user-1")
	require.NoError(t, err)
	assert.Equal(t, int64(0), affected)

	_, err = repo.RenameKnowledgeFolderPath(ctx, tenantID, kbID, "", "whatever", "user-1")
	assert.Error(t, err, "renaming the root itself is meaningless and must be rejected")
}

func TestRenameKnowledgeFolderPathUnderscoreInPath(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := NewKnowledgeRepository(db).(*knowledgeRepository)
	ctx := context.Background()

	const tenantID = uint64(1)
	kbID := uuid.New().String()

	parent := insertKnowledgeInFolder(t, db, tenantID, kbID, "my_docs", "intro.md")
	child := insertKnowledgeInFolder(t, db, tenantID, kbID, "my_docs/spec", "design.md")

	affected, err := repo.RenameKnowledgeFolderPath(ctx, tenantID, kbID, "my_docs", "handbook", "user-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), affected)
	assert.Equal(t, "handbook", folderPathOf(t, db, parent))
	assert.Equal(t, "handbook/spec", folderPathOf(t, db, child))
}

func TestRenameKnowledgeFolderPathRejectsActiveUploadInSubtree(t *testing.T) {
	db := setupKnowledgeTestDB(t)
	repo := NewKnowledgeRepository(db).(*knowledgeRepository)
	ctx := context.Background()

	const tenantID = uint64(1)
	const kbID = "kb-active-upload"
	documentID := insertKnowledgeInFolder(t, db, tenantID, kbID, "docs", "intro.md")
	session := newKnowledgeUploadTestSession(
		"upload-in-docs", tenantID, kbID, "user-1", 1024, types.KnowledgeUploadUploading,
	)
	session.FolderPath = "docs/spec"
	require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))

	_, err := repo.RenameKnowledgeFolderPath(ctx, tenantID, kbID, "docs", "handbook", "user-1")
	require.ErrorIs(t, err, types.ErrKnowledgeFolderHasActiveUploads)
	assert.Equal(t, "docs", folderPathOf(t, db, documentID))

	// 其他知识库的上传不应阻止当前目录操作。
	session.KnowledgeBaseID = "kb-other"
	session.ID = "upload-in-other-kb"
	require.NoError(t, repo.CreateKnowledgeUploadSession(ctx, session))
	require.NoError(t, db.Delete(&types.KnowledgeUploadSession{}, "id = ?", "upload-in-docs").Error)

	_, err = repo.RenameKnowledgeFolderPath(ctx, tenantID, kbID, "docs", "handbook", "user-1")
	require.NoError(t, err)
	assert.Equal(t, "handbook", folderPathOf(t, db, documentID))
}
