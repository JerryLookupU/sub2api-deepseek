package apicompat

import "encoding/json"

// CompactionTrimLimits defines per-block character caps applied when pre-trimming
// a Responses `input` array before forwarding it to the summarization model on the
// compact path. This mirrors the inline transcript truncation in claw-code-go's
// buildTranscript (message text > 1000, tool result > 300 chars): it bounds the
// tokens shipped upstream and focuses the model on signal rather than raw dumps.
type CompactionTrimLimits struct {
	MessageTextChars int // cap for input_text / output_text blocks
	ToolOutputChars  int // cap for function_call_output (tool results)
}

// DefaultCompactionTrimLimits are the built-in caps used on the compact path.
var DefaultCompactionTrimLimits = CompactionTrimLimits{
	MessageTextChars: 1000,
	ToolOutputChars:  300,
}

// TrimResponsesInputForCompaction pre-trims oversized text / tool-output blocks in
// a Responses `input` array (json.RawMessage) so the summarization model receives a
// focused, token-bounded transcript.
//
// It is a RAW-PRESERVING transform: each item is decoded as a map[string]json.RawMessage
// and only the trimmed field (content / output) is replaced, so unmodeled fields such
// as a prior compaction_summary.encrypted_content, function call_id, or any unknown
// field survive verbatim. Items that are not trimmed keep their original bytes.
//
// It never trims compaction_summary / compaction_trigger items (already compact /
// control flow) nor system/developer role items (they carry Codex/developer
// instructions that become Anthropic system and must be preserved). On any parse
// error it fails open and returns the input unchanged.
func TrimResponsesInputForCompaction(input json.RawMessage, limits CompactionTrimLimits) json.RawMessage {
	if len(input) == 0 {
		return input
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(input, &rawItems); err != nil {
		return input
	}
	if len(rawItems) == 0 {
		return input
	}

	changed := false
	for i, raw := range rawItems {
		// Peek only type/role without forcing the item through a narrow struct,
		// so unmodeled fields are never dropped.
		var head struct {
			Type string `json:"type"`
			Role string `json:"role"`
		}
		_ = json.Unmarshal(raw, &head)

		switch head.Type {
		case "compaction_summary", "compaction_trigger":
			// Already compacted or a control item — never trim, keep original bytes.
		case "function_call_output":
			if trimmed, ok := trimRawStringField(raw, "output", limits.ToolOutputChars); ok {
				rawItems[i] = trimmed
				changed = true
			}
		default:
			// system/developer role items carry Codex / developer instructions that
			// become Anthropic system — never trim them.
			if head.Role == "system" || head.Role == "developer" {
				continue
			}
			if trimmed, ok := trimRawContentField(raw, "content", limits.MessageTextChars); ok {
				rawItems[i] = trimmed
				changed = true
			}
		}
	}

	if !changed {
		return input
	}
	out, err := json.Marshal(rawItems)
	if err != nil {
		return input
	}
	return out
}

// trimRawStringField trims a top-level string field (e.g. "output") on a raw JSON
// object, preserving every other field. Returns (newRaw, changed). Non-string
// fields and parse errors fail safe (unchanged).
func trimRawStringField(raw json.RawMessage, field string, maxChars int) (json.RawMessage, bool) {
	if maxChars <= 0 {
		return raw, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw, false
	}
	value, ok := obj[field]
	if !ok {
		return raw, false
	}
	var s string
	if err := json.Unmarshal(value, &s); err != nil {
		return raw, false // field is not a plain string — leave it alone
	}
	trimmed, ok := truncateForCompaction(s, maxChars)
	if !ok {
		return raw, false
	}
	enc, _ := json.Marshal(trimmed)
	obj[field] = enc
	out, err := json.Marshal(obj)
	if err != nil {
		return raw, false
	}
	return out, true
}

// trimRawContentField trims text inside a field that is either a JSON string or a
// []ResponsesContentPart on a raw JSON object, preserving all other fields.
// Returns (newRaw, changed).
func trimRawContentField(raw json.RawMessage, field string, maxChars int) (json.RawMessage, bool) {
	if maxChars <= 0 {
		return raw, false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw, false
	}
	content, ok := obj[field]
	if !ok || len(content) == 0 {
		return raw, false
	}
	trimmed, changed := truncateContentRaw(content, maxChars)
	if !changed {
		return raw, false
	}
	obj[field] = trimmed
	out, err := json.Marshal(obj)
	if err != nil {
		return raw, false
	}
	return out, true
}

// truncateForCompaction truncates s to at most maxChars runes, appending a visible
// marker. Returns (truncated, true) only when s exceeded the cap.
func truncateForCompaction(s string, maxChars int) (string, bool) {
	if maxChars <= 0 {
		return s, false
	}
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s, false
	}
	return string(runes[:maxChars]) + "... [truncated]", true
}

// truncateContentRaw truncates text inside a content field that is either a JSON
// string or a []ResponsesContentPart. Image and other non-text parts are left
// untouched. Returns (newRaw, changed).
func truncateContentRaw(content json.RawMessage, maxChars int) (json.RawMessage, bool) {
	if len(content) == 0 || maxChars <= 0 {
		return content, false
	}

	// Plain string form.
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		if v, ok := truncateForCompaction(s, maxChars); ok {
			out, _ := json.Marshal(v)
			return out, true
		}
		return content, false
	}

	// Array of content parts — operate per-part on raw maps so unmodeled part-level
	// fields (e.g. cache_control) survive when a text part is trimmed.
	var parts []json.RawMessage
	if err := json.Unmarshal(content, &parts); err != nil {
		return content, false
	}
	changed := false
	for i, part := range parts {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(part, &head)
		switch head.Type {
		case "input_text", "output_text", "text":
			if trimmed, ok := trimRawStringField(part, "text", maxChars); ok {
				parts[i] = trimmed
				changed = true
			}
		}
	}
	if !changed {
		return content, false
	}
	out, _ := json.Marshal(parts)
	return out, true
}
