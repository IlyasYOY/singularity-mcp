package main

import (
	"fmt"
	"os"

	"github.com/IlyasYOY/singularity-mcp/internal/config"
	"github.com/IlyasYOY/singularity-mcp/internal/singularity"
	mcptools "github.com/IlyasYOY/singularity-mcp/internal/tools"
	"github.com/IlyasYOY/singularity-mcp/openapi"
	"github.com/mark3labs/mcp-go/server"
)

const version = "0.3.0"

func usage(version string) string {
	return fmt.Sprintf(`singularity-mcp %s

Usage:
  singularity-mcp [flags]

Flags:
  -token string
        Singularity API bearer token (env: SINGULARITY_TOKEN)
  -base-url string
        Singularity API base URL (env: SINGULARITY_BASE_URL, default: %s)
  -timeout duration
        HTTP request timeout (env: SINGULARITY_TIMEOUT, default: %s)
  -approval-timeout duration
        MCP write approval timeout (env: SINGULARITY_MCP_APPROVAL_TIMEOUT, default: %s)
  -operation-timeout duration
        total API operation timeout (env: SINGULARITY_MCP_OPERATION_TIMEOUT, default: %s)
  -max-pages int
        maximum pages per all/search operation (env: SINGULARITY_MCP_MAX_PAGES, default: %d)
  -max-items int
        maximum combined items (env: SINGULARITY_MCP_MAX_ITEMS, default: %d)
  -max-response-bytes int
        maximum bytes per HTTP response (env: SINGULARITY_MCP_MAX_RESPONSE_BYTES, default: %d)
  -require-write-approval
        require MCP elicitation approval before write operations (env: SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL, default: true)
  -version
        print version and exit
  -help, -h
        print help and exit
`, version, config.DefaultBaseURL, config.DefaultTimeout, config.DefaultApprovalTimeout, config.DefaultOperationTimeout, config.DefaultMaxPages, config.DefaultMaxItems, config.DefaultMaxResponseBytes)
}

func toolOptions(cfg config.Config) mcptools.Options {
	return mcptools.Options{
		RequireWriteApproval: cfg.RequireWriteApproval,
		ApprovalTimeout:      cfg.ApprovalTimeout,
		OperationTimeout:     cfg.OperationTimeout,
	}
}

func main() {
	result, err := config.Parse(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if result.HelpOnly {
		fmt.Print(usage(version))
		return
	}
	if result.VersionOnly {
		fmt.Printf("singularity-mcp %s\n", version)
		return
	}

	catalog, err := singularity.NewCatalog(openapi.Snapshot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	client, err := singularity.NewAPIClient(result.Config.BaseURL, result.Config.Token, result.Config.Timeout,
		singularity.WithPaginationLimits(result.Config.MaxPages, result.Config.MaxItems),
		singularity.WithMaxResponseBytes(result.Config.MaxResponseBytes))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mcpServer := mcptools.NewServerWithOptions(client, catalog, version, toolOptions(result.Config))
	if err := server.ServeStdio(mcpServer); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
