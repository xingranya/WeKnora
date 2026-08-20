package chat

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func collectRawOpenAIStream(t *testing.T, body string) []types.StreamResponse {
	t.Helper()
	model := &RemoteAPIChat{modelName: "terminal-test"}
	stream := make(chan types.StreamResponse, 16)
	model.processRawHTTPStream(context.Background(), &http.Response{
		Body: io.NopCloser(strings.NewReader(body)),
	}, stream, nil)
	var responses []types.StreamResponse
	for response := range stream {
		responses = append(responses, response)
	}
	return responses
}

func TestRawOpenAIStreamPartialEOFIsTerminalError(t *testing.T) {
	responses := collectRawOpenAIStream(t,
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"\"}]}\n\n")
	require.Len(t, responses, 2)
	require.Equal(t, types.ResponseTypeAnswer, responses[0].ResponseType)
	require.Equal(t, "partial", responses[0].Content)
	require.False(t, responses[0].Done)
	require.Equal(t, types.ResponseTypeError, responses[1].ResponseType)
	require.True(t, responses[1].Done)
}

func TestRawOpenAIStreamFinishReasonAllowsEOF(t *testing.T) {
	responses := collectRawOpenAIStream(t,
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"complete\"},\"finish_reason\":\"\"}]}\n\n"+
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	require.NotEmpty(t, responses)
	for _, response := range responses {
		require.NotEqual(t, types.ResponseTypeError, response.ResponseType)
	}
	require.Equal(t, "stop", responses[len(responses)-1].FinishReason)
	require.True(t, responses[len(responses)-1].Done)
}

func TestRawOpenAIStreamDoneSentinelIsTerminal(t *testing.T) {
	responses := collectRawOpenAIStream(t,
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"complete\"},\"finish_reason\":\"\"}]}\n\n"+
			"data: [DONE]\n\n")
	require.NotEmpty(t, responses)
	require.Equal(t, types.ResponseTypeAnswer, responses[len(responses)-1].ResponseType)
	require.True(t, responses[len(responses)-1].Done)
}
