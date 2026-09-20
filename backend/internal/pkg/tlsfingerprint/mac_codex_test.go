package tlsfingerprint

import "testing"

func TestNewMacCodexProfileIsStableAndIndependent(t *testing.T) {
	first := NewMacCodexProfile()
	second := NewMacCodexProfile()
	if first == nil || second == nil {
		t.Fatal("NewMacCodexProfile returned nil")
	}
	if first.Name != MacCodexProfileName || second.Name != MacCodexProfileName {
		t.Fatalf("profile names = %q, %q; want %q", first.Name, second.Name, MacCodexProfileName)
	}
	if len(first.Extensions) != 14 || first.Extensions[1] == 65037 || first.Extensions[len(first.Extensions)-1] != 41 {
		t.Fatalf("unexpected macOS extension order: %#v", first.Extensions)
	}
	first.CipherSuites[0] = 0
	first.Extensions[0] = 999
	if second.CipherSuites[0] == 0 || second.Extensions[0] == 999 {
		t.Fatal("profiles share mutable slices")
	}
}
