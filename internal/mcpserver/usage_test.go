package mcpserver_test

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestTokenUsageHasNoMCPMutation_AT_41 proves AT-41's honesty boundary:
// extension usage reporting remains CLI-only.
func TestTokenUsageHasNoMCPMutation_AT_41(t *testing.T) {
	cs := client(t)
	listed, err := cs.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if strings.Contains(tool.Name, "usage") {
			t.Fatalf("usage mutation leaked onto MCP surface: %s", tool.Name)
		}
	}
}
