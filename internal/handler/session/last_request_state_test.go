package session

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type lastRequestStateSessionStub struct {
	interfaces.SessionService
	state *types.SessionLastRequestState
}

func (s *lastRequestStateSessionStub) UpdateSessionLastRequestState(
	_ context.Context, _ string, state *types.SessionLastRequestState,
) error {
	copy := *state
	s.state = &copy
	return nil
}

func TestPersistLastRequestStateKeepsAgentSourceTenantID(t *testing.T) {
	sessionService := &lastRequestStateSessionStub{}
	h := &Handler{sessionService: sessionService}
	reqCtx := &qaRequestContext{
		sessionID:              "session-1",
		reqAgentID:             "agent-1",
		reqAgentSourceTenantID: 42,
		reqAgentEnabled:        true,
	}

	h.persistLastRequestState(context.Background(), reqCtx, qaModeAgent)

	require.NotNil(t, sessionService.state)
	require.Equal(t, "agent-1", sessionService.state.AgentID)
	require.Equal(t, uint64(42), sessionService.state.AgentSourceTenantID)
}
