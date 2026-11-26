package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/moncef-an/db-mcp/internal/config"
	"github.com/moncef-an/db-mcp/internal/db"
)

const (
	serverName    = "db-mcp"
	serverVersion = "0.1.0"
)

// Server wraps the MCP SDK server and coordinates tool registration and stdio transport.
type Server struct {
	mcpServer *mcp.Server
	repo      db.Repository
	cfg       *config.Config
	logger    *slog.Logger
}

// NewServer constructs an MCP server and registers all active tools.
func NewServer(repo db.Repository, cfg *config.Config, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))
	}
	var normalizedCfg config.Config
	if cfg != nil {
		normalizedCfg = *cfg
	}
	if normalizedCfg.MaxRows <= 0 {
		normalizedCfg.MaxRows = config.DefaultMaxRows
	} else if normalizedCfg.MaxRows > config.HardMaxRows {
		normalizedCfg.MaxRows = config.HardMaxRows
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)

	s := &Server{
		mcpServer: srv,
		repo:      repo,
		cfg:       &normalizedCfg,
		logger:    logger,
	}

	s.registerTools()
	return s
}

// Run starts the MCP server over stdio and blocks until the client disconnects or ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	s.logger.Info("starting MCP server over stdio", "name", serverName, "version", serverVersion)
	if err := s.mcpServer.Run(ctx, &mcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp server run: %w", err)
	}
	return nil
}
