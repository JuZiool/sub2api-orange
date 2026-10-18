// Package codextransport implements the verified official Codex transport.
// Application identity, TLS and HTTP settings are selected before transmission;
// unsupported profiles never silently fall back to another transport.
// Orange 特有：参考 HTExplicit/sub2api 0044fda5e 的采样档案与传输实现。
package codextransport

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

//go:embed reference.json
var referenceJSON []byte

type hello struct {
	CipherSuites   []uint16          `json:"cipher_suites"`
	Extensions     []uint16          `json:"extensions"`
	ExtensionData  map[string]string `json:"extension_data"`
	ALPN           []string          `json:"alpn"`
	Groups         []uint16          `json:"groups"`
	KeyShareGroups []uint16          `json:"key_share_groups"`
	Signatures     []uint16          `json:"signature_algorithms"`
	Versions       []uint16          `json:"versions"`
	PSKModes       []uint8           `json:"psk_modes"`
}

type wireProfile struct {
	HeaderCase        map[string]string `json:"header_case"`
	TLS               hello             `json:"tls"`
	HeaderOrder       []string          `json:"header_order"`
	PseudoHeaderOrder []string          `json:"pseudo_header_order"`
	Settings          []struct {
		ID    uint16 `json:"id"`
		Value uint32 `json:"value"`
	} `json:"settings"`
	ConnectionFlow uint32 `json:"connection_flow"`
}

type reference struct {
	Version      string `json:"version"`
	SourceCommit string `json:"source_commit"`
	Builds       []struct {
		System       string `json:"system"`
		Machine      string `json:"machine"`
		BinarySHA256 string `json:"binary_sha256"`
	} `json:"builds"`
	Profiles map[string]wireProfile `json:"profiles"`
}

var verified = func() reference {
	var value reference
	if err := json.Unmarshal(referenceJSON, &value); err != nil {
		panic(err)
	}
	return value
}()

func LatestVersion() string               { return verified.Version }
func SupportsVersion(version string) bool { return version == verified.Version }

type Error struct{ Reason string }

func (e *Error) Error() string { return "Codex strict transport: " + e.Reason }
func IsError(err error) bool   { var profileError *Error; return errors.As(err, &profileError) }

// Failure marks an uncertain transport outcome, including errors after a body
// may have been written. Protocol retry code must not turn it into an HTTP replay.
type Failure struct{ Err error }

func (e *Failure) Error() string { return "Codex strict transport failed: " + e.Err.Error() }
func (e *Failure) Unwrap() error { return e.Err }
func IsFailure(err error) bool   { var failure *Failure; return errors.As(err, &failure) }

type Binding struct {
	ReferenceConfiguration string `json:"reference_configuration"`
	RequestZstd            bool   `json:"-"`
	ProfileID              string `json:"profile_id"`
	Version                string `json:"version"`
	SourceCommit           string `json:"source_commit"`
	System                 string `json:"system"`
	Arch                   string `json:"arch"`
	Strict                 bool   `json:"strict"`
	Validated              bool   `json:"validated"`
	Scope                  string `json:"-"`
	Plane                  string `json:"plane"`
	SourceID               int64  `json:"-"`
	DeviceID               string `json:"-"`
	Revision               string `json:"-"`
}

var userAgent = regexp.MustCompile(`^[^/]+/([0-9A-Za-z.\-]+) \((Mac OS|Windows|Ubuntu) [^;]+; (arm64|x86_64)\)`)

func Select(ua, scope, plane string) (Binding, error) {
	m := userAgent.FindStringSubmatch(strings.TrimSpace(ua))
	if len(m) == 0 {
		return Binding{}, &Error{"User-Agent has no supported fixed device profile"}
	}
	if !SupportsVersion(m[1]) {
		return Binding{}, &Error{fmt.Sprintf("version %s has no verified profile", m[1])}
	}
	system := map[string]string{"Mac OS": "Darwin", "Windows": "Windows", "Ubuntu": "Linux"}[m[2]]
	arch := m[3]
	matched := false
	for _, build := range verified.Builds {
		machine := strings.ToLower(build.Machine)
		if machine == "amd64" {
			machine = "x86_64"
		}
		if machine == "aarch64" {
			machine = "arm64"
		}
		if build.System == system && machine == arch {
			matched = true
			break
		}
	}
	if !matched {
		return Binding{}, &Error{fmt.Sprintf("%s/%s has no verified official sample", system, arch)}
	}
	if plane != "http" && plane != "ws" {
		return Binding{}, &Error{"unsupported protocol plane"}
	}
	return Binding{ProfileID: "codex-rust-" + verified.Version + "-r1", Version: verified.Version, SourceCommit: verified.SourceCommit,
		System: system, Arch: arch, Strict: true, Validated: true, Scope: scope, Plane: plane, ReferenceConfiguration: "official binary with isolated custom CA"}, nil
}

func (b Binding) Key() string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%t",
		b.ProfileID, b.Version, b.SourceCommit, b.System, b.Arch, b.Scope, b.Plane, b.SourceID, b.DeviceID, b.Revision, b.RequestZstd)))
	return fmt.Sprintf("%x", sum[:16])
}

// Validate rejects mutated or stale bindings before opening a connection.
func (b Binding) Validate() error {
	if !b.Strict || !b.Validated || !SupportsVersion(b.Version) || b.ProfileID != "codex-rust-"+verified.Version+"-r1" || b.SourceCommit != verified.SourceCommit {
		return &Error{"profile has not been verified"}
	}
	if b.Plane != "http" && b.Plane != "ws" {
		return &Error{"missing protocol profile"}
	}
	if strings.TrimSpace(b.Scope) == "" {
		return &Error{"credential source scope is missing"}
	}
	for _, build := range verified.Builds {
		machine := strings.ToLower(build.Machine)
		if machine == "amd64" {
			machine = "x86_64"
		}
		if machine == "aarch64" {
			machine = "arm64"
		}
		if b.System == build.System && b.Arch == machine {
			return nil
		}
	}
	return &Error{"device platform has no verified official sample"}
}

type contextKey struct{}

func WithBinding(ctx context.Context, b Binding) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, b)
}

// WithoutBinding clears an earlier attempt when failover chooses another account.
func WithoutBinding(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, struct{}{})
}
func FromContext(ctx context.Context) (Binding, bool) {
	if ctx == nil {
		return Binding{}, false
	}
	b, ok := ctx.Value(contextKey{}).(Binding)
	return b, ok
}
