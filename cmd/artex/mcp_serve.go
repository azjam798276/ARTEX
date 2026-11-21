package main

import (
	"fmt"
	"log"
	"os"

	"github.com/Autumn-27/artex/mcpserver"
)

// runMCPServe serves one ARTEX instance to an MCP client over stdio.
//
// Everything it writes to stdout is a JSON-RPC message: the transport owns the
// stream, so diagnostics go to stderr through the standard logger. It exits 0
// when the client closes the pipe, which is the normal way a session ends.
func runMCPServe(apiURL, token, tokenEnv string, insecure bool) int {
	// A token given both ways is a configuration mistake worth naming: the
	// environment variable is the intended one and wins, so say which was used
	// rather than silently ignoring -mcp-token.
	if token == "" && tokenEnv != "" {
		token = os.Getenv(tokenEnv)
		if token == "" {
			log.Printf("[mcp] %s is empty: set it to an ARTEX API token, or pass -mcp-token", tokenEnv)
			return 2
		}
	}

	client, err := mcpserver.NewClient(apiURL, token, insecure)
	if err != nil {
		log.Printf("[mcp] %v", err)
		return 2
	}
	srv := mcpserver.NewServer(client, version)

	// stderr, not stdout: stdout is the protocol channel.
	fmt.Fprintf(os.Stderr, "[mcp] serving %s to stdio (%d tools, protocol %s)\n",
		apiURL, len(srv.ToolNames()), mcpserver.ProtocolVersion)

	if err := mcpserver.Serve(srv, os.Stdin, os.Stdout); err != nil {
		log.Printf("[mcp] %v", err)
		return 1
	}
	return 0
}
