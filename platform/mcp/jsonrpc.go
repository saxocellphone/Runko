package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// protocolVersion is the MCP protocol revision this adapter implements.
const protocolVersion = "2024-11-05"

// Server is the MCP stdio server: a thin remote adapter (§8.3, §17.4) that
// bridges newline-delimited JSON-RPC 2.0 on stdin/stdout to runkod's REST
// API. It holds no state beyond the client - every tool call round-trips
// to the daemon, so answers are as fresh as the CLI's would be.
type Server struct {
	Client *Client
}

// rpcRequest is one incoming JSON-RPC 2.0 message. ID is kept raw: it may
// be a number or a string and must be echoed back byte-identically; a nil
// ID marks a notification, which never gets a response.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// toolContent is the MCP tools/call result: tool output (or a structured
// Error, with IsError set) serialized as JSON text content.
type toolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content []toolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Serve reads newline-delimited JSON-RPC requests from r until EOF (the
// MCP stdio transport framing), writing one response line per request.
// Notifications get no response; a malformed line gets a -32700; an
// unknown method gets a -32601. Serve itself only errors on I/O failure -
// protocol-level problems are answered in-band, never fatal, since a
// long-lived agent session shouldn't die because one message was bad.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(w)

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if err := enc.Encode(rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
				Error: &rpcError{Code: -32700, Message: fmt.Sprintf("parse error: %v", err)}}); err != nil {
				return err
			}
			continue
		}
		resp, respond := s.handle(ctx, req)
		if !respond {
			continue
		}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// handle answers one request. The bool is false for notifications
// (requests without an id), which must not be answered per JSON-RPC 2.0.
func (s *Server) handle(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	isNotification := len(req.ID) == 0 || string(req.ID) == "null"
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}

	switch req.Method {
	case "initialize":
		resp.Result = map[string]interface{}{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
			"serverInfo":      map[string]interface{}{"name": "runko", "version": "v1"},
		}
	case "ping":
		resp.Result = map[string]interface{}{}
	case "tools/list":
		resp.Result = map[string]interface{}{"tools": Tools}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: fmt.Sprintf("invalid params: %v", err)}
			break
		}
		out, toolErr := s.CallTool(ctx, params.Name, params.Arguments)
		var payload interface{} = out
		isError := false
		if toolErr != nil {
			payload = toolErr
			isError = true
		}
		text, err := json.Marshal(payload)
		if err != nil {
			resp.Error = &rpcError{Code: -32603, Message: fmt.Sprintf("marshal tool result: %v", err)}
			break
		}
		resp.Result = toolCallResult{
			Content: []toolContent{{Type: "text", Text: string(text)}},
			IsError: isError,
		}
	default:
		if strings.HasPrefix(req.Method, "notifications/") {
			return rpcResponse{}, false
		}
		resp.Error = &rpcError{Code: -32601, Message: fmt.Sprintf("method %q not found", req.Method)}
	}

	if isNotification {
		return rpcResponse{}, false
	}
	return resp, true
}
