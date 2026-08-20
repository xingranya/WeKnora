package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionLastRequestStateRoundTripKeepsAgentSourceTenantID(t *testing.T) {
	original := &SessionLastRequestState{
		AgentID:             "agent-1",
		AgentSourceTenantID: 42,
		AgentEnabled:        true,
	}

	value, err := original.Value()
	require.NoError(t, err)

	var restored SessionLastRequestState
	require.NoError(t, restored.Scan(value))
	require.Equal(t, original.AgentID, restored.AgentID)
	require.Equal(t, original.AgentSourceTenantID, restored.AgentSourceTenantID)
	require.Equal(t, original.AgentEnabled, restored.AgentEnabled)
}

func TestSessionLastRequestStateLegacyJSONDefaultsAgentSourceTenantID(t *testing.T) {
	var state SessionLastRequestState
	require.NoError(t, state.Scan([]byte(`{"agent_id":"agent-1","agent_enabled":true}`)))
	require.Zero(t, state.AgentSourceTenantID)
}
