package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultBaseURL                = "https://api.singularity-app.com"
	DefaultTimeout                = 30 * time.Second
	DefaultApprovalTimeout        = 2 * time.Minute
	DefaultOperationTimeout       = 2 * time.Minute
	DefaultMaxPages               = 100
	DefaultMaxItems               = 10000
	DefaultMaxResponseBytes int64 = 1048576
)

type Config struct {
	Token                string
	BaseURL              string
	Timeout              time.Duration
	ApprovalTimeout      time.Duration
	RequireWriteApproval bool
	OperationTimeout     time.Duration
	MaxPages             int
	MaxItems             int
	MaxResponseBytes     int64
}

type Result struct {
	Config      Config
	VersionOnly bool
	HelpOnly    bool
}

type Getter func(string) string

func Parse(args []string, getenv Getter) (Result, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	versionRequested := hasVersionFlag(args)
	helpRequested := hasHelpFlag(args)
	approvalTimeoutOverridden := hasNamedFlag(args, "approval-timeout")
	operationTimeoutOverridden := hasNamedFlag(args, "operation-timeout")
	maxPagesOverridden := hasNamedFlag(args, "max-pages")
	maxItemsOverridden := hasNamedFlag(args, "max-items")
	maxResponseBytesOverridden := hasNamedFlag(args, "max-response-bytes")

	cfg := Config{
		Token:                getenv("SINGULARITY_TOKEN"),
		BaseURL:              valueOrDefault(getenv("SINGULARITY_BASE_URL"), DefaultBaseURL),
		Timeout:              DefaultTimeout,
		ApprovalTimeout:      DefaultApprovalTimeout,
		RequireWriteApproval: true,
		OperationTimeout:     DefaultOperationTimeout,
		MaxPages:             DefaultMaxPages,
		MaxItems:             DefaultMaxItems,
		MaxResponseBytes:     DefaultMaxResponseBytes,
	}
	bypassEnv := versionRequested || helpRequested
	if err := parseDurationEnv(getenv, "SINGULARITY_MCP_OPERATION_TIMEOUT", operationTimeoutOverridden, bypassEnv, &cfg.OperationTimeout); err != nil {
		return Result{}, err
	}
	if err := parseIntEnv(getenv, "SINGULARITY_MCP_MAX_PAGES", maxPagesOverridden, bypassEnv, &cfg.MaxPages); err != nil {
		return Result{}, err
	}
	if err := parseIntEnv(getenv, "SINGULARITY_MCP_MAX_ITEMS", maxItemsOverridden, bypassEnv, &cfg.MaxItems); err != nil {
		return Result{}, err
	}
	if err := parseInt64Env(getenv, "SINGULARITY_MCP_MAX_RESPONSE_BYTES", maxResponseBytesOverridden, bypassEnv, &cfg.MaxResponseBytes); err != nil {
		return Result{}, err
	}
	if raw := getenv("SINGULARITY_MCP_REQUIRE_WRITE_APPROVAL"); raw != "" {
		requireWriteApproval, err := strconv.ParseBool(raw)
		if err != nil {
			if versionRequested || helpRequested {
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
			if versionRequested || helpRequested {
				timeout = DefaultTimeout
			} else {
				return Result{}, fmt.Errorf("parse SINGULARITY_TIMEOUT: %w", err)
			}
		}
		if err == nil {
			cfg.Timeout = timeout
		}
	}
	if raw := getenv("SINGULARITY_MCP_APPROVAL_TIMEOUT"); raw != "" && !approvalTimeoutOverridden {
		approvalTimeout, err := time.ParseDuration(raw)
		if err != nil {
			if versionRequested || helpRequested {
				approvalTimeout = DefaultApprovalTimeout
			} else {
				return Result{}, fmt.Errorf("parse SINGULARITY_MCP_APPROVAL_TIMEOUT: %w", err)
			}
		}
		if err == nil {
			cfg.ApprovalTimeout = approvalTimeout
		}
	}

	fs := flag.NewFlagSet("singularity-mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	token := fs.String("token", cfg.Token, "Singularity API bearer token")
	baseURL := fs.String("base-url", cfg.BaseURL, "Singularity API base URL")
	timeout := fs.Duration("timeout", cfg.Timeout, "HTTP request timeout")
	approvalTimeout := fs.Duration("approval-timeout", cfg.ApprovalTimeout, "MCP write approval timeout")
	operationTimeout := fs.Duration("operation-timeout", cfg.OperationTimeout, "total API operation timeout")
	maxPages := fs.Int("max-pages", cfg.MaxPages, "maximum pages per all/search operation")
	maxItems := fs.Int("max-items", cfg.MaxItems, "maximum combined items per all/search operation")
	maxResponseBytes := fs.Int64("max-response-bytes", cfg.MaxResponseBytes, "maximum bytes per HTTP response")
	requireWriteApproval := fs.Bool("require-write-approval", cfg.RequireWriteApproval, "require MCP elicitation approval before write operations")
	versionOnly := fs.Bool("version", false, "print version and exit")
	helpOnly := fs.Bool("help", false, "print help and exit")
	helpShort := fs.Bool("h", false, "print help and exit")
	if err := fs.Parse(args); err != nil {
		return Result{}, err
	}

	cfg.Token = *token
	cfg.BaseURL = *baseURL
	cfg.Timeout = *timeout
	cfg.ApprovalTimeout = *approvalTimeout
	cfg.RequireWriteApproval = *requireWriteApproval
	cfg.OperationTimeout = *operationTimeout
	cfg.MaxPages = *maxPages
	cfg.MaxItems = *maxItems
	cfg.MaxResponseBytes = *maxResponseBytes
	if *helpOnly || *helpShort {
		return Result{Config: cfg, HelpOnly: true}, nil
	}
	if *versionOnly {
		return Result{Config: cfg, VersionOnly: true}, nil
	}
	if err := validateBaseURL(cfg.BaseURL); err != nil {
		return Result{}, err
	}
	if cfg.Timeout <= 0 {
		return Result{}, errors.New("timeout must be positive")
	}
	if cfg.ApprovalTimeout <= 0 {
		return Result{}, errors.New("approval timeout must be positive")
	}
	if cfg.OperationTimeout <= 0 {
		return Result{}, errors.New("operation timeout must be positive")
	}
	if cfg.MaxPages <= 0 {
		return Result{}, errors.New("max pages must be positive")
	}
	if cfg.MaxItems <= 0 {
		return Result{}, errors.New("max items must be positive")
	}
	if cfg.MaxResponseBytes <= 0 {
		return Result{}, errors.New("max response bytes must be positive")
	}
	return Result{Config: cfg}, nil
}

func parseDurationEnv(getenv Getter, key string, overridden, bypass bool, dst *time.Duration) error {
	if raw := getenv(key); raw != "" && !overridden {
		v, err := time.ParseDuration(raw)
		if err != nil {
			if bypass {
				return nil
			}
			return fmt.Errorf("parse %s: %w", key, err)
		}
		*dst = v
	}
	return nil
}
func parseIntEnv(getenv Getter, key string, overridden, bypass bool, dst *int) error {
	if raw := getenv(key); raw != "" && !overridden {
		v, err := strconv.Atoi(raw)
		if err != nil {
			if bypass {
				return nil
			}
			return fmt.Errorf("parse %s: %w", key, err)
		}
		*dst = v
	}
	return nil
}
func parseInt64Env(getenv Getter, key string, overridden, bypass bool, dst *int64) error {
	if raw := getenv(key); raw != "" && !overridden {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			if bypass {
				return nil
			}
			return fmt.Errorf("parse %s: %w", key, err)
		}
		*dst = v
	}
	return nil
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

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "-help", "--help", "-help=true", "--help=true", "-h", "--h", "-h=true", "--h=true":
			return true
		}
	}
	return false
}

func hasNamedFlag(args []string, name string) bool {
	short := "-" + name
	long := "--" + name
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if arg == short || arg == long || strings.HasPrefix(arg, short+"=") || strings.HasPrefix(arg, long+"=") {
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
