package mcp

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName    = "db-mcp"
	serverVersion = "0.1.0"
)

// Server wraps the MCP SDK server and coordinates tool registration and stdio transport.
type Server struct {
	mcpServer *mcp.Server
	logger    *slog.Logger
}

// NewServer constructs an MCP server and registers all active tools.
func NewServer(logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    serverName,
		Version: serverVersion,
	}, nil)

	s := &Server{
		mcpServer: srv,
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
