package mcp

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// PingInput defines the input schema for the temporary Phase 1 ping tool.
type PingInput struct {
	Message string `json:"message,omitempty" jsonschema:"Optional message to echo back"`
}

// PingOutput defines the structured output schema for the temporary Phase 1 ping tool.
type PingOutput struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func (s *Server) registerTools() {
	mcp.AddTool(s.mcpServer, &mcp.Tool{
		Name:        "ping",
		Description: "Verify that the db-mcp server is running and responsive over stdio.",
	}, s.handlePing)
}

func (s *Server) handlePing(_ context.Context, _ *mcp.CallToolRequest, in PingInput) (*mcp.CallToolResult, PingOutput, error) {
	msg := strings.TrimSpace(in.Message)
	if len(msg) > 256 {
		return toolError("message exceeds 256 character limit"), PingOutput{}, nil
	}
	if msg == "" {
		msg = "pong"
	}

	s.logger.Debug("handled ping tool call", "message_len", len(msg))
	return nil, PingOutput{
		Status:  "ok",
		Message: msg,
	}, nil
}

// toolError returns an explicit MCP tool error result (IsError: true) so operational
// failures are surfaced to the LLM without crashing or emitting protocol-level errors.
func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			&mcp.TextContent{Text: msg},
		},
	}
}
