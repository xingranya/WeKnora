package service

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	filesvc "github.com/Tencent/WeKnora/internal/application/service/file"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type routingStorageResolver struct {
	interfaces.StorageBackendResolver
	mu       sync.Mutex
	services map[string]interfaces.FileService
	calls    []string
}

func (r *routingStorageResolver) ResolveFileService(
	_ context.Context,
	_ *types.Tenant,
	backendID string,
	_ string,
	_ string,
) (interfaces.FileService, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, backendID)
	return r.services[backendID], "minio", nil
}

type deleteRoutingChunkRepo struct {
	interfaces.ChunkRepository
}

func (deleteRoutingChunkRepo) ListImageInfoByKnowledgeIDs(
	context.Context, uint64, []string,
) ([]interfaces.ChunkImageInfo, error) {
	return nil, nil
}

type deleteRoutingChunkService struct {
	interfaces.ChunkService
	repo interfaces.ChunkRepository
}

type kbDeleteScopeTrackingKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	listCalls int
}

func (r *kbDeleteScopeTrackingKnowledgeRepo) ListKnowledgeByKnowledgeBaseID(
	context.Context, uint64, string,
) ([]*types.Knowledge, error) {
	r.listCalls++
	return nil, nil
}

func (s deleteRoutingChunkService) GetRepository() interfaces.ChunkRepository { return s.repo }
func (deleteRoutingChunkService) DeleteChunksByKnowledgeID(context.Context, string) error {
	return nil
}

func TestMovedKnowledgeSingleDeleteUsesSourceResourceBackend(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.Tenant{}, &types.Knowledge{}, &types.KnowledgeFolder{},
		&types.KnowledgeTagRelation{}, &types.TaskPendingOp{},
	))
	tenant := &types.Tenant{Name: "tenant", Status: "active"}
	require.NoError(t, db.Create(tenant).Error)
	const sourceBackend = "source-backend"
	const targetBackend = "target-backend"
	const sourceRef = "resource://AbCdEfGhIjKlMnOpQrStUv"
	knowledge := &types.Knowledge{
		ID: uuid.NewString(), TenantID: tenant.ID, KnowledgeBaseID: "kb-target",
		ParseStatus: types.ParseStatusCompleted, FilePath: sourceRef,
	}
	require.NoError(t, db.Create(knowledge).Error)
	resourceCatalog := &kbDeleteResourceCatalog{resources: map[string]*types.StoredResource{
		sourceRef: {
			Handle: "AbCdEfGhIjKlMnOpQrStUv", TenantID: tenant.ID,
			StorageBackendID: sourceBackend, Provider: "minio",
			PhysicalPath: types.BuildStorageBackendPath(sourceBackend, "minio://bucket/source.pdf"),
		},
	}}
	sourcePhysical := &kbDeleteRecordingFileService{}
	targetPhysical := &kbDeleteRecordingFileService{}
	resolver := &routingStorageResolver{services: map[string]interfaces.FileService{
		sourceBackend: filesvc.NewResourceCatalogFileService(
			filesvc.NewBackendScopedFileService(sourceBackend, sourcePhysical), resourceCatalog,
		),
		targetBackend: filesvc.NewBackendScopedFileService(targetBackend, targetPhysical),
	}}
	targetBackendCopy := targetBackend
	repo := apprepo.NewKnowledgeRepository(db)
	svc := &knowledgeService{
		repo: repo,
		kbService: lifecycleKBService{kb: &types.KnowledgeBase{
			ID: "kb-target", TenantID: tenant.ID, StorageBackendID: &targetBackendCopy,
		}},
		tenantRepo:      kbDeleteTenantRepo{tenant: tenant},
		chunkService:    deleteRoutingChunkService{repo: deleteRoutingChunkRepo{}},
		graphEngine:     parentChildGraphRepo{},
		storageResolver: resolver,
		resourceCatalog: resourceCatalog,
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, tenant.ID)
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)

	require.NoError(t, svc.DeleteKnowledge(ctx, knowledge.ID))
	require.Equal(t, []string{sourceBackend}, resolver.calls)
	require.Equal(t, []string{"minio://bucket/source.pdf"}, sourcePhysical.deleted)
	require.Empty(t, targetPhysical.deleted)
}

func TestMovedKnowledgeBaseDeleteRoutesSourceFileAndImagesIndividually(t *testing.T) {
	const sourceBackend = "source-backend"
	const targetBackend = "target-backend"
	const sourceRef = "resource://LmNoPqRsTuVwXyZaBcDeFg"
	const imageRef = "resource://ZyXwVuTsRqPoNmLkJiHgFe"
	resourceCatalog := &kbDeleteResourceCatalog{resources: map[string]*types.StoredResource{
		sourceRef: {
			Handle: "LmNoPqRsTuVwXyZaBcDeFg", TenantID: 1, StorageBackendID: sourceBackend,
			Provider: "minio", PhysicalPath: types.BuildStorageBackendPath(sourceBackend, "minio://bucket/source.pdf"),
		},
		imageRef: {
			Handle: "ZyXwVuTsRqPoNmLkJiHgFe", TenantID: 1, StorageBackendID: sourceBackend,
			Provider: "minio", PhysicalPath: types.BuildStorageBackendPath(sourceBackend, "minio://bucket/image.png"),
		},
	}}
	sourcePhysical := &kbDeleteRecordingFileService{}
	targetPhysical := &kbDeleteRecordingFileService{}
	resolver := &routingStorageResolver{services: map[string]interfaces.FileService{
		sourceBackend: filesvc.NewResourceCatalogFileService(
			filesvc.NewBackendScopedFileService(sourceBackend, sourcePhysical), resourceCatalog,
		),
		targetBackend: filesvc.NewBackendScopedFileService(targetBackend, targetPhysical),
	}}
	targetBackendCopy := targetBackend
	svc := &knowledgeBaseService{
		kgRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
			{ID: "knowledge-1", TenantID: 1, KnowledgeBaseID: "kb-target", FilePath: sourceRef},
		}},
		chunkRepo: kbDeleteImageChunkRepo{imageInfos: []interfaces.ChunkImageInfo{
			{KnowledgeID: "knowledge-1", ImageInfo: `[{"url":"` + imageRef + `"}]`},
		}},
		fileSvc:         targetPhysical,
		storageResolver: resolver,
		resourceCatalog: resourceCatalog,
		tenantRepo:      kbDeleteTenantRepo{tenant: &types.Tenant{ID: 1}},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID: 1, KnowledgeBaseID: "kb-target", StorageBackendID: &targetBackendCopy,
	})
	require.NoError(t, err)

	require.NoError(t, svc.ProcessKBDelete(
		context.Background(), asynq.NewTask(types.TypeKBDelete, payload),
	))
	require.Equal(t, []string{sourceBackend, sourceBackend}, resolver.calls)
	require.ElementsMatch(t, []string{
		"minio://bucket/source.pdf", "minio://bucket/image.png",
	}, sourcePhysical.deleted)
	require.Empty(t, targetPhysical.deleted)
}

func TestKnowledgeBaseDeleteWorkerRejectsCrossTenantPayloadBeforeSideEffects(t *testing.T) {
	kbRepo := &kbDeleteDeletedKBRepo{kb: &types.KnowledgeBase{
		ID: "kb-victim", TenantID: 99,
	}}
	knowledgeRepo := &kbDeleteScopeTrackingKnowledgeRepo{}
	svc := &knowledgeBaseService{repo: kbRepo, kgRepo: knowledgeRepo}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID: 7, KnowledgeBaseID: "kb-victim",
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(
		context.Background(), asynq.NewTask(types.TypeKBDelete, payload),
	)

	require.ErrorIs(t, err, asynq.SkipRetry)
	require.Zero(t, knowledgeRepo.listCalls)
}
