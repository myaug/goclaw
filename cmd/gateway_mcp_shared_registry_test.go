package cmd

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/providers"
)

// The gateway-wide MCP manager connects every enabled server without grant
// filtering (initMCPFromDB). Its tools must stay out of the shared tool
// registry, which every agent registry is cloned from, while admin tool
// browsing (ServerToolInfos) keeps working.
func TestGatewayMCPManagerKeepsSharedRegistryFree(t *testing.T) {
	srv := mcpserver.NewMCPServer("vault", "1.0.0", mcpserver.WithToolCapabilities(false))
	for _, name := range []string{"lookup", "transfer"} {
		srv.AddTool(mcpgo.NewTool(name, mcpgo.WithDescription(name)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("ok"), nil
		})
	}
	ts := httptest.NewServer(mcpserver.NewStreamableHTTPServer(srv))
	defer ts.Close()

	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	cfg.Tools.Browser.Enabled = false
	toolsReg, _, mcpMgr, _, _, _, _, _, _, _, _, _ := setupToolRegistry(cfg, cfg.Agents.Defaults.Workspace, providers.NewRegistry(nil))
	defer mcpMgr.Stop()

	// Same calls initMCPFromDB makes once it has built configs from the DB rows.
	enabled := true
	mcpMgr.SetConfigs(map[string]*config.MCPServerConfig{
		"vault": {Transport: "streamable-http", URL: ts.URL, Enabled: &enabled},
	})
	if err := mcpMgr.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if got := len(mcpMgr.ServerToolInfos("vault")); got != 2 {
		t.Fatalf("gateway manager tool infos = %d, want 2 (admin browsing)", got)
	}
	for _, name := range toolsReg.List() {
		if strings.HasPrefix(name, "mcp_vault__") {
			t.Fatalf("shared registry holds gateway-connected MCP tool %q: %v", name, toolsReg.List())
		}
	}
	if _, ok := toolsReg.GetToolGroup("mcp"); ok {
		t.Fatal("shared registry has an mcp tool group from the gateway manager")
	}
}
