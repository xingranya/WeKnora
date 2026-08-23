package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/datasource"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/storageallowlist"
	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

// ErrInvalidTenantID represents an error for invalid tenant ID
var ErrInvalidTenantID = errors.New("invalid tenant ID")

const kbTaskCleanupTimeout = 5 * time.Second

// knowledgeBaseService implements the knowledge base service interface
type knowledgeBaseService struct {
	repo            interfaces.KnowledgeBaseRepository
	kgRepo          interfaces.KnowledgeRepository
	chunkRepo       interfaces.ChunkRepository
	shareRepo       interfaces.KBShareRepository
	kbShareService  interfaces.KBShareService
	modelService    interfaces.ModelService
	retrieveEngine  interfaces.RetrieveEngineRegistry
	ownership       retriever.TenantStoreOwnership
	tenantRepo      interfaces.TenantRepository
	fileSvc         interfaces.FileService
	storageResolver interfaces.StorageBackendResolver
	resourceCatalog interfaces.ResourceCatalog
	graphEngine     interfaces.RetrieveGraphRepository
	asynqClient     interfaces.TaskEnqueuer
	taskInspector   interfaces.TaskInspector
	taskPendingRepo interfaces.TaskPendingOpsRepository
	dsRepo          interfaces.DataSourceRepository
	syncLogRepo     interfaces.SyncLogRepository
	dsScheduler     *datasource.Scheduler
	audit           interfaces.AuditLogService
}

func knowledgeInFlightParseStatuses() []string {
	return []string{
		types.ParseStatusPending,
		types.ParseStatusProcessing,
		types.ParseStatusFinalizing,
		types.ParseStatusMoving,
	}
}

type knowledgeListAndQuotaDeleter interface {
	DeleteKnowledgeListAndAdjustStorage(
		ctx context.Context,
		tenantID uint64,
		knowledgeBaseID string,
		ids []string,
	) error
}

type knowledgeListDeleteClaimer interface {
	ClaimKnowledgeListForKBDelete(
		ctx context.Context,
		tenantID uint64,
		knowledgeBaseID string,
		ids []string,
	) ([]*types.Knowledge, error)
}

type deletedKnowledgeBaseLookup interface {
	GetKnowledgeBaseByIDAndTenantUnscoped(
		ctx context.Context,
		id string,
		tenantID uint64,
	) (*types.KnowledgeBase, error)
}

type deletedKnowledgeBaseRecoveryLookup interface {
	ListDeletedKnowledgeBasesWithActiveKnowledge(
		ctx context.Context,
		limit int,
	) ([]*types.KnowledgeBase, error)
}

type deletedKnowledgeBaseCleanupDataSourceLookup interface {
	ListKnowledgeBaseCleanupDataSourceIDs(
		ctx context.Context,
		knowledgeBaseID string,
	) ([]string, error)
}

func kbDeleteTaskID(knowledgeBaseID string) string {
	return "kb-delete-" + knowledgeBaseID
}

func (s *knowledgeBaseService) recoverConflictingKBDeleteTask(
	ctx context.Context,
	knowledgeBaseID string,
	task *asynq.Task,
) error {
	runtimeInspector, ok := s.taskInspector.(interfaces.RuntimeTaskInspector)
	if !ok {
		return nil
	}
	taskID := kbDeleteTaskID(knowledgeBaseID)
	existing, supported, err := runtimeInspector.GetRuntimeTask(
		ctx,
		types.QueueMaintenance,
		taskID,
	)
	if err != nil {
		return fmt.Errorf("inspect conflicting knowledge base delete task: %w", err)
	}
	if !supported || existing == nil {
		return nil
	}
	if existing.State != types.RuntimeTaskArchived && existing.State != types.RuntimeTaskCompleted {
		return nil
	}
	supported, err = runtimeInspector.ForceDeleteRuntimeTask(ctx, types.QueueMaintenance, taskID)
	if err != nil {
		return fmt.Errorf("remove terminal knowledge base delete task: %w", err)
	}
	if !supported {
		return errors.New("runtime task deletion is unavailable")
	}
	_, err = s.asynqClient.Enqueue(task, asynq.TaskID(taskID))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		return nil
	}
	return err
}

// NewKnowledgeBaseService creates a new knowledge base service
func NewKnowledgeBaseService(repo interfaces.KnowledgeBaseRepository,
	kgRepo interfaces.KnowledgeRepository,
	chunkRepo interfaces.ChunkRepository,
	shareRepo interfaces.KBShareRepository,
	kbShareService interfaces.KBShareService,
	modelService interfaces.ModelService,
	retrieveEngine interfaces.RetrieveEngineRegistry,
	ownership retriever.TenantStoreOwnership,
	tenantRepo interfaces.TenantRepository,
	fileSvc interfaces.FileService,
	storageResolver interfaces.StorageBackendResolver,
	resourceCatalog interfaces.ResourceCatalog,
	graphEngine interfaces.RetrieveGraphRepository,
	asynqClient interfaces.TaskEnqueuer,
	taskInspector interfaces.TaskInspector,
	taskPendingRepo interfaces.TaskPendingOpsRepository,
	dsRepo interfaces.DataSourceRepository,
	syncLogRepo interfaces.SyncLogRepository,
	dsScheduler *datasource.Scheduler,
	audit interfaces.AuditLogService,
) interfaces.KnowledgeBaseService {
	return &knowledgeBaseService{
		repo:            repo,
		kgRepo:          kgRepo,
		chunkRepo:       chunkRepo,
		shareRepo:       shareRepo,
		kbShareService:  kbShareService,
		modelService:    modelService,
		retrieveEngine:  retrieveEngine,
		ownership:       ownership,
		tenantRepo:      tenantRepo,
		fileSvc:         fileSvc,
		storageResolver: storageResolver,
		resourceCatalog: resourceCatalog,
		graphEngine:     graphEngine,
		asynqClient:     asynqClient,
		taskInspector:   taskInspector,
		taskPendingRepo: taskPendingRepo,
		dsRepo:          dsRepo,
		syncLogRepo:     syncLogRepo,
		dsScheduler:     dsScheduler,
		audit:           audit,
	}
}

// GetRepository gets the knowledge base repository
// Parameters:
//   - ctx: Context with authentication and request information
//
// Returns:
//   - interfaces.KnowledgeBaseRepository: Knowledge base repository
func (s *knowledgeBaseService) GetRepository() interfaces.KnowledgeBaseRepository {
	return s.repo
}

// CreateKnowledgeBase creates a new knowledge base.
//
// When VectorStoreID is set, the binding is validated against the caller's
// tenant scope and the engine registry before persisting. A nil or
// empty-string VectorStoreID is normalized to nil ("use the tenant's
// effective engines") to match the retrieve-engine factory's pre-condition.
func (s *knowledgeBaseService) CreateKnowledgeBase(ctx context.Context,
	kb *types.KnowledgeBase,
) (*types.KnowledgeBase, error) {
	// Generate UUID and set creation timestamps
	if kb.ID == "" {
		kb.ID = uuid.New().String()
	}
	kb.CreatedAt = time.Now()
	kb.TenantID = types.MustTenantIDFromContext(ctx)
	kb.UpdatedAt = time.Now()
	// Record the creator so RBAC's RequireOwnershipOrRole can let
	// Contributors edit their own KBs without granting them tenant-wide
	// edit rights. The X-API-Key auth path attaches a synthetic
	// `system-<tenantID>` user; we deliberately skip those so the KB
	// stays tenant-owned (CreatorID == ""), which matches the original
	// API-key semantics (any human Admin can manage it) and prevents a
	// later "list KBs by creator" feature from surfacing rows nobody can
	// re-attribute.
	if uid, ok := types.UserIDFromContext(ctx); ok && !types.IsSyntheticUserID(uid) {
		kb.CreatorID = uid
	}
	kb.EnsureDefaults()
	applyTenantDefaultStorageProvider(ctx, kb)
	if err := s.applyAndValidateStorageBackend(ctx, kb); err != nil {
		return nil, err
	}

	// Fold empty-string vector_store_id into nil so this path and the
	// retrieve-engine factory's pre-condition share a single representation.
	wasEmpty := kb.VectorStoreID != nil && *kb.VectorStoreID == ""
	kb.Normalize()
	if wasEmpty {
		logger.Debugf(ctx,
			"[kb.create] empty vector_store_id normalized to nil for tenant=%d",
			kb.TenantID)
	}

	if kb.HasVectorStore() {
		if err := s.validateVectorStoreBinding(ctx, kb.TenantID, *kb.VectorStoreID); err != nil {
			return nil, err
		}
	}

	logger.Infof(ctx, "Creating knowledge base, ID: %s, tenant ID: %d, name: %s", kb.ID, kb.TenantID, kb.Name)

	if err := s.repo.CreateKnowledgeBase(ctx, kb); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": kb.ID,
			"tenant_id":         kb.TenantID,
		})
		return nil, err
	}
	recordKBActivity(ctx, s.audit, kb.TenantID, kb.ID, types.AuditActionKBCreated,
		"knowledge_base", kb.ID, types.AuditOutcomeSuccess, map[string]any{
			"name": kb.Name, "type": kb.Type,
		})

	logger.Infof(ctx, "Knowledge base created successfully, ID: %s, name: %s", kb.ID, kb.Name)
	return kb, nil
}

func (s *knowledgeBaseService) applyAndValidateStorageBackend(ctx context.Context, kb *types.KnowledgeBase) error {
	if s.storageResolver == nil || kb == nil {
		return nil
	}
	tenant, _ := types.TenantInfoFromContext(ctx)
	if tenant == nil {
		return apperrors.NewBadRequestError("workspace context missing")
	}
	id := ""
	if kb.StorageBackendID != nil {
		id = strings.TrimSpace(*kb.StorageBackendID)
	}
	provider := kb.GetStorageProvider()
	// A newly created KB without an explicit instance follows the concrete
	// tenant default. The legacy provider is only a fallback for workspaces
	// that have not been migrated yet.
	if id == "" && tenant.DefaultStorageBackendID != nil && strings.TrimSpace(*tenant.DefaultStorageBackendID) != "" {
		provider = ""
	}
	backend, err := s.storageResolver.ResolveBackend(ctx, tenant, id, provider)
	if err != nil {
		return apperrors.NewBadRequestError("storage backend is unavailable").WithDetails(err.Error())
	}
	if backend == nil {
		return nil
	}
	kb.StorageBackendID = &backend.ID
	kb.SetStorageProvider(backend.Provider)
	return nil
}

// applyTenantDefaultStorageProvider fills an empty KB storage provider from the
// tenant's global default (Settings → Storage engine). Frontend should send the
// same value; this keeps API clients and legacy UIs consistent.
func applyTenantDefaultStorageProvider(ctx context.Context, kb *types.KnowledgeBase) {
	if kb == nil || strings.TrimSpace(kb.GetStorageProvider()) != "" {
		return
	}
	tenant, _ := ctx.Value(types.TenantInfoContextKey).(*types.Tenant)
	provider := ""
	if tenant != nil && tenant.StorageEngineConfig != nil {
		provider = strings.ToLower(strings.TrimSpace(tenant.StorageEngineConfig.DefaultProvider))
	}
	if provider == "" || !storageallowlist.IsAllowed(provider) {
		provider = storageallowlist.FirstAllowed()
	}
	if provider == "" {
		return
	}
	kb.SetStorageProvider(provider)
}

// validateVectorStoreBinding routes through retriever.VerifyBinding so the
// ownership + registry sentinel hierarchy stays the single source of truth.
// The service layer's responsibility is to:
//
//  1. fast-reject malformed UUIDs (cheap pre-flight that also avoids a DB
//     round trip for type-confusion inputs like "' OR 1=1 --"),
//  2. translate retriever sentinels into user-facing AppErrors with
//     generic messages and the typed error codes.
//
// UUID parse failures map to the same "vector store not found" message as
// cross-tenant attempts to avoid an enumeration oracle that distinguishes
// "malformed input" from "non-existent UUID".
func (s *knowledgeBaseService) validateVectorStoreBinding(
	ctx context.Context, tenantID uint64, storeID string,
) error {
	sanitized := secutils.SanitizeForLog(storeID)

	if _, err := uuid.Parse(storeID); err != nil {
		logger.WarnWithFields(ctx, logger.Fields{
			"tenant_id": tenantID,
			"store_id":  sanitized,
			"reason":    "malformed vector_store_id",
		}, "[kb.create] vector store id is not a valid UUID")
		return apperrors.NewVectorStoreBindingInvalidError("vector store not found")
	}

	switch err := retriever.VerifyBinding(
		ctx, s.retrieveEngine, s.ownership, tenantID, storeID,
	); {
	case err == nil:
		return nil
	case errors.Is(err, retriever.ErrVectorStoreForbidden):
		logger.WarnWithFields(ctx, logger.Fields{
			"tenant_id": tenantID,
			"store_id":  sanitized,
			"reason":    "cross-tenant or unknown store",
		}, "[kb.create] vector store not owned by tenant")
		return apperrors.NewVectorStoreBindingInvalidError("vector store not found")
	case errors.Is(err, retriever.ErrVectorStoreNotFound),
		errors.Is(err, retriever.ErrVectorStoreUnavailable):
		logger.WarnWithFields(ctx, logger.Fields{
			"tenant_id": tenantID,
			"store_id":  sanitized,
			"reason":    "store recorded in DB but no engine could be resolved",
		}, "[kb.create] vector store currently unavailable")
		return apperrors.NewVectorStoreUnavailableError(
			"vector store is currently unavailable; check its connection configuration")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The caller went away or ran out of time while the binding was being
		// verified, which can now include rebuilding the store's engine. That
		// is not a server fault, so it must not be logged and answered as one.
		return err
	default:
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"tenant_id": tenantID,
			"store_id":  sanitized,
			"reason":    "binding verification failed",
		})
		return apperrors.NewInternalServerError("failed to verify vector store binding")
	}
}

// GetKnowledgeBaseByID retrieves a knowledge base by its ID
func (s *knowledgeBaseService) GetKnowledgeBaseByID(ctx context.Context, id string) (*types.KnowledgeBase, error) {
	if id == "" {
		logger.Error(ctx, "Knowledge base ID is empty")
		return nil, errors.New("knowledge base ID cannot be empty")
	}

	kb, err := s.repo.GetKnowledgeBaseByID(ctx, id)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": id,
		})
		return nil, err
	}

	kb.EnsureDefaults()
	return kb, nil
}

// GetKnowledgeBaseByIDOnly retrieves knowledge base by ID without tenant filter
// Used for cross-tenant shared KB access where permission is checked elsewhere
func (s *knowledgeBaseService) GetKnowledgeBaseByIDOnly(ctx context.Context, id string) (*types.KnowledgeBase, error) {
	if id == "" {
		logger.Error(ctx, "Knowledge base ID is empty")
		return nil, errors.New("knowledge base ID cannot be empty")
	}

	kb, err := s.repo.GetKnowledgeBaseByID(ctx, id)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": id,
		})
		return nil, err
	}

	kb.EnsureDefaults()
	return kb, nil
}

// GetKnowledgeBasesByIDsOnly retrieves knowledge bases by IDs without tenant filter (batch).
func (s *knowledgeBaseService) GetKnowledgeBasesByIDsOnly(ctx context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	kbs, err := s.repo.GetKnowledgeBaseByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, kb := range kbs {
		if kb != nil {
			kb.EnsureDefaults()
		}
	}
	return kbs, nil
}

// ListKnowledgeBases returns all knowledge bases for a tenant
func (s *knowledgeBaseService) ListKnowledgeBases(ctx context.Context) ([]*types.KnowledgeBase, error) {
	tenantID := types.MustTenantIDFromContext(ctx)

	kbs, err := s.repo.ListKnowledgeBasesByTenantID(ctx, tenantID)
	if err != nil {
		for _, kb := range kbs {
			kb.EnsureDefaults()
		}

		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"tenant_id": tenantID,
		})
		return nil, err
	}

	// Query knowledge count and chunk count for each knowledge base
	for _, kb := range kbs {
		kb.EnsureDefaults()

		// Get knowledge count
		switch kb.Type {
		case types.KnowledgeBaseTypeDocument:
			knowledgeCount, err := s.kgRepo.CountKnowledgeByKnowledgeBaseID(ctx, tenantID, kb.ID)
			if err != nil {
				logger.Warnf(ctx, "Failed to get knowledge count for knowledge base %s: %v", kb.ID, err)
			} else {
				kb.KnowledgeCount = knowledgeCount
			}
		case types.KnowledgeBaseTypeFAQ:
			// Get chunk count
			chunkCount, err := s.chunkRepo.CountChunksByKnowledgeBaseID(ctx, tenantID, kb.ID)
			if err != nil {
				logger.Warnf(ctx, "Failed to get chunk count for knowledge base %s: %v", kb.ID, err)
			} else {
				kb.ChunkCount = chunkCount
			}
		}

		// Check if there is a processing import task
		processingCount, err := s.kgRepo.CountKnowledgeByStatus(
			ctx,
			tenantID,
			kb.ID,
			knowledgeInFlightParseStatuses(),
		)
		if err != nil {
			logger.Warnf(ctx, "Failed to check processing status for knowledge base %s: %v", kb.ID, err)
		} else {
			kb.IsProcessing = processingCount > 0
			kb.ProcessingCount = processingCount
		}
	}

	// Per-user pin stamping + ordering. The "main" list view is the
	// only path that needs to honour the caller's personal pin set;
	// agent/share/IM callers go through ListKnowledgeBasesByTenantID
	// which also enriches but keys off the user in their own context.
	if userID, ok := types.UserIDFromContext(ctx); ok && userID != "" {
		s.applyUserKBPins(ctx, tenantID, userID, kbs)
	}
	return kbs, nil
}

// ListKnowledgeBasesByTenantID returns all knowledge bases for the given tenant (e.g. for shared agent context).
func (s *knowledgeBaseService) ListKnowledgeBasesByTenantID(ctx context.Context, tenantID uint64) ([]*types.KnowledgeBase, error) {
	kbs, err := s.repo.ListKnowledgeBasesByTenantID(ctx, tenantID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"tenant_id": tenantID,
		})
		return nil, err
	}
	for _, kb := range kbs {
		kb.EnsureDefaults()
		switch kb.Type {
		case types.KnowledgeBaseTypeDocument:
			if cnt, err := s.kgRepo.CountKnowledgeByKnowledgeBaseID(ctx, tenantID, kb.ID); err == nil {
				kb.KnowledgeCount = cnt
			}
		case types.KnowledgeBaseTypeFAQ:
			if cnt, err := s.chunkRepo.CountChunksByKnowledgeBaseID(ctx, tenantID, kb.ID); err == nil {
				kb.ChunkCount = cnt
			}
		}
		if processingCount, err := s.kgRepo.CountKnowledgeByStatus(ctx, tenantID, kb.ID, knowledgeInFlightParseStatuses()); err == nil {
			kb.IsProcessing = processingCount > 0
			kb.ProcessingCount = processingCount
		}
	}

	// Stamp pin state from the caller's perspective. The tenantID
	// argument may not match the caller's own tenant (this method is
	// also used to list a shared-agent's source-tenant KBs); we still
	// scope user_kb_pins by `tenantID` since a pin tied to one tenant
	// shouldn't surface when browsing another tenant's KBs.
	if userID, ok := types.UserIDFromContext(ctx); ok && userID != "" {
		s.applyUserKBPins(ctx, tenantID, userID, kbs)
	}
	return kbs, nil
}

// FillKnowledgeBaseCounts fills KnowledgeCount, ChunkCount, IsProcessing, ProcessingCount for the given KB using kb.TenantID.
func (s *knowledgeBaseService) FillKnowledgeBaseCounts(ctx context.Context, kb *types.KnowledgeBase) error {
	if kb == nil {
		return nil
	}
	tenantID := kb.TenantID
	kb.EnsureDefaults()
	switch kb.Type {
	case types.KnowledgeBaseTypeDocument:
		if cnt, err := s.kgRepo.CountKnowledgeByKnowledgeBaseID(ctx, tenantID, kb.ID); err == nil {
			kb.KnowledgeCount = cnt
		}
	case types.KnowledgeBaseTypeFAQ:
		if cnt, err := s.chunkRepo.CountChunksByKnowledgeBaseID(ctx, tenantID, kb.ID); err == nil {
			kb.ChunkCount = cnt
		}
	}
	if processingCount, err := s.kgRepo.CountKnowledgeByStatus(ctx, tenantID, kb.ID, knowledgeInFlightParseStatuses()); err == nil {
		kb.IsProcessing = processingCount > 0
		kb.ProcessingCount = processingCount
	}
	return nil
}

// UpdateKnowledgeBase updates a knowledge base's mutable properties.
//
// IMPORTANT — vector_store_id immutability contract:
// The vector_store_id binding is deliberately not accepted by this method.
// Two layers enforce immutability:
//
//  1. ORM layer: the GORM tag `<-:create` on KnowledgeBase.VectorStoreID
//     makes every UPDATE path (Save / Updates / Select-Updates) a no-op for
//     that column. Verified by repository/knowledgebase_sqlite_test.go.
//  2. Service layer: this method intentionally omits VectorStoreID from its
//     parameter list, and the matching handler DTO UpdateKnowledgeBaseRequest
//     omits the field as well. A reflection-based regression test
//     (handler/knowledgebase_request_test.go) fails if either DTO field
//     is added back, alerting future maintainers.
//
// Any future cross-store rebind workflow must use raw SQL through a
// dedicated repository method — the only sanctioned write path post-creation.
func (s *knowledgeBaseService) UpdateKnowledgeBase(ctx context.Context,
	id string,
	name string,
	description string,
	config *types.KnowledgeBaseConfig,
) (*types.KnowledgeBase, error) {
	if id == "" {
		logger.Error(ctx, "Knowledge base ID is empty")
		return nil, errors.New("knowledge base ID cannot be empty")
	}

	logger.Infof(ctx, "Updating knowledge base, ID: %s, name: %s", id, name)

	// Get existing knowledge base
	kb, err := s.repo.GetKnowledgeBaseByID(ctx, id)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": id,
		})
		return nil, err
	}

	changedFields := make([]string, 0, 3)
	if kb.Name != name {
		changedFields = append(changedFields, "name")
	}
	if kb.Description != description {
		changedFields = append(changedFields, "description")
	}
	if config != nil {
		changedFields = append(changedFields, "config")
	}

	// Update the knowledge base properties
	kb.Name = name
	kb.Description = description
	if config != nil {
		kb.ChunkingConfig = config.ChunkingConfig
		kb.ImageProcessingConfig = config.ImageProcessingConfig
		if config.FAQConfig != nil {
			kb.FAQConfig = config.FAQConfig
		}
		if config.WikiConfig != nil {
			kb.WikiConfig = config.WikiConfig
		}
		if config.AutoTagConfig != nil {
			config.AutoTagConfig.Normalize()
			kb.AutoTagConfig = config.AutoTagConfig
		}
		// Update indexing strategy — syncs to ExtractConfig for backward compat
		if config.IndexingStrategy != nil {
			if !config.IndexingStrategy.HasAnyIndexing() {
				return nil, errors.New("at least one indexing strategy must be enabled")
			}
			kb.IndexingStrategy = *config.IndexingStrategy
			// Ensure WikiConfig exists when wiki indexing is enabled so that
			// wiki-specific tunables (synthesis model, granularity, …) have a home.
			if kb.WikiConfig == nil && config.IndexingStrategy.WikiEnabled {
				kb.WikiConfig = &types.WikiConfig{}
			}
			// Sync GraphEnabled → ExtractConfig
			if kb.ExtractConfig != nil {
				kb.ExtractConfig.Enabled = config.IndexingStrategy.GraphEnabled
			} else if config.IndexingStrategy.GraphEnabled {
				kb.ExtractConfig = &types.ExtractConfig{Enabled: true}
			}
		}
	}
	kb.UpdatedAt = time.Now()
	kb.EnsureDefaults()

	logger.Info(ctx, "Saving knowledge base update")
	if err := s.repo.UpdateKnowledgeBase(ctx, kb); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": id,
		})
		return nil, err
	}
	recordKBActivity(ctx, s.audit, kb.TenantID, kb.ID, types.AuditActionKBUpdated,
		"knowledge_base", kb.ID, types.AuditOutcomeSuccess, map[string]any{
			"name": kb.Name, "changed_fields": changedFields,
		})

	logger.Infof(ctx, "Knowledge base updated successfully, ID: %s, name: %s", kb.ID, kb.Name)
	return kb, nil
}

// TogglePinKnowledgeBase toggles whether the calling user has pinned
// this knowledge base. Pin state is per-(user, kb) as of migration
// 000050; previously this method flipped a tenant-wide column on the
// KB row which broke down under RBAC (only Admin/creator could pin,
// and the pin reordered the list for everyone in the tenant). The
// public signature is unchanged so the HTTP handler / CLI / SDK don't
// move.
//
// The KB still has to belong to the caller's tenant — the route is
// already gated behind KBAccessRead, but we re-check via
// GetKnowledgeBaseByIDAndTenant so a stale param survives a tenant
// switch cleanly.
func (s *knowledgeBaseService) TogglePinKnowledgeBase(
	ctx context.Context, id string,
) (*types.KnowledgeBase, error) {
	if id == "" {
		return nil, errors.New("knowledge base ID cannot be empty")
	}
	tenantID := types.MustTenantIDFromContext(ctx)
	userID, ok := types.UserIDFromContext(ctx)
	if !ok || userID == "" {
		// API-key callers without a user identity can't have a personal
		// pin set. We surface this rather than silently flipping a
		// shared-tenant flag like the old behaviour.
		return nil, errors.New("pin requires an authenticated user")
	}

	// Look the KB up without a tenant filter: the route's KBAccessRead
	// guard already validated that this caller can see this KB (own,
	// org-shared, or agent-shared). Filtering by the caller's tenant
	// here would 404 every legitimate pin against a shared KB whose
	// owning tenant differs from the caller's active tenant.
	kb, err := s.repo.GetKnowledgeBaseByID(ctx, id)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": id,
			"tenant_id":         tenantID,
		})
		return nil, err
	}

	// Read current pin state to decide direction. ListUserKBPinIDs is
	// already optimised for the "many KBs at once" path; for a single-id
	// check the round-trip is acceptable and avoids leaking a second
	// repository method just for this.
	pins, err := s.repo.ListUserKBPinIDs(ctx, tenantID, userID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": id,
			"tenant_id":         tenantID,
			"user_id":           userID,
		})
		return nil, err
	}
	_, currentlyPinned := pins[id]

	pinnedAt, err := s.repo.SetUserKBPin(ctx, tenantID, userID, id, !currentlyPinned)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": id,
			"tenant_id":         tenantID,
			"user_id":           userID,
			"target_pinned":     !currentlyPinned,
		})
		return nil, err
	}

	kb.EnsureDefaults()
	kb.IsPinned = !currentlyPinned
	kb.PinnedAt = pinnedAt
	logger.Infof(ctx, "Knowledge base pin toggled, ID: %s, user: %s, is_pinned: %v",
		id, userID, kb.IsPinned)
	return kb, nil
}

// applyUserKBPins stamps IsPinned / PinnedAt onto each KB in the slice
// from the caller's perspective and sorts the slice so pinned rows
// float to the top (newest pin first, ties broken by created_at desc).
// Safe to call with an empty userID (no-op stamp; default sort by
// created_at preserved).
func (s *knowledgeBaseService) applyUserKBPins(
	ctx context.Context, tenantID uint64, userID string, kbs []*types.KnowledgeBase,
) {
	if len(kbs) == 0 || userID == "" {
		return
	}
	pins, err := s.repo.ListUserKBPinIDs(ctx, tenantID, userID)
	if err != nil {
		// Pin enrichment is best-effort: a transient DB blip here
		// should not break listing KBs. Log and bail without altering
		// the slice — caller still gets a valid list, just unsorted by
		// pin.
		logger.Warnf(ctx, "applyUserKBPins: failed to load pins for tenant=%d user=%s: %v",
			tenantID, userID, err)
		return
	}
	if len(pins) == 0 {
		return
	}
	for _, kb := range kbs {
		if ts, ok := pins[kb.ID]; ok {
			kb.IsPinned = true
			t := ts
			kb.PinnedAt = &t
		}
	}
	sort.SliceStable(kbs, func(i, j int) bool {
		a, b := kbs[i], kbs[j]
		if a.IsPinned != b.IsPinned {
			return a.IsPinned
		}
		if a.IsPinned && b.IsPinned {
			at, bt := a.PinnedAt, b.PinnedAt
			if at != nil && bt != nil && !at.Equal(*bt) {
				return at.After(*bt)
			}
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
}

// DeleteKnowledgeBase deletes a knowledge base by its ID
// This method marks the knowledge base as deleted and enqueues an async task
// to handle the heavy cleanup operations (embeddings, chunks, files, graph data)
func (s *knowledgeBaseService) DeleteKnowledgeBase(ctx context.Context, id string) error {
	if id == "" {
		logger.Error(ctx, "Knowledge base ID is empty")
		return errors.New("knowledge base ID cannot be empty")
	}

	logger.Infof(ctx, "Deleting knowledge base, ID: %s", id)

	// Get tenant ID from context
	tenantID := types.MustTenantIDFromContext(ctx)
	tenantInfo, _ := types.TenantInfoFromContext(ctx)

	// Load the KB before soft-delete so we can snapshot its VectorStoreID
	// into the async cleanup payload. GORM's soft-delete filter hides the
	// row from subsequent reads, so this read must happen first.
	kb, err := s.repo.GetKnowledgeBaseByID(ctx, id)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": id,
		})
		return err
	}
	var vectorStoreIDSnapshot *string
	var storageBackendIDSnapshot *string
	if kb != nil {
		vectorStoreIDSnapshot = kb.VectorStoreID
		storageBackendIDSnapshot = kb.StorageBackendID
	}

	// Step 1: Delete the knowledge base record first (mark as deleted)
	logger.Infof(ctx, "Deleting knowledge base from database")
	err = s.repo.DeleteKnowledgeBase(ctx, id)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": id,
		})
		return err
	}
	deletedName := ""
	if kb != nil {
		deletedName = kb.Name
	}
	recordKBActivity(ctx, s.audit, tenantID, id, types.AuditActionKBDeleted,
		"knowledge_base", id, types.AuditOutcomeSuccess, map[string]any{"name": deletedName})

	// Stop both ephemeral queue work and durable wiki operations that target
	// the now-deleted KB. ProcessKBDelete repeats this with document IDs and
	// performs one final scrub after heavy cleanup to close enqueue races.
	//
	// Run detached with a bounded timeout so a disconnecting API client cannot
	// truncate this best-effort scrub mid-scan, matching ProcessKBDelete's
	// cleanup semantics. The KB row is already soft-deleted, so the async
	// delete task remains the durable backstop even if this pass is cut short.
	kbCleanupCtx, cancelKBCleanup := context.WithTimeout(
		context.WithoutCancel(ctx), kbTaskCleanupTimeout,
	)
	s.cleanupTasksForKnowledgeBase(kbCleanupCtx, id, nil, nil)
	cancelKBCleanup()

	// Step 1b: Remove all organization shares for this KB so org settings no longer show them
	if s.shareRepo != nil {
		if delErr := s.shareRepo.DeleteByKnowledgeBaseID(ctx, id); delErr != nil {
			logger.Warnf(ctx, "Failed to delete KB shares for knowledge base %s: %v", id, delErr)
		}
	}

	// Step 1c: Stop and soft-delete all data sources bound to this KB so cron
	// schedules and in-flight sync logs do not keep running against a deleted KB.
	dataSourceIDs := s.deleteDataSourcesForKnowledgeBase(ctx, id)
	if len(dataSourceIDs) > 0 {
		dsCancelCtx, cancelDSCancel := context.WithTimeout(
			context.WithoutCancel(ctx), kbTaskCleanupTimeout,
		)
		s.cancelTasksForKnowledgeBase(dsCancelCtx, id, nil, dataSourceIDs)
		cancelDSCancel()
	}

	// Step 2: Enqueue async task for heavy cleanup operations
	payload := types.KBDeletePayload{
		TenantID:         tenantID,
		KnowledgeBaseID:  id,
		DataSourceIDs:    dataSourceIDs,
		EffectiveEngines: tenantInfo.GetEffectiveEngines(),
		VectorStoreID:    vectorStoreIDSnapshot,    // snapshot taken before soft-delete
		StorageBackendID: storageBackendIDSnapshot, // 软删除前快照
	}
	langfuse.InjectTracing(ctx, &payload)

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		logger.Errorf(ctx, "Failed to marshal KB delete payload; durable recovery will retry: %v", err)
		return fmt.Errorf("knowledge base was deleted but cleanup task payload could not be created: %w", err)
	}

	task := asynq.NewTask(types.TypeKBDelete, payloadBytes,
		asynq.Queue(types.QueueMaintenance), asynq.MaxRetry(3), asynq.Timeout(2*time.Hour))
	info, err := s.asynqClient.Enqueue(task, asynq.TaskID(kbDeleteTaskID(id)))
	if errors.Is(err, asynq.ErrTaskIDConflict) {
		logger.Infof(ctx, "KB delete task already exists, knowledge base ID: %s", id)
		return nil
	}
	if err != nil {
		logger.Errorf(ctx, "Failed to enqueue KB delete task; durable recovery will retry: %v", err)
		return fmt.Errorf("knowledge base was deleted but cleanup task enqueue failed: %w", err)
	}

	logger.Infof(ctx, "KB delete task enqueued: %s, knowledge base ID: %s", info.ID, id)
	logger.Infof(ctx, "Knowledge base deleted successfully, ID: %s", id)
	return nil
}

// RecoverPendingKBDeletes 重新提交已软删除但仍有知识、目录或附属资源的清理任务。
// 数据库残留是持久化恢复依据，因此 Redis 短暂不可用不会永久丢失任务。
func (s *knowledgeBaseService) RecoverPendingKBDeletes(ctx context.Context, limit int) error {
	lookup, ok := s.repo.(deletedKnowledgeBaseRecoveryLookup)
	if !ok {
		return errors.New("knowledge base repository does not support delete recovery")
	}
	if s.asynqClient == nil || s.tenantRepo == nil {
		return errors.New("knowledge base delete recovery dependencies are unavailable")
	}
	knowledgeBases, err := lookup.ListDeletedKnowledgeBasesWithActiveKnowledge(ctx, limit)
	if err != nil {
		return fmt.Errorf("list pending knowledge base deletes: %w", err)
	}
	var recoveryErr error
	for _, knowledgeBase := range knowledgeBases {
		if knowledgeBase == nil {
			continue
		}
		tenant, tenantErr := s.tenantRepo.GetTenantByID(ctx, knowledgeBase.TenantID)
		if tenantErr != nil || tenant == nil {
			if tenantErr == nil {
				tenantErr = errors.New("tenant not found")
			}
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf(
				"load tenant %d for KB delete recovery: %w",
				knowledgeBase.TenantID,
				tenantErr,
			))
			continue
		}
		var dataSourceIDs []string
		if dataSourceLookup, ok := s.repo.(deletedKnowledgeBaseCleanupDataSourceLookup); ok {
			var dataSourceErr error
			dataSourceIDs, dataSourceErr = dataSourceLookup.ListKnowledgeBaseCleanupDataSourceIDs(
				ctx,
				knowledgeBase.ID,
			)
			if dataSourceErr != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf(
					"list data sources for KB delete recovery %s: %w",
					knowledgeBase.ID,
					dataSourceErr,
				))
				continue
			}
		}
		payload := types.KBDeletePayload{
			TenantID:         knowledgeBase.TenantID,
			KnowledgeBaseID:  knowledgeBase.ID,
			DataSourceIDs:    dataSourceIDs,
			EffectiveEngines: tenant.GetEffectiveEngines(),
			VectorStoreID:    knowledgeBase.VectorStoreID,
			StorageBackendID: knowledgeBase.StorageBackendID,
		}
		payloadBytes, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf(
				"marshal KB delete recovery payload for %s: %w",
				knowledgeBase.ID,
				marshalErr,
			))
			continue
		}
		task := asynq.NewTask(
			types.TypeKBDelete,
			payloadBytes,
			asynq.Queue(types.QueueMaintenance),
			asynq.MaxRetry(3),
			asynq.Timeout(2*time.Hour),
		)
		_, enqueueErr := s.asynqClient.Enqueue(task, asynq.TaskID(kbDeleteTaskID(knowledgeBase.ID)))
		if enqueueErr == nil {
			continue
		}
		if errors.Is(enqueueErr, asynq.ErrTaskIDConflict) {
			if conflictErr := s.recoverConflictingKBDeleteTask(
				ctx,
				knowledgeBase.ID,
				task,
			); conflictErr != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf(
					"recover conflicting KB delete task for %s: %w",
					knowledgeBase.ID,
					conflictErr,
				))
			}
			continue
		}
		recoveryErr = errors.Join(recoveryErr, fmt.Errorf(
			"enqueue KB delete recovery for %s: %w",
			knowledgeBase.ID,
			enqueueErr,
		))
	}
	return recoveryErr
}

// ProcessKBDelete handles async knowledge base deletion task
// This method performs heavy cleanup operations: deleting embeddings, chunks, files, and graph data
func (s *knowledgeBaseService) ProcessKBDelete(ctx context.Context, t *asynq.Task) error {
	var payload types.KBDeletePayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		logger.Errorf(ctx, "Failed to unmarshal KB delete payload: %v", err)
		return err
	}

	tenantID := payload.TenantID
	kbID := payload.KnowledgeBaseID
	var knowledgeIDs []string

	// Set tenant context for downstream services
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	lookup, ok := s.repo.(deletedKnowledgeBaseLookup)
	if ok {
		deletedKB, err := lookup.GetKnowledgeBaseByIDAndTenantUnscoped(ctx, kbID, tenantID)
		if err != nil {
			return fmt.Errorf("validate knowledge base delete task scope: %w", err)
		}
		if deletedKB == nil || deletedKB.TenantID != tenantID || deletedKB.ID != kbID {
			return errors.Join(
				asynq.SkipRetry,
				fmt.Errorf("knowledge base delete task tenant mismatch: tenant=%d kb=%s", tenantID, kbID),
			)
		}
	}
	defer func() {
		// Workers may enqueue downstream work while the delete task performs
		// heavy storage cleanup. A detached, bounded final scrub runs on every
		// return path, including retryable failures and cancellation.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), kbTaskCleanupTimeout)
		defer cancel()
		s.cleanupTasksForKnowledgeBase(cleanupCtx, kbID, knowledgeIDs, payload.DataSourceIDs)
	}()

	logger.Infof(ctx, "Processing KB delete task for knowledge base: %s", kbID)
	if s.shareRepo != nil {
		if err := s.shareRepo.DeleteByKnowledgeBaseID(ctx, kbID); err != nil {
			return fmt.Errorf("delete knowledge base shares: %w", err)
		}
	}
	dataSourceIDs, err := s.cleanupDataSourcesForKnowledgeBase(
		ctx,
		kbID,
		payload.DataSourceIDs,
	)
	if err != nil {
		return fmt.Errorf("delete knowledge base data sources: %w", err)
	}
	payload.DataSourceIDs = dataSourceIDs

	// Step 1: Get all knowledge entries in this knowledge base
	logger.Infof(ctx, "Fetching all knowledge entries in knowledge base, ID: %s", kbID)
	knowledgeList, err := s.kgRepo.ListKnowledgeByKnowledgeBaseID(ctx, tenantID, kbID)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": kbID,
		})
		return err
	}
	logger.Infof(ctx, "Found %d knowledge entries to delete", len(knowledgeList))
	knowledgeIDs = make([]string, 0, len(knowledgeList))
	for _, knowledge := range knowledgeList {
		knowledgeIDs = append(knowledgeIDs, knowledge.ID)
	}
	if len(knowledgeIDs) > 0 {
		claimer, ok := s.kgRepo.(knowledgeListDeleteClaimer)
		if !ok {
			return errors.New("knowledge repository does not support delete claims")
		}
		claimed, err := claimer.ClaimKnowledgeListForKBDelete(ctx, tenantID, kbID, knowledgeIDs)
		if err != nil {
			return fmt.Errorf("claim knowledge list for knowledge base cleanup: %w", err)
		}
		knowledgeList = claimed
		knowledgeIDs = knowledgeIDs[:0]
		for _, knowledge := range knowledgeList {
			knowledgeIDs = append(knowledgeIDs, knowledge.ID)
		}
	}

	// Repeat the best-effort queue scrub with document IDs. Some batch tasks
	// only carry knowledge_id(s), and active work from the first pass may have
	// enqueued another downstream task before cancellation reached it.
	s.cleanupTasksForKnowledgeBase(ctx, kbID, knowledgeIDs, payload.DataSourceIDs)

	// Step 2: Delete all knowledge entries and their resources
	if len(knowledgeList) > 0 {
		logger.Infof(ctx, "Deleting all knowledge entries and their resources")
		cleanupFileSvc := s.fileSvc
		storageBackendID := ""
		storageProvider := ""
		if payload.StorageBackendID != nil {
			storageBackendID = strings.TrimSpace(*payload.StorageBackendID)
		}
		if storageBackendID == "" {
			if lookup, ok := s.repo.(deletedKnowledgeBaseLookup); ok {
				deletedKB, err := lookup.GetKnowledgeBaseByIDAndTenantUnscoped(ctx, kbID, tenantID)
				if err != nil {
					return fmt.Errorf("load deleted knowledge base storage snapshot: %w", err)
				}
				if deletedKB != nil {
					if deletedKB.StorageBackendID != nil {
						storageBackendID = strings.TrimSpace(*deletedKB.StorageBackendID)
					}
					storageProvider = deletedKB.GetStorageProvider()
				}
			}
		}
		// 删除 chunk 前必须先收集图片引用；失败时保留原始行并交给队列重试。
		chunkImageInfos, err := s.chunkRepo.ListImageInfoByKnowledgeIDs(ctx, tenantID, knowledgeIDs)
		if err != nil {
			return fmt.Errorf("collect image URLs for knowledge base cleanup: %w", err)
		}
		imageInfoStrs := make([]string, 0, len(chunkImageInfos))
		for _, chunkImageInfo := range chunkImageInfos {
			imageInfoStrs = append(imageInfoStrs, chunkImageInfo.ImageInfo)
		}
		imageURLs := collectImageURLs(ctx, imageInfoStrs)
		needsDefaultStorage := func(filePath string) bool {
			if strings.TrimSpace(filePath) == "" {
				return false
			}
			if _, ok := types.ParseResourcePath(filePath); ok {
				return false
			}
			_, _, scoped := types.ParseStorageBackendPath(filePath)
			return !scoped
		}
		requiresDefaultStorage := false
		for _, knowledge := range knowledgeList {
			requiresDefaultStorage = requiresDefaultStorage || needsDefaultStorage(knowledge.FilePath)
		}
		for _, imageURL := range imageURLs {
			requiresDefaultStorage = requiresDefaultStorage || needsDefaultStorage(imageURL)
		}
		if requiresDefaultStorage &&
			(storageBackendID != "" || strings.TrimSpace(storageProvider) != "") {
			resolved, err := s.resolveKBDeleteStorageService(ctx, tenantID, storageBackendID, storageProvider)
			if err != nil {
				return err
			}
			cleanupFileSvc = resolved
		}

		// 向量存储已不存在或不属于当前租户时，不访问该存储，但继续清理
		// 当前租户自己的文件、chunk、图和数据库行。
		logger.Infof(ctx, "Deleting embeddings from vector store")
		retrieveEngine, err := retriever.CreateRetrieveEngineFromPayload(
			ctx,
			s.retrieveEngine,
			s.ownership,
			payload.TenantID,
			payload.EffectiveEngines,
			payload.VectorStoreID,
		)
		if errors.Is(err, retriever.ErrVectorStoreForbidden) || errors.Is(err, retriever.ErrVectorStoreNotFound) {
			logger.Warnf(ctx, "Skipping unavailable vector store during KB cleanup: %v (tenant=%d, kb=%s)",
				err, payload.TenantID, payload.KnowledgeBaseID)
			retrieveEngine = nil
		} else if err != nil {
			logger.Errorf(ctx, "KB delete task deferred: %v (tenant=%d, kb=%s)", err, payload.TenantID, payload.KnowledgeBaseID)
			return err
		}
		if retrieveEngine != nil {
			type groupKey struct {
				EmbeddingModelID string
				Type             string
			}
			embeddingGroups := make(map[groupKey][]string)
			for _, knowledge := range knowledgeList {
				key := groupKey{EmbeddingModelID: knowledge.EmbeddingModelID, Type: knowledge.Type}
				embeddingGroups[key] = append(embeddingGroups[key], knowledge.ID)
			}

			for key, knowledgeGroup := range embeddingGroups {
				if strings.TrimSpace(key.EmbeddingModelID) == "" {
					continue
				}
				embeddingModel, err := s.modelService.GetEmbeddingModel(ctx, key.EmbeddingModelID)
				if err != nil {
					return fmt.Errorf("load embedding model %s for knowledge base cleanup: %w", key.EmbeddingModelID, err)
				}
				if err := retrieveEngine.DeleteByKnowledgeIDList(ctx, knowledgeGroup, embeddingModel.GetDimensions(), key.Type); err != nil {
					return fmt.Errorf("delete embeddings for model %s: %w", key.EmbeddingModelID, err)
				}
			}
		}

		// 先删物理资源再删 chunk。若存储瞬时失败，chunk 中的图片引用仍可供重试。
		logger.Infof(ctx, "Deleting physical files and extracted images")
		for _, knowledge := range knowledgeList {
			if knowledge.FilePath != "" {
				if err := s.deleteKBStoredFile(ctx, cleanupFileSvc, true, knowledge.FilePath); err != nil {
					return fmt.Errorf("delete source file for knowledge %s: %w", knowledge.ID, err)
				}
			}
		}
		for _, imageURL := range imageURLs {
			if err := s.deleteKBStoredFile(ctx, cleanupFileSvc, true, imageURL); err != nil {
				return fmt.Errorf("delete extracted image %s: %w", imageURL, err)
			}
		}

		logger.Infof(ctx, "Deleting all chunks in knowledge base")
		for _, knowledgeID := range knowledgeIDs {
			if err := s.chunkRepo.DeleteChunksByKnowledgeID(ctx, tenantID, knowledgeID); err != nil {
				return fmt.Errorf("delete chunks for knowledge %s: %w", knowledgeID, err)
			}
		}

		logger.Infof(ctx, "Deleting knowledge graph data")
		namespaces := make([]types.NameSpace, 0, len(knowledgeList))
		for _, knowledge := range knowledgeList {
			namespaces = append(namespaces, types.NameSpace{
				KnowledgeBase: knowledge.KnowledgeBaseID,
				Knowledge:     knowledge.ID,
			})
		}
		if s.graphEngine != nil && len(namespaces) > 0 {
			if err := s.graphEngine.DelGraph(ctx, namespaces); err != nil {
				return fmt.Errorf("delete knowledge graph: %w", err)
			}
		}

		for _, knowledgeID := range knowledgeIDs {
			if err := s.kgRepo.DeleteKnowledgeTagRelations(ctx, knowledgeID); err != nil {
				return fmt.Errorf("delete tag relations for knowledge %s: %w", knowledgeID, err)
			}
		}
	}

	// 知识软删除、空间配额扣减和显式目录删除必须处于同一事务。
	// 即使知识库没有文档，也要清理用户创建的空目录。
	logger.Infof(ctx, "Finalizing knowledge records, quota and folders in database")
	transactionalDeleter, ok := s.kgRepo.(knowledgeListAndQuotaDeleter)
	if !ok {
		return errors.New("knowledge repository does not support transactional quota cleanup")
	}
	if err := transactionalDeleter.DeleteKnowledgeListAndAdjustStorage(ctx, tenantID, kbID, knowledgeIDs); err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": kbID,
		})
		return err
	}

	logger.Infof(ctx, "KB delete task completed successfully, knowledge base ID: %s", kbID)
	return nil
}

func (s *knowledgeBaseService) resolveKBDeleteStorageService(
	ctx context.Context,
	tenantID uint64,
	backendID string,
	provider string,
) (interfaces.FileService, error) {
	if s.storageResolver == nil || s.tenantRepo == nil {
		return nil, errors.New("storage backend resolver is unavailable for knowledge base cleanup")
	}
	tenant, err := s.tenantRepo.GetTenantByID(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("load tenant for knowledge base cleanup: %w", err)
	}
	if tenant == nil {
		return nil, errors.New("tenant is unavailable for knowledge base cleanup")
	}
	baseDir := strings.TrimSpace(os.Getenv("LOCAL_STORAGE_BASE_DIR"))
	resolved, _, err := s.storageResolver.ResolveFileService(
		ctx,
		tenant,
		strings.TrimSpace(backendID),
		strings.TrimSpace(provider),
		baseDir,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve storage backend for knowledge base cleanup: %w", err)
	}
	if resolved == nil {
		return nil, errors.New("resolved storage backend is unavailable for knowledge base cleanup")
	}
	return resolved, nil
}

func (s *knowledgeBaseService) deleteKBStoredFile(
	ctx context.Context,
	preferred interfaces.FileService,
	resolvePathBackend bool,
	filePath string,
) error {
	if strings.TrimSpace(filePath) == "" {
		return nil
	}
	fileSvc := preferred
	if resolvePathBackend {
		if _, isResource := types.ParseResourcePath(filePath); isResource {
			if s.resourceCatalog == nil {
				return fileSvc.DeleteFile(ctx, filePath)
			}
			resource, err := s.resourceCatalog.Resolve(ctx, filePath)
			if errors.Is(err, interfaces.ErrResourceNotFound) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("resolve resource for knowledge base cleanup: %w", err)
			}
			if resource == nil {
				return nil
			}
			if resource.TenantID != types.MustTenantIDFromContext(ctx) {
				return fmt.Errorf(
					"resource tenant mismatch during knowledge base cleanup: resource_tenant=%d task_tenant=%d",
					resource.TenantID,
					types.MustTenantIDFromContext(ctx),
				)
			}
			resolved, err := s.resolveKBDeleteStorageService(
				ctx,
				resource.TenantID,
				resource.StorageBackendID,
				resource.Provider,
			)
			if err != nil {
				return err
			}
			fileSvc = resolved
		} else if backendID, innerPath, ok := types.ParseStorageBackendPath(filePath); ok {
			resolved, err := s.resolveKBDeleteStorageService(
				ctx,
				types.MustTenantIDFromContext(ctx),
				backendID,
				types.ParseProviderScheme(innerPath),
			)
			if err != nil {
				return err
			}
			fileSvc = resolved
		}
	}
	if fileSvc == nil {
		return errors.New("file service is unavailable for knowledge base cleanup")
	}
	return fileSvc.DeleteFile(ctx, filePath)
}

// cancelTasksForKnowledgeBase removes queue work for a deleted KB when the
// configured task backend supports knowledge-base-wide inspection. Queue
// cleanup is an optimization: the soft-deleted database row remains the
// durable source of truth, so backend failures must not fail KB deletion.
func (s *knowledgeBaseService) cancelTasksForKnowledgeBase(
	ctx context.Context,
	kbID string,
	knowledgeIDs []string,
	dataSourceIDs []string,
) {
	canceller, ok := s.taskInspector.(interfaces.KnowledgeBaseTaskCanceller)
	if !ok || kbID == "" {
		return
	}
	if _, _, err := canceller.CancelTasksForKnowledgeBase(ctx, kbID, knowledgeIDs, dataSourceIDs); err != nil {
		logger.Warnf(ctx, "Failed to cancel queued tasks for deleted KB %s: %v", kbID, err)
	}
}

// cleanupTasksForKnowledgeBase removes both asynq records and durable wiki
// operations. The latter must be cleared as well or startup recovery can
// recreate Redis triggers for a KB that no longer exists.
func (s *knowledgeBaseService) cleanupTasksForKnowledgeBase(
	ctx context.Context,
	kbID string,
	knowledgeIDs []string,
	dataSourceIDs []string,
) {
	if kbID == "" {
		return
	}
	cleaner, ok := s.taskPendingRepo.(interfaces.TaskPendingOpsScopeCleaner)
	if ok {
		// Clear durable work before scanning Redis. A large or degraded queue
		// must not consume the caller's entire deadline and starve the database
		// fence that prevents startup recovery from reviving this KB.
		if err := cleaner.DeleteByScope(ctx, types.TaskScopeKnowledgeBase, kbID); err != nil {
			logger.Warnf(ctx, "Failed to clear durable tasks for deleted KB %s: %v", kbID, err)
		}
	}
	s.cancelTasksForKnowledgeBase(ctx, kbID, knowledgeIDs, dataSourceIDs)
}

func (s *knowledgeBaseService) cleanupDataSourcesForKnowledgeBase(
	ctx context.Context,
	kbID string,
	knownIDs []string,
) ([]string, error) {
	dataSourceIDs := make([]string, 0, len(knownIDs))
	seen := make(map[string]struct{}, len(knownIDs))
	appendID := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		dataSourceIDs = append(dataSourceIDs, id)
	}
	for _, id := range knownIDs {
		appendID(id)
	}

	var cleanupErr error
	var activeDataSources []*types.DataSource
	if s.dsRepo != nil {
		dataSources, err := s.dsRepo.FindByKnowledgeBase(ctx, kbID)
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("list data sources: %w", err))
		} else {
			activeDataSources = dataSources
			for _, dataSource := range dataSources {
				if dataSource != nil {
					appendID(dataSource.ID)
				}
			}
		}
	}

	for _, dataSource := range activeDataSources {
		if dataSource == nil || strings.TrimSpace(dataSource.ID) == "" {
			continue
		}
		if err := s.dsRepo.Delete(ctx, dataSource.ID); err != nil {
			cleanupErr = errors.Join(
				cleanupErr,
				fmt.Errorf("delete data source %s: %w", dataSource.ID, err),
			)
		}
	}
	for _, dataSourceID := range dataSourceIDs {
		if s.dsScheduler != nil {
			s.dsScheduler.Remove(dataSourceID)
		}
		if s.syncLogRepo != nil {
			if err := s.syncLogRepo.CancelPendingByDataSource(ctx, dataSourceID); err != nil {
				cleanupErr = errors.Join(
					cleanupErr,
					fmt.Errorf("cancel pending sync logs for %s: %w", dataSourceID, err),
				)
			}
		}
	}
	return dataSourceIDs, cleanupErr
}

// deleteDataSourcesForKnowledgeBase 执行请求路径的即时清理。
// 异步删除任务会严格重试这里记录的任何失败，因此该快速路径只记录告警。
func (s *knowledgeBaseService) deleteDataSourcesForKnowledgeBase(ctx context.Context, kbID string) []string {
	dataSourceIDs, err := s.cleanupDataSourcesForKnowledgeBase(ctx, kbID, nil)
	if err != nil {
		logger.Warnf(ctx, "Failed to delete data sources for deleted KB %s: %v", kbID, err)
	}
	return dataSourceIDs
}

// SetEmbeddingModel sets the embedding model for a knowledge base
func (s *knowledgeBaseService) SetEmbeddingModel(ctx context.Context, id string, modelID string) error {
	if id == "" {
		logger.Error(ctx, "Knowledge base ID is empty")
		return errors.New("knowledge base ID cannot be empty")
	}

	if modelID == "" {
		logger.Error(ctx, "Model ID is empty")
		return errors.New("model ID cannot be empty")
	}

	logger.Infof(ctx, "Setting embedding model for knowledge base, knowledge base ID: %s, model ID: %s", id, modelID)

	// Get the knowledge base
	kb, err := s.repo.GetKnowledgeBaseByID(ctx, id)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id": id,
		})
		return err
	}

	// Update the knowledge base's embedding model
	kb.EmbeddingModelID = modelID
	kb.UpdatedAt = time.Now()

	logger.Info(ctx, "Saving knowledge base embedding model update")
	err = s.repo.UpdateKnowledgeBase(ctx, kb)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{
			"knowledge_base_id":  id,
			"embedding_model_id": modelID,
		})
		return err
	}

	logger.Infof(
		ctx,
		"Knowledge base embedding model set successfully, knowledge base ID: %s, model ID: %s",
		id,
		modelID,
	)
	return nil
}

// CopyKnowledgeBase copies a knowledge base to a new knowledge base (shallow copy).
// Source and target must belong to the tenant in context; cross-tenant access is rejected.
//
// Defensive checks:
//
//   - When dstKB != "" (clone into an existing target), the source's
//     EmbeddingModelID and VectorStoreID must match the target's. Mismatched
//     embedding models would silently mix incompatible vector spaces;
//     mismatched vector stores would require copying physical vector data
//     between stores, which is not yet supported.
//   - When dstKB == "" (create a new target), VectorStoreID is copied from
//     the source so the new KB shares the same physical vector index. GORM
//     `<-:create` allows INSERT, so the new row is well-formed.
//
// The handler's CopyKnowledgeBase endpoint runs the same checks synchronously
// before enqueueing the async clone task, so the 400 errors here are
// defense-in-depth for the worker entry point.
func (s *knowledgeBaseService) CopyKnowledgeBase(ctx context.Context,
	srcKB string, dstKB string,
) (*types.KnowledgeBase, *types.KnowledgeBase, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	// Load source KB with tenant scope to prevent cross-tenant cloning
	sourceKB, err := s.repo.GetKnowledgeBaseByIDAndTenant(ctx, srcKB, tenantID)
	if err != nil {
		logger.Errorf(ctx, "Get source knowledge base failed: %v", err)
		return nil, nil, err
	}
	sourceKB.EnsureDefaults()
	var targetKB *types.KnowledgeBase
	if dstKB != "" {
		// Load target KB with tenant scope so we only clone into the caller's tenant
		targetKB, err = s.repo.GetKnowledgeBaseByIDAndTenant(ctx, dstKB, tenantID)
		if err != nil {
			return nil, nil, err
		}

		// Defense 1: embedding model must match. Mixing incompatible
		// vector spaces would produce semantically broken search results.
		if sourceKB.EmbeddingModelID != targetKB.EmbeddingModelID {
			return nil, nil, apperrors.NewBadRequestError(
				"source and target knowledge bases use different embedding models; " +
					"clone into a target with the same embedding model")
		}

		// Defense 2: vector store binding must match. Cross-store cloning
		// would require copying physical vector data between stores.
		// (both nil → equal; both same UUID → equal; otherwise → rejected)
		if !sourceKB.SharesStoreWith(targetKB) {
			return nil, nil, apperrors.NewBadRequestError(
				"source and target knowledge bases are bound to different vector stores; " +
					"cross-store cloning is not yet supported")
		}

		// Defense 3: the concrete storage instance must match. Comparing only
		// provider names would incorrectly allow COS-A -> COS-B clones.
		if tenant, _ := ctx.Value(types.TenantInfoContextKey).(*types.Tenant); tenant != nil {
			defaultID, defaultProvider := "", ""
			if tenant.DefaultStorageBackendID != nil {
				defaultID = *tenant.DefaultStorageBackendID
			}
			if tenant.StorageEngineConfig != nil {
				defaultProvider = tenant.StorageEngineConfig.DefaultProvider
			}
			if !sourceKB.SharesStorageBackendWith(targetKB, defaultID, defaultProvider) {
				return nil, nil, apperrors.NewBadRequestError(
					"source and target knowledge bases use different storage instances; cross-storage-backend cloning is not supported")
			}
		}
	} else {
		var faqConfig *types.FAQConfig
		if sourceKB.FAQConfig != nil {
			cfg := *sourceKB.FAQConfig
			faqConfig = &cfg
		}
		// Preserve VectorStoreID so the cloned KB lands on the same
		// physical index. GORM `<-:create` permits the value at INSERT.
		targetKB = &types.KnowledgeBase{
			ID:                    uuid.New().String(),
			Name:                  sourceKB.Name,
			Type:                  sourceKB.Type,
			Description:           sourceKB.Description,
			TenantID:              tenantID,
			ChunkingConfig:        sourceKB.ChunkingConfig,
			ImageProcessingConfig: sourceKB.ImageProcessingConfig,
			EmbeddingModelID:      sourceKB.EmbeddingModelID,
			SummaryModelID:        sourceKB.SummaryModelID,
			VLMConfig:             sourceKB.VLMConfig,
			StorageProviderConfig: sourceKB.StorageProviderConfig,
			StorageBackendID:      sourceKB.StorageBackendID,
			StorageConfig:         sourceKB.StorageConfig,
			FAQConfig:             faqConfig,
			VectorStoreID:         sourceKB.VectorStoreID,
		}
		// The clone is owned by the caller, not the original creator —
		// otherwise a Contributor copying someone else's KB would still
		// not be able to edit the result. Skip synthetic API-key users
		// (see CreateKnowledgeBase for the same reasoning).
		if uid, ok := types.UserIDFromContext(ctx); ok && !types.IsSyntheticUserID(uid) {
			targetKB.CreatorID = uid
		}
		targetKB.EnsureDefaults()
		if err := s.repo.CreateKnowledgeBase(ctx, targetKB); err != nil {
			return nil, nil, err
		}
	}
	return sourceKB, targetKB, nil
}

// DuplicateKnowledgeBase creates a new KB from the source KB's settings only.
// Runtime/content state is deliberately reset so this path never copies
// knowledge entries, chunks, FAQ content, wiki pages, indexes, shares or pins.
func (s *knowledgeBaseService) DuplicateKnowledgeBase(
	ctx context.Context,
	srcKB string,
) (*types.KnowledgeBase, error) {
	srcKB = strings.TrimSpace(srcKB)
	if srcKB == "" {
		return nil, apperrors.NewBadRequestError("source knowledge base ID cannot be empty")
	}

	tenantID := types.MustTenantIDFromContext(ctx)
	sourceKB, err := s.repo.GetKnowledgeBaseByIDAndTenant(ctx, srcKB, tenantID)
	if err != nil {
		logger.Errorf(ctx, "Get source knowledge base failed: %v", err)
		return nil, err
	}
	sourceKB.EnsureDefaults()

	targetKB, err := cloneKnowledgeBaseConfiguration(sourceKB)
	if err != nil {
		return nil, err
	}
	targetKB.ID = uuid.New().String()
	targetKB.TenantID = tenantID
	targetKB.Name = s.buildDuplicateKnowledgeBaseName(ctx, tenantID, sourceKB.Name)
	targetKB.CreatorID = ""
	if uid, ok := types.UserIDFromContext(ctx); ok && !types.IsSyntheticUserID(uid) {
		targetKB.CreatorID = uid
	}
	now := time.Now()
	targetKB.CreatedAt = now
	targetKB.UpdatedAt = now
	targetKB.DeletedAt.Valid = false
	targetKB.DeletedAt.Time = time.Time{}
	targetKB.IsTemporary = false
	targetKB.IsPinned = false
	targetKB.PinnedAt = nil
	targetKB.KnowledgeCount = 0
	targetKB.ChunkCount = 0
	targetKB.IsProcessing = false
	targetKB.ProcessingCount = 0
	targetKB.ShareCount = 0
	targetKB.CreatorName = ""
	targetKB.EnsureDefaults()
	targetKB.Normalize()

	if targetKB.HasVectorStore() {
		if err := s.validateVectorStoreBinding(ctx, tenantID, *targetKB.VectorStoreID); err != nil {
			return nil, err
		}
	}

	if err := s.repo.CreateKnowledgeBase(ctx, targetKB); err != nil {
		return nil, err
	}
	recordKBActivity(ctx, s.audit, tenantID, targetKB.ID, types.AuditActionKBDuplicated,
		"knowledge_base", targetKB.ID, types.AuditOutcomeSuccess, map[string]any{
			"source_kb_id": sourceKB.ID, "name": targetKB.Name,
		})
	return targetKB, nil
}

func duplicateKBCopySuffix(locale string) string {
	locale = strings.ToLower(locale)
	switch {
	case strings.HasPrefix(locale, "zh"):
		return " 副本"
	case strings.HasPrefix(locale, "ko"):
		return " 사본"
	case strings.HasPrefix(locale, "ru"):
		return " копия"
	default:
		return " Copy"
	}
}

func duplicateKBDefaultName(locale string) string {
	locale = strings.ToLower(locale)
	switch {
	case strings.HasPrefix(locale, "zh"):
		return "知识库"
	case strings.HasPrefix(locale, "ko"):
		return "지식베이스"
	case strings.HasPrefix(locale, "ru"):
		return "База знаний"
	default:
		return "Knowledge Base"
	}
}

func (s *knowledgeBaseService) buildDuplicateKnowledgeBaseName(
	ctx context.Context,
	tenantID uint64,
	sourceName string,
) string {
	locale := types.LanguageFromContextOrDefault(ctx)
	suffix := duplicateKBCopySuffix(locale)

	baseName := strings.TrimSpace(sourceName)
	if baseName == "" {
		baseName = duplicateKBDefaultName(locale)
	}

	kbs, err := s.repo.ListKnowledgeBasesByTenantID(ctx, tenantID)
	if err != nil {
		logger.Warnf(ctx, "List tenant knowledge bases failed while building duplicate name: %v", err)
		return baseName + suffix
	}

	existing := make(map[string]struct{}, len(kbs))
	for _, kb := range kbs {
		if kb == nil {
			continue
		}
		existing[kb.Name] = struct{}{}
	}

	candidate := baseName + suffix
	if _, ok := existing[candidate]; !ok {
		return candidate
	}
	for i := 2; ; i++ {
		candidate = fmt.Sprintf("%s%s %d", baseName, suffix, i)
		if _, ok := existing[candidate]; !ok {
			return candidate
		}
	}
}

func cloneKnowledgeBaseConfiguration(sourceKB *types.KnowledgeBase) (*types.KnowledgeBase, error) {
	if sourceKB == nil {
		return nil, apperrors.NewBadRequestError("source knowledge base cannot be empty")
	}
	data, err := json.Marshal(sourceKB)
	if err != nil {
		return nil, err
	}
	var targetKB types.KnowledgeBase
	if err := json.Unmarshal(data, &targetKB); err != nil {
		return nil, err
	}
	return &targetKB, nil
}
