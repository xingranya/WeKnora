package router

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.uber.org/dig"
)

// SyncTaskExecutor 在没有 Redis 的 Lite 模式中异步执行任务。
// activeTaskIDs 覆盖任务的延迟、执行和重试阶段，保证显式 TaskID 在
// 整个未完成生命周期内只能被接收一次。
type SyncTaskExecutor struct {
	mu            sync.RWMutex
	handlers      map[string]func(context.Context, *asynq.Task) error
	activeTaskIDs map[string]struct{}
}

func NewSyncTaskExecutor() *SyncTaskExecutor {
	return &SyncTaskExecutor{
		handlers:      make(map[string]func(context.Context, *asynq.Task) error),
		activeTaskIDs: make(map[string]struct{}),
	}
}

// RegisterHandler 为任务类型注册处理函数。
func (e *SyncTaskExecutor) RegisterHandler(pattern string, handler func(context.Context, *asynq.Task) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.handlers[pattern] = handler
}

type syncTaskOptions struct {
	delay       time.Duration
	maxRetry    int
	maxRetrySet bool
	taskID      string
	taskIDSet   bool
}

func defaultSyncTaskOptions() syncTaskOptions {
	return syncTaskOptions{maxRetry: 25}
}

func (o *syncTaskOptions) apply(optionType asynq.OptionType, value interface{}) {
	switch optionType {
	case asynq.ProcessInOpt:
		if delay, ok := value.(time.Duration); ok {
			o.delay = delay
		}
	case asynq.MaxRetryOpt:
		if maxRetry, ok := value.(int); ok {
			o.maxRetry = maxRetry
			o.maxRetrySet = true
		}
	case asynq.TaskIDOpt:
		if taskID, ok := value.(string); ok {
			o.taskID = taskID
			o.taskIDSet = true
		}
	}
}

// applyEmbeddedTaskOptions 读取 asynq.NewTask 上的选项。asynq 没有公开
// Task.Options；这里仅按固定底层类型读取 Lite 模式必须支持的三种标量选项，
// 不使用 unsafe，也不解释其他私有状态。调用 Enqueue 时传入的同类选项会在后面覆盖它们。
func (o *syncTaskOptions) applyEmbeddedTaskOptions(task *asynq.Task) {
	if task == nil {
		return
	}
	taskValue := reflect.ValueOf(task)
	if taskValue.Kind() != reflect.Ptr || taskValue.IsNil() {
		return
	}
	optionsValue := taskValue.Elem().FieldByName("opts")
	if !optionsValue.IsValid() || optionsValue.Kind() != reflect.Slice {
		return
	}
	for i := 0; i < optionsValue.Len(); i++ {
		value := optionsValue.Index(i)
		if value.Kind() != reflect.Interface || value.IsNil() {
			continue
		}
		value = value.Elem()
		if value.Type().PkgPath() != "github.com/hibiken/asynq" {
			continue
		}
		switch value.Type().Name() {
		case "processInOption":
			o.apply(asynq.ProcessInOpt, time.Duration(value.Int()))
		case "retryOption":
			o.apply(asynq.MaxRetryOpt, int(value.Int()))
		case "taskIDOption":
			o.apply(asynq.TaskIDOpt, value.String())
		}
	}
}

func (e *SyncTaskExecutor) reserveTaskID(taskID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeTaskIDs == nil {
		e.activeTaskIDs = make(map[string]struct{})
	}
	if _, exists := e.activeTaskIDs[taskID]; exists {
		return false
	}
	e.activeTaskIDs[taskID] = struct{}{}
	return true
}

func (e *SyncTaskExecutor) releaseTaskID(taskID string) {
	e.mu.Lock()
	delete(e.activeTaskIDs, taskID)
	e.mu.Unlock()
}

// Enqueue 实现 interfaces.TaskEnqueuer。任务会进入独立 goroutine，
// 并保持与 asynq 一致的 ProcessIn、MaxRetry 和 TaskID 覆盖顺序。
func (e *SyncTaskExecutor) Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error) {
	if task == nil {
		return nil, errors.New("sync task executor: task cannot be nil")
	}
	if strings.TrimSpace(task.Type()) == "" {
		return nil, errors.New("sync task executor: task type cannot be empty")
	}

	e.mu.RLock()
	handler, ok := e.handlers[task.Type()]
	e.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("sync task executor: no handler registered for type %q", task.Type())
	}

	options := defaultSyncTaskOptions()
	options.applyEmbeddedTaskOptions(task)
	for _, opt := range opts {
		if opt != nil {
			options.apply(opt.Type(), opt.Value())
		}
	}
	// 显式 MaxRetry(0) 表示首次失败后不重试；负值同样收敛为零。
	if options.maxRetrySet && options.maxRetry < 0 {
		options.maxRetry = 0
	}

	taskID := options.taskID
	if options.taskIDSet {
		if strings.TrimSpace(taskID) == "" {
			return nil, errors.New("sync task executor: task ID cannot be empty")
		}
	} else {
		taskID = uuid.New().String()
	}
	if !e.reserveTaskID(taskID) {
		return nil, fmt.Errorf("%w: %s", asynq.ErrTaskIDConflict, taskID)
	}

	info := &asynq.TaskInfo{
		ID:    taskID,
		Queue: "sync",
		Type:  task.Type(),
	}

	go func() {
		defer e.releaseTaskID(taskID)
		if options.delay > 0 {
			time.Sleep(options.delay)
		}

		// 标记为后台任务，使 Lite 模式复用与 Redis worker 相同的模型并发治理。
		ctx := types.WithBackgroundTask(context.Background())
		start := time.Now()
		logger.Infof(ctx, "[SyncTask] Executing task type=%s id=%s", task.Type(), taskID)

		var lastErr error
		for attempt := 0; attempt <= options.maxRetry; attempt++ {
			if attempt > 0 {
				backoff := time.Duration(attempt) * 5 * time.Second
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
				logger.Infof(ctx, "[SyncTask] Retrying task type=%s id=%s attempt=%d/%d backoff=%s",
					task.Type(), taskID, attempt, options.maxRetry, backoff)
				time.Sleep(backoff)
			}

			attemptCtx := types.WithTaskRetryMetadata(ctx, attempt, options.maxRetry)
			lastErr = handler(attemptCtx, task)
			if lastErr == nil {
				logger.Infof(ctx, "[SyncTask] Task completed type=%s id=%s elapsed=%v",
					task.Type(), taskID, time.Since(start))
				return
			}
			if errors.Is(lastErr, asynq.SkipRetry) {
				break
			}
		}

		logger.Errorf(ctx, "[SyncTask] Task failed (exhausted retries) type=%s id=%s elapsed=%v err=%v",
			task.Type(), taskID, time.Since(start), lastErr)
	}()

	return info, nil
}

type SyncTaskParams struct {
	dig.In

	Executor             *SyncTaskExecutor
	KnowledgeService     interfaces.KnowledgeService
	KnowledgeBaseService interfaces.KnowledgeBaseService
	TagService           interfaces.KnowledgeTagService
	DataSourceService    interfaces.DataSourceService
	ChunkExtractor       interfaces.TaskHandler `name:"chunkExtractor"`
	DataTableSummary     interfaces.TaskHandler `name:"dataTableSummary"`
	ImageMultimodal      interfaces.TaskHandler `name:"imageMultimodal"`
	KnowledgePostProcess interfaces.TaskHandler `name:"knowledgePostProcess"`
	KnowledgeAutoTag     interfaces.TaskHandler `name:"knowledgeAutoTag"`
	WikiIngest           interfaces.TaskHandler `name:"wikiIngest"`
	TemporaryDocument    interfaces.TemporaryDocumentService
	MemoryService        interfaces.MemoryService
}

// RegisterSyncHandlers registers all task handlers on the SyncTaskExecutor.
// Used in Lite mode instead of RunAsynqServer.
func RegisterSyncHandlers(params SyncTaskParams) {
	params.Executor.RegisterHandler(types.TypeChunkExtract, params.ChunkExtractor.Handle)
	params.Executor.RegisterHandler(types.TypeDataTableSummary, params.DataTableSummary.Handle)
	params.Executor.RegisterHandler(types.TypeDocumentProcess, params.KnowledgeService.ProcessDocument)
	params.Executor.RegisterHandler(types.TypeTemporaryDocumentProcess, params.TemporaryDocument.Process)
	params.Executor.RegisterHandler(types.TypeManualProcess, params.KnowledgeService.ProcessManualUpdate)
	params.Executor.RegisterHandler(types.TypeFAQImport, params.KnowledgeService.ProcessFAQImport)
	params.Executor.RegisterHandler(types.TypeQuestionGeneration, params.KnowledgeService.ProcessQuestionGeneration)
	params.Executor.RegisterHandler(types.TypeQuestionIndexCleanup, params.KnowledgeService.ProcessQuestionIndexCleanup)
	params.Executor.RegisterHandler(types.TypeSummaryGeneration, params.KnowledgeService.ProcessSummaryGeneration)
	params.Executor.RegisterHandler(types.TypeKBClone, params.KnowledgeService.ProcessKBClone)
	params.Executor.RegisterHandler(types.TypeKnowledgeMove, params.KnowledgeService.ProcessKnowledgeMove)
	params.Executor.RegisterHandler(types.TypeKnowledgeListDelete, params.KnowledgeService.ProcessKnowledgeListDelete)
	params.Executor.RegisterHandler(types.TypeKnowledgeListReparse, params.KnowledgeService.ProcessKnowledgeListReparse)
	params.Executor.RegisterHandler(types.TypeIndexDelete, params.TagService.ProcessIndexDelete)
	params.Executor.RegisterHandler(types.TypeKBDelete, params.KnowledgeBaseService.ProcessKBDelete)
	params.Executor.RegisterHandler(types.TypeImageMultimodal, params.ImageMultimodal.Handle)
	params.Executor.RegisterHandler(types.TypeKnowledgePostProcess, params.KnowledgePostProcess.Handle)
	params.Executor.RegisterHandler(types.TypeKnowledgeAutoTag, params.KnowledgeAutoTag.Handle)
	params.Executor.RegisterHandler(types.TypeDataSourceSync, params.DataSourceService.ProcessSync)
	params.Executor.RegisterHandler(types.TypeWikiIngest, params.WikiIngest.Handle)
	params.Executor.RegisterHandler(types.TypeWikiFinalize, params.WikiIngest.Handle)
	params.Executor.RegisterHandler(types.TypeMemoryExtract, params.MemoryService.Handle)
	logger.Infof(context.Background(), "[SyncTask] All task handlers registered (Lite mode, no Redis)")
}
