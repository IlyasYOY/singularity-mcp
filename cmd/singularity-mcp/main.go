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

const version = "0.2.0"

func main() {
	result, err := config.Parse(os.Args[1:], os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
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
	client, err := singularity.NewAPIClient(result.Config.BaseURL, result.Config.Token, result.Config.Timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	mcpServer := mcptools.NewServer(client, catalog, version)
	if err := server.ServeStdio(mcpServer); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
