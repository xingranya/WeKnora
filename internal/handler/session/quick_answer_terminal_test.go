package session

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/event"
	streamstore "github.com/Tencent/WeKnora/internal/stream"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type quickAnswerTerminalMessageStub struct {
	interfaces.MessageService
	updateCalls      int
	indexCalls       int
	streamErrorSent  bool
	streamSentAtSave bool
	updatedMessage   *types.Message
}

func (s *quickAnswerTerminalMessageStub) UpdateMessage(_ context.Context, message *types.Message) error {
	s.updateCalls++
	s.streamSentAtSave = s.streamErrorSent
	copy := *message
	s.updatedMessage = &copy
	return nil
}

func (s *quickAnswerTerminalMessageStub) IndexMessageToKB(
	context.Context, string, string, string, string,
) {
	s.indexCalls++
}

func TestQuickAnswerErrorPersistsAfterSSEWithoutIndexing(t *testing.T) {
	messageService := &quickAnswerTerminalMessageStub{}
	h := &Handler{messageService: messageService}
	eventBus := event.NewEventBus()

	// 模拟生产中的 SSE 订阅顺序：流处理器先消费错误，终态处理器随后落库。
	eventBus.On(event.EventError, func(context.Context, event.Event) error {
		messageService.streamErrorSent = true
		return nil
	})

	assistantMessage := &types.Message{
		ID:          "assistant-1",
		SessionID:   "session-1",
		Role:        "assistant",
		IsCompleted: false,
	}
	streamCtx := &sseStreamContext{
		eventBus:         eventBus,
		asyncCtx:         context.Background(),
		assistantMessage: assistantMessage,
		terminal:         &qaTerminalCoordinator{},
	}
	reqCtx := &qaRequestContext{
		sessionID: "session-1",
		query:     "question",
		session:   &types.Session{TenantID: 42},
	}
	h.registerQuickAnswerTerminalHandlers(streamCtx, reqCtx)

	require.NoError(t, eventBus.Emit(context.Background(), event.Event{
		Type: event.EventError,
		Data: event.ErrorData{Error: "upstream failed", Stage: "knowledge_qa_execution"},
	}))

	require.True(t, messageService.streamSentAtSave, "错误 SSE 必须先于消息持久化")
	require.Equal(t, 1, messageService.updateCalls)
	require.Zero(t, messageService.indexCalls)
	require.NotNil(t, messageService.updatedMessage)
	require.True(t, messageService.updatedMessage.IsCompleted)
	require.Equal(t, publicSessionFailureMessage, messageService.updatedMessage.Content)
	require.NotContains(t, messageService.updatedMessage.Content, "upstream failed")

	// 错误已经抢占终态后，迟到的完成事件不得再次落库或建立索引。
	require.NoError(t, eventBus.Emit(context.Background(), event.Event{
		Type: event.EventAgentFinalAnswer,
		Data: event.AgentFinalAnswerData{Content: "late answer", Done: true},
	}))
	require.Equal(t, 1, messageService.updateCalls)
	require.Zero(t, messageService.indexCalls)
	require.Equal(t, publicSessionFailureMessage, assistantMessage.Content)
}

func TestCompleteAssistantMessageSkipsEmptyAnswerIndex(t *testing.T) {
	messageService := &quickAnswerTerminalMessageStub{}
	h := &Handler{messageService: messageService}
	message := &types.Message{ID: "assistant-1", SessionID: "session-1", Role: "assistant"}

	h.completeAssistantMessage(context.Background(), message, "question", "user-1")

	require.Equal(t, 1, messageService.updateCalls)
	require.True(t, message.IsCompleted)
	require.Zero(t, messageService.indexCalls)
}

func TestStoppedPartialAnswerPersistsWithoutIndexing(t *testing.T) {
	messageService := &quickAnswerTerminalMessageStub{}
	h := &Handler{messageService: messageService}
	eventBus := event.NewEventBus()
	asyncCtx, cancel := context.WithCancel(context.Background())
	message := &types.Message{
		ID:        "assistant-1",
		SessionID: "session-1",
		Role:      "assistant",
		Content:   "partial answer",
	}
	h.setupStopEventHandler(eventBus, "session-1", 42, message, cancel, &qaTerminalCoordinator{})

	require.NoError(t, eventBus.Emit(context.Background(), event.Event{
		Type:      event.EventStop,
		SessionID: "session-1",
		Data:      event.StopData{SessionID: "session-1", MessageID: message.ID, Reason: "user_requested"},
	}))

	require.ErrorIs(t, asyncCtx.Err(), context.Canceled)
	require.Equal(t, 1, messageService.updateCalls)
	require.True(t, message.IsCompleted)
	require.Equal(t, "partial answer", message.Content)
	require.Zero(t, messageService.indexCalls)
}

func TestQuickAnswerPanicEmitsStableTerminalAndPersistsFailure(t *testing.T) {
	messageService := &quickAnswerTerminalMessageStub{}
	h := &Handler{messageService: messageService}
	eventBus := event.NewEventBus()
	eventBus.On(event.EventError, func(_ context.Context, evt event.Event) error {
		data, ok := evt.Data.(event.ErrorData)
		require.True(t, ok)
		require.Equal(t, qaUnexpectedFailureMessage, data.Error)
		messageService.streamErrorSent = true
		return nil
	})

	assistantMessage := &types.Message{ID: "assistant-1", SessionID: "session-1", Role: "assistant"}
	streamCtx := &sseStreamContext{
		eventBus:         eventBus,
		asyncCtx:         context.Background(),
		assistantMessage: assistantMessage,
		terminal:         &qaTerminalCoordinator{},
	}
	reqCtx := &qaRequestContext{sessionID: "session-1", session: &types.Session{TenantID: 42}}
	h.registerQuickAnswerTerminalHandlers(streamCtx, reqCtx)

	h.handleQAServicePanic(streamCtx, reqCtx, "Knowledge QA", "private panic", []byte("private stack"))

	require.Equal(t, 1, messageService.updateCalls)
	require.Zero(t, messageService.indexCalls)
	require.True(t, messageService.streamSentAtSave)
	require.True(t, assistantMessage.IsCompleted)
	require.Equal(t, qaUnexpectedFailureMessage, assistantMessage.Content)
	require.NotContains(t, assistantMessage.Content, "private panic")
}

func newAgentTerminalTestContext(
	t *testing.T,
) (*Handler, *quickAnswerTerminalMessageStub, *sseStreamContext, *qaRequestContext, *event.EventBus, interfaces.StreamManager) {
	t.Helper()
	messageService := &quickAnswerTerminalMessageStub{}
	h := &Handler{messageService: messageService}
	eventBus := event.NewEventBus()
	manager := streamstore.NewMemoryStreamManager()
	message := &types.Message{ID: "assistant-agent", SessionID: "session-agent", Role: "assistant"}
	terminal := &qaTerminalCoordinator{}
	streamHandler := NewAgentStreamHandler(
		context.Background(), message.SessionID, message.ID, "request-agent", 42,
		time.Time{}, message, manager, eventBus, nil, true,
	)
	streamHandler.Subscribe()
	streamCtx := &sseStreamContext{
		eventBus:         eventBus,
		asyncCtx:         context.Background(),
		assistantMessage: message,
		streamHandler:    streamHandler,
		terminal:         terminal,
	}
	reqCtx := &qaRequestContext{
		sessionID: message.SessionID,
		query:     "",
		session:   &types.Session{TenantID: 42},
	}
	h.registerAgentTerminalHandlers(streamCtx, reqCtx)
	return h, messageService, streamCtx, reqCtx, eventBus, manager
}

func TestAgentSuccessPersistsBeforeComplete(t *testing.T) {
	_, messageService, streamCtx, _, eventBus, manager := newAgentTerminalTestContext(t)
	require.NoError(t, eventBus.Emit(context.Background(), event.Event{
		ID:   "agent-complete",
		Type: event.EventAgentComplete,
		Data: event.AgentCompleteData{
			MessageID:   streamCtx.assistantMessage.ID,
			FinalAnswer: "durable answer",
			Outcome:     event.AgentOutcomeSuccess,
		},
	}))

	require.Equal(t, 1, messageService.updateCalls)
	require.True(t, messageService.updatedMessage.IsCompleted)
	require.Equal(t, "durable answer", messageService.updatedMessage.Content)
	events, _, err := manager.GetEvents(context.Background(), "session-agent", "assistant-agent", 0)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	require.Equal(t, types.ResponseTypeComplete, events[len(events)-1].Type)
}

func TestAgentFailureSnapshotDoesNotEmitCompleteOrSuccessSideEffects(t *testing.T) {
	_, messageService, streamCtx, _, eventBus, manager := newAgentTerminalTestContext(t)
	require.NoError(t, eventBus.Emit(context.Background(), event.Event{
		ID:   "agent-failed-snapshot",
		Type: event.EventAgentComplete,
		Data: event.AgentCompleteData{
			MessageID:   streamCtx.assistantMessage.ID,
			FinalAnswer: "partial answer",
			Outcome:     event.AgentOutcomeFailed,
		},
	}))
	require.Zero(t, messageService.updateCalls)

	require.NoError(t, eventBus.Emit(context.Background(), event.Event{
		ID:   "agent-error",
		Type: event.EventError,
		Data: event.ErrorData{Error: "private failure", Stage: "agent_execution"},
	}))
	require.Equal(t, 1, messageService.updateCalls)
	require.Zero(t, messageService.indexCalls)
	require.Equal(t, publicSessionFailureMessage, messageService.updatedMessage.Content)
	events, _, err := manager.GetEvents(context.Background(), "session-agent", "assistant-agent", 0)
	require.NoError(t, err)
	for _, streamEvent := range events {
		require.NotEqual(t, types.ResponseTypeComplete, streamEvent.Type)
	}
}

func TestIsTerminalStreamEvent(t *testing.T) {
	tests := []struct {
		name string
		evt  interfaces.StreamEvent
		want bool
	}{
		{name: "完成", evt: interfaces.StreamEvent{Type: types.ResponseTypeComplete, Done: true}, want: true},
		{name: "终态错误", evt: interfaces.StreamEvent{Type: types.ResponseTypeError, Done: true}, want: true},
		{name: "非终态错误", evt: interfaces.StreamEvent{Type: types.ResponseTypeError}, want: false},
		{name: "用户停止", evt: interfaces.StreamEvent{Type: types.ResponseType(event.EventStop), Done: true}, want: true},
		{name: "答案片段", evt: interfaces.StreamEvent{Type: types.ResponseTypeAnswer}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isTerminalStreamEvent(tt.evt))
		})
	}
}
