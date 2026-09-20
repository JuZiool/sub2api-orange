package tlsfingerprint

import (
	"testing"

	utls "github.com/refraction-networking/utls"
)

// 回归：macOS Codex profile 只声明扩展 ID，不构造扩展对象。
// dialer 必须把 41 / pre_shared_key 映射为真实的 UtlsPreSharedKeyExtension。
// 曾经的缺陷：case 41 缺失，落到 default 分支发成空的 GenericExtension，
// 上游以 "error decoding message" 拒绝握手，单机多窗口账号全部 502。
func TestBuildClientHelloSpecFromProfileMapsPreSharedKeyExtension(t *testing.T) {
	profiles := []*Profile{
		NewMacCodexProfile(),
		{Name: "explicit-41", Extensions: []uint16{0, 43, 41}},
	}
	for _, profile := range profiles {
		spec := buildClientHelloSpecFromProfile(profile)
		if spec == nil {
			t.Fatalf("profile %q: nil spec", profile.Name)
		}

		foundPSK := false
		for _, ext := range spec.Extensions {
			if generic, ok := ext.(*utls.GenericExtension); ok && generic.Id == 41 {
				t.Fatalf("profile %q: extension 41 built as an empty GenericExtension; "+
					"want *utls.UtlsPreSharedKeyExtension (an empty pre_shared_key breaks the handshake)", profile.Name)
			}
			if _, ok := ext.(*utls.UtlsPreSharedKeyExtension); ok {
				foundPSK = true
			}
		}
		if !foundPSK {
			t.Fatalf("profile %q: no UtlsPreSharedKeyExtension in the built ClientHello", profile.Name)
		}
	}
}

// 回归：macOS Codex profile 的扩展顺序必须保留 41 在末位，且不含 ECH。
func TestMacCodexProfileExtensionOrder(t *testing.T) {
	extensions := NewMacCodexProfile().Extensions
	if len(extensions) != 14 {
		t.Fatalf("extension count = %d; want 14", len(extensions))
	}
	if extensions[len(extensions)-1] != 41 {
		t.Fatalf("last extension = %d; want 41 (pre_shared_key must be final)", extensions[len(extensions)-1])
	}
	for _, id := range extensions {
		if id == 65037 {
			t.Fatalf("macOS Codex profile must not advertise ECH: %#v", extensions)
		}
	}
}
