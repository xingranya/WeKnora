package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	filesvc "github.com/Tencent/WeKnora/internal/application/service/file"
	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type kbTaskCancelCall struct {
	kbID          string
	knowledgeIDs  []string
	dataSourceIDs []string
}

type recordingKBTaskInspector struct {
	repo                 *kbDeleteKBRepo
	calls                []kbTaskCancelCall
	cancelErr            error
	sawSoftDeletedRecord bool
}

func (r *recordingKBTaskInspector) CancelTasksForKnowledge(
	context.Context,
	string,
) (int, int, error) {
	return 0, 0, nil
}

func (r *recordingKBTaskInspector) HasQueuedTasksForKnowledge(context.Context, string) (bool, error) {
	return false, nil
}

func (r *recordingKBTaskInspector) QueueStats(context.Context) ([]types.QueueStat, bool, error) {
	return nil, true, nil
}

func (r *recordingKBTaskInspector) WorkerServerStats(context.Context) ([]types.WorkerServerStat, bool, error) {
	return nil, true, nil
}

func (r *recordingKBTaskInspector) CancelTasksForKnowledgeBase(
	_ context.Context,
	kbID string,
	knowledgeIDs []string,
	dataSourceIDs []string,
) (int, int, error) {
	r.calls = append(r.calls, kbTaskCancelCall{
		kbID:          kbID,
		knowledgeIDs:  append([]string(nil), knowledgeIDs...),
		dataSourceIDs: append([]string(nil), dataSourceIDs...),
	})
	if r.repo != nil && r.repo.deletedID == kbID {
		r.sawSoftDeletedRecord = true
	}
	return 0, 0, r.cancelErr
}

var (
	_ interfaces.TaskInspector              = (*recordingKBTaskInspector)(nil)
	_ interfaces.KnowledgeBaseTaskCanceller = (*recordingKBTaskInspector)(nil)
)

type recordingKBDeleteEnqueuer struct {
	calls int
	task  *asynq.Task
	err   error
	errs  []error
}

type recordingKBPendingRepo struct {
	interfaces.TaskPendingOpsRepository
	scopeIDs  []string
	deleteErr error
}

func (r *recordingKBPendingRepo) DeleteByScope(_ context.Context, scope, scopeID string) error {
	if scope == types.TaskScopeKnowledgeBase {
		r.scopeIDs = append(r.scopeIDs, scopeID)
	}
	return r.deleteErr
}

func (r *recordingKBDeleteEnqueuer) Enqueue(
	task *asynq.Task,
	_ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	r.calls++
	r.task = task
	if len(r.errs) >= r.calls && r.errs[r.calls-1] != nil {
		return nil, r.errs[r.calls-1]
	}
	if r.err != nil {
		return nil, r.err
	}
	return &asynq.TaskInfo{ID: "kb-delete-task"}, nil
}

func TestDeleteKnowledgeBaseForwardsDataSourceTaskScope(t *testing.T) {
	const kbID = "kb-with-datasource"
	storageBackendID := "storage-backend-1"
	kbRepo := &kbDeleteKBRepo{fakeKBRepo: *newFakeKBRepo()}
	kbRepo.rows[kbID] = &types.KnowledgeBase{
		ID:               kbID,
		TenantID:         1,
		Name:             "test",
		StorageBackendID: &storageBackendID,
	}
	inspector := &recordingKBTaskInspector{repo: kbRepo}
	enqueuer := &recordingKBDeleteEnqueuer{}
	dsRepo := newKBDeleteDSRepo(kbID, &types.DataSource{ID: "datasource-1", KnowledgeBaseID: kbID})
	svc := &knowledgeBaseService{
		repo:          kbRepo,
		asynqClient:   enqueuer,
		taskInspector: inspector,
		dsRepo:        dsRepo,
	}

	err := svc.DeleteKnowledgeBase(ctxWithTenantStorage(1, "local"), kbID)

	require.NoError(t, err)
	require.Len(t, inspector.calls, 2)
	assert.Empty(t, inspector.calls[0].dataSourceIDs)
	assert.Equal(t, []string{"datasource-1"}, inspector.calls[1].dataSourceIDs)
	require.NotNil(t, enqueuer.task)
	var payload types.KBDeletePayload
	require.NoError(t, json.Unmarshal(enqueuer.task.Payload(), &payload))
	assert.Equal(t, []string{"datasource-1"}, payload.DataSourceIDs)
	require.NotNil(t, payload.StorageBackendID)
	assert.Equal(t, storageBackendID, *payload.StorageBackendID)
}

func TestDeleteKnowledgeBaseReportsEnqueueFailure(t *testing.T) {
	const kbID = "kb-enqueue-failure"
	kbRepo := &kbDeleteKBRepo{fakeKBRepo: *newFakeKBRepo()}
	kbRepo.rows[kbID] = &types.KnowledgeBase{ID: kbID, TenantID: 1, Name: "test"}
	enqueuer := &recordingKBDeleteEnqueuer{err: errors.New("redis unavailable")}
	svc := &knowledgeBaseService{repo: kbRepo, asynqClient: enqueuer}

	err := svc.DeleteKnowledgeBase(ctxWithTenantStorage(1, "local"), kbID)

	require.ErrorContains(t, err, "cleanup task enqueue failed")
	assert.Equal(t, kbID, kbRepo.deletedID)
	assert.Equal(t, 1, enqueuer.calls)
}

type kbDeleteRecoveryRepo struct {
	fakeKBRepo
	pending       []*types.KnowledgeBase
	dataSourceIDs map[string][]string
}

func (r *kbDeleteRecoveryRepo) ListDeletedKnowledgeBasesWithActiveKnowledge(
	context.Context,
	int,
) ([]*types.KnowledgeBase, error) {
	return r.pending, nil
}

func (r *kbDeleteRecoveryRepo) ListKnowledgeBaseCleanupDataSourceIDs(
	_ context.Context,
	knowledgeBaseID string,
) ([]string, error) {
	return append([]string(nil), r.dataSourceIDs[knowledgeBaseID]...), nil
}

func TestRecoverPendingKBDeletesRequeuesDurableDatabaseState(t *testing.T) {
	storageBackendID := "storage-backend-1"
	vectorStoreID := "vector-store-1"
	kbRepo := &kbDeleteRecoveryRepo{
		fakeKBRepo: *newFakeKBRepo(),
		dataSourceIDs: map[string][]string{
			"kb-recovery": {"data-source-recovery"},
		},
		pending: []*types.KnowledgeBase{
			{
				ID:               "kb-recovery",
				TenantID:         1,
				StorageBackendID: &storageBackendID,
				VectorStoreID:    &vectorStoreID,
			},
		},
	}
	enqueuer := &recordingKBDeleteEnqueuer{}
	svc := &knowledgeBaseService{
		repo:        kbRepo,
		tenantRepo:  kbDeleteTenantRepo{tenant: &types.Tenant{ID: 1}},
		asynqClient: enqueuer,
	}

	err := svc.RecoverPendingKBDeletes(context.Background(), 100)

	require.NoError(t, err)
	require.Equal(t, 1, enqueuer.calls)
	var payload types.KBDeletePayload
	require.NoError(t, json.Unmarshal(enqueuer.task.Payload(), &payload))
	assert.Equal(t, "kb-recovery", payload.KnowledgeBaseID)
	assert.Equal(t, []string{"data-source-recovery"}, payload.DataSourceIDs)
	require.NotNil(t, payload.StorageBackendID)
	assert.Equal(t, storageBackendID, *payload.StorageBackendID)
	require.NotNil(t, payload.VectorStoreID)
	assert.Equal(t, vectorStoreID, *payload.VectorStoreID)
}

type archivedKBDeleteTaskInspector struct {
	interfaces.TaskInspector
	interfaces.RuntimeTaskInspector
	deleted bool
}

func (i *archivedKBDeleteTaskInspector) GetRuntimeTask(
	context.Context,
	string,
	string,
) (*types.RuntimeTaskInfo, bool, error) {
	return &types.RuntimeTaskInfo{State: types.RuntimeTaskArchived}, true, nil
}

func (i *archivedKBDeleteTaskInspector) ForceDeleteRuntimeTask(
	context.Context,
	string,
	string,
) (bool, error) {
	i.deleted = true
	return true, nil
}

func TestRecoverPendingKBDeletesReplacesArchivedTask(t *testing.T) {
	kbRepo := &kbDeleteRecoveryRepo{
		fakeKBRepo: *newFakeKBRepo(),
		pending: []*types.KnowledgeBase{
			{ID: "kb-archived-recovery", TenantID: 1},
		},
	}
	enqueuer := &recordingKBDeleteEnqueuer{errs: []error{asynq.ErrTaskIDConflict, nil}}
	inspector := &archivedKBDeleteTaskInspector{}
	svc := &knowledgeBaseService{
		repo:          kbRepo,
		tenantRepo:    kbDeleteTenantRepo{tenant: &types.Tenant{ID: 1}},
		asynqClient:   enqueuer,
		taskInspector: inspector,
	}

	err := svc.RecoverPendingKBDeletes(context.Background(), 100)

	require.NoError(t, err)
	assert.True(t, inspector.deleted)
	assert.Equal(t, 2, enqueuer.calls)
}

func TestDeleteKnowledgeBaseCancelsQueuedTasksBestEffort(t *testing.T) {
	tests := []struct {
		name       string
		cancelErr  error
		pendingErr error
	}{
		{name: "success"},
		{name: "inspector failure", cancelErr: errors.New("redis unavailable")},
		{name: "durable queue failure", pendingErr: errors.New("database unavailable")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const kbID = "kb-task-cleanup"
			kbRepo := &kbDeleteKBRepo{fakeKBRepo: *newFakeKBRepo()}
			kbRepo.rows[kbID] = &types.KnowledgeBase{ID: kbID, TenantID: 1, Name: "test"}
			inspector := &recordingKBTaskInspector{repo: kbRepo, cancelErr: tt.cancelErr}
			pendingRepo := &recordingKBPendingRepo{deleteErr: tt.pendingErr}
			enqueuer := &recordingKBDeleteEnqueuer{}
			svc := &knowledgeBaseService{
				repo:            kbRepo,
				asynqClient:     enqueuer,
				taskInspector:   inspector,
				taskPendingRepo: pendingRepo,
			}

			err := svc.DeleteKnowledgeBase(ctxWithTenantStorage(1, "local"), kbID)

			require.NoError(t, err)
			require.Len(t, inspector.calls, 1)
			assert.Equal(t, kbID, inspector.calls[0].kbID)
			assert.Empty(t, inspector.calls[0].knowledgeIDs)
			assert.True(t, inspector.sawSoftDeletedRecord)
			assert.Equal(t, []string{kbID}, pendingRepo.scopeIDs)
			assert.Equal(t, 1, enqueuer.calls)
		})
	}
}

type emptyKBKnowledgeRepo struct {
	interfaces.KnowledgeRepository
}

func (emptyKBKnowledgeRepo) ListKnowledgeByKnowledgeBaseID(
	context.Context,
	uint64,
	string,
) ([]*types.Knowledge, error) {
	return nil, nil
}

func (emptyKBKnowledgeRepo) DeleteKnowledgeListAndAdjustStorage(
	context.Context,
	uint64,
	string,
	[]string,
) error {
	return nil
}

func TestProcessKBDeleteRepeatsQueueCleanup(t *testing.T) {
	inspector := &recordingKBTaskInspector{}
	pendingRepo := &recordingKBPendingRepo{}
	svc := &knowledgeBaseService{
		kgRepo:          emptyKBKnowledgeRepo{},
		taskInspector:   inspector,
		taskPendingRepo: pendingRepo,
	}
	payload, err := json.Marshal(types.KBDeletePayload{TenantID: 1, KnowledgeBaseID: "kb-race"})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.NoError(t, err)
	require.Len(t, inspector.calls, 2)
	for _, call := range inspector.calls {
		assert.Equal(t, "kb-race", call.kbID)
		assert.Empty(t, call.knowledgeIDs)
	}
	assert.Equal(t, []string{"kb-race", "kb-race"}, pendingRepo.scopeIDs)
}

type populatedKBKnowledgeRepo struct {
	interfaces.KnowledgeRepository
	items []*types.Knowledge
}

type kbDeleteDeletedKBRepo struct {
	kbDeleteKBRepo
	kb *types.KnowledgeBase
}

func (r kbDeleteDeletedKBRepo) GetKnowledgeBaseByIDAndTenantUnscoped(
	_ context.Context,
	_ string,
	_ uint64,
) (*types.KnowledgeBase, error) {
	return r.kb, nil
}

func (r populatedKBKnowledgeRepo) ListKnowledgeByKnowledgeBaseID(
	context.Context,
	uint64,
	string,
) ([]*types.Knowledge, error) {
	return r.items, nil
}

func (populatedKBKnowledgeRepo) DeleteKnowledgeList(context.Context, uint64, []string) error {
	return nil
}

func (populatedKBKnowledgeRepo) DeleteKnowledgeListAndAdjustStorage(context.Context, uint64, string, []string) error {
	return nil
}

func (r populatedKBKnowledgeRepo) ClaimKnowledgeListForKBDelete(
	context.Context,
	uint64,
	string,
	[]string,
) ([]*types.Knowledge, error) {
	for _, knowledge := range r.items {
		knowledge.ParseStatus = types.ParseStatusDeleting
	}
	return r.items, nil
}

func (populatedKBKnowledgeRepo) DeleteKnowledgeTagRelations(context.Context, string) error {
	return nil
}

type kbCleanupChunkRepo struct {
	interfaces.ChunkRepository
}

func (kbCleanupChunkRepo) ListImageInfoByKnowledgeIDs(
	context.Context,
	uint64,
	[]string,
) ([]interfaces.ChunkImageInfo, error) {
	return nil, nil
}

func (kbCleanupChunkRepo) DeleteChunksByKnowledgeID(context.Context, uint64, string) error {
	return nil
}

type kbCleanupModelService struct {
	interfaces.ModelService
}

func (kbCleanupModelService) GetEmbeddingModel(context.Context, string) (embedding.Embedder, error) {
	return kbCleanupEmbedder{}, nil
}

type kbCleanupEmbedder struct{}

func (kbCleanupEmbedder) Embed(context.Context, string) ([]float32, error) { return nil, nil }
func (kbCleanupEmbedder) BatchEmbed(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (kbCleanupEmbedder) GetModelName() string { return "test" }
func (kbCleanupEmbedder) GetDimensions() int   { return 1 }
func (kbCleanupEmbedder) GetModelID() string   { return "test" }
func (kbCleanupEmbedder) BatchEmbedWithPool(
	context.Context,
	embedding.Embedder,
	[]string,
) ([][]float32, error) {
	return nil, nil
}

func TestProcessKBDeleteCollectsKnowledgeIDsForEveryScrub(t *testing.T) {
	inspector := &recordingKBTaskInspector{}
	svc := &knowledgeBaseService{
		kgRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
			{ID: "knowledge-1", KnowledgeBaseID: "kb-1", EmbeddingModelID: "model-1"},
			{ID: "knowledge-2", KnowledgeBaseID: "kb-1", EmbeddingModelID: "model-1"},
		}},
		chunkRepo:     kbCleanupChunkRepo{},
		modelService:  kbCleanupModelService{},
		taskInspector: inspector,
	}
	payload, err := json.Marshal(types.KBDeletePayload{TenantID: 1, KnowledgeBaseID: "kb-1"})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.NoError(t, err)
	require.Len(t, inspector.calls, 2)
	for _, call := range inspector.calls {
		assert.Equal(t, []string{"knowledge-1", "knowledge-2"}, call.knowledgeIDs)
	}
}

type kbDeleteRecordingFileService struct {
	interfaces.FileService
	deleted []string
	err     error
}

type kbDeleteResourceCatalog struct {
	interfaces.ResourceCatalog
	resources map[string]*types.StoredResource
	deleted   []string
}

func (c *kbDeleteResourceCatalog) Resolve(_ context.Context, reference string) (*types.StoredResource, error) {
	resource := c.resources[reference]
	if resource == nil {
		return nil, interfaces.ErrResourceNotFound
	}
	return resource, nil
}

func (c *kbDeleteResourceCatalog) ResolvePath(
	ctx context.Context,
	reference string,
) (string, *types.StoredResource, error) {
	resource, err := c.Resolve(ctx, reference)
	if err != nil {
		return "", nil, err
	}
	return resource.PhysicalPath, resource, nil
}

func (c *kbDeleteResourceCatalog) MarkDeleted(_ context.Context, reference string) error {
	c.deleted = append(c.deleted, reference)
	delete(c.resources, reference)
	return nil
}

func (s *kbDeleteRecordingFileService) DeleteFile(_ context.Context, path string) error {
	s.deleted = append(s.deleted, path)
	return s.err
}

type kbDeleteStorageResolver struct {
	interfaces.StorageBackendResolver
	fileService interfaces.FileService
	backendIDs  []string
	err         error
}

func (r *kbDeleteStorageResolver) ResolveFileService(
	_ context.Context,
	_ *types.Tenant,
	backendID string,
	_ string,
	_ string,
) (interfaces.FileService, string, error) {
	r.backendIDs = append(r.backendIDs, backendID)
	if r.err != nil {
		return nil, "", r.err
	}
	return r.fileService, "minio", nil
}

type kbDeleteTenantRepo struct {
	interfaces.TenantRepository
	tenant *types.Tenant
}

func (r kbDeleteTenantRepo) GetTenantByID(_ context.Context, _ uint64) (*types.Tenant, error) {
	return r.tenant, nil
}

type kbDeleteImageChunkRepo struct {
	kbCleanupChunkRepo
	imageInfos []interfaces.ChunkImageInfo
}

func (r kbDeleteImageChunkRepo) ListImageInfoByKnowledgeIDs(
	context.Context,
	uint64,
	[]string,
) ([]interfaces.ChunkImageInfo, error) {
	return r.imageInfos, nil
}

func TestProcessKBDeleteUsesStorageBackendSnapshotForResources(t *testing.T) {
	const storageBackendID = "storage-backend-1"
	storageBackendIDSnapshot := storageBackendID
	const sourceRef = "resource://AbCdEfGhIjKlMnOpQrStUv"
	const imageRef = "resource://ZyXwVuTsRqPoNmLkJiHgFe"
	fallbackFileSvc := &kbDeleteRecordingFileService{}
	physicalFileSvc := &kbDeleteRecordingFileService{}
	resourceCatalog := &kbDeleteResourceCatalog{resources: map[string]*types.StoredResource{
		sourceRef: {
			Handle:           "AbCdEfGhIjKlMnOpQrStUv",
			TenantID:         1,
			StorageBackendID: storageBackendID,
			Provider:         "minio",
			PhysicalPath:     "storage://storage-backend-1/minio://weknora/source.pdf",
		},
		imageRef: {
			Handle:           "ZyXwVuTsRqPoNmLkJiHgFe",
			TenantID:         1,
			StorageBackendID: storageBackendID,
			Provider:         "minio",
			PhysicalPath:     "storage://storage-backend-1/minio://weknora/image.png",
		},
	}}
	routedFileSvc := filesvc.NewResourceCatalogFileService(
		filesvc.NewBackendScopedFileService(storageBackendID, physicalFileSvc),
		resourceCatalog,
	)
	storageResolver := &kbDeleteStorageResolver{fileService: routedFileSvc}
	svc := &knowledgeBaseService{
		kgRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
			{
				ID:               "knowledge-1",
				KnowledgeBaseID:  "kb-1",
				EmbeddingModelID: "model-1",
				FilePath:         sourceRef,
			},
		}},
		chunkRepo: kbDeleteImageChunkRepo{imageInfos: []interfaces.ChunkImageInfo{
			{KnowledgeID: "knowledge-1", ImageInfo: `[{"url":"` + imageRef + `"}]`},
		}},
		modelService:    kbCleanupModelService{},
		fileSvc:         fallbackFileSvc,
		storageResolver: storageResolver,
		resourceCatalog: resourceCatalog,
		tenantRepo: kbDeleteTenantRepo{tenant: &types.Tenant{
			ID: 1,
		}},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:         1,
		KnowledgeBaseID:  "kb-1",
		StorageBackendID: &storageBackendIDSnapshot,
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.NoError(t, err)
	assert.Equal(t, []string{storageBackendID, storageBackendID}, storageResolver.backendIDs)
	assert.ElementsMatch(t, []string{
		"minio://weknora/source.pdf",
		"minio://weknora/image.png",
	}, physicalFileSvc.deleted)
	assert.ElementsMatch(t, []string{sourceRef, imageRef}, resourceCatalog.deleted)
	assert.Empty(t, fallbackFileSvc.deleted)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))
	require.NoError(t, err)
	assert.Equal(t, []string{storageBackendID, storageBackendID}, storageResolver.backendIDs)
	assert.Len(t, physicalFileSvc.deleted, 2, "资源已删除后的重试不能重复访问物理对象")
}

func TestProcessKBDeleteLegacyPayloadResolvesResourceOwnerBackend(t *testing.T) {
	const storageBackendID = "storage-backend-legacy"
	const sourceRef = "resource://LmNoPqRsTuVwXyZaBcDeFg"
	physicalFileSvc := &kbDeleteRecordingFileService{}
	resourceCatalog := &kbDeleteResourceCatalog{resources: map[string]*types.StoredResource{
		sourceRef: {
			Handle:           "LmNoPqRsTuVwXyZaBcDeFg",
			TenantID:         7,
			StorageBackendID: storageBackendID,
			Provider:         "minio",
			PhysicalPath:     "storage://storage-backend-legacy/minio://weknora/legacy.pdf",
		},
	}}
	routedFileSvc := filesvc.NewResourceCatalogFileService(
		filesvc.NewBackendScopedFileService(storageBackendID, physicalFileSvc),
		resourceCatalog,
	)
	storageResolver := &kbDeleteStorageResolver{fileService: routedFileSvc}
	svc := &knowledgeBaseService{
		kgRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
			{ID: "knowledge-legacy", KnowledgeBaseID: "kb-legacy", FilePath: sourceRef},
		}},
		chunkRepo:       kbCleanupChunkRepo{},
		fileSvc:         &kbDeleteRecordingFileService{},
		storageResolver: storageResolver,
		resourceCatalog: resourceCatalog,
		tenantRepo: kbDeleteTenantRepo{tenant: &types.Tenant{
			ID: 7,
		}},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:        7,
		KnowledgeBaseID: "kb-legacy",
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.NoError(t, err)
	assert.Equal(t, []string{storageBackendID}, storageResolver.backendIDs)
	assert.Equal(t, []string{"minio://weknora/legacy.pdf"}, physicalFileSvc.deleted)
	assert.Equal(t, []string{sourceRef}, resourceCatalog.deleted)
}

func TestProcessKBDeleteLegacyProviderPathUsesDeletedKBSnapshot(t *testing.T) {
	const storageBackendID = "storage-backend-history"
	physicalFileSvc := &kbDeleteRecordingFileService{}
	routedFileSvc := filesvc.NewBackendScopedFileService(storageBackendID, physicalFileSvc)
	storageResolver := &kbDeleteStorageResolver{fileService: routedFileSvc}
	storageBackendIDSnapshot := storageBackendID
	kbRepo := kbDeleteDeletedKBRepo{kb: &types.KnowledgeBase{
		ID:               "kb-history",
		TenantID:         9,
		StorageBackendID: &storageBackendIDSnapshot,
	}}
	svc := &knowledgeBaseService{
		repo: &kbRepo,
		kgRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
			{
				ID:              "knowledge-history",
				KnowledgeBaseID: "kb-history",
				FilePath:        "minio://weknora/history.pdf",
			},
		}},
		chunkRepo:       kbCleanupChunkRepo{},
		storageResolver: storageResolver,
		tenantRepo:      kbDeleteTenantRepo{tenant: &types.Tenant{ID: 9}},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:        9,
		KnowledgeBaseID: "kb-history",
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.NoError(t, err)
	assert.Equal(t, []string{storageBackendID}, storageResolver.backendIDs)
	assert.Equal(t, []string{"minio://weknora/history.pdf"}, physicalFileSvc.deleted)
}

func TestProcessKBDeletePhysicalFailureKeepsKnowledgeForRetry(t *testing.T) {
	const storageBackendID = "storage-backend-1"
	const sourceRef = "resource://QrStUvWxYzAbCdEfGhIjKl"
	storageBackendIDSnapshot := storageBackendID
	physicalFileSvc := &kbDeleteRecordingFileService{err: errors.New("minio unavailable")}
	resourceCatalog := &kbDeleteResourceCatalog{resources: map[string]*types.StoredResource{
		sourceRef: {
			Handle:           "QrStUvWxYzAbCdEfGhIjKl",
			TenantID:         1,
			StorageBackendID: storageBackendID,
			Provider:         "minio",
			PhysicalPath:     "storage://storage-backend-1/minio://weknora/retry.pdf",
		},
	}}
	routedFileSvc := filesvc.NewResourceCatalogFileService(
		filesvc.NewBackendScopedFileService(storageBackendID, physicalFileSvc),
		resourceCatalog,
	)
	repo := &kbDeleteTrackingKnowledgeRepo{populatedKBKnowledgeRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
		{ID: "knowledge-retry", KnowledgeBaseID: "kb-retry", FilePath: sourceRef},
	}}}
	svc := &knowledgeBaseService{
		kgRepo:          repo,
		chunkRepo:       kbCleanupChunkRepo{},
		storageResolver: &kbDeleteStorageResolver{fileService: routedFileSvc},
		resourceCatalog: resourceCatalog,
		tenantRepo:      kbDeleteTenantRepo{tenant: &types.Tenant{ID: 1}},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:         1,
		KnowledgeBaseID:  "kb-retry",
		StorageBackendID: &storageBackendIDSnapshot,
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.ErrorContains(t, err, "minio unavailable")
	assert.Zero(t, repo.deleteCalls)
	assert.Empty(t, resourceCatalog.deleted)
}

// kbDeleteDeferredRegistry reports a retryable engine-resolution failure from
// the rebuild path, matching what GetOrLoadByStoreID does when the caller
// goes away or the store engine cannot be produced yet.
type kbDeleteDeferredRegistry struct {
	err error
}

func (kbDeleteDeferredRegistry) Register(interfaces.RetrieveEngineService) error { return nil }
func (kbDeleteDeferredRegistry) GetRetrieveEngineService(types.RetrieverEngineType) (
	interfaces.RetrieveEngineService, error,
) {
	return nil, nil
}
func (kbDeleteDeferredRegistry) GetAllRetrieveEngineServices() []interfaces.RetrieveEngineService {
	return nil
}
func (kbDeleteDeferredRegistry) GetByStoreID(string) (interfaces.RetrieveEngineService, error) {
	return nil, errors.New("store not in registry")
}
func (r kbDeleteDeferredRegistry) GetOrLoadByStoreID(
	context.Context, uint64, string,
) (interfaces.RetrieveEngineService, error) {
	return nil, r.err
}

type kbDeleteOwnership struct {
	owned map[string]uint64
}

func (o *kbDeleteOwnership) StoreOwnedBy(_ context.Context, storeID string, tenantID uint64) (bool, error) {
	owner, ok := o.owned[storeID]
	return ok && owner == tenantID, nil
}

type kbDeleteFixedRegistry struct {
	engine interfaces.RetrieveEngineService
}

func (kbDeleteFixedRegistry) Register(interfaces.RetrieveEngineService) error { return nil }
func (r kbDeleteFixedRegistry) GetRetrieveEngineService(types.RetrieverEngineType) (
	interfaces.RetrieveEngineService, error,
) {
	return r.engine, nil
}
func (r kbDeleteFixedRegistry) GetAllRetrieveEngineServices() []interfaces.RetrieveEngineService {
	return []interfaces.RetrieveEngineService{r.engine}
}
func (r kbDeleteFixedRegistry) GetByStoreID(string) (interfaces.RetrieveEngineService, error) {
	return r.engine, nil
}
func (r kbDeleteFixedRegistry) GetOrLoadByStoreID(
	context.Context,
	uint64,
	string,
) (interfaces.RetrieveEngineService, error) {
	return r.engine, nil
}

type kbDeleteFailingEngine struct {
	interfaces.RetrieveEngineService
	deleteErr   error
	deleteCalls int
}

func (e *kbDeleteFailingEngine) Support() []types.RetrieverType {
	return []types.RetrieverType{types.VectorRetrieverType}
}

func (e *kbDeleteFailingEngine) EngineType() types.RetrieverEngineType {
	return types.PostgresRetrieverEngineType
}

func (e *kbDeleteFailingEngine) DeleteByKnowledgeIDList(
	context.Context,
	[]string,
	int,
	string,
) error {
	e.deleteCalls++
	return e.deleteErr
}

type kbDeleteFailingChunkRepo struct {
	kbCleanupChunkRepo
	deleteErr error
}

func (r kbDeleteFailingChunkRepo) DeleteChunksByKnowledgeID(context.Context, uint64, string) error {
	return r.deleteErr
}

type kbDeleteFailingGraphRepo struct {
	interfaces.RetrieveGraphRepository
	deleteErr error
}

func (r kbDeleteFailingGraphRepo) DelGraph(context.Context, []types.NameSpace) error {
	return r.deleteErr
}

type kbDeleteTrackingKnowledgeRepo struct {
	populatedKBKnowledgeRepo
	deleteCalls int
}

func (r *kbDeleteTrackingKnowledgeRepo) DeleteKnowledgeList(context.Context, uint64, []string) error {
	r.deleteCalls++
	return nil
}

func (r *kbDeleteTrackingKnowledgeRepo) DeleteKnowledgeListAndAdjustStorage(
	context.Context,
	uint64,
	string,
	[]string,
) error {
	r.deleteCalls++
	return nil
}

func TestProcessKBDeleteMissingVectorStoreContinuesNonVectorCleanup(t *testing.T) {
	const storeID = "00000000-0000-0000-0000-0000000000cc"
	storeIDSnapshot := storeID
	repo := &kbDeleteTrackingKnowledgeRepo{populatedKBKnowledgeRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
		{ID: "knowledge-1", KnowledgeBaseID: "kb-1", EmbeddingModelID: "model-1"},
	}}}
	svc := &knowledgeBaseService{
		kgRepo:         repo,
		chunkRepo:      kbCleanupChunkRepo{},
		retrieveEngine: kbDeleteDeferredRegistry{err: retriever.ErrVectorStoreNotFound},
		ownership:      &kbDeleteOwnership{owned: map[string]uint64{storeID: 1}},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		VectorStoreID:   &storeIDSnapshot,
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.NoError(t, err)
	assert.Equal(t, 1, repo.deleteCalls)
}

func TestProcessKBDeleteVectorFailureKeepsKnowledgeForRetry(t *testing.T) {
	const storeID = "00000000-0000-0000-0000-0000000000ab"
	storeIDSnapshot := storeID
	engine := &kbDeleteFailingEngine{deleteErr: errors.New("vector delete unavailable")}
	repo := &kbDeleteTrackingKnowledgeRepo{populatedKBKnowledgeRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
		{ID: "knowledge-vector-retry", KnowledgeBaseID: "kb-vector-retry", EmbeddingModelID: "model-1"},
	}}}
	svc := &knowledgeBaseService{
		kgRepo:         repo,
		chunkRepo:      kbCleanupChunkRepo{},
		modelService:   kbCleanupModelService{},
		retrieveEngine: kbDeleteFixedRegistry{engine: engine},
		ownership:      &kbDeleteOwnership{owned: map[string]uint64{storeID: 1}},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:        1,
		KnowledgeBaseID: "kb-vector-retry",
		VectorStoreID:   &storeIDSnapshot,
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.ErrorContains(t, err, "vector delete unavailable")
	assert.Equal(t, 1, engine.deleteCalls)
	assert.Zero(t, repo.deleteCalls)
}

func TestProcessKBDeleteChunkFailureKeepsKnowledgeForRetry(t *testing.T) {
	repo := &kbDeleteTrackingKnowledgeRepo{populatedKBKnowledgeRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
		{ID: "knowledge-chunk-retry", KnowledgeBaseID: "kb-chunk-retry"},
	}}}
	svc := &knowledgeBaseService{
		kgRepo: repo,
		chunkRepo: kbDeleteFailingChunkRepo{
			deleteErr: errors.New("chunk delete unavailable"),
		},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:        1,
		KnowledgeBaseID: "kb-chunk-retry",
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.ErrorContains(t, err, "chunk delete unavailable")
	assert.Zero(t, repo.deleteCalls)
}

func TestProcessKBDeleteGraphFailureKeepsKnowledgeForRetry(t *testing.T) {
	repo := &kbDeleteTrackingKnowledgeRepo{populatedKBKnowledgeRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
		{ID: "knowledge-graph-retry", KnowledgeBaseID: "kb-graph-retry"},
	}}}
	svc := &knowledgeBaseService{
		kgRepo:      repo,
		chunkRepo:   kbCleanupChunkRepo{},
		graphEngine: kbDeleteFailingGraphRepo{deleteErr: errors.New("graph delete unavailable")},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:        1,
		KnowledgeBaseID: "kb-graph-retry",
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.ErrorContains(t, err, "graph delete unavailable")
	assert.Zero(t, repo.deleteCalls)
}

func TestProcessKBDeleteStorageResolutionFailureRetries(t *testing.T) {
	storageBackendID := "storage-backend-1"
	repo := &kbDeleteTrackingKnowledgeRepo{populatedKBKnowledgeRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
		{ID: "knowledge-1", KnowledgeBaseID: "kb-1", FilePath: "resource://source-file"},
	}}}
	svc := &knowledgeBaseService{
		kgRepo:    repo,
		chunkRepo: kbCleanupChunkRepo{},
		storageResolver: &kbDeleteStorageResolver{
			err: errors.New("storage unavailable"),
		},
		tenantRepo: kbDeleteTenantRepo{tenant: &types.Tenant{ID: 1}},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:         1,
		KnowledgeBaseID:  "kb-1",
		StorageBackendID: &storageBackendID,
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.ErrorContains(t, err, "storage unavailable")
	assert.Equal(t, 0, repo.deleteCalls, "存储实例不可用时不能删除知识行")
}

func TestProcessKBDeleteEngineResolutionFailureRetries(t *testing.T) {
	const storeID = "00000000-0000-0000-0000-0000000000dd"
	storeIDPtr := storeID
	repo := &kbDeleteTrackingKnowledgeRepo{populatedKBKnowledgeRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
		{ID: "knowledge-1", KnowledgeBaseID: "kb-1", EmbeddingModelID: "model-1"},
	}}}
	svc := &knowledgeBaseService{
		kgRepo:         repo,
		chunkRepo:      kbCleanupChunkRepo{},
		modelService:   kbCleanupModelService{},
		retrieveEngine: kbDeleteDeferredRegistry{err: context.Canceled},
		ownership:      &kbDeleteOwnership{owned: map[string]uint64{storeID: 1}},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		VectorStoreID:   &storeIDPtr,
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 0, repo.deleteCalls, "knowledge rows must not be deleted when engine resolution is deferred")
}

func TestProcessKBDeleteUnavailableStoreRetries(t *testing.T) {
	const storeID = "00000000-0000-0000-0000-0000000000ee"
	storeIDPtr := storeID
	repo := &kbDeleteTrackingKnowledgeRepo{populatedKBKnowledgeRepo: populatedKBKnowledgeRepo{items: []*types.Knowledge{
		{ID: "knowledge-1", KnowledgeBaseID: "kb-1", EmbeddingModelID: "model-1"},
	}}}
	svc := &knowledgeBaseService{
		kgRepo:         repo,
		chunkRepo:      kbCleanupChunkRepo{},
		modelService:   kbCleanupModelService{},
		retrieveEngine: kbDeleteDeferredRegistry{err: retriever.ErrVectorStoreUnavailable},
		ownership:      &kbDeleteOwnership{owned: map[string]uint64{storeID: 1}},
	}
	payload, err := json.Marshal(types.KBDeletePayload{
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		VectorStoreID:   &storeIDPtr,
	})
	require.NoError(t, err)

	err = svc.ProcessKBDelete(context.Background(), asynq.NewTask(types.TypeKBDelete, payload))

	require.ErrorIs(t, err, retriever.ErrVectorStoreUnavailable)
	assert.Equal(t, 0, repo.deleteCalls, "knowledge rows must not be deleted when engine resolution is deferred")
}

func TestCancelTasksForKnowledgeBaseForwardsKnowledgeIDs(t *testing.T) {
	inspector := &recordingKBTaskInspector{}
	svc := &knowledgeBaseService{taskInspector: inspector}

	svc.cancelTasksForKnowledgeBase(
		context.Background(),
		"kb-1",
		[]string{"knowledge-1", "knowledge-2"},
		[]string{"datasource-1"},
	)

	require.Len(t, inspector.calls, 1)
	assert.Equal(t, "kb-1", inspector.calls[0].kbID)
	assert.Equal(t, []string{"knowledge-1", "knowledge-2"}, inspector.calls[0].knowledgeIDs)
	assert.Equal(t, []string{"datasource-1"}, inspector.calls[0].dataSourceIDs)
}
