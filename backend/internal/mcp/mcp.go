// Package mcp is the MCP protocol server (REQ-043): JSON-RPC over HTTP, WITHOUT any domain
// knowledge — the tool table (with its per-tool rights coverage) lives in the api layer. The
// server's address is server-side infrastructure, never part of a request.
package mcp

import (
	"context"
	"encoding/json"
	"net/http"

	"devlab/backend/internal/auth"
)

// Tool is one exposed capability: name, description, JSON schema and its call. Every call is
// covered by the rights system — the api layer checks hp_devlab_access per tool before Call.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
	Call        func(ctx context.Context, u auth.User, args json.RawMessage) (any, error)
}

// Server speaks the protocol over the given tool table.
type Server struct {
	_ struct{}
}

// NewServer builds the protocol server for a tool table.
func NewServer(tools []Tool) *Server {
	panic("TODO(B12)")
}

// Handler returns the JSON-RPC-over-HTTP handler.
func (s *Server) Handler() http.Handler {
	panic("TODO(B12)")
}
