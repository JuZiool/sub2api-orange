package service

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// Orange 特有：连接测试的提示词校验与请求级传递。
const accountTestPromptMaxCharacters = 8192
const accountTestPromptContextKey = "orange_account_test_prompt"

func validateAccountTextTestPrompt(prompt, model, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "compact", "image", "video", "search", "tts", "stt", "realtime":
		return nil
	}
	if mode != "text" && (isImageGenerationModel(model) || isOpenAIImageModel(model) || isGrokImageGenerationModel(model) || isGrokVideoGenerationModel(model)) {
		return nil
	}
	if !utf8.ValidString(prompt) || utf8.RuneCountInString(prompt) > accountTestPromptMaxCharacters {
		return errors.New("test prompt must be valid UTF-8 and at most 8192 characters")
	}
	return nil
}

func resolveAccountTestPrompt(prompts ...string) string {
	if len(prompts) > 0 && strings.TrimSpace(prompts[0]) != "" {
		return prompts[0]
	}
	return "hi"
}

func accountTestUserPrompt(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.GetString(accountTestPromptContextKey)
}

func resolveAntigravityTestPrompt(prompts ...string) (string, int) {
	if len(prompts) > 0 && strings.TrimSpace(prompts[0]) != "" {
		return prompts[0], 1024
	}
	return ".", 1
}
