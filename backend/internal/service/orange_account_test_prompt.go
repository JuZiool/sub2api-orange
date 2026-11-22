// Orange 特有：账号连接测试的自定义提示词（校验 + 请求级传递 + 默认回落）。
//
// 上游 0.2.24 的 TestAccountRequest.Prompt 只在 OpenAI Chat Completions、Gemini、
// Grok 媒体、部分国产厂商等分支生效；Claude(createTestPayload)、Bedrock
// (buildBedrockTestPayload)、OpenAI Responses(createOpenAITestPayload) 与 Grok 文本
// (testGrokResponsesConnection) 仍固定发送 "hi"。本文件把提示词收敛到一处：
// TestAccountConnection 校验后写入 gin context，各平台 payload 构造函数按变参读取，
// 留空回落默认 "hi"，其它调用方（如用量巡检 account_usage_service）行为不变。
// 提示词不落库，作用域是单次测试请求。
package service

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

const (
	// OrangeAccountTestPromptMaxCharacters 自定义测试提示词的最大字符数（按 rune 计）。
	OrangeAccountTestPromptMaxCharacters = 8192
	// orangeAccountTestPromptContextKey 请求级传递测试提示词的 gin context key。
	orangeAccountTestPromptContextKey = "orange_account_test_prompt"
	// orangeAccountTestPromptCustomOutputLimit 自定义提示词时为 Antigravity 测试放宽的输出 token 上限，
	// 保证模型能完整回答任意长度的提示词；默认提示词仍用 antigravityTestMaxOutputTokens。
	orangeAccountTestPromptCustomOutputLimit = 1024
)

// OrangeValidateAccountTestPrompt 校验一次连接测试的自定义提示词：合法 UTF-8 且不超过
// OrangeAccountTestPromptMaxCharacters 个字符；留空表示使用默认提示词，直接通过。
//
// scope 可选传入 (modelID, mode)：媒体类测试（compact/image/video/search/tts/stt/realtime，
// 以及按图片/视频模型自动识别的测试）的 prompt 是生成指令而不是对话文本，沿用旧版语义
// 不做长度限制。
func OrangeValidateAccountTestPrompt(prompt string, scope ...string) error {
	modelID, mode := "", ""
	if len(scope) > 0 {
		modelID = scope[0]
	}
	if len(scope) > 1 {
		mode = scope[1]
	}

	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "compact", "image", "video", "search", "tts", "stt", "realtime":
		return nil
	}
	if mode != "text" && (isImageGenerationModel(modelID) || isOpenAIImageModel(modelID) ||
		isGrokImageGenerationModel(modelID) || isGrokVideoGenerationModel(modelID)) {
		return nil
	}
	if !utf8.ValidString(prompt) || utf8.RuneCountInString(prompt) > OrangeAccountTestPromptMaxCharacters {
		return fmt.Errorf("test prompt must be valid UTF-8 and at most %d characters", OrangeAccountTestPromptMaxCharacters)
	}
	return nil
}

// orangeResolveAccountTestPrompt 返回本次测试实际发送的提示词：留空（或仅空白）回落默认 "hi"
// （复用上游 defaultGeminiTextTestPrompt，默认值只保留一份）。
func orangeResolveAccountTestPrompt(prompts ...string) string {
	if len(prompts) > 0 && strings.TrimSpace(prompts[0]) != "" {
		return prompts[0]
	}
	return defaultGeminiTextTestPrompt
}

// orangeAccountTestUserPrompt 从 gin context 取本次测试的提示词（由 TestAccountConnection 写入）。
func orangeAccountTestUserPrompt(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.GetString(orangeAccountTestPromptContextKey)
}

// OrangeResolveAntigravityTestPrompt 返回 Antigravity 测试请求的提示词与 maxOutputTokens：
// 自定义提示词放宽输出上限，留空沿用默认提示词与上游的输出上限。
func OrangeResolveAntigravityTestPrompt(prompts ...string) (string, int) {
	if len(prompts) > 0 && strings.TrimSpace(prompts[0]) != "" {
		return prompts[0], orangeAccountTestPromptCustomOutputLimit
	}
	return defaultGeminiTextTestPrompt, antigravityTestMaxOutputTokens
}
