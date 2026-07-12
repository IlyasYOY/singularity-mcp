package httptransport

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IlyasYOY/singularity-mcp/internal/singularity"
	mcptools "github.com/IlyasYOY/singularity-mcp/internal/tools"
	"github.com/IlyasYOY/singularity-mcp/openapi"
	"github.com/mark3labs/mcp-go/client"
	clienttransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

func TestProtectInjectsTokenAndStripsHeader(t *testing.T) {
	var gotToken, gotAuthorization, gotBody string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		gotToken, err = TokenFromContext(r.Context())
		if err != nil {
			t.Error(err)
		}
		gotAuthorization = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusAccepted)
	})

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"ok":true}`))
	req.Header.Set("Authorization", "Bearer singularity-secret")
	rec := httptest.NewRecorder()
	protect(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted || gotToken != "singularity-secret" || gotAuthorization != "" || gotBody != `{"ok":true}` {
		t.Fatalf("code=%d token=%q authorization=%q body=%q", rec.Code, gotToken, gotAuthorization, gotBody)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestProtectRejectsInvalidCredentialsAndOrigins(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	tests := []struct {
		name    string
		headers http.Header
		status  int
	}{
		{name: "missing", headers: http.Header{}, status: http.StatusUnauthorized},
		{name: "wrong scheme", headers: http.Header{"Authorization": {"Basic abc"}}, status: http.StatusUnauthorized},
		{name: "empty", headers: http.Header{"Authorization": {"Bearer "}}, status: http.StatusUnauthorized},
		{name: "whitespace", headers: http.Header{"Authorization": {"Bearer abc def"}}, status: http.StatusUnauthorized},
		{name: "combined", headers: http.Header{"Authorization": {"Bearer abc,Bearer def"}}, status: http.StatusUnauthorized},
		{name: "duplicate", headers: http.Header{"Authorization": {"Bearer abc", "Bearer def"}}, status: http.StatusUnauthorized},
		{name: "origin", headers: http.Header{"Authorization": {"Bearer abc"}, "Origin": {"https://evil.example"}}, status: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called = false
			req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
			req.Header = tt.headers.Clone()
			rec := httptest.NewRecorder()
			protect(next).ServeHTTP(rec, req)
			if rec.Code != tt.status || called {
				t.Fatalf("code=%d called=%v", rec.Code, called)
			}
			if tt.status == http.StatusUnauthorized && rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
			}
		})
	}
}

func TestProtectRejectsOversizedBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(make([]byte, maxRequestBytes+1)))
	req.Header.Set("Authorization", "Bearer abc")
	rec := httptest.NewRecorder()
	protect(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler called")
	})).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestTokenFromContextMissing(t *testing.T) {
	if _, err := TokenFromContext(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestServerHealthAndSecurityDefaults(t *testing.T) {
	mcpServer := server.NewMCPServer("test", "1")
	srv := New(mcpServer, Config{Address: "127.0.0.1:0", Path: "/mcp"})
	if srv.http.MaxHeaderBytes != maxHeaderBytes || srv.http.TLSConfig.MinVersion != tls.VersionTLS12 || srv.http.WriteTimeout != 0 {
		t.Fatalf("HTTP server defaults = %#v", srv.http)
	}

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("health status=%d body=%q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST health status=%d", rec.Code)
	}
}

func TestServerNativeTLSAndGracefulShutdown(t *testing.T) {
	certFile, keyFile := testCertificateFiles(t)
	mcpServer := server.NewMCPServer("test", "1")
	srv := New(mcpServer, Config{Address: "127.0.0.1:0", Path: "/mcp", TLSCert: certFile, TLSKey: keyFile})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ServeListener(listener) }()

	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, // The test certificate is intentionally self-signed.
	}}}
	resp, err := httpClient.Get("https://" + listener.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if err := srv.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

func TestStreamableHTTPForwardsRequestScopedTokens(t *testing.T) {
	var mu sync.Mutex
	var authorizations []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		authorizations = append(authorizations, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"projects":[]}`)
	}))
	defer api.Close()

	mcpURL := newSingularityHTTPTestServer(t, api.URL, mcptools.Options{RequireWriteApproval: false})
	for _, token := range []string{"first-secret", "second-secret"} {
		c, err := client.NewStreamableHttpClient(mcpURL, clienttransport.WithHTTPHeaders(map[string]string{
			"Authorization": "Bearer " + token,
		}))
		if err != nil {
			t.Fatal(err)
		}
		startHTTPClient(t, c)
		req := mcp.CallToolRequest{}
		req.Params.Name = "singularity_projects"
		req.Params.Arguments = map[string]any{"operation": "list", "maxCount": float64(1)}
		result, err := c.CallTool(t.Context(), req)
		if err != nil || result.IsError {
			t.Fatalf("token %q: result=%v err=%v", token, result, err)
		}
		_ = c.Close()
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"Bearer first-secret", "Bearer second-secret"}
	if len(authorizations) != len(want) || authorizations[0] != want[0] || authorizations[1] != want[1] {
		t.Fatalf("authorizations = %v", authorizations)
	}
}

func TestStreamableHTTPWriteApprovalFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name      string
		action    mcp.ElicitationResponseAction
		approved  bool
		wantCalls int
		wantError bool
	}{
		{name: "accepted", action: mcp.ElicitationResponseActionAccept, approved: true, wantCalls: 1},
		{name: "declined", action: mcp.ElicitationResponseActionDecline, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer write-secret" {
					t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
				}
				_, _ = io.WriteString(w, `{"id":"P-1"}`)
			}))
			defer api.Close()

			mcpURL := newSingularityHTTPTestServer(t, api.URL, mcptools.Options{RequireWriteApproval: true, ApprovalTimeout: time.Second})
			transport, err := clienttransport.NewStreamableHTTP(mcpURL, clienttransport.WithHTTPHeaders(map[string]string{
				"Authorization": "Bearer write-secret",
			}), clienttransport.WithContinuousListening())
			if err != nil {
				t.Fatal(err)
			}
			c := client.NewClient(transport, client.WithElicitationHandler(elicitationHandler{action: tt.action, approved: tt.approved}))
			startHTTPClient(t, c)
			defer c.Close()

			req := mcp.CallToolRequest{}
			req.Params.Name = "singularity_projects"
			req.Params.Arguments = map[string]any{"operation": "create", "body": map[string]any{"title": "HTTP project"}}
			result, err := c.CallTool(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError != tt.wantError || calls != tt.wantCalls {
				t.Fatalf("isError=%v calls=%d result=%v", result.IsError, calls, result)
			}
		})
	}
}

type elicitationHandler struct {
	action   mcp.ElicitationResponseAction
	approved bool
}

func (h elicitationHandler) Elicit(context.Context, mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	return &mcp.ElicitationResult{ElicitationResponse: mcp.ElicitationResponse{
		Action:  h.action,
		Content: map[string]any{"approved": h.approved},
	}}, nil
}

func newSingularityHTTPTestServer(t *testing.T, apiURL string, options mcptools.Options) string {
	t.Helper()
	catalog, err := singularity.NewCatalog(openapi.Snapshot)
	if err != nil {
		t.Fatal(err)
	}
	apiClient, err := singularity.NewAPIClient(apiURL, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	options.TokenProvider = TokenFromContext
	mcpServer := mcptools.NewServerWithOptions(apiClient, catalog, "test", options)
	httpServer := New(mcpServer, Config{Address: "127.0.0.1:0", Path: "/mcp"})
	testServer := httptest.NewServer(httpServer.Handler())
	t.Cleanup(func() {
		testServer.Close()
		_ = httpServer.Shutdown(context.Background())
	})
	return testServer.URL + "/mcp"
}

func startHTTPClient(t *testing.T, c *client.Client) {
	t.Helper()
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "http-test", Version: "1"}
	if _, err := c.Initialize(t.Context(), init); err != nil {
		t.Fatal(err)
	}
}

func testCertificateFiles(t *testing.T) (string, string) {
	t.Helper()
	seed := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	certificate := seed.TLS.Certificates[0]
	seed.Close()

	certPEM := bytes.Buffer{}
	for _, der := range certificate.Certificate {
		_ = pem.Encode(&certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	privateKey, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKey})
	certFile := t.TempDir() + "/cert.pem"
	keyFile := t.TempDir() + "/key.pem"
	if err := os.WriteFile(certFile, certPEM.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
