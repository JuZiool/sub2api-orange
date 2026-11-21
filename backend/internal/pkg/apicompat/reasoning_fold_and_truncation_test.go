package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reasoning content of a previous assistant turn must never be re-emitted as
// visible assistant text. Folding it into "<thinking>…</thinking>" text put the
// wrapper in the model-visible transcript, and models then reproduced that
// prefix inside their own visible output.
func TestChatCompletionsToResponsesDropsReasoningFromAssistantText(t *testing.T) {
	req := &ChatCompletionsRequest{
		Model: "deepseek-chat",
		Messages: []ChatMessage{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
			{
				Role:             "assistant",
				Content:          json.RawMessage(`"the answer"`),
				ReasoningContent: "internal chain of thought",
			},
		},
	}

	out, err := ChatCompletionsToResponses(req)
	require.NoError(t, err)

	blob, err := json.Marshal(out)
	require.NoError(t, err)

	body := string(blob)
	assert.NotContains(t, body, "<thinking>")
	assert.NotContains(t, body, "</thinking>")
	assert.NotContains(t, body, "internal chain of thought")
	// The visible answer itself must survive.
	assert.Contains(t, body, "the answer")
}

// The same rule applies when reasoning arrives as a structured content part.
func TestParseAssistantContentSkipsReasoningParts(t *testing.T) {
	raw := json.RawMessage(`[
		{"type":"thinking","thinking":"internal chain of thought"},
		{"type":"text","text":"the answer"}
	]`)

	got, err := parseAssistantContent(raw)
	require.NoError(t, err)

	assert.NotContains(t, got, "<thinking>")
	assert.NotContains(t, got, "internal chain of thought")
	assert.Equal(t, "the answer", got)
}

// A reasoning-only assistant turn contributes no visible text at all.
func TestParseAssistantContentReasoningOnlyYieldsEmptyText(t *testing.T) {
	got, err := parseAssistantContent(json.RawMessage(`[{"type":"reasoning","text":"only reasoning"}]`))
	require.NoError(t, err)
	assert.Empty(t, got)
}

// The upstream stream ending without a terminal event is a truncation, not a
// successful stop. Reporting "stop" made agent clients keep a half-written
// message and stop retrying.
func TestFinalizeResponsesChatStreamReportsTruncation(t *testing.T) {
	state := NewResponsesEventToChatState()

	chunks := FinalizeResponsesChatStream(state)
	require.NotEmpty(t, chunks)
	require.Len(t, chunks[0].Choices, 1)
	require.NotNil(t, chunks[0].Choices[0].FinishReason)
	assert.Equal(t, "length", *chunks[0].Choices[0].FinishReason)

	// Idempotent: a second call must not emit another finish chunk.
	assert.Empty(t, FinalizeResponsesChatStream(state))
}

// Truncation reported through incomplete_details must win over a status the
// upstream mislabels as completed.
func TestResponsesCompletedWithMaxOutputTokensReportsLength(t *testing.T) {
	state := NewResponsesEventToChatState()

	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type: "response.completed",
		Response: &ResponsesResponse{
			Status:            "completed",
			IncompleteDetails: &ResponsesIncompleteDetails{Reason: "max_output_tokens"},
		},
	}, state)

	require.NotEmpty(t, chunks)
	var finishReason *string
	for _, chunk := range chunks {
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil {
				finishReason = choice.FinishReason
			}
		}
	}
	require.NotNil(t, finishReason)
	assert.Equal(t, "length", *finishReason)
}

// A genuinely completed turn still reports tool_calls / stop unchanged.
func TestResponsesCompletedKeepsStopAndToolCalls(t *testing.T) {
	clean := NewResponsesEventToChatState()
	chunks := ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:     "response.completed",
		Response: &ResponsesResponse{Status: "completed"},
	}, clean)
	require.NotEmpty(t, chunks)
	require.NotNil(t, chunks[0].Choices[0].FinishReason)
	assert.Equal(t, "stop", *chunks[0].Choices[0].FinishReason)

	withTool := NewResponsesEventToChatState()
	withTool.SawToolCall = true
	chunks = ResponsesEventToChatChunks(&ResponsesStreamEvent{
		Type:     "response.completed",
		Response: &ResponsesResponse{Status: "completed"},
	}, withTool)
	require.NotEmpty(t, chunks)
	require.NotNil(t, chunks[0].Choices[0].FinishReason)
	assert.Equal(t, "tool_calls", *chunks[0].Choices[0].FinishReason)
}
