package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"time"
)

const (
	DefaultBaseURL = "https://api.singularity-app.com"
	DefaultTimeout = 30 * time.Second
)

type Config struct {
	Token                string
	BaseURL              string
	Timeout              time.Duration
	RequireWriteApproval bool
}

type Result struct {
	Config      Config
	VersionOnly bool
}

type Getter func(string) string

func Parse(args []string, getenv Getter) (Result, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	versionRequested := hasVersionFlag(args)

	cfg := Config{
		Token:                getenv("SINGULARITY_TOKEN"),
		BaseURL:              valueOrDefault(getenv("SINGULARITY_BASE_URL"), DefaultBaseURL),
		Timeout:              DefaultTimeout,
		RequireWriteApproval: true,
	}
	if raw := getenv("SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL"); raw != "" {
		requireWriteApproval, err := strconv.ParseBool(raw)
		if err != nil {
			if versionRequested {
				requireWriteApproval = cfg.RequireWriteApproval
			} else {
				return Result{}, fmt.Errorf("parse SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL: %w", err)
			}
		}
		cfg.RequireWriteApproval = requireWriteApproval
	}
	if raw := getenv("SINGULARITY_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil {
			if versionRequested {
				timeout = DefaultTimeout
			} else {
				return Result{}, fmt.Errorf("parse SINGULARITY_TIMEOUT: %w", err)
			}
		}
		if err == nil {
			cfg.Timeout = timeout
		}
	}

	fs := flag.NewFlagSet("singularity-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	token := fs.String("token", cfg.Token, "Singularity API bearer token")
	baseURL := fs.String("base-url", cfg.BaseURL, "Singularity API base URL")
	timeout := fs.Duration("timeout", cfg.Timeout, "HTTP request timeout")
	requireWriteApproval := fs.Bool("require-write-approval", cfg.RequireWriteApproval, "require MCP elicitation approval before write operations")
	versionOnly := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return Result{}, err
	}

	cfg.Token = *token
	cfg.BaseURL = *baseURL
	cfg.Timeout = *timeout
	cfg.RequireWriteApproval = *requireWriteApproval
	if *versionOnly {
		return Result{Config: cfg, VersionOnly: true}, nil
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return Result{}, err
	}
	if cfg.Timeout <= 0 {
		return Result{}, errors.New("timeout must be positive")
	}
	return Result{Config: cfg}, nil
}

func hasVersionFlag(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-version", "--version", "-version=true", "--version=true":
			return true
		}
	}
	return false
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func validateBaseURL(value string) error {
	u, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("parse base URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("base URL must include scheme and host: %q", value)
	}
	return nil
}
