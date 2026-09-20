package tlsfingerprint

import utls "github.com/refraction-networking/utls"

// MacCodexProfileName is the stable name of the built-in macOS Codex profile.
// This is an adapted TLS 1.2/1.3 CLI profile, not a verified capture of the
// native macOS Codex client. Account and window identifiers remain in the
// Codex application layer.
const MacCodexProfileName = "Mac Codex (macOS arm64)"

// NewMacCodexProfile returns a fresh built-in macOS Codex TLS profile.
//
// The slices are copied so callers cannot mutate the process-wide defaults or
// accidentally affect another account/window. This is a network-layer profile
// only; it does not make multiple accounts share an application identity.
func NewMacCodexProfile() *Profile {
	return &Profile{
		Name:         MacCodexProfileName,
		CipherSuites: append([]uint16(nil), defaultCipherSuites...),
		Curves:       []uint16{uint16(utls.X25519), uint16(utls.CurveP256), uint16(utls.CurveP384)},
		PointFormats: []uint16{0},
		EnableGREASE: false,
		SignatureAlgorithms: []uint16{
			0x0403, 0x0804, 0x0401,
			0x0503, 0x0805, 0x0501,
			0x0806, 0x0601, 0x0201,
		},
		ALPNProtocols:     []string{"http/1.1"},
		SupportedVersions: []uint16{utls.VersionTLS13, utls.VersionTLS12},
		KeyShareGroups:    []uint16{uint16(utls.X25519)},
		PSKModes:          []uint16{uint16(utls.PskModeDHE)},
		// This order follows the compact macOS CLI shape used by
		// CLIProxyAPI's captured Claude Code profile. ECH/GREASE are omitted
		// so the profile is stable across uTLS versions and does not advertise
		// a capability that the Codex client may not send.
		Extensions: []uint16{0, 23, 65281, 10, 11, 35, 16, 5, 13, 18, 51, 45, 43, 41},
	}
}
