package httptransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/mark3labs/mcp-go/server"
)

const (
	maxRequestBytes   = 1 << 20
	maxHeaderBytes    = 16 << 10
	sessionIdleTTL    = 30 * time.Minute
	heartbeatInterval = 30 * time.Second
)

type tokenContextKey struct{}

// Config configures the Streamable HTTP listener.
type Config struct {
	Address string
	Path    string
	TLSCert string
	TLSKey  string
}

// Server serves an MCP server over Streamable HTTP or HTTPS.
type Server struct {
	http       *http.Server
	streamable *server.StreamableHTTPServer
	tlsCert    string
	tlsKey     string
}

// New creates a bounded HTTP server. Configuration validation is performed by
// the config package before this point.
func New(mcpServer *server.MCPServer, cfg Config) *Server {
	streamable := server.NewStreamableHTTPServer(
		mcpServer,
		server.WithHeartbeatInterval(heartbeatInterval),
		server.WithSessionIdleTTL(sessionIdleTTL),
		// The wrapper below replaces local-browser protection with a stricter
		// no-Origin policy and mandatory bearer credentials. This also permits
		// same-host reverse proxies that preserve the public Host header.
		server.WithDisableLocalhostProtection(true),
	)
	mux := http.NewServeMux()
	mux.Handle(cfg.Path, protect(streamable))
	mux.HandleFunc("/healthz", health)

	httpServer := &http.Server{
		Addr:              cfg.Address,
		Handler:           mux,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    maxHeaderBytes,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	return &Server{http: httpServer, streamable: streamable, tlsCert: cfg.TLSCert, tlsKey: cfg.TLSKey}
}

// Handler exposes the configured handler for in-process tests and embedding.
func (s *Server) Handler() http.Handler { return s.http.Handler }

// Serve blocks until the HTTP server stops.
func (s *Server) Serve() error {
	listener, err := net.Listen("tcp", s.http.Addr)
	if err != nil {
		return err
	}
	return s.ServeListener(listener)
}

// ServeListener serves on listener. It is exposed for deterministic embedding
// and end-to-end tests that need the selected address.
func (s *Server) ServeListener(listener net.Listener) error {
	if s.tlsCert != "" {
		certificate, err := tls.LoadX509KeyPair(s.tlsCert, s.tlsKey)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("load TLS certificate: %w", err)
		}
		tlsConfig := s.http.TLSConfig.Clone()
		tlsConfig.Certificates = []tls.Certificate{certificate}
		listener = tls.NewListener(listener, tlsConfig)
	}
	err := s.http.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops HTTP traffic and transport cleanup workers.
func (s *Server) Shutdown(ctx context.Context) error {
	httpErr := s.http.Shutdown(ctx)
	transportErr := s.streamable.Shutdown(ctx)
	return errors.Join(httpErr, transportErr)
}

// TokenFromContext returns the request-scoped Singularity bearer token.
func TokenFromContext(ctx context.Context) (string, error) {
	token, ok := ctx.Value(tokenContextKey{}).(string)
	if !ok || token == "" {
		return "", errors.New("HTTP bearer token is unavailable")
	}
	return token, nil
}

func protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Header.Get("Origin") != "" {
			http.Error(w, "browser-originated requests are not allowed", http.StatusForbidden)
			return
		}
		token, err := bearerToken(r.Header)
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.Body != nil {
			body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
			_ = r.Body.Close()
			if err != nil {
				http.Error(w, "failed to read request body", http.StatusBadRequest)
				return
			}
			if len(body) > maxRequestBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}

		ctx := context.WithValue(r.Context(), tokenContextKey{}, token)
		request := r.Clone(ctx)
		request.Header = r.Header.Clone()
		request.Header.Del("Authorization")
		next.ServeHTTP(w, request)
	})
}

func bearerToken(header http.Header) (string, error) {
	values := header.Values("Authorization")
	if len(values) != 1 {
		return "", errors.New("exactly one Authorization header is required")
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", errors.New("Authorization must use the Bearer scheme")
	}
	if strings.Contains(token, ",") || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
		return "", fmt.Errorf("invalid bearer token")
	}
	return token, nil
}

func health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}
