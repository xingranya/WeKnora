package repository

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const knowledgeTagTestDDL = `
CREATE TABLE IF NOT EXISTS knowledges (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    type VARCHAR(50) NOT NULL DEFAULT 'file',
    title VARCHAR(255) NOT NULL DEFAULT '',
    parse_status VARCHAR(50) NOT NULL DEFAULT 'completed',
    deleted_at DATETIME
);
CREATE TABLE IF NOT EXISTS knowledge_tags (
    id VARCHAR(36) PRIMARY KEY,
    seq_id INTEGER NOT NULL,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    name VARCHAR(128) NOT NULL,
    color VARCHAR(32),
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS knowledge_tag_relations (
    knowledge_id VARCHAR(36) NOT NULL,
    tag_id VARCHAR(36) NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (knowledge_id, tag_id)
);
CREATE TABLE IF NOT EXISTS chunks (
    id VARCHAR(36) PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    tag_id VARCHAR(36),
    deleted_at DATETIME
);
`

func setupKnowledgeTagTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupKnowledgeTestDB(t)
	require.NoError(t, db.Exec(knowledgeTagTestDDL).Error)
	return db
}

func seedKnowledgeTagFixture(t *testing.T, db *gorm.DB) (kbID, knowledgeID, tagA, tagB string) {
	t.Helper()
	kbID = uuid.New().String()
	knowledgeID = uuid.New().String()
	tagA = uuid.New().String()
	tagB = uuid.New().String()
	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, parse_status)
		VALUES (?, 1, ?, 'file', 'doc', 'completed')
	`, knowledgeID, kbID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_tags (id, seq_id, tenant_id, knowledge_base_id, name)
		VALUES (?, 1, 1, ?, 'alpha'), (?, 2, 1, ?, 'beta')
	`, tagA, kbID, tagB, kbID).Error)
	return kbID, knowledgeID, tagA, tagB
}

func TestSetKnowledgeTags_DedupesAndReplaces(t *testing.T) {
	db := setupKnowledgeTagTestDB(t)
	repo := &knowledgeRepository{db: db}
	kbID, knowledgeID, tagA, tagB := seedKnowledgeTagFixture(t, db)
	ctx := context.Background()

	err := repo.SetKnowledgeTags(ctx, 1, kbID, knowledgeID, []string{tagA, tagA, tagB})
	require.NoError(t, err)

	var count int64
	require.NoError(t, db.Model(&types.KnowledgeTagRelation{}).
		Where("knowledge_id = ?", knowledgeID).Count(&count).Error)
	assert.Equal(t, int64(2), count)

	err = repo.SetKnowledgeTags(ctx, 1, kbID, knowledgeID, []string{tagB})
	require.NoError(t, err)
	require.NoError(t, db.Model(&types.KnowledgeTagRelation{}).
		Where("knowledge_id = ?", knowledgeID).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	err = repo.SetKnowledgeTags(ctx, 1, kbID, knowledgeID, nil)
	require.NoError(t, err)
	require.NoError(t, db.Model(&types.KnowledgeTagRelation{}).
		Where("knowledge_id = ?", knowledgeID).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestAddKnowledgeTagRelations_IsIncrementalIdempotentAndScoped(t *testing.T) {
	db := setupKnowledgeTagTestDB(t)
	repo := &knowledgeRepository{db: db}
	kbID, knowledgeID, tagA, tagB := seedKnowledgeTagFixture(t, db)
	ctx := context.Background()

	require.NoError(t, repo.SetKnowledgeTags(ctx, 1, kbID, knowledgeID, []string{tagA}))
	require.NoError(t, repo.AddKnowledgeTagRelations(ctx, 1, kbID, knowledgeID, []string{tagA, tagB, tagB}))
	require.NoError(t, repo.AddKnowledgeTagRelations(ctx, 1, kbID, knowledgeID, []string{tagB}))

	var relations []types.KnowledgeTagRelation
	require.NoError(t, db.Where("knowledge_id = ?", knowledgeID).Find(&relations).Error)
	assert.Len(t, relations, 2)

	otherKBTag := uuid.New().String()
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_tags (id, seq_id, tenant_id, knowledge_base_id, name)
		VALUES (?, 3, 1, ?, 'other')
	`, otherKBTag, uuid.New().String()).Error)
	err := repo.AddKnowledgeTagRelations(ctx, 1, kbID, knowledgeID, []string{otherKBTag})
	require.Error(t, err)
}

func TestAddKnowledgeTagRelations_RejectsCancelledKnowledge(t *testing.T) {
	db := setupKnowledgeTagTestDB(t)
	repo := &knowledgeRepository{db: db}
	kbID, knowledgeID, _, tagB := seedKnowledgeTagFixture(t, db)
	require.NoError(t, db.Model(&types.Knowledge{}).
		Where("id = ?", knowledgeID).
		Update("parse_status", types.ParseStatusCancelled).Error)

	err := repo.AddKnowledgeTagRelations(context.Background(), 1, kbID, knowledgeID, []string{tagB})
	require.Error(t, err)

	var count int64
	require.NoError(t, db.Model(&types.KnowledgeTagRelation{}).
		Where("knowledge_id = ?", knowledgeID).
		Count(&count).Error)
	assert.Zero(t, count)
}

func TestKnowledgeTagMutationsRejectBlockedLifecycleStates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*knowledgeRepository, string, string, string) error
	}{
		{
			name: "replace",
			mutate: func(repo *knowledgeRepository, kbID, knowledgeID, tagID string) error {
				return repo.SetKnowledgeTags(context.Background(), 1, kbID, knowledgeID, []string{tagID})
			},
		},
		{
			name: "append",
			mutate: func(repo *knowledgeRepository, kbID, knowledgeID, tagID string) error {
				return repo.AddKnowledgeTagRelations(context.Background(), 1, kbID, knowledgeID, []string{tagID})
			},
		},
	}

	blockedStatuses := []string{
		types.ParseStatusMoving,
		types.ParseStatusDeleting,
		types.ParseStatusCancelled,
	}
	for _, test := range tests {
		for _, status := range blockedStatuses {
			t.Run(test.name+"/"+status, func(t *testing.T) {
				db := setupKnowledgeTagTestDB(t)
				repo := &knowledgeRepository{db: db}
				kbID, knowledgeID, tagA, tagB := seedKnowledgeTagFixture(t, db)
				require.NoError(t, repo.SetKnowledgeTags(context.Background(), 1, kbID, knowledgeID, []string{tagA}))
				require.NoError(t, db.Model(&types.Knowledge{}).
					Where("id = ?", knowledgeID).
					Update("parse_status", status).Error)

				err := test.mutate(repo, kbID, knowledgeID, tagB)
				require.ErrorIs(t, err, ErrKnowledgeTagMutationConflict)
				if status == types.ParseStatusMoving {
					require.ErrorIs(t, err, types.ErrKnowledgeMoveInProgress)
				}

				var relations []types.KnowledgeTagRelation
				require.NoError(t, db.Where("knowledge_id = ?", knowledgeID).Find(&relations).Error)
				require.Len(t, relations, 1)
				assert.Equal(t, tagA, relations[0].TagID)
			})
		}
	}
}

func TestKnowledgeTagMutationsRejectUnexpectedKnowledgeScope(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*knowledgeRepository, uint64, string, string, string) error
	}{
		{
			name: "replace",
			mutate: func(repo *knowledgeRepository, tenantID uint64, kbID, knowledgeID, tagID string) error {
				return repo.SetKnowledgeTags(context.Background(), tenantID, kbID, knowledgeID, []string{tagID})
			},
		},
		{
			name: "append",
			mutate: func(repo *knowledgeRepository, tenantID uint64, kbID, knowledgeID, tagID string) error {
				return repo.AddKnowledgeTagRelations(context.Background(), tenantID, kbID, knowledgeID, []string{tagID})
			},
		},
	}

	for _, test := range tests {
		for _, scope := range []struct {
			name     string
			tenantID uint64
			kbID     string
		}{
			{name: "tenant", tenantID: 2},
			{name: "knowledge-base", tenantID: 1, kbID: uuid.New().String()},
		} {
			t.Run(test.name+"/"+scope.name, func(t *testing.T) {
				db := setupKnowledgeTagTestDB(t)
				repo := &knowledgeRepository{db: db}
				kbID, knowledgeID, tagA, tagB := seedKnowledgeTagFixture(t, db)
				require.NoError(t, repo.SetKnowledgeTags(context.Background(), 1, kbID, knowledgeID, []string{tagA}))
				expectedKBID := scope.kbID
				if expectedKBID == "" {
					expectedKBID = kbID
				}

				err := test.mutate(repo, scope.tenantID, expectedKBID, knowledgeID, tagB)
				require.ErrorIs(t, err, ErrKnowledgeTagMutationConflict)

				var relations []types.KnowledgeTagRelation
				require.NoError(t, db.Where("knowledge_id = ?", knowledgeID).Find(&relations).Error)
				require.Len(t, relations, 1)
				assert.Equal(t, tagA, relations[0].TagID)
			})
		}
	}
}

func TestSetKnowledgeTagsValidatesBeforeReplacing(t *testing.T) {
	db := setupKnowledgeTagTestDB(t)
	repo := &knowledgeRepository{db: db}
	kbID, knowledgeID, tagA, _ := seedKnowledgeTagFixture(t, db)
	require.NoError(t, repo.SetKnowledgeTags(context.Background(), 1, kbID, knowledgeID, []string{tagA}))

	foreignTagID := uuid.New().String()
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_tags (id, seq_id, tenant_id, knowledge_base_id, name)
		VALUES (?, 9, 1, ?, 'foreign')
	`, foreignTagID, uuid.New().String()).Error)

	err := repo.SetKnowledgeTags(context.Background(), 1, kbID, knowledgeID, []string{foreignTagID})
	require.Error(t, err)

	var relations []types.KnowledgeTagRelation
	require.NoError(t, db.Where("knowledge_id = ?", knowledgeID).Find(&relations).Error)
	require.Len(t, relations, 1)
	assert.Equal(t, tagA, relations[0].TagID)
}

func TestKnowledgeTagMutationUsesRowLockOutsideSQLite(t *testing.T) {
	sqlDB, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })

	db, err := gorm.Open(
		postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true},
	)
	require.NoError(t, err)

	var knowledge types.Knowledge
	result := knowledgeTagMutationRowQuery(db, 1, "knowledge-id").First(&knowledge)
	require.NoError(t, result.Error)
	assert.Contains(t, result.Statement.SQL.String(), "FOR UPDATE")
}

type knowledgeTagInterleaveContextKey struct{}

type knowledgeTagInterleaveGate struct {
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func setupConcurrentKnowledgeTagTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf(
		"file:%s?_busy_timeout=5000&_journal_mode=WAL",
		filepath.Join(t.TempDir(), "knowledge-tag-interleave.db"),
	)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(4)
	require.NoError(t, db.Exec(knowledgesTestDDL).Error)
	require.NoError(t, db.Exec(knowledgeTagTestDDL).Error)
	require.NoError(t, db.AutoMigrate(&types.TaskPendingOp{}))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func runKnowledgeTagInterleavedMoveOnce(
	ctx context.Context,
	repo *knowledgeRepository,
	knowledgeID, sourceKBID, targetKBID string,
) error {
	claimed, _, accepted, err := repo.ClaimKnowledgeForMove(
		ctx,
		1,
		knowledgeID,
		sourceKBID,
		targetKBID,
		"tag-interleave-move",
		"reuse_vectors",
	)
	if err != nil {
		return err
	}
	if !accepted || claimed == nil {
		return errors.New("move claim was not accepted")
	}
	if err := repo.DeleteKnowledgeTagRelations(ctx, knowledgeID); err != nil {
		return err
	}
	claimed.KnowledgeBaseID = targetKBID
	claimed.ParseStatus = types.ParseStatusMoving
	claimed.UpdatedAt = time.Now()
	staged, err := repo.StageClaimedKnowledgeMove(ctx, claimed, "tag-interleave-move")
	if err != nil {
		return err
	}
	if !staged {
		return errors.New("move claim was lost before staging")
	}
	return nil
}

func runKnowledgeTagInterleavedMove(
	ctx context.Context,
	repo *knowledgeRepository,
	knowledgeID, sourceKBID, targetKBID string,
) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := runKnowledgeTagInterleavedMoveOnce(ctx, repo, knowledgeID, sourceKBID, targetKBID)
		if err == nil || !strings.Contains(strings.ToLower(err.Error()), "database is locked") {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		// SQLite 没有行级写锁；移动 worker 遇到单写者冲突后按队列语义重试。
		time.Sleep(10 * time.Millisecond)
	}
}

func TestKnowledgeTagMutationsSerializeWithMoveClaim(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *knowledgeRepository, string, string, string) error
	}{
		{
			name: "replace",
			mutate: func(ctx context.Context, repo *knowledgeRepository, kbID, knowledgeID, tagID string) error {
				return repo.SetKnowledgeTags(ctx, 1, kbID, knowledgeID, []string{tagID})
			},
		},
		{
			name: "append",
			mutate: func(ctx context.Context, repo *knowledgeRepository, kbID, knowledgeID, tagID string) error {
				return repo.AddKnowledgeTagRelations(ctx, 1, kbID, knowledgeID, []string{tagID})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupConcurrentKnowledgeTagTestDB(t)
			repo := &knowledgeRepository{db: db}
			sourceKBID, knowledgeID, tagA, tagB := seedKnowledgeTagFixture(t, db)
			if test.name == "replace" {
				require.NoError(t, repo.SetKnowledgeTags(context.Background(), 1, sourceKBID, knowledgeID, []string{tagA}))
			}
			targetKBID := uuid.New().String()

			gate := &knowledgeTagInterleaveGate{
				reached: make(chan struct{}),
				release: make(chan struct{}),
			}
			mutationMarker := "mutation-" + test.name
			moveMarker := "move-" + test.name
			require.NoError(t, db.Callback().Query().After("gorm:query").Register(
				"test:pause-tag-mutation-"+test.name,
				func(tx *gorm.DB) {
					if tx.Statement.Context.Value(knowledgeTagInterleaveContextKey{}) != mutationMarker {
						return
					}
					if _, ok := tx.Statement.Dest.(*types.Knowledge); !ok {
						return
					}
					gate.once.Do(func() {
						close(gate.reached)
						<-gate.release
					})
				},
			))

			moveAttempted := make(chan struct{})
			var moveAttemptOnce sync.Once
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register(
				"test:observe-move-claim-"+test.name,
				func(tx *gorm.DB) {
					if tx.Statement.Context.Value(knowledgeTagInterleaveContextKey{}) == moveMarker {
						moveAttemptOnce.Do(func() { close(moveAttempted) })
					}
				},
			))

			mutationDone := make(chan error, 1)
			mutationCtx := context.WithValue(context.Background(), knowledgeTagInterleaveContextKey{}, mutationMarker)
			go func() {
				mutationDone <- test.mutate(mutationCtx, repo, sourceKBID, knowledgeID, tagB)
			}()
			select {
			case <-gate.reached:
			case <-time.After(2 * time.Second):
				t.Fatal("tag mutation did not reach the locked validation point")
			}

			moveDone := make(chan error, 1)
			moveCtx := context.WithValue(context.Background(), knowledgeTagInterleaveContextKey{}, moveMarker)
			go func() {
				moveDone <- runKnowledgeTagInterleavedMove(moveCtx, repo, knowledgeID, sourceKBID, targetKBID)
			}()
			select {
			case <-moveAttempted:
			case <-time.After(2 * time.Second):
				t.Fatal("move did not attempt to claim the locked knowledge")
			}
			select {
			case err := <-moveDone:
				t.Fatalf("move escaped tag transaction before release: %v", err)
			case <-time.After(50 * time.Millisecond):
			}

			close(gate.release)
			require.NoError(t, <-mutationDone)
			require.NoError(t, <-moveDone)

			var relationCount int64
			require.NoError(t, db.Model(&types.KnowledgeTagRelation{}).
				Where("knowledge_id = ?", knowledgeID).
				Count(&relationCount).Error)
			assert.Zero(t, relationCount, "move cleanup must be the final tag-relation write")

			moved, err := repo.GetKnowledgeByID(context.Background(), 1, knowledgeID)
			require.NoError(t, err)
			assert.Equal(t, targetKBID, moved.KnowledgeBaseID)
			assert.Equal(t, types.ParseStatusMoving, moved.ParseStatus)
		})
	}
}

func TestGetKnowledgeTags_ReturnsTagDetails(t *testing.T) {
	db := setupKnowledgeTagTestDB(t)
	repo := &knowledgeRepository{db: db}
	kbID, knowledgeID, tagA, tagB := seedKnowledgeTagFixture(t, db)
	ctx := context.Background()

	require.NoError(t, repo.SetKnowledgeTags(ctx, 1, kbID, knowledgeID, []string{tagA, tagB}))

	tagMap, err := repo.GetKnowledgeTags(ctx, []string{knowledgeID})
	require.NoError(t, err)
	require.Len(t, tagMap[knowledgeID], 2)

	names := map[string]bool{}
	for _, tag := range tagMap[knowledgeID] {
		names[tag.Name] = true
	}
	assert.True(t, names["alpha"])
	assert.True(t, names["beta"])
}

func TestDeleteKnowledgeTagRelations(t *testing.T) {
	db := setupKnowledgeTagTestDB(t)
	repo := &knowledgeRepository{db: db}
	kbID, knowledgeID, tagA, _ := seedKnowledgeTagFixture(t, db)
	ctx := context.Background()

	require.NoError(t, repo.SetKnowledgeTags(ctx, 1, kbID, knowledgeID, []string{tagA}))
	require.NoError(t, repo.DeleteKnowledgeTagRelations(ctx, knowledgeID))

	var count int64
	require.NoError(t, db.Model(&types.KnowledgeTagRelation{}).
		Where("knowledge_id = ?", knowledgeID).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestApplyKnowledgeListFilter_TagIDsOrSemantics(t *testing.T) {
	db := setupKnowledgeTagTestDB(t)
	repo := &knowledgeRepository{db: db}
	ctx := context.Background()

	kbID := uuid.New().String()
	docA := uuid.New().String()
	docB := uuid.New().String()
	docC := uuid.New().String()
	tagA := uuid.New().String()
	tagB := uuid.New().String()

	for _, row := range []struct{ id, title string }{
		{docA, "a"}, {docB, "b"}, {docC, "c"},
	} {
		require.NoError(t, db.Exec(`
			INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, parse_status)
			VALUES (?, 1, ?, 'file', ?, 'completed')
		`, row.id, kbID, row.title).Error)
	}
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_tags (id, seq_id, tenant_id, knowledge_base_id, name)
		VALUES (?, 1, 1, ?, 't-a'), (?, 2, 1, ?, 't-b')
	`, tagA, kbID, tagB, kbID).Error)
	require.NoError(t, repo.SetKnowledgeTags(ctx, 1, kbID, docA, []string{tagA}))
	require.NoError(t, repo.SetKnowledgeTags(ctx, 1, kbID, docB, []string{tagB}))
	require.NoError(t, repo.SetKnowledgeTags(ctx, 1, kbID, docC, []string{tagA, tagB}))

	query := db.WithContext(ctx).Model(&types.Knowledge{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", uint64(1), kbID)
	query = applyKnowledgeListFilter(query, types.KnowledgeListFilter{
		TagIDs: []string{tagA, tagB},
	})

	var ids []string
	require.NoError(t, query.Pluck("id", &ids).Error)
	assert.ElementsMatch(t, []string{docA, docB, docC}, ids)
}

func TestBatchCountReferences_ScopedToKnowledgeBase(t *testing.T) {
	db := setupKnowledgeTagTestDB(t)
	knowledgeRepo := &knowledgeRepository{db: db}
	tagRepo := &knowledgeTagRepository{db: db}
	ctx := context.Background()

	kb1 := uuid.New().String()
	kb2 := uuid.New().String()
	doc1 := uuid.New().String()
	doc2 := uuid.New().String()
	tag1 := uuid.New().String()
	tag2 := uuid.New().String()

	require.NoError(t, db.Exec(`
		INSERT INTO knowledges (id, tenant_id, knowledge_base_id, type, title, parse_status)
		VALUES (?, 1, ?, 'file', 'kb1-doc', 'completed'), (?, 1, ?, 'file', 'kb2-doc', 'completed')
	`, doc1, kb1, doc2, kb2).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO knowledge_tags (id, seq_id, tenant_id, knowledge_base_id, name)
		VALUES (?, 1, 1, ?, 'tag-kb1'), (?, 2, 1, ?, 'tag-kb2')
	`, tag1, kb1, tag2, kb2).Error)
	require.NoError(t, knowledgeRepo.SetKnowledgeTags(ctx, 1, kb1, doc1, []string{tag1}))
	// Stale relation: doc in kb2 still linked to tag1 (simulates pre-fix move bug)
	require.NoError(t, db.Create(&types.KnowledgeTagRelation{KnowledgeID: doc2, TagID: tag1}).Error)

	counts, err := tagRepo.BatchCountReferences(ctx, 1, kb1, []string{tag1})
	require.NoError(t, err)
	assert.Equal(t, int64(1), counts[tag1].KnowledgeCount)
}
