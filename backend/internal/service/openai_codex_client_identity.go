package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"
)

// CodexClientIdentityExtraKey 持久化在 accounts.extra 中的每账号 Codex 客户端身份。
// Orange 特有，参考 HTE 0044fda5e：仅显式启用严格传输的凭据源持久化此档案。
// 严格 HTTP / WS / 探针 / token 刷新共用同一派生档案。该值描述模拟身份，
// 不代表探测到了真实硬件，也不覆盖客户端真实的沙箱及权限元数据。
const CodexClientIdentityExtraKey = "codex_client_identity"

const codexClientIdentitySchemaVersion = 1

// codexClientIdentity 描述一台"运行 Codex TUI 的机器"：操作系统、系统版本、架构、
// 终端以及与该系统配套的 sandbox 标签。字符串形态严格对齐 codex-rs 的
// os_info / terminal-detection / sandbox_tags 输出，保证 UA 与 turn metadata 自洽。
type codexClientIdentity struct {
	Version     int    `json:"v"`
	OSType      string `json:"os_type"`
	OSVersion   string `json:"os_version"`
	Arch        string `json:"arch"`
	Terminal    string `json:"terminal"`
	Sandbox     string `json:"sandbox"`
	GeneratedAt string `json:"generated_at,omitempty"`
}

const (
	codexClientOSMac     = "Mac OS"
	codexClientOSWindows = "Windows"
	codexClientOSUbuntu  = "Ubuntu"

	codexClientSandboxMac     = "seatbelt"
	codexClientSandboxWindows = "windows_sandbox"
	codexClientSandboxLinux   = "seccomp"
)

var (
	codexClientMacVersions     = []string{"15.5.0", "15.6.1", "15.7.0", "26.0.1", "26.1.0"}
	codexClientWindowsVersions = []string{"10.0.26100", "10.0.22631", "10.0.19045"}
	codexClientUbuntuVersions  = []string{"22.4.0", "24.4.0"}
	codexClientAppleTerminals  = []string{"453", "455", "456"}
)

var (
	codexClientOSVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	codexClientTerminalPattern  = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,64}$`)
)

// valid 做 schema 校验而不只是非空：系统/沙箱必须配对（seatbelt=Mac OS、
// windows_sandbox=Windows、seccomp=Ubuntu），版本/架构/终端形态受限且不含空白或
// CR/LF。持久化值或外部写入的坏值一律视为缺失并重新按种子派生，避免脏值进入 UA。
func (id codexClientIdentity) valid() bool {
	if id.Version != codexClientIdentitySchemaVersion {
		return false
	}
	switch id.OSType {
	case codexClientOSMac:
		if id.Sandbox != codexClientSandboxMac || (id.Arch != "arm64" && id.Arch != "x86_64") {
			return false
		}
	case codexClientOSWindows:
		if id.Sandbox != codexClientSandboxWindows || id.Arch != "x86_64" {
			return false
		}
	case codexClientOSUbuntu:
		if id.Sandbox != codexClientSandboxLinux || id.Arch != "x86_64" {
			return false
		}
	default:
		return false
	}
	return codexClientOSVersionPattern.MatchString(id.OSVersion) &&
		codexClientTerminalPattern.MatchString(id.Terminal)
}

// codexSandboxForUserAgent 从有效出站 UA 的 `(<os> <ver>; <arch>)` 段推出与之配套的
// sandbox 标签；无法解析时返回空串（调用方不改写 sandbox）。turn metadata 声明的沙箱
// 必须跟随最终发出的 UA，而不是账号身份本身：管理员显式 UA 覆盖时二者可能不同。
var codexUserAgentPlatformPattern = regexp.MustCompile(`\(([^();]+?) [^ ();]+; [^();]+\)`)

func codexSandboxForUserAgent(userAgent string) string {
	m := codexUserAgentPlatformPattern.FindStringSubmatch(userAgent)
	if m == nil {
		return ""
	}
	osType := strings.ToLower(strings.TrimSpace(m[1]))
	switch {
	case osType == "":
		return ""
	case strings.HasPrefix(osType, "mac") || strings.HasPrefix(osType, "darwin"):
		return codexClientSandboxMac
	case strings.HasPrefix(osType, "windows"):
		return codexClientSandboxWindows
	default:
		return codexClientSandboxLinux
	}
}

// UserAgent 拼出真实 Codex TUI 的 User-Agent：
// codex-tui/<ver> (<os> <osver>; <arch>) <terminal> (codex-tui; <ver>)。
// 首段版本、尾部括号组版本与 version 头必须同源（codex-rs 三处都取同一个
// CARGO_PKG_VERSION）。
func (id codexClientIdentity) UserAgent(version string) string {
	return fmt.Sprintf("%s/%s (%s %s; %s) %s (%s; %s)",
		codexTUIOriginator, version, id.OSType, id.OSVersion, id.Arch, id.Terminal, codexTUIOriginator, version)
}

// codexTUIOriginator 是交互式 Codex TUI 的 originator（app-server initialize 的
// clientInfo.name，codex-rs tui/src/lib.rs）。
const codexTUIOriginator = "codex-tui"

// deriveCodexClientIdentity 从种子确定性派生一份自洽身份。沿用 HTE 的固定
// 权重：macOS 60%（arm64 为主），Windows 25%，Ubuntu 15%；这不是用户统计。
// 终端与系统匹配，sandbox 标签与系统匹配。
func deriveCodexClientIdentity(seed string) codexClientIdentity {
	h := sha256.Sum256([]byte("sub2api:codex-client-identity:v1:" + seed))
	id := codexClientIdentity{Version: codexClientIdentitySchemaVersion}
	switch osPick := h[0]; {
	case osPick < 154: // 60%
		id.OSType = codexClientOSMac
		id.Sandbox = codexClientSandboxMac
		if h[1] < 230 { // 90%
			id.Arch = "arm64"
		} else {
			id.Arch = "x86_64"
		}
		id.OSVersion = codexClientMacVersions[int(h[2])%len(codexClientMacVersions)]
		switch term := h[3]; {
		case term < 102: // 40%
			id.Terminal = fmt.Sprintf("iTerm.app/3.5.%d", 10+int(h[4])%6)
		case term < 153: // 20%
			id.Terminal = "Apple_Terminal/" + codexClientAppleTerminals[int(h[4])%len(codexClientAppleTerminals)]
		case term < 191: // 15%
			id.Terminal = fmt.Sprintf("ghostty/1.3.%d", int(h[4])%3)
		case term < 230: // 15%
			id.Terminal = codexClientVSCodeTerminal(h[4], h[5])
		default: // 10%
			id.Terminal = fmt.Sprintf("WarpTerminal/v0.2026.%02d.%02d.08.12.stable_%02d",
				1+int(h[4])%9, 1+int(h[5])%28, 1+int(h[6])%3)
		}
	case osPick < 218: // 25%
		id.OSType = codexClientOSWindows
		id.Sandbox = codexClientSandboxWindows
		id.Arch = "x86_64"
		id.OSVersion = codexClientWindowsVersions[int(h[2])%len(codexClientWindowsVersions)]
		switch term := h[3]; {
		case term < 153: // 60%
			id.Terminal = "WindowsTerminal"
		case term < 217: // 25%
			id.Terminal = codexClientVSCodeTerminal(h[4], h[5])
		default: // 15%
			id.Terminal = "unknown"
		}
	default: // 15%
		id.OSType = codexClientOSUbuntu
		id.Sandbox = codexClientSandboxLinux
		id.Arch = "x86_64"
		id.OSVersion = codexClientUbuntuVersions[int(h[2])%len(codexClientUbuntuVersions)]
		switch int(h[3]) % 3 {
		case 0:
			id.Terminal = "gnome-terminal"
		case 1:
			id.Terminal = "xterm-256color"
		default:
			id.Terminal = codexClientVSCodeTerminal(h[4], h[5])
		}
	}
	return id
}

func codexClientVSCodeTerminal(major, minor byte) string {
	return fmt.Sprintf("vscode/1.%d.%d", 103+int(major)%3, int(minor)%3)
}

// codexClientIdentityFromExtra 读取持久化身份；缺失、非对象或字段不全时返回 false。
func codexClientIdentityFromExtra(extra map[string]any) (codexClientIdentity, bool) {
	if extra == nil {
		return codexClientIdentity{}, false
	}
	raw, ok := extra[CodexClientIdentityExtraKey]
	if !ok || raw == nil {
		return codexClientIdentity{}, false
	}
	var encoded []byte
	switch typed := raw.(type) {
	case string:
		encoded = []byte(typed)
	case map[string]any:
		data, err := json.Marshal(typed)
		if err != nil {
			return codexClientIdentity{}, false
		}
		encoded = data
	default:
		data, err := json.Marshal(typed)
		if err != nil {
			return codexClientIdentity{}, false
		}
		encoded = data
	}
	var id codexClientIdentity
	if err := json.Unmarshal(encoded, &id); err != nil || !id.valid() {
		return codexClientIdentity{}, false
	}
	return id, true
}

// codexClientIdentityExtraValue 把身份编码为可直接写入 extra 的 map。
func codexClientIdentityExtraValue(id codexClientIdentity, now time.Time) map[string]any {
	return map[string]any{
		"v":            id.Version,
		"os_type":      id.OSType,
		"os_version":   id.OSVersion,
		"arch":         id.Arch,
		"terminal":     id.Terminal,
		"sandbox":      id.Sandbox,
		"generated_at": now.UTC().Format(time.RFC3339),
	}
}

// ensureCodexClientIdentityExtra 在完整 extra 上补齐身份（已存在且合法则保持不变）。
// 只对 OpenAI OAuth-like 账号生效；种子取 extra 中的 codex_fingerprint_seed，
// 调用方需先确保种子已就位（prepareCodexFingerprintExtraFor* 负责）。
func ensureCodexClientIdentityExtra(platform, accountType string, extra map[string]any, now time.Time) map[string]any {
	if platform != PlatformOpenAI || (accountType != AccountTypeOAuth && accountType != AccountTypeSetupToken) {
		return extra
	}
	if _, ok := codexClientIdentityFromExtra(extra); ok {
		return extra
	}
	seed, ok := codexFingerprintSeed(extra)
	if !ok {
		return extra
	}
	if extra == nil {
		extra = make(map[string]any, 1)
	}
	extra[CodexClientIdentityExtraKey] = codexClientIdentityExtraValue(deriveCodexClientIdentity(seed), now)
	return extra
}

// PreserveCodexManagedIdentityExtra is called under the account row lock. A
// concurrent save must neither replace an established device nor persist a
// client profile derived from a seed that lost the initialization race.
func PreserveCodexManagedIdentityExtra(account *Account, current map[string]any) map[string]any {
	prepared := maps.Clone(account.Extra)
	if prepared == nil {
		prepared = make(map[string]any)
	}
	if !account.IsOpenAIOAuthLike() {
		return prepared
	}
	if seed, valid := codexFingerprintSeed(current); valid {
		prepared[codexFingerprintSeedExtraKey] = seed
	}
	delete(prepared, CodexClientIdentityExtraKey)
	if _, valid := codexClientIdentityFromExtra(current); valid {
		prepared[CodexClientIdentityExtraKey] = current[CodexClientIdentityExtraKey]
	} else {
		_, initialized := codexClientIdentityFromExtra(account.Extra)
		if codexVerifiedTransportEnabled(prepared) || initialized {
			prepared = ensureCodexClientIdentityExtra(account.Platform, account.Type, prepared, time.Now())
		}
	}
	return prepared
}
