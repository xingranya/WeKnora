package im

import (
	"context"
	"errors"
	"testing"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/stretchr/testify/require"
)

func TestResolveIMTerminalAnswerNeverPromotesPartialFailure(t *testing.T) {
	answer, err := resolveIMTerminalAnswer(
		event.AgentOutcomeFailed,
		"partial provider output",
		errors.New("provider failed"),
	)
	require.Error(t, err)
	require.Equal(t, imFailureReply, answer)
	require.NotContains(t, answer, "partial provider output")
}

func TestResolveIMTerminalAnswerNeverPromotesPartialStop(t *testing.T) {
	answer, err := resolveIMTerminalAnswer(
		event.AgentOutcomeStopped,
		"partial provider output",
		nil,
	)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, imStoppedReply, answer)
}

func TestResolveIMTerminalAnswerKeepsSuccessfulAnswer(t *testing.T) {
	answer, err := resolveIMTerminalAnswer(
		event.AgentOutcomeSuccess,
		"complete answer",
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, "complete answer", answer)
}

func TestRecordIMTerminalOutcomeFailsClosedBeforePersistence(t *testing.T) {
	var outcome event.AgentOutcome
	recordIMTerminalOutcome(&outcome, event.AgentOutcomeSuccess)
	recordIMTerminalOutcome(&outcome, event.AgentOutcomeFailed)
	require.Equal(t, event.AgentOutcomeFailed, outcome)

	outcome = event.AgentOutcomeStopped
	recordIMTerminalOutcome(&outcome, event.AgentOutcomeFailed)
	require.Equal(t, event.AgentOutcomeStopped, outcome)
}
