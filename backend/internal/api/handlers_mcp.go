// MCP endpoint (REQ-043): POST /api/mcp mounts the protocol server. The TOOL TABLE lives here
// (full DataSource parity — everything the UI can do, including config_get/config_set), each
// tool covered by hp_devlab_access and checked per call. The server address is server-side
// infrastructure, never part of a request. B12 fills the table and the mount.
package api

import "net/http"

// mcpEndpoint serves the JSON-RPC-over-HTTP MCP protocol.
func (s *Server) mcpEndpoint(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "POST /api/mcp is not wired yet (MCP parity, Welle 2)")
}
