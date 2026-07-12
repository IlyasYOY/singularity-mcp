package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func TestBinaryStreamableHTTPSmoke(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer header-token" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"projects":[]}`)
	}))
	defer api.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()

	binary := t.TempDir() + "/singularity-mcp"
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary,
		"-transport", "http",
		"-http-address", address,
		"-base-url", api.URL,
		"-require-write-approval=false",
	)
	cmd.Env = append(cmd.Environ(), "SINGULARITY_TOKEN=")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	lineCh := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		if scanner.Scan() {
			lineCh <- scanner.Text()
		}
	}()
	select {
	case line := <-lineCh:
		if !strings.Contains(line, "serving Streamable HTTP") {
			t.Fatalf("startup line = %q", line)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	c, err := client.NewStreamableHttpClient("http://"+address+"/mcp", transport.WithHTTPHeaders(map[string]string{
		"Authorization": "Bearer header-token",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	init := mcp.InitializeRequest{}
	init.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcp.Implementation{Name: "binary-http-smoke", Version: "1"}
	if _, err := c.Initialize(ctx, init); err != nil {
		t.Fatal(err)
	}
	req := mcp.CallToolRequest{}
	req.Params.Name = "singularity_projects"
	req.Params.Arguments = map[string]any{"operation": "list", "maxCount": float64(1)}
	result, err := c.CallTool(ctx, req)
	if err != nil || result.IsError {
		t.Fatalf("result=%v err=%v", result, err)
	}
	_ = c.Close()

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if ctx.Err() != nil {
		t.Fatal(fmt.Errorf("server did not shut down in time: %w", ctx.Err()))
	}
}
