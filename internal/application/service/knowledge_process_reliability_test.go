package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type processReliabilityKnowledgeRepo struct {
	interfaces.KnowledgeRepository

	mu               sync.Mutex
	knowledge        *types.Knowledge
	getErr           error
	updateErr        error
	updateErrs       map[int]error
	updates          int
	columnUpdates    int
	columnUpdateErrs map[int]error
	atomicTenant     *types.Tenant
	reserveErrors    []error
	finalizeErrors   []error
	reserveDeltas    []int64
	finalizeDeltas   []int64
	beforePersistURL func()
}

func TestFailChunkProcessingPreservesLifecycleClaims(t *testing.T) {
	tests := []struct {
		status      string
		wantMoveErr bool
	}{
		{status: types.ParseStatusMoving, wantMoveErr: true},
		{status: types.ParseStatusDeleting},
		{status: types.ParseStatusCancelled},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			repo := &processReliabilityKnowledgeRepo{knowledge: &types.Knowledge{
				ID: "knowledge-1", TenantID: 7, ParseStatus: test.status,
			}}
			service := &knowledgeService{repo: repo}
			knowledge := *repo.knowledge

			err := service.failChunkProcessing(context.Background(), &knowledge, errors.New("parse failed"))

			if test.wantMoveErr {
				require.ErrorIs(t, err, types.ErrKnowledgeMoveInProgress)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, test.status, repo.knowledge.ParseStatus)
			require.Empty(t, repo.knowledge.ErrorMessage)
		})
	}
}

func TestDocumentWorkersRejectMalformedPayloads(t *testing.T) {
	service := &knowledgeService{}
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "document",
			run: func() error {
				return service.ProcessDocument(
					context.Background(), asynq.NewTask(types.TypeDocumentProcess, []byte("{")),
				)
			},
		},
		{
			name: "manual",
			run: func() error {
				return service.ProcessManualUpdate(
					context.Background(), asynq.NewTask(types.TypeManualProcess, []byte("{")),
				)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			require.Error(t, err)
			require.ErrorIs(t, err, asynq.SkipRetry)
		})
	}
}

func TestLiteRetryMetadataMarksFailedOnlyOnFinalAttempt(t *testing.T) {
	for _, worker := range []struct {
		name     string
		taskType string
		payload  interface{}
		run      func(*knowledgeService, context.Context, *asynq.Task) error
	}{
		{
			name:     "document",
			taskType: types.TypeDocumentProcess,
			payload: types.DocumentProcessPayload{
				TenantID: 1, KnowledgeID: "knowledge-retry", KnowledgeBaseID: "kb-1",
			},
			run: func(s *knowledgeService, ctx context.Context, task *asynq.Task) error {
				return s.ProcessDocument(ctx, task)
			},
		},
		{
			name:     "manual",
			taskType: types.TypeManualProcess,
			payload: types.ManualProcessPayload{
				TenantID: 1, KnowledgeID: "knowledge-retry", KnowledgeBaseID: "kb-1", Content: "正文",
			},
			run: func(s *knowledgeService, ctx context.Context, task *asynq.Task) error {
				return s.ProcessManualUpdate(ctx, task)
			},
		},
	} {
		t.Run(worker.name, func(t *testing.T) {
			encoded, err := json.Marshal(worker.payload)
			require.NoError(t, err)
			repo := &processReliabilityKnowledgeRepo{knowledge: &types.Knowledge{
				ID: "knowledge-retry", TenantID: 1, KnowledgeBaseID: "kb-1",
				ParseStatus: types.ParseStatusPending,
			}}
			temporaryErr := errors.New("知识库查询暂时失败")
			svc := &knowledgeService{
				repo:       repo,
				tenantRepo: &processReliabilityTenantRepo{tenant: &types.Tenant{ID: 1}},
				kbService:  processReliabilityKBService{err: temporaryErr},
			}
			task := asynq.NewTask(worker.taskType, encoded)

			firstErr := worker.run(svc, types.WithTaskRetryMetadata(context.Background(), 0, 1), task)
			require.ErrorContains(t, firstErr, temporaryErr.Error())
			require.Equal(t, types.ParseStatusPending, repo.knowledge.ParseStatus)
			require.Empty(t, repo.knowledge.ErrorMessage)

			finalErr := worker.run(svc, types.WithTaskRetryMetadata(context.Background(), 1, 1), task)
			require.Error(t, finalErr)
			require.Equal(t, types.ParseStatusFailed, repo.knowledge.ParseStatus)
			require.Contains(t, repo.knowledge.ErrorMessage, temporaryErr.Error())
		})
	}
}

func (r *processReliabilityKnowledgeRepo) GetKnowledgeByID(
	context.Context, uint64, string,
) (*types.Knowledge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.knowledge == nil {
		return nil, apprepo.ErrKnowledgeNotFound
	}
	copy := *r.knowledge
	return &copy, nil
}

func (r *processReliabilityKnowledgeRepo) UpdateKnowledge(
	_ context.Context, knowledge *types.Knowledge,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates++
	if err := r.updateErrs[r.updates]; err != nil {
		return err
	}
	if r.updateErr != nil {
		return r.updateErr
	}
	copy := *knowledge
	r.knowledge = &copy
	return nil
}

func (r *processReliabilityKnowledgeRepo) StartKnowledgeProcessing(
	_ context.Context,
	_ uint64,
	_ string,
	startedAt time.Time,
) (bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updates++
	if err := r.updateErrs[r.updates]; err != nil {
		return false, "", err
	}
	if r.updateErr != nil {
		return false, "", r.updateErr
	}
	if r.knowledge == nil {
		return false, types.ParseStatusDeleting, nil
	}
	status := r.knowledge.ParseStatus
	switch status {
	case "", types.ParseStatusPending, types.ParseStatusProcessing,
		types.ParseStatusFailed, types.ManualKnowledgeStatusDraft:
	default:
		return false, status, nil
	}
	r.knowledge.ParseStatus = types.ParseStatusProcessing
	r.knowledge.ErrorMessage = ""
	r.knowledge.UpdatedAt = startedAt
	return true, types.ParseStatusProcessing, nil
}

func (r *processReliabilityKnowledgeRepo) UpdateKnowledgeColumns(
	_ context.Context, _ string, values map[string]interface{},
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.columnUpdates++
	if err := r.columnUpdateErrs[r.columnUpdates]; err != nil {
		return err
	}
	if r.knowledge == nil {
		return apprepo.ErrKnowledgeNotFound
	}
	if value, ok := values["parse_status"].(string); ok {
		r.knowledge.ParseStatus = value
	}
	if value, ok := values["error_message"].(string); ok {
		r.knowledge.ErrorMessage = value
	}
	if value, ok := values["file_path"].(string); ok {
		r.knowledge.FilePath = value
	}
	if value, ok := values["file_name"].(string); ok {
		r.knowledge.FileName = value
	}
	if value, ok := values["file_type"].(string); ok {
		r.knowledge.FileType = value
	}
	if value, ok := values["file_hash"].(string); ok {
		r.knowledge.FileHash = value
	}
	if value, ok := values["file_size"].(int64); ok {
		r.knowledge.FileSize = value
	}
	if value, ok := values["source_file_quota_bytes"].(int64); ok {
		r.knowledge.SourceFileQuotaSize = value
	}
	return nil
}

func (r *processReliabilityKnowledgeRepo) PersistFileURLSource(
	_ context.Context,
	_ uint64,
	_ string,
	filePath string,
	fileName string,
	fileType string,
	fileSize int64,
	fileHash string,
	updatedAt time.Time,
) (bool, string, error) {
	if r.beforePersistURL != nil {
		hook := r.beforePersistURL
		r.beforePersistURL = nil
		hook()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.columnUpdates++
	if err := r.columnUpdateErrs[r.columnUpdates]; err != nil {
		return false, "", err
	}
	if r.knowledge == nil {
		return false, types.ParseStatusDeleting, nil
	}
	if r.knowledge.ParseStatus != types.ParseStatusProcessing {
		return false, r.knowledge.ParseStatus, nil
	}
	r.knowledge.FilePath = filePath
	r.knowledge.FileName = fileName
	r.knowledge.FileType = fileType
	r.knowledge.FileSize = fileSize
	r.knowledge.FileHash = fileHash
	r.knowledge.UpdatedAt = updatedAt
	return true, r.knowledge.ParseStatus, nil
}

func TestProcessDocumentFileURLDeleteClaimCompensatesUnownedObject(t *testing.T) {
	content := []byte("orphan candidate")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(content)
	}))
	defer server.Close()

	t.Setenv("SSRF_WHITELIST", "127.0.0.1,localhost,::1")
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)
	tenant := &types.Tenant{ID: 1, StorageQuota: 1024}
	repo := &processReliabilityKnowledgeRepo{
		knowledge: &types.Knowledge{
			ID: "knowledge-file-url-delete-race", TenantID: tenant.ID,
			KnowledgeBaseID: "kb-1", Type: "file_url", Source: server.URL + "/race.txt",
			ParseStatus: types.ParseStatusPending,
		},
		atomicTenant: tenant,
	}
	repo.beforePersistURL = func() {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		repo.knowledge.ParseStatus = types.ParseStatusDeleting
	}
	fileService := &processReliabilityFileService{objects: make(map[string][]byte)}
	service := &knowledgeService{
		repo:          repo,
		tenantRepo:    &processReliabilityTenantRepo{tenant: tenant},
		tenantService: processReliabilityTenantService{},
		kbService: processReliabilityKBService{kb: &types.KnowledgeBase{
			ID: "kb-1", TenantID: tenant.ID,
		}},
		fileSvc: fileService,
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID: tenant.ID, KnowledgeID: repo.knowledge.ID, KnowledgeBaseID: "kb-1",
		FileURL: server.URL + "/race.txt", FileName: "race.txt", FileType: "txt",
	})

	require.NoError(t, service.ProcessDocument(context.Background(), task))
	require.Empty(t, fileService.objects)
	require.Equal(t, types.ParseStatusDeleting, repo.knowledge.ParseStatus)
	require.Empty(t, repo.knowledge.FilePath)
	require.Zero(t, repo.knowledge.SourceFileQuotaBytes())
	require.Zero(t, tenant.StorageUsed)
}

func (r *processReliabilityKnowledgeRepo) KnowledgeHoldsFilePath(
	_ context.Context,
	_ uint64,
	_ string,
	filePath string,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.knowledge != nil && r.knowledge.FilePath == filePath, nil
}

func (r *processReliabilityKnowledgeRepo) FailKnowledgeProcessing(
	_ context.Context,
	_ uint64,
	_ string,
	errorMessage string,
	_ time.Time,
) (bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.columnUpdates++
	if err := r.columnUpdateErrs[r.columnUpdates]; err != nil {
		return false, "", err
	}
	if r.knowledge == nil {
		return false, types.ParseStatusDeleting, nil
	}
	switch r.knowledge.ParseStatus {
	case types.ParseStatusMoving, types.ParseStatusDeleting, types.ParseStatusCancelled:
		return false, r.knowledge.ParseStatus, nil
	}
	r.knowledge.ParseStatus = types.ParseStatusFailed
	r.knowledge.ErrorMessage = errorMessage
	return true, types.ParseStatusFailed, nil
}

func (r *processReliabilityKnowledgeRepo) ReserveSourceFileQuota(
	_ context.Context, _ uint64, _ string, targetBytes int64,
) (bool, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reserveErrors) > 0 {
		err := r.reserveErrors[0]
		r.reserveErrors = r.reserveErrors[1:]
		if err != nil {
			return false, 0, err
		}
	}
	if r.knowledge == nil {
		return false, 0, nil
	}
	if r.knowledge.ParseStatus == types.ParseStatusMoving {
		return false, 0, types.ErrKnowledgeMoveInProgress
	}
	if r.knowledge.ParseStatus == types.ParseStatusDeleting ||
		r.knowledge.ParseStatus == types.ParseStatusCancelled {
		return false, 0, nil
	}
	delta := targetBytes - r.knowledge.SourceFileQuotaBytes()
	r.reserveDeltas = append(r.reserveDeltas, delta)
	r.knowledge.SourceFileQuotaSize = targetBytes
	if r.atomicTenant != nil {
		r.atomicTenant.StorageUsed += delta
	}
	return true, delta, nil
}

func (r *processReliabilityKnowledgeRepo) FinalizeIndexedKnowledge(
	_ context.Context, final *types.Knowledge,
) (bool, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.finalizeErrors) > 0 {
		err := r.finalizeErrors[0]
		r.finalizeErrors = r.finalizeErrors[1:]
		if err != nil {
			return false, 0, err
		}
	}
	if r.knowledge == nil {
		return false, 0, nil
	}
	if r.knowledge.ParseStatus == types.ParseStatusMoving {
		return false, 0, types.ErrKnowledgeMoveInProgress
	}
	if r.knowledge.ParseStatus == types.ParseStatusDeleting ||
		r.knowledge.ParseStatus == types.ParseStatusCancelled {
		return false, 0, nil
	}
	delta := final.StorageSize - r.knowledge.StorageSize
	r.finalizeDeltas = append(r.finalizeDeltas, delta)
	copy := *final
	r.knowledge = &copy
	if r.atomicTenant != nil {
		r.atomicTenant.StorageUsed += delta
	}
	return true, delta, nil
}

func (r *processReliabilityKnowledgeRepo) ResetIndexedKnowledgeStorage(
	_ context.Context,
	_ uint64,
	_ string,
	_ string,
) (bool, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.knowledge == nil {
		return false, 0, nil
	}
	if r.knowledge.ParseStatus == types.ParseStatusMoving {
		return false, 0, types.ErrKnowledgeMoveInProgress
	}
	if r.knowledge.ParseStatus == types.ParseStatusDeleting ||
		r.knowledge.ParseStatus == types.ParseStatusCancelled {
		return false, 0, nil
	}
	delta := -r.knowledge.StorageSize
	r.knowledge.StorageSize = 0
	if r.atomicTenant != nil {
		r.atomicTenant.StorageUsed += delta
	}
	return true, delta, nil
}

type processReliabilityTenantRepo struct {
	interfaces.TenantRepository

	tenant    *types.Tenant
	err       error
	adjustErr error
	deltas    []int64
}

func (r *processReliabilityTenantRepo) GetTenantByID(
	context.Context, uint64,
) (*types.Tenant, error) {
	if r.err != nil {
		return nil, r.err
	}
	copy := *r.tenant
	return &copy, nil
}

func (r *processReliabilityTenantRepo) AdjustStorageUsed(_ context.Context, _ uint64, delta int64) error {
	r.deltas = append(r.deltas, delta)
	if r.adjustErr != nil {
		return r.adjustErr
	}
	r.tenant.StorageUsed += delta
	return nil
}

type processReliabilityKBService struct {
	interfaces.KnowledgeBaseService

	kb  *types.KnowledgeBase
	err error
}

type processingInterleavingKBService struct {
	interfaces.KnowledgeBaseService
	kb   *types.KnowledgeBase
	hook func()
}

func (s *processingInterleavingKBService) GetKnowledgeBaseByID(
	context.Context, string,
) (*types.KnowledgeBase, error) {
	if s.hook != nil {
		hook := s.hook
		s.hook = nil
		hook()
	}
	return s.kb, nil
}

func (s processReliabilityKBService) GetKnowledgeBaseByID(
	context.Context, string,
) (*types.KnowledgeBase, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.kb, nil
}

type processReliabilityModelService struct {
	interfaces.ModelService

	embedder embedding.Embedder
	err      error
}

func (s processReliabilityModelService) GetEmbeddingModel(
	context.Context, string,
) (embedding.Embedder, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.embedder, nil
}

type failingProcessRetrieveRegistry struct {
	interfaces.RetrieveEngineRegistry
	err error
}

type fixedStorageRetrieveEngine struct {
	parentChildRetrieveEngine
	storageSize int64
}

func (e *fixedStorageRetrieveEngine) EstimateStorageSize(
	context.Context, embedding.Embedder, []*types.IndexInfo, []types.RetrieverType,
) int64 {
	return e.storageSize
}

type processReliabilityTaskEnqueuer struct {
	err   error
	errs  []error
	tasks []*asynq.Task
}

type processReliabilityTenantService struct {
	interfaces.TenantService
}

func (processReliabilityTenantService) GetWeKnoraCloudCredentials(
	context.Context,
) *types.WeKnoraCloudCredentials {
	return nil
}

type processReliabilityFileService struct {
	mu sync.Mutex

	objects         map[string][]byte
	saveReaderCalls int
	saveBytesCalls  int
}

func (s *processReliabilityFileService) CheckConnectivity(context.Context) error { return nil }

func (s *processReliabilityFileService) SaveFile(
	context.Context, *multipart.FileHeader, uint64, string,
) (string, error) {
	return "", errors.New("测试未实现 SaveFile")
}

func (s *processReliabilityFileService) SaveBytes(
	_ context.Context, data []byte, tenantID uint64, fileName string, _ bool,
) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveBytesCalls++
	path := fmt.Sprintf("memory/%d/random-%d-%s", tenantID, s.saveBytesCalls, fileName)
	s.objects[path] = append([]byte(nil), data...)
	return path, nil
}

func (s *processReliabilityFileService) SaveReader(
	_ context.Context, reader io.Reader, _ int64, fileName, _ string, tenantID uint64, knowledgeID string,
) (string, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveReaderCalls++
	path := fmt.Sprintf("memory/%d/%s/%s", tenantID, knowledgeID, fileName)
	s.objects[path] = data
	return path, nil
}

func (s *processReliabilityFileService) PrepareReaderPath(
	_ context.Context, _ int64, fileName, _ string, tenantID uint64, knowledgeID string,
) (string, error) {
	return fmt.Sprintf("memory/%d/%s/%s", tenantID, knowledgeID, fileName), nil
}

func (s *processReliabilityFileService) SaveReaderTo(
	_ context.Context,
	reader io.Reader,
	_ int64,
	_ string,
	_ string,
	_ uint64,
	_ string,
	filePath string,
) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[filePath] = data
	return nil
}

func (s *processReliabilityFileService) FinalizeReaderPath(
	_ context.Context,
	_ int64,
	_ string,
	_ string,
	_ uint64,
	_ string,
	filePath string,
) (string, error) {
	return filePath, nil
}

func (s *processReliabilityFileService) GetFile(_ context.Context, filePath string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[filePath]
	if !ok {
		return nil, errors.New("对象不存在")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *processReliabilityFileService) GetFileURL(context.Context, string) (string, error) {
	return "", errors.New("测试未实现 GetFileURL")
}

func (s *processReliabilityFileService) DeleteFile(_ context.Context, filePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, filePath)
	return nil
}

func (s *processReliabilityFileService) CopyFile(
	context.Context, string, uint64, string,
) (string, error) {
	return "", errors.New("测试未实现 CopyFile")
}

func (e *processReliabilityTaskEnqueuer) Enqueue(
	task *asynq.Task, _ ...asynq.Option,
) (*asynq.TaskInfo, error) {
	e.tasks = append(e.tasks, task)
	if len(e.errs) > 0 {
		err := e.errs[0]
		e.errs = e.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	if e.err != nil {
		return nil, e.err
	}
	return &asynq.TaskInfo{ID: "task-1", Queue: types.QueuePostProcess}, nil
}

func (r failingProcessRetrieveRegistry) GetRetrieveEngineService(
	types.RetrieverEngineType,
) (interfaces.RetrieveEngineService, error) {
	return nil, r.err
}

func newDocumentProcessTask(t *testing.T, payload types.DocumentProcessPayload) *asynq.Task {
	t.Helper()
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	return asynq.NewTask(types.TypeDocumentProcess, encoded)
}

func TestProcessDocumentReturnsRetrieveEngineCreationErrorWithoutPanicking(t *testing.T) {
	t.Parallel()

	engineErr := errors.New("向量引擎暂时不可用")
	tenant := &types.Tenant{
		ID: 1,
		RetrieverEngines: types.RetrieverEngines{Engines: []types.RetrieverEngineParams{
			{
				RetrieverType:       types.VectorRetrieverType,
				RetrieverEngineType: types.PostgresRetrieverEngineType,
			},
		}},
	}
	repo := &processReliabilityKnowledgeRepo{knowledge: &types.Knowledge{
		ID:              "knowledge-1",
		TenantID:        tenant.ID,
		KnowledgeBaseID: "kb-1",
		ParseStatus:     types.ParseStatusPending,
	}}
	service := &knowledgeService{
		repo:       repo,
		tenantRepo: &processReliabilityTenantRepo{tenant: tenant},
		kbService: processReliabilityKBService{kb: &types.KnowledgeBase{
			ID:               "kb-1",
			TenantID:         tenant.ID,
			EmbeddingModelID: "embedding-1",
			IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
		}},
		modelService: processReliabilityModelService{embedder: parentChildEmbedder{}},
		chunkService: &parentChildChunkService{},
		retrieveEngine: failingProcessRetrieveRegistry{
			err: engineErr,
		},
	}

	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        tenant.ID,
		KnowledgeID:     "knowledge-1",
		KnowledgeBaseID: "kb-1",
		Passages:        []string{"需要索引的内容"},
	})

	require.NotPanics(t, func() {
		err := service.ProcessDocument(context.Background(), task)
		require.ErrorIs(t, err, engineErr)
	})
}

func TestProcessDocumentTreatsDeletedKnowledgeAsExpectedTermination(t *testing.T) {
	t.Parallel()

	service := &knowledgeService{
		repo: &processReliabilityKnowledgeRepo{getErr: apprepo.ErrKnowledgeNotFound},
		tenantRepo: &processReliabilityTenantRepo{tenant: &types.Tenant{
			ID: 1,
		}},
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        1,
		KnowledgeID:     "deleted-knowledge",
		KnowledgeBaseID: "kb-1",
		Passages:        []string{"不会处理"},
	})

	require.NoError(t, service.ProcessDocument(context.Background(), task))
}

func TestProcessDocumentRetriesWhileKnowledgeIsMoving(t *testing.T) {
	t.Parallel()

	repo := &processReliabilityKnowledgeRepo{knowledge: &types.Knowledge{
		ID:              "moving-knowledge",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		ParseStatus:     types.ParseStatusMoving,
	}}
	service := &knowledgeService{
		repo:       repo,
		tenantRepo: &processReliabilityTenantRepo{tenant: &types.Tenant{ID: 1}},
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        1,
		KnowledgeID:     "moving-knowledge",
		KnowledgeBaseID: "kb-1",
		Passages:        []string{"稍后处理"},
	})

	err := service.ProcessDocument(context.Background(), task)
	require.ErrorIs(t, err, types.ErrKnowledgeMoveInProgress)
	require.NotErrorIs(t, err, asynq.SkipRetry)
	require.Zero(t, repo.updates)
}

func TestDocumentAndManualAtomicStartDoNotOverwriteInterleavedMoveClaim(t *testing.T) {
	for _, worker := range []struct {
		name     string
		taskType string
		payload  interface{}
		run      func(*knowledgeService, *asynq.Task) error
	}{
		{
			name: "document", taskType: types.TypeDocumentProcess,
			payload: types.DocumentProcessPayload{
				TenantID: 1, KnowledgeID: "knowledge-interleave", KnowledgeBaseID: "kb-1",
				Passages: []string{"正文"},
			},
			run: func(s *knowledgeService, task *asynq.Task) error {
				return s.ProcessDocument(context.Background(), task)
			},
		},
		{
			name: "manual", taskType: types.TypeManualProcess,
			payload: types.ManualProcessPayload{
				TenantID: 1, KnowledgeID: "knowledge-interleave", KnowledgeBaseID: "kb-1", Content: "正文",
			},
			run: func(s *knowledgeService, task *asynq.Task) error {
				return s.ProcessManualUpdate(context.Background(), task)
			},
		},
	} {
		t.Run(worker.name, func(t *testing.T) {
			repo := &processReliabilityKnowledgeRepo{knowledge: &types.Knowledge{
				ID: "knowledge-interleave", TenantID: 1, KnowledgeBaseID: "kb-1",
				ParseStatus: types.ParseStatusPending,
			}}
			kbService := &processingInterleavingKBService{kb: &types.KnowledgeBase{
				ID: "kb-1", TenantID: 1,
			}}
			kbService.hook = func() {
				repo.mu.Lock()
				defer repo.mu.Unlock()
				repo.knowledge.ParseStatus = types.ParseStatusMoving
				repo.knowledge.Metadata = types.JSON(`{"_weknora_move_claim":{"task_id":"winner"}}`)
			}
			svc := &knowledgeService{
				repo:       repo,
				tenantRepo: &processReliabilityTenantRepo{tenant: &types.Tenant{ID: 1}},
				kbService:  kbService,
			}
			payload, err := json.Marshal(worker.payload)
			require.NoError(t, err)

			err = worker.run(svc, asynq.NewTask(worker.taskType, payload))

			require.ErrorIs(t, err, types.ErrKnowledgeMoveInProgress)
			require.Equal(t, types.ParseStatusMoving, repo.knowledge.ParseStatus)
			require.Contains(t, string(repo.knowledge.Metadata), "winner")
		})
	}
}

func TestProcessChunksRetriesWithoutOverwritingMovingClaim(t *testing.T) {
	t.Parallel()

	knowledge := &types.Knowledge{
		ID:              "moving-knowledge",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		ParseStatus:     types.ParseStatusMoving,
	}
	repo := &processReliabilityKnowledgeRepo{knowledge: knowledge}
	service := &knowledgeService{repo: repo}
	ctx := context.WithValue(context.Background(), types.TenantInfoContextKey, &types.Tenant{ID: 1})

	err := service.processChunks(ctx, &types.KnowledgeBase{ID: "kb-1"}, knowledge, nil)
	require.ErrorIs(t, err, types.ErrKnowledgeMoveInProgress)
	require.NotErrorIs(t, err, asynq.SkipRetry)
	require.Equal(t, types.ParseStatusMoving, repo.knowledge.ParseStatus)
	require.Zero(t, repo.updates)
	require.Zero(t, repo.columnUpdates)
}

func TestProcessDocumentReturnsTransientTenantLookupError(t *testing.T) {
	t.Parallel()

	lookupErr := errors.New("空间数据库暂时不可用")
	service := &knowledgeService{
		tenantRepo: &processReliabilityTenantRepo{err: lookupErr},
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        1,
		KnowledgeID:     "knowledge-1",
		KnowledgeBaseID: "kb-1",
	})

	require.ErrorIs(t, service.ProcessDocument(context.Background(), task), lookupErr)
}

func TestProcessDocumentReturnsTransientKnowledgeLookupError(t *testing.T) {
	t.Parallel()

	lookupErr := errors.New("知识数据库暂时不可用")
	service := &knowledgeService{
		repo: &processReliabilityKnowledgeRepo{getErr: lookupErr},
		tenantRepo: &processReliabilityTenantRepo{tenant: &types.Tenant{
			ID: 1,
		}},
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        1,
		KnowledgeID:     "knowledge-1",
		KnowledgeBaseID: "kb-1",
	})

	require.ErrorIs(t, service.ProcessDocument(context.Background(), task), lookupErr)
}

func TestProcessDocumentReturnsProcessingStatePersistenceError(t *testing.T) {
	t.Parallel()

	persistErr := errors.New("无法持久化处理中状态")
	repo := &processReliabilityKnowledgeRepo{
		knowledge: &types.Knowledge{
			ID:              "knowledge-1",
			TenantID:        1,
			KnowledgeBaseID: "kb-1",
			ParseStatus:     types.ParseStatusPending,
		},
		updateErr: persistErr,
	}
	service := &knowledgeService{
		repo:       repo,
		tenantRepo: &processReliabilityTenantRepo{tenant: &types.Tenant{ID: 1}},
		kbService: processReliabilityKBService{kb: &types.KnowledgeBase{
			ID:       "kb-1",
			TenantID: 1,
		}},
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        1,
		KnowledgeID:     "knowledge-1",
		KnowledgeBaseID: "kb-1",
		Passages:        []string{"内容"},
	})

	require.ErrorIs(t, service.ProcessDocument(context.Background(), task), persistErr)
}

func TestProcessDocumentReturnsFinalStatePersistenceError(t *testing.T) {
	t.Parallel()

	persistErr := errors.New("无法持久化解析结果")
	repo := &processReliabilityKnowledgeRepo{
		knowledge: &types.Knowledge{
			ID:              "knowledge-1",
			TenantID:        1,
			KnowledgeBaseID: "kb-1",
			ParseStatus:     types.ParseStatusPending,
		},
		finalizeErrors: []error{persistErr},
	}
	service := &knowledgeService{
		repo:         repo,
		tenantRepo:   &processReliabilityTenantRepo{tenant: &types.Tenant{ID: 1}},
		kbService:    processReliabilityKBService{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 1}},
		chunkService: &parentChildChunkService{},
		graphEngine:  parentChildGraphRepo{},
		task:         &processReliabilityTaskEnqueuer{},
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        1,
		KnowledgeID:     "knowledge-1",
		KnowledgeBaseID: "kb-1",
		Passages:        []string{"内容"},
	})

	require.ErrorIs(t, service.ProcessDocument(context.Background(), task), persistErr)
}

func TestProcessDocumentReturnsPostProcessEnqueueError(t *testing.T) {
	t.Parallel()

	enqueueErr := errors.New("后处理队列暂时不可用")
	repo := &processReliabilityKnowledgeRepo{knowledge: &types.Knowledge{
		ID:              "knowledge-1",
		TenantID:        1,
		KnowledgeBaseID: "kb-1",
		ParseStatus:     types.ParseStatusPending,
	}}
	service := &knowledgeService{
		repo:         repo,
		tenantRepo:   &processReliabilityTenantRepo{tenant: &types.Tenant{ID: 1}},
		kbService:    processReliabilityKBService{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 1}},
		chunkService: &parentChildChunkService{},
		graphEngine:  parentChildGraphRepo{},
		task:         &processReliabilityTaskEnqueuer{err: enqueueErr},
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        1,
		KnowledgeID:     "knowledge-1",
		KnowledgeBaseID: "kb-1",
		Passages:        []string{"内容"},
	})

	require.ErrorIs(t, service.ProcessDocument(context.Background(), task), enqueueErr)
}

func TestProcessDocumentReturnsStorageQuotaUpdateError(t *testing.T) {
	t.Parallel()

	quotaErr := errors.New("空间用量写入失败")
	tenant := &types.Tenant{
		ID: 1,
		RetrieverEngines: types.RetrieverEngines{Engines: []types.RetrieverEngineParams{
			{
				RetrieverType:       types.VectorRetrieverType,
				RetrieverEngineType: types.PostgresRetrieverEngineType,
			},
		}},
	}
	repo := &processReliabilityKnowledgeRepo{
		knowledge: &types.Knowledge{
			ID:              "knowledge-1",
			TenantID:        tenant.ID,
			KnowledgeBaseID: "kb-1",
			ParseStatus:     types.ParseStatusPending,
		},
		atomicTenant:   tenant,
		finalizeErrors: []error{quotaErr},
	}
	retrieveEngine := &fixedStorageRetrieveEngine{storageSize: 128}
	tenantRepo := &processReliabilityTenantRepo{tenant: tenant}
	service := &knowledgeService{
		repo:       repo,
		tenantRepo: tenantRepo,
		kbService: processReliabilityKBService{kb: &types.KnowledgeBase{
			ID:               "kb-1",
			TenantID:         tenant.ID,
			EmbeddingModelID: "embedding-1",
			IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
		}},
		modelService:   processReliabilityModelService{embedder: parentChildEmbedder{}},
		chunkService:   &parentChildChunkService{},
		retrieveEngine: parentChildRetrieveRegistry{engine: retrieveEngine},
		graphEngine:    parentChildGraphRepo{},
		task:           &processReliabilityTaskEnqueuer{},
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        tenant.ID,
		KnowledgeID:     "knowledge-1",
		KnowledgeBaseID: "kb-1",
		Passages:        []string{"内容"},
	})

	require.ErrorIs(t, service.ProcessDocument(context.Background(), task), quotaErr)
	require.Empty(t, tenantRepo.deltas)
	require.Zero(t, tenant.StorageUsed)
}

func TestProcessDocumentRetryDoesNotDoubleCountIndexedStorage(t *testing.T) {
	t.Parallel()

	tenant := &types.Tenant{
		ID: 1,
		RetrieverEngines: types.RetrieverEngines{Engines: []types.RetrieverEngineParams{
			{
				RetrieverType:       types.VectorRetrieverType,
				RetrieverEngineType: types.PostgresRetrieverEngineType,
			},
		}},
	}
	repo := &processReliabilityKnowledgeRepo{
		knowledge: &types.Knowledge{
			ID:              "knowledge-1",
			TenantID:        tenant.ID,
			KnowledgeBaseID: "kb-1",
			ParseStatus:     types.ParseStatusPending,
		},
		atomicTenant: tenant,
	}
	retrieveEngine := &fixedStorageRetrieveEngine{storageSize: 128}
	tenantRepo := &processReliabilityTenantRepo{tenant: tenant}
	enqueuer := &processReliabilityTaskEnqueuer{errs: []error{
		errors.New("首次入队响应失败"),
		nil,
	}}
	service := &knowledgeService{
		repo:       repo,
		tenantRepo: tenantRepo,
		kbService: processReliabilityKBService{kb: &types.KnowledgeBase{
			ID:               "kb-1",
			TenantID:         tenant.ID,
			EmbeddingModelID: "embedding-1",
			IndexingStrategy: types.IndexingStrategy{VectorEnabled: true},
		}},
		modelService:   processReliabilityModelService{embedder: parentChildEmbedder{}},
		chunkService:   &parentChildChunkService{},
		retrieveEngine: parentChildRetrieveRegistry{engine: retrieveEngine},
		graphEngine:    parentChildGraphRepo{},
		task:           enqueuer,
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        tenant.ID,
		KnowledgeID:     "knowledge-1",
		KnowledgeBaseID: "kb-1",
		Passages:        []string{"内容"},
	})

	require.Error(t, service.ProcessDocument(context.Background(), task))
	require.NoError(t, service.ProcessDocument(context.Background(), task))
	require.Empty(t, tenantRepo.deltas)
	require.Equal(t, []int64{128, 0}, repo.finalizeDeltas)
	require.Equal(t, int64(128), tenant.StorageUsed)
	require.Equal(t, int64(128), repo.knowledge.StorageSize)
}

func TestProcessDocumentFileURLRetryReusesStoredObjectAndSourceQuota(t *testing.T) {
	content := []byte("file URL content")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Disposition", `attachment; filename="document.txt"`)
		_, _ = w.Write(content)
	}))
	defer server.Close()

	t.Setenv("SSRF_WHITELIST", "127.0.0.1,localhost,::1")
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)

	tenant := &types.Tenant{ID: 1, StorageQuota: 1024}
	repo := &processReliabilityKnowledgeRepo{
		knowledge: &types.Knowledge{
			ID:              "knowledge-file-url",
			TenantID:        tenant.ID,
			KnowledgeBaseID: "kb-1",
			Type:            "file_url",
			Source:          server.URL + "/document.txt",
			ParseStatus:     types.ParseStatusPending,
		},
		atomicTenant: tenant,
	}
	fileService := &processReliabilityFileService{objects: make(map[string][]byte)}
	tenantRepo := &processReliabilityTenantRepo{tenant: tenant}
	enqueuer := &processReliabilityTaskEnqueuer{errs: []error{
		errors.New("首次后处理入队失败"),
		nil,
	}}
	service := &knowledgeService{
		repo:          repo,
		tenantRepo:    tenantRepo,
		tenantService: processReliabilityTenantService{},
		kbService: processReliabilityKBService{kb: &types.KnowledgeBase{
			ID:       "kb-1",
			TenantID: tenant.ID,
		}},
		fileSvc:      fileService,
		chunkService: &parentChildChunkService{},
		graphEngine:  parentChildGraphRepo{},
		task:         enqueuer,
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        tenant.ID,
		KnowledgeID:     repo.knowledge.ID,
		KnowledgeBaseID: repo.knowledge.KnowledgeBaseID,
		FileURL:         repo.knowledge.Source,
		FileName:        "document.txt",
		FileType:        "txt",
	})

	require.Error(t, service.ProcessDocument(context.Background(), task))
	require.NoError(t, service.ProcessDocument(context.Background(), task))

	wantHash := fmt.Sprintf("%x", sha256.Sum256(content))
	require.Equal(t, int32(1), requests.Load())
	require.Equal(t, 1, fileService.saveReaderCalls)
	require.Zero(t, fileService.saveBytesCalls)
	require.Equal(t, "memory/1/knowledge-file-url/document.txt", repo.knowledge.FilePath)
	require.Equal(t, int64(len(content)), repo.knowledge.FileSize)
	require.Equal(t, wantHash, repo.knowledge.FileHash)
	require.Equal(t, int64(len(content)), repo.knowledge.SourceFileQuotaBytes())
	require.Empty(t, tenantRepo.deltas)
	require.Equal(t, []int64{int64(len(content)), 0}, repo.reserveDeltas)
}

func TestProcessDocumentFileURLMetadataRetryCleansUnownedStableObject(t *testing.T) {
	content := []byte("stable file URL content")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(content)
	}))
	defer server.Close()

	t.Setenv("SSRF_WHITELIST", "127.0.0.1,localhost,::1")
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)

	persistErr := errors.New("文件路径写库暂时失败")
	tenant := &types.Tenant{ID: 1, StorageQuota: 1024}
	repo := &processReliabilityKnowledgeRepo{
		knowledge: &types.Knowledge{
			ID:              "knowledge-file-url-stable",
			TenantID:        tenant.ID,
			KnowledgeBaseID: "kb-1",
			Type:            "file_url",
			Source:          server.URL + "/stable.txt",
			ParseStatus:     types.ParseStatusPending,
		},
		columnUpdateErrs: map[int]error{1: persistErr},
		atomicTenant:     tenant,
	}
	fileService := &processReliabilityFileService{objects: make(map[string][]byte)}
	tenantRepo := &processReliabilityTenantRepo{tenant: tenant}
	service := &knowledgeService{
		repo:          repo,
		tenantRepo:    tenantRepo,
		tenantService: processReliabilityTenantService{},
		kbService: processReliabilityKBService{kb: &types.KnowledgeBase{
			ID:       "kb-1",
			TenantID: tenant.ID,
		}},
		fileSvc:      fileService,
		chunkService: &parentChildChunkService{},
		graphEngine:  parentChildGraphRepo{},
		task:         &processReliabilityTaskEnqueuer{},
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        tenant.ID,
		KnowledgeID:     repo.knowledge.ID,
		KnowledgeBaseID: repo.knowledge.KnowledgeBaseID,
		FileURL:         repo.knowledge.Source,
		FileName:        "stable.txt",
		FileType:        "txt",
	})

	require.ErrorIs(t, service.ProcessDocument(context.Background(), task), persistErr)
	require.NoError(t, service.ProcessDocument(context.Background(), task))

	require.Equal(t, int32(2), requests.Load())
	require.Equal(t, 2, fileService.saveReaderCalls)
	require.Len(t, fileService.objects, 1)
	require.Equal(t, "memory/1/knowledge-file-url-stable/stable.txt", repo.knowledge.FilePath)
	require.Equal(t, int64(len(content)), repo.knowledge.FileSize)
	require.Empty(t, tenantRepo.deltas)
	require.Equal(t, []int64{int64(len(content))}, repo.reserveDeltas)
}

func TestProcessDocumentFileURLAtomicQuotaFailureRetriesWithoutPartialWrite(t *testing.T) {
	content := []byte("quota marker retry")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(content)
	}))
	defer server.Close()

	t.Setenv("SSRF_WHITELIST", "127.0.0.1,localhost,::1")
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)

	markerErr := errors.New("源文件配额标记写入失败")
	tenant := &types.Tenant{ID: 1, StorageQuota: 1024}
	repo := &processReliabilityKnowledgeRepo{
		knowledge: &types.Knowledge{
			ID:              "knowledge-file-url-quota",
			TenantID:        tenant.ID,
			KnowledgeBaseID: "kb-1",
			Type:            "file_url",
			Source:          server.URL + "/quota.txt",
			ParseStatus:     types.ParseStatusPending,
		},
		atomicTenant:  tenant,
		reserveErrors: []error{markerErr, nil},
	}
	fileService := &processReliabilityFileService{objects: make(map[string][]byte)}
	tenantRepo := &processReliabilityTenantRepo{tenant: tenant}
	service := &knowledgeService{
		repo:          repo,
		tenantRepo:    tenantRepo,
		tenantService: processReliabilityTenantService{},
		kbService: processReliabilityKBService{kb: &types.KnowledgeBase{
			ID:       "kb-1",
			TenantID: tenant.ID,
		}},
		fileSvc:      fileService,
		chunkService: &parentChildChunkService{},
		graphEngine:  parentChildGraphRepo{},
		task:         &processReliabilityTaskEnqueuer{},
	}
	task := newDocumentProcessTask(t, types.DocumentProcessPayload{
		TenantID:        tenant.ID,
		KnowledgeID:     repo.knowledge.ID,
		KnowledgeBaseID: repo.knowledge.KnowledgeBaseID,
		FileURL:         repo.knowledge.Source,
		FileName:        "quota.txt",
		FileType:        "txt",
	})

	require.ErrorIs(t, service.ProcessDocument(context.Background(), task), markerErr)
	require.NoError(t, service.ProcessDocument(context.Background(), task))

	size := int64(len(content))
	require.Equal(t, int32(1), requests.Load())
	require.Empty(t, tenantRepo.deltas)
	require.Equal(t, []int64{size}, repo.reserveDeltas)
	require.Equal(t, size, tenant.StorageUsed)
	require.Equal(t, size, repo.knowledge.SourceFileQuotaBytes())
}
