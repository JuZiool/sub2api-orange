package codextransport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/imroc/req/v3"
	reqhttp2 "github.com/imroc/req/v3/http2"
	utls "github.com/refraction-networking/utls"
)

type Options struct {
	ProxyURL              *url.URL
	RootCAs               *x509.CertPool
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	ResponseHeaderTimeout time.Duration
	DialContext           func(context.Context, string, string) (net.Conn, error)
}

// New returns a streaming RoundTripper. It does not use req's high-level retry
// or response-decoding layer. TLS state is private to this binding and pool.
func New(binding Binding, opts Options) (*req.Transport, error) {
	if err := binding.Validate(); err != nil {
		return nil, err
	}
	if opts.ProxyURL != nil {
		switch opts.ProxyURL.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return nil, &Error{"unsupported proxy scheme"}
		}
		if opts.ProxyURL.Hostname() == "" {
			return nil, &Error{"proxy host is empty"}
		}
	}
	profile, ok := verified.Profiles[binding.Plane]
	if !ok {
		return nil, &Error{"missing protocol profile"}
	}
	client := req.C().DisableCompression().DisableAutoDecode().SetTimeout(0)
	order := profile.HeaderOrder
	if binding.Plane == "http" {
		if h1, ok := verified.Profiles["http1"]; ok {
			order = h1.HeaderOrder
		}
	}
	client.SetCommonHeaderOrder(order...)
	if len(profile.PseudoHeaderOrder) > 0 {
		client.SetCommonPseudoHeaderOder(profile.PseudoHeaderOrder...)
	}
	settings := make([]reqhttp2.Setting, 0, len(profile.Settings))
	for _, s := range profile.Settings {
		settings = append(settings, reqhttp2.Setting{ID: reqhttp2.SettingID(s.ID), Val: s.Value})
	}
	if len(settings) > 0 {
		client.SetHTTP2SettingsFrame(settings...).SetHTTP2ConnectionFlow(profile.ConnectionFlow).SetHTTP2MaxHeaderListSize(16384)
	}
	transport := client.GetTransport()
	transport.Proxy = nil
	if opts.ProxyURL != nil {
		transport.Proxy = http.ProxyURL(opts.ProxyURL)
	}
	transport.MaxIdleConns = opts.MaxIdleConns
	transport.MaxIdleConnsPerHost = opts.MaxIdleConnsPerHost
	transport.MaxConnsPerHost = opts.MaxConnsPerHost
	transport.IdleConnTimeout = opts.IdleConnTimeout
	transport.ResponseHeaderTimeout = opts.ResponseHeaderTimeout
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.DisableCompression = true
	transport.DialContext = opts.DialContext
	if transport.DialContext == nil {
		transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	if opts.ProxyURL != nil {
		dial := transport.DialContext
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dial(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if err = conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return conn, nil
		}
	}
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: opts.RootCAs}
	transport.SetTLSHandshake(func(ctx context.Context, addr string, plain net.Conn) (net.Conn, *tls.ConnectionState, error) {
		host := addr
		if parsed, _, err := net.SplitHostPort(addr); err == nil {
			host = parsed
		}
		host = strings.Trim(host, "[]")
		var err error
		// A fresh connection always sends a full hello. Live connection reuse is
		// retained, but tickets must not link separate connections or pool revisions.
		config := &utls.Config{ServerName: host, RootCAs: opts.RootCAs, MinVersion: utls.VersionTLS12, MaxVersion: utls.VersionTLS13, ClientSessionCache: nil}
		conn := utls.UClient(plain, config, utls.HelloCustom)
		if err = conn.ApplyPreset(clientHello(profile.TLS)); err != nil {
			_ = plain.Close()
			return nil, nil, fmt.Errorf("apply Codex TLS profile: %w", err)
		}
		handshake, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err = conn.HandshakeContext(handshake); err != nil {
			_ = plain.Close()
			return nil, nil, fmt.Errorf("codex TLS handshake: %w", err)
		}
		if err = conn.SetDeadline(time.Time{}); err != nil {
			_ = conn.Close()
			return nil, nil, err
		}
		cs := conn.ConnectionState()
		state := &tls.ConnectionState{Version: cs.Version, HandshakeComplete: cs.HandshakeComplete, DidResume: cs.DidResume,
			CipherSuite: cs.CipherSuite, NegotiatedProtocol: cs.NegotiatedProtocol,
			NegotiatedProtocolIsMutual: cs.NegotiatedProtocolIsMutual, //nolint:staticcheck // req/v3 still requires this legacy flag to activate HTTP/2.
			ServerName:                 cs.ServerName, PeerCertificates: cs.PeerCertificates, VerifiedChains: cs.VerifiedChains,
			SignedCertificateTimestamps: cs.SignedCertificateTimestamps, OCSPResponse: cs.OCSPResponse}
		verifiedConn := &verifiedTLSConn{UConn: conn, state: *state}
		if cs.NegotiatedProtocol != "h2" {
			spelling := profile.HeaderCase
			if binding.Plane == "http" {
				spelling = verified.Profiles["http1"].HeaderCase
			}
			return &http1WireConn{Conn: verifiedConn, spelling: spelling}, state, nil
		}
		return verifiedConn, state, nil
	})
	// Own the request copy: internal ordering keys and transport retry decisions
	// must not mutate the caller's frozen headers or replay a posted JSON body.
	transport.WrapRoundTripFunc(func(next http.RoundTripper) req.HttpRoundTripFunc {
		return func(request *http.Request) (*http.Response, error) {
			transmitted := false
			defer func() {
				if !transmitted && request != nil && request.Body != nil {
					_ = request.Body.Close()
				}
			}()
			if request == nil || request.URL == nil {
				return nil, &Error{"request URL is missing"}
			}
			if request.URL.Scheme != "https" {
				return nil, &Error{"verified transport requires HTTPS"}
			}
			selected, err := Select(request.Header.Get("User-Agent"), binding.Scope, binding.Plane)
			if err != nil {
				return nil, err
			}
			if selected.ProfileID != binding.ProfileID || selected.System != binding.System || selected.Arch != binding.Arch {
				return nil, &Error{"final User-Agent does not match the frozen transport profile"}
			}
			wire := request.Clone(request.Context())
			wire.GetBody = nil
			transmitted = true
			response, err := next.RoundTrip(wire)
			if err != nil {
				return response, &Failure{Err: err}
			}
			response.Request = wire
			if response.Body != nil {
				body := &strictResponseBody{ReadCloser: response.Body}
				if writer, ok := response.Body.(io.Writer); ok {
					// Upgraded WebSockets must retain their duplex body interface.
					response.Body = &struct {
						*strictResponseBody
						io.Writer
					}{body, writer}
				} else {
					response.Body = body
				}
			}
			return response, nil
		}
	})
	return transport, nil
}

type strictResponseBody struct{ io.ReadCloser }

func (b *strictResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF && !IsFailure(err) {
		err = &Failure{Err: err}
	}
	return n, err
}

func clientHello(h hello) *utls.ClientHelloSpec {
	extensions := make([]utls.TLSExtension, 0, len(h.Extensions))
	for _, kind := range h.Extensions {
		var ext utls.TLSExtension
		switch kind {
		case 0:
			ext = &utls.SNIExtension{}
		case 5:
			ext = &utls.StatusRequestExtension{}
		case 10:
			groups := make([]utls.CurveID, len(h.Groups))
			for i, g := range h.Groups {
				groups[i] = utls.CurveID(g)
			}
			ext = &utls.SupportedCurvesExtension{Curves: groups}
		case 11:
			ext = &utls.SupportedPointsExtension{SupportedPoints: []uint8{0}}
		case 13:
			signatures := make([]utls.SignatureScheme, len(h.Signatures))
			for i, s := range h.Signatures {
				signatures[i] = utls.SignatureScheme(s)
			}
			ext = &utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: signatures}
		case 16:
			ext = &utls.ALPNExtension{AlpnProtocols: append([]string(nil), h.ALPN...)}
		case 23:
			ext = &utls.ExtendedMasterSecretExtension{}
		case 35:
			ext = &utls.SessionTicketExtension{}
		case 43:
			ext = &utls.SupportedVersionsExtension{Versions: append([]uint16(nil), h.Versions...)}
		case 45:
			ext = &utls.PSKKeyExchangeModesExtension{Modes: append([]uint8(nil), h.PSKModes...)}
		case 51:
			shares := make([]utls.KeyShare, len(h.KeyShareGroups))
			for i, g := range h.KeyShareGroups {
				shares[i] = utls.KeyShare{Group: utls.CurveID(g)}
			}
			ext = &utls.KeyShareExtension{KeyShares: shares}
		default:
			data, err := hex.DecodeString(h.ExtensionData[fmt.Sprint(kind)])
			if err != nil {
				panic("invalid embedded Codex extension")
			}
			ext = &utls.GenericExtension{Id: kind, Data: data}
		}
		extensions = append(extensions, ext)
	}
	// Official captures permute their extension order on fresh handshakes.
	extensions = utls.ShuffleChromeTLSExtensions(extensions)
	return &utls.ClientHelloSpec{TLSVersMin: utls.VersionTLS12, TLSVersMax: utls.VersionTLS13,
		CipherSuites: append([]uint16(nil), h.CipherSuites...), CompressionMethods: []uint8{0}, Extensions: extensions}
}

// req/h2 requires the standard ConnectionState signature on custom TLS connections.
type verifiedTLSConn struct {
	*utls.UConn
	state tls.ConnectionState
}

func (c *verifiedTLSConn) ConnectionState() tls.ConnectionState { return c.state }
