package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type blockingSummaryChat struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *blockingSummaryChat) Chat(
	context.Context, []chat.Message, *chat.ChatOptions,
) (*types.ChatResponse, error) {
	m.once.Do(func() { close(m.started) })
	<-m.release
	return &types.ChatResponse{Content: "不得发布的旧摘要"}, nil
}

func (m *blockingSummaryChat) ChatStream(
	context.Context, []chat.Message, *chat.ChatOptions,
) (<-chan types.StreamResponse, error) {
	return nil, nil
}

func (m *blockingSummaryChat) GetModelName() string { return "blocking-summary" }
func (m *blockingSummaryChat) GetModelID() string   { return "blocking-summary" }

type summaryMoveChunkRepo struct {
	interfaces.ChunkRepository
	mu          sync.Mutex
	chunks      map[string]*types.Chunk
	updateCalls int
	createCalls int
}

func (r *summaryMoveChunkRepo) ListChunksByKnowledgeID(
	context.Context, uint64, string,
) ([]*types.Chunk, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]*types.Chunk, 0, len(r.chunks))
	for _, chunk := range r.chunks {
		copy := *chunk
		result = append(result, &copy)
	}
	return result, nil
}

func (r *summaryMoveChunkRepo) GetChunkByID(
	_ context.Context, _ uint64, id string,
) (*types.Chunk, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	chunk := r.chunks[id]
	if chunk == nil {
		return nil, errors.New("chunk not found")
	}
	copy := *chunk
	return &copy, nil
}

func (r *summaryMoveChunkRepo) ListChunksByParentIDs(
	context.Context, uint64, []string,
) ([]*types.Chunk, error) {
	return nil, nil
}

func (r *summaryMoveChunkRepo) UpdateChunk(context.Context, *types.Chunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateCalls++
	return nil
}

func (r *summaryMoveChunkRepo) CreateChunks(context.Context, []*types.Chunk) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls++
	return nil
}

type summaryMoveModelService struct {
	interfaces.ModelService
	model chat.Chat
}

func (s summaryMoveModelService) GetChatModel(context.Context, string) (chat.Chat, error) {
	return s.model, nil
}

func TestSummaryRefreshBlockedByLLMIsDiscardedAfterMoveCompletes(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.Knowledge{}, &types.TaskPendingOp{}))
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	knowledge := &types.Knowledge{
		ID: uuid.NewString(), TenantID: tenant.ID, KnowledgeBaseID: "kb-source",
		ParseStatus: types.ParseStatusCompleted, SummaryStatus: types.SummaryStatusCompleted,
		Description: "原摘要",
	}
	require.NoError(t, db.Create(knowledge).Error)
	repo := apprepo.NewKnowledgeRepository(db)
	chunkRepo := &summaryMoveChunkRepo{chunks: map[string]*types.Chunk{
		"text-1": {
			ID: "text-1", TenantID: tenant.ID, KnowledgeID: knowledge.ID,
			KnowledgeBaseID: "kb-source", ChunkType: types.ChunkTypeText,
			Content: "这是用于生成摘要的完整正文内容，长度足以触发模型调用并验证移动并发栅栏。", IsEnabled: true,
		},
	}}
	model := &blockingSummaryChat{started: make(chan struct{}), release: make(chan struct{})}
	svc := &knowledgeService{
		config: &config.Config{Conversation: &config.ConversationConfig{
			GenerateSummaryPrompt: "请生成摘要",
		}},
		repo: repo,
		kbService: lifecycleKBService{kb: &types.KnowledgeBase{
			ID: "kb-source", TenantID: tenant.ID, SummaryModelID: "summary-model",
		}},
		chunkRepo:    chunkRepo,
		modelService: summaryMoveModelService{model: model},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenant.ID)
	result := make(chan error, 1)
	go func() {
		_, refreshErr := svc.RegenerateKnowledgeSummary(ctx, knowledge.ID)
		result <- refreshErr
	}()
	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		t.Fatal("等待摘要模型调用超时")
	}

	mover := repo.(knowledgeMoveCoordinator)
	claimed, _, ok, err := mover.ClaimKnowledgeForMove(
		context.Background(), tenant.ID, knowledge.ID,
		"kb-source", "kb-target", "move-summary", "reuse_vectors",
	)
	require.NoError(t, err)
	require.True(t, ok)
	claimed.KnowledgeBaseID = "kb-target"
	claimed.ParseStatus = types.ParseStatusCompleted
	claimed.UpdatedAt = knowledge.UpdatedAt
	completed, err := mover.CompleteClaimedKnowledgeMove(context.Background(), claimed, "move-summary")
	require.NoError(t, err)
	require.True(t, completed)
	close(model.release)

	select {
	case refreshErr := <-result:
		require.ErrorIs(t, refreshErr, ErrSummaryRefreshStale)
	case <-time.After(2 * time.Second):
		t.Fatal("等待摘要刷新返回超时")
	}
	var persisted types.Knowledge
	require.NoError(t, db.First(&persisted, "id = ?", knowledge.ID).Error)
	require.Equal(t, "kb-target", persisted.KnowledgeBaseID)
	require.Equal(t, types.ParseStatusCompleted, persisted.ParseStatus)
	require.Equal(t, "原摘要", persisted.Description)
	require.Equal(t, types.SummaryStatusFailed, persisted.SummaryStatus)
	require.Contains(t, string(persisted.Metadata), "move-summary")
	require.Zero(t, chunkRepo.updateCalls)
	require.Zero(t, chunkRepo.createCalls)
}
