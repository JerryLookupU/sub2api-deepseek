package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type codexModelsResponse struct {
	Models []codexModelInfo `json:"models"`
}

type codexModelInfo struct {
	Slug                          string                `json:"slug"`
	DisplayName                   string                `json:"display_name"`
	Description                   string                `json:"description"`
	DefaultReasoningLevel         string                `json:"default_reasoning_level"`
	SupportedReasoningLevels      []codexReasoningLevel `json:"supported_reasoning_levels"`
	ShellType                     string                `json:"shell_type"`
	Visibility                    string                `json:"visibility"`
	SupportedInAPI                bool                  `json:"supported_in_api"`
	Priority                      int                   `json:"priority"`
	AdditionalSpeedTiers          []string              `json:"additional_speed_tiers"`
	AvailabilityNUX               any                   `json:"availability_nux"`
	Upgrade                       any                   `json:"upgrade"`
	BaseInstructions              string                `json:"base_instructions"`
	ModelMessages                 any                   `json:"model_messages"`
	SupportsReasoningSummaries    bool                  `json:"supports_reasoning_summaries"`
	DefaultReasoningSummary       string                `json:"default_reasoning_summary"`
	SupportVerbosity              bool                  `json:"support_verbosity"`
	DefaultVerbosity              any                   `json:"default_verbosity"`
	ApplyPatchToolType            string                `json:"apply_patch_tool_type"`
	WebSearchToolType             string                `json:"web_search_tool_type"`
	TruncationPolicy              codexTruncationPolicy `json:"truncation_policy"`
	SupportsParallelToolCalls     bool                  `json:"supports_parallel_tool_calls"`
	SupportsImageDetailOriginal   bool                  `json:"supports_image_detail_original"`
	ContextWindow                 int                   `json:"context_window"`
	AutoCompactTokenLimit         any                   `json:"auto_compact_token_limit"`
	EffectiveContextWindowPercent int                   `json:"effective_context_window_percent"`
	ExperimentalSupportedTools    []string              `json:"experimental_supported_tools"`
	InputModalities               []string              `json:"input_modalities"`
	SupportsSearchTool            bool                  `json:"supports_search_tool"`
}

type codexReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

type codexTruncationPolicy struct {
	Mode  string `json:"mode"`
	Limit int    `json:"limit"`
}

var deepseekCodexModels = []codexModelInfo{
	newDeepseekCodexModel("deepseek-v4-pro", "DeepSeek V4 Pro", "DeepSeek V4 Pro via Sub2API.", 20),
	newDeepseekCodexModel("deepseek-v4-flash", "DeepSeek V4 Flash", "DeepSeek V4 Flash via Sub2API.", 21),
}

func isCodexModelsCatalogRequest(c *gin.Context) bool {
	return c.Query("client_version") != ""
}

func writeCodexModelsCatalog(c *gin.Context) {
	models := make([]codexModelInfo, len(deepseekCodexModels))
	copy(models, deepseekCodexModels)
	c.JSON(http.StatusOK, codexModelsResponse{Models: models})
}

func newDeepseekCodexModel(slug, displayName, description string, priority int) codexModelInfo {
	return codexModelInfo{
		Slug:                  slug,
		DisplayName:           displayName,
		Description:           description,
		DefaultReasoningLevel: "medium",
		SupportedReasoningLevels: []codexReasoningLevel{
			{Effort: "low", Description: "Fast responses with lighter reasoning"},
			{Effort: "medium", Description: "Balances speed and reasoning depth"},
			{Effort: "high", Description: "Greater reasoning depth for complex work"},
		},
		ShellType:                     "shell_command",
		Visibility:                    "list",
		SupportedInAPI:                true,
		Priority:                      priority,
		AdditionalSpeedTiers:          []string{},
		AvailabilityNUX:               nil,
		Upgrade:                       nil,
		BaseInstructions:              "",
		ModelMessages:                 nil,
		SupportsReasoningSummaries:    false,
		DefaultReasoningSummary:       "none",
		SupportVerbosity:              false,
		DefaultVerbosity:              nil,
		ApplyPatchToolType:            "freeform",
		WebSearchToolType:             "text",
		TruncationPolicy:              codexTruncationPolicy{Mode: "tokens", Limit: 10000},
		SupportsParallelToolCalls:     true,
		SupportsImageDetailOriginal:   false,
		ContextWindow:                 128000,
		AutoCompactTokenLimit:         nil,
		EffectiveContextWindowPercent: 95,
		ExperimentalSupportedTools:    []string{},
		InputModalities:               []string{"text"},
		SupportsSearchTool:            false,
	}
}
