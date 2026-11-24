// Orange 特有：账号连接测试的自定义提示词（校验 + 请求级记录 + payload 注入 + 默认回落）。
//
// 上游 0.2.24 的 TestAccountRequest.Prompt 只在 OpenAI Chat Completions、Gemini、
// Grok 媒体、部分国产厂商等分支生效；Claude(createTestPayload)、Bedrock
// (buildBedrockTestPayload)、OpenAI Responses(createOpenAITestPayload) 与 Grok 文本
// (testGrokResponsesConnection) 仍固定发送 "hi"。上游文件里只保留两处接线：
//  1. TestAccountConnection 开头一次 orangePrepareAccountTest 校验并记录提示词；
//  2. 每个 payload 构造完成后一次 orangeSetXxxTestPrompt 注入。
//
// 规则（全部在本文件）：
//   - 提示词留空（后台巡检、管理员未填）→ 完全不改动 payload，行为与上游一致（默认 "hi"、
//     Grok 配额探测 body 保持原样）；
//   - 提示词非空 → 只覆盖本条测试请求的对话文本，Antigravity 额外放宽输出 token 上限；
//   - 提示词不落库，作用域是单次测试请求。
package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/sjson"
)

const (
	// OrangeAccountTestPromptMaxCharacters 自定义测试提示词的最大字符数（按 rune 计）。
	OrangeAccountTestPromptMaxCharacters = 8192
	// orangeAccountTestPromptContextKey 请求级传递测试提示词的 gin context key。
	orangeAccountTestPromptContextKey = "orange_account_test_prompt"
	// orangeAccountTestPromptCustomOutputLimit 自定义提示词时为 Antigravity 测试放宽的输出 token 上限，
	// 保证模型能完整回答任意长度的提示词；默认提示词仍用 antigravityTestMaxOutputTokens。
	orangeAccountTestPromptCustomOutputLimit = 1024
	// 各平台 payload 中对话文本的位置（Claude/Bedrock 形状与 OpenAI Responses 形状）。
	orangeAccountTestPromptClaudePath    = "messages.0.content.0.text"
	orangeAccountTestPromptResponsesPath = "input.0.content.0.text"
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

// orangePrepareAccountTest 校验一次连接测试的提示词，并记录到请求级 gin context
// 供各平台 payload 注入读取；校验失败时返回错误，由上游调用点按原方式回给前端。
func orangePrepareAccountTest(c *gin.Context, prompt, modelID, mode string) error {
	if err := OrangeValidateAccountTestPrompt(prompt, modelID, mode); err != nil {
		return err
	}
	c.Set(orangeAccountTestPromptContextKey, prompt)
	return nil
}

// orangeRequestTestPrompt 取本次测试请求的提示词；未设置（后台巡检、管理员未填）时返回空串。
func orangeRequestTestPrompt(c *gin.Context) string {
	if c == nil {
		return ""
	}
	return c.GetString(orangeAccountTestPromptContextKey)
}

// orangeSetTestPromptText 把自定义提示词写入 payload 的指定 JSON 位置。
// 提示词按 json.Marshal 的转义写入，与改造前「在构造函数里内联写入」逐字节一致；
// 提示词留空时原样返回 body，payload 与上游逐字节一致。
func orangeSetTestPromptText(c *gin.Context, body []byte, path string) []byte {
	prompt := orangeRequestTestPrompt(c)
	if strings.TrimSpace(prompt) == "" {
		return body
	}
	encoded, err := json.Marshal(prompt)
	if err != nil {
		return body
	}
	updated, err := sjson.SetRawBytes(body, path, encoded)
	if err != nil {
		return body
	}
	return updated
}

// orangeSetClaudeTestPrompt 注入 Claude / Bedrock 形状测试 payload 的对话文本。
func orangeSetClaudeTestPrompt(c *gin.Context, body []byte) []byte {
	return orangeSetTestPromptText(c, body, orangeAccountTestPromptClaudePath)
}

// orangeSetOpenAITestPrompt 注入 OpenAI Responses 形状测试 payload 的对话文本。
func orangeSetOpenAITestPrompt(c *gin.Context, body []byte) []byte {
	return orangeSetTestPromptText(c, body, orangeAccountTestPromptResponsesPath)
}

// orangeSetGrokTestPrompt 覆盖 Grok Responses 测试请求的 input；留空时原样返回。
// 共享的配额探测 body 构造函数（grok_quota_service.go）保持默认 "hi"，只改本条测试请求。
func orangeSetGrokTestPrompt(c *gin.Context, body []byte) ([]byte, error) {
	prompt := orangeRequestTestPrompt(c)
	if strings.TrimSpace(prompt) == "" {
		return body, nil
	}
	return sjson.SetBytes(body, "input", prompt)
}

// OrangeResolveAntigravityTestPrompt 返回 Antigravity 测试请求的提示词与 maxOutputTokens：
// 自定义提示词放宽输出上限，留空回落上游 antigravityTestPrompt 的默认提示词与上游上限。
func OrangeResolveAntigravityTestPrompt(prompt string) (string, int) {
	if strings.TrimSpace(prompt) != "" {
		return prompt, orangeAccountTestPromptCustomOutputLimit
	}
	return antigravityTestPrompt(prompt), antigravityTestMaxOutputTokens
}
