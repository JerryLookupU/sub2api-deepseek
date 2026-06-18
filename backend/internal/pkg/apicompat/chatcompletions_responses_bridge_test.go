package apicompat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResponsesInputToChatMessages_DeveloperRoleMapsToSystem(t *testing.T) {
	messages, err := responsesInputToChatMessages("", json.RawMessage(`[{"role":"developer","content":"follow project instructions"}]`))
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assert.Equal(t, "system", messages[0].Role)
	assert.JSONEq(t, `"follow project instructions"`, string(messages[0].Content))
}

func TestResponsesInputToChatMessages_KeepsChatCompletionRoles(t *testing.T) {
	input := json.RawMessage(`[
		{"role":"system","content":"system message"},
		{"role":"user","content":"user message"},
		{"role":"assistant","content":"assistant message"},
		{"role":"tool","content":"tool message"}
	]`)

	messages, err := responsesInputToChatMessages("", input)
	require.NoError(t, err)
	require.Len(t, messages, 4)

	assert.Equal(t, []string{"system", "user", "assistant", "tool"}, chatMessageRoles(messages))
}

func TestResponsesInputToChatMessages_EmptyRoleFallsBackToUser(t *testing.T) {
	messages, err := responsesInputToChatMessages("", json.RawMessage(`[{"role":"","content":"hello"}]`))
	require.NoError(t, err)
	require.Len(t, messages, 1)

	assert.Equal(t, "user", messages[0].Role)
}

func TestResponsesInputToChatMessages_DeveloperRoleTrimAndCaseInsensitive(t *testing.T) {
	input := json.RawMessage(`[
		{"role":" Developer ","content":"one"},
		{"role":"\tDEVELOPER\n","content":"two"}
	]`)

	messages, err := responsesInputToChatMessages("", input)
	require.NoError(t, err)
	require.Len(t, messages, 2)

	assert.Equal(t, []string{"system", "system"}, chatMessageRoles(messages))
}

func TestResponsesToChatCompletionsRequest_InstructionsAndInputDeveloperRole(t *testing.T) {
	req := &ResponsesRequest{
		Model:        "gpt-4o",
		Instructions: "Use concise answers.",
		Input: json.RawMessage(`[
			{"role":"developer","content":[{"type":"input_text","text":"Prefer JSON."}]},
			{"role":"user","content":"Hello"}
		]`),
	}

	out, err := ResponsesToChatCompletionsRequest(req)
	require.NoError(t, err)
	require.Len(t, out.Messages, 3)

	assert.Equal(t, []string{"system", "system", "user"}, chatMessageRoles(out.Messages))
	assert.JSONEq(t, `"Use concise answers."`, string(out.Messages[0].Content))
	assert.JSONEq(t, `"Prefer JSON."`, string(out.Messages[1].Content))
	assert.JSONEq(t, `"Hello"`, string(out.Messages[2].Content))
}

func TestChatCompletionsChunkToResponsesEvents_MessageItemCarriesContentBeforeTextDelta(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState("deepseek-v4-flash")
	text := "OK"

	events := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{
		ID:    "chatcmpl-test",
		Model: "deepseek-v4-flash",
		Choices: []ChatChunkChoice{{
			Delta: ChatDelta{Content: &text},
		}},
	}, state)

	require.Len(t, events, 3)
	require.Equal(t, "response.created", events[0].Type)
	require.Equal(t, "response.output_item.added", events[1].Type)
	require.NotNil(t, events[1].Item)
	require.Equal(t, "message", events[1].Item.Type)
	require.Equal(t, "assistant", events[1].Item.Role)
	require.Equal(t, []ResponsesContentPart{{Type: "output_text", Text: ""}}, events[1].Item.Content)
	require.Equal(t, "response.output_text.delta", events[2].Type)
	require.Equal(t, events[1].Item.ID, events[2].ItemID)
	require.Equal(t, "OK", events[2].Delta)
}

func TestChatCompletionsChunkToResponsesEvents_ReasoningOpensItemBeforeSummaryDelta(t *testing.T) {
	state := NewChatCompletionsToResponsesStreamState("deepseek-v4-flash")
	reasoning := "think"
	text := "OK"

	reasoningEvents := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{
		ID:    "chatcmpl-test",
		Model: "deepseek-v4-flash",
		Choices: []ChatChunkChoice{{
			Delta: ChatDelta{ReasoningContent: &reasoning},
		}},
	}, state)
	require.Len(t, reasoningEvents, 4)
	require.Equal(t, "response.created", reasoningEvents[0].Type)
	require.Equal(t, "response.output_item.added", reasoningEvents[1].Type)
	require.Equal(t, "reasoning", reasoningEvents[1].Item.Type)
	require.Equal(t, "response.reasoning_summary_part.added", reasoningEvents[2].Type)
	require.Equal(t, reasoningEvents[1].Item.ID, reasoningEvents[2].ItemID)
	require.Equal(t, "response.reasoning_summary_text.delta", reasoningEvents[3].Type)
	require.Equal(t, reasoningEvents[1].Item.ID, reasoningEvents[3].ItemID)

	textEvents := ChatCompletionsChunkToResponsesEvents(&ChatCompletionsChunk{
		ID:    "chatcmpl-test",
		Model: "deepseek-v4-flash",
		Choices: []ChatChunkChoice{{
			Delta: ChatDelta{Content: &text},
		}},
	}, state)
	require.Len(t, textEvents, 2)
	require.Equal(t, "response.output_item.added", textEvents[0].Type)
	require.Equal(t, "message", textEvents[0].Item.Type)
	require.Equal(t, 1, textEvents[0].OutputIndex)
	require.Equal(t, "response.output_text.delta", textEvents[1].Type)
	require.Equal(t, 1, textEvents[1].OutputIndex)
}

func chatMessageRoles(messages []ChatMessage) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	return roles
}
