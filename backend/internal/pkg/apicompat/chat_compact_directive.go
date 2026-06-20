package apicompat

import "encoding/json"

// PrependChatSystemDirective prepends a system message carrying directive to the
// front of a ChatCompletionsRequest's messages. Used on the /responses/compact
// chat-fallback path so the summarization model receives the compaction rules as a
// leading system instruction. An empty directive is a no-op.
func PrependChatSystemDirective(req *ChatCompletionsRequest, directive string) {
	if req == nil || directive == "" {
		return
	}
	content, err := json.Marshal(directive)
	if err != nil {
		return
	}
	systemMsg := ChatMessage{Role: "system", Content: content}
	req.Messages = append([]ChatMessage{systemMsg}, req.Messages...)
}
