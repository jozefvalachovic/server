// Package mcp provides a zero-dependency Model Context Protocol (MCP) server
// handler that exposes typed Go functions as tools callable by AI agents.
//
// The implementation follows the MCP 2024-11-05 specification using the
// Streamable HTTP transport: a single POST endpoint accepts JSON-RPC 2.0
// requests and returns JSON-RPC 2.0 responses. No external dependencies or
// SSE streaming is required for the basic tool-call lifecycle.
//
// # Protocol flow
//
//  1. Agent sends initialize  → server replies with its capabilities.
//  2. Agent sends initialized (notification, no id) → server replies 204.
//  3. Agent sends tools/list  → server returns the tool catalogue.
//  4. Agent sends tools/call  → server dispatches to Tool.Handler and returns
//     the result in the MCP content-block format.
//
// # Usage
//
//	mux.Handle("/mcp", mcp.Handler(mcp.Config{
//	    Name:    "my-service",
//	    Version: "1.0.0",
//	    Tools: []mcp.Tool{
//	        {
//	            Name:        "get_product",
//	            Description: "Fetch a product by its numeric ID.",
//	            Input:       (*GetProductInput)(nil),
//	            Handler: func(ctx context.Context, raw json.RawMessage) (any, error) {
//	                var in GetProductInput
//	                if err := json.Unmarshal(raw, &in); err != nil {
//	                    return nil, err
//	                }
//	                return findProduct(in.ID)
//	            },
//	        },
//	    },
//	}))
//
// Mount via routes.RegisterMCP for automatic trailing-slash handling:
//
//	routes.RegisterMCP(mux, "/mcp", mcp.Config{ … })
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"time"
)

// ── Public API ────────────────────────────────────────────────────────────────

// Tool defines one MCP tool exposed to agents.
type Tool struct {
	// Name is the identifier agents use when calling this tool.
	// Use snake_case (e.g. "list_products", "send_email").
	Name string

	// Description explains what the tool does. Agents use this to determine
	// when to call the tool, so be precise and action-oriented.
	Description string

	// Input is a typed nil pointer (or zero value) of the struct that
	// describes the tool's JSON input parameters. The package reflects its
	// fields to produce a JSON Schema that agents use to compose calls.
	//   Input: (*MyInputStruct)(nil)
	// Pass nil for tools that take no parameters.
	Input any

	// Handler is invoked when the agent calls this tool. raw is the
	// JSON-encoded arguments object exactly as sent by the agent; unmarshal
	// it into the same type as Input. Return any JSON-serialisable value on
	// success, or a non-nil error to signal a tool-level failure (the agent
	// will see isError:true in the response).
	Handler func(ctx context.Context, raw json.RawMessage) (any, error)
}

// Config holds server metadata and the list of exposed tools.
type Config struct {
	// Name is the server name advertised during the initialize handshake.
	Name string
	// Version is the server version advertised during the initialize handshake.
	Version string
	// Tools is the list of tools the agent may discover and call.
	Tools []Tool
	// AllowedOrigins restricts the Access-Control-Allow-Origin header to the
	// listed origins. When empty (the default) the header is set to "*".
	AllowedOrigins []string
}

// Handler returns an http.Handler that implements the MCP Streamable HTTP
// transport. Mount it with routes.RegisterMCP for correct path handling.
func Handler(cfg Config) http.Handler {
	if cfg.Name == "" {
		cfg.Name = "mcp-server"
	}
	if cfg.Version == "" {
		cfg.Version = "1.0.0"
	}

	// Pre-build the tool catalogue once (schema reflection is not cheap).
	catalogue := make([]toolDef, 0, len(cfg.Tools))
	for _, t := range cfg.Tools {
		catalogue = append(catalogue, toolDef{
			tool:   t,
			schema: buildSchema(t.Input),
		})
	}

	// Pre-build static responses so no allocations occur on the hot path.
	toolEntries := make([]toolEntry, len(catalogue))
	for i, td := range catalogue {
		toolEntries[i] = toolEntry{
			Name:        td.tool.Name,
			Description: td.tool.Description,
			InputSchema: td.schema,
		}
	}
	capJSON, _ := json.Marshal(capabilityDoc{Name: cfg.Name, Version: cfg.Version, Protocol: "mcp"})
	capJSON = append(capJSON, '\n')

	allowedOrigin := "*"
	if len(cfg.AllowedOrigins) > 0 {
		allowedOrigin = strings.Join(cfg.AllowedOrigins, ", ")
	}

	s := &server{
		cfg:           cfg,
		allowedOrigin: allowedOrigin,
		tools:         catalogue,
		initResult: initializeResult{
			ProtocolVersion: "2024-11-05",
			Capabilities:    initCapabilities{},
			ServerInfo:      serverInfo{Name: cfg.Name, Version: cfg.Version},
		},
		listResult:     toolsListResult{Tools: toolEntries},
		capabilityJSON: capJSON,
	}
	return s
}

// ── Internal types ─────────────────────────────────────────────────────────

type toolDef struct {
	tool   Tool
	schema inputSchema
}

type server struct {
	cfg            Config
	allowedOrigin  string // pre-computed, immutable after construction
	tools          []toolDef
	initResult     initializeResult // pre-built, immutable after construction
	listResult     toolsListResult  // pre-built, immutable after construction
	capabilityJSON []byte           // pre-encoded GET response
}

// ── Typed response structs ────────────────────────────────────────────────────
// Using concrete types instead of map[string]any eliminates per-request heap
// allocations for every MCP method response.

type initializeResult struct {
	ProtocolVersion string           `json:"protocolVersion"`
	Capabilities    initCapabilities `json:"capabilities"`
	ServerInfo      serverInfo       `json:"serverInfo"`
}

type initCapabilities struct {
	Tools struct{} `json:"tools"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolsListResult struct {
	Tools []toolEntry `json:"tools"`
}

type toolEntry struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type toolCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type capabilityDoc struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Protocol string `json:"protocol"`
}

type emptyResult struct{}

// ── JSON-RPC 2.0 wire types ───────────────────────────────────────────────────

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // string | number | null | absent
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Standard JSON-RPC error codes.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInternalError  = -32603
)

// ── HTTP handler ──────────────────────────────────────────────────────────────

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// MCP uses POST for all JSON-RPC messages.
	// Allow OPTIONS for CORS pre-flight (agents may run cross-origin).
	switch r.Method {
	case http.MethodOptions:
		setCORSHeaders(w, s.allowedOrigin)
		w.WriteHeader(http.StatusNoContent)
		return
	case http.MethodGet:
		// Some clients ping the endpoint; return a minimal capability document.
		setCORSHeaders(w, s.allowedOrigin)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(s.capabilityJSON)
		return
	case http.MethodPost:
		// intentional fall-through
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	setCORSHeaders(w, s.allowedOrigin)

	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, nil, codeParseError, "Parse error")
		return
	}
	if req.JSONRPC != "2.0" {
		writeError(w, req.ID, codeInvalidRequest, "Invalid JSON-RPC version")
		return
	}

	// Notifications have no id — do not send a response body, just 204.
	isNotification := req.ID == nil || string(req.ID) == "null"

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	result, rErr := s.dispatch(ctx, req)

	if isNotification {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	if rErr != nil {
		resp.Error = rErr
	} else {
		resp.Result = result
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *server) dispatch(ctx context.Context, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		return s.handleInitialize(req.Params)
	case "initialized":
		return nil, nil // notification, caller will 204
	case "ping":
		return &emptyResult{}, nil
	case "tools/list":
		return s.handleToolsList()
	case "tools/call":
		return s.handleToolsCall(ctx, req.Params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("Method not found: %s", req.Method)}
	}
}

// ── MCP method handlers ───────────────────────────────────────────────────────

func (s *server) handleInitialize(_ json.RawMessage) (any, *rpcError) {
	return &s.initResult, nil
}

func (s *server) handleToolsList() (any, *rpcError) {
	return &s.listResult, nil
}

func (s *server) handleToolsCall(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpcError{Code: codeInvalidRequest, Message: "Invalid tools/call params"}
	}

	for _, td := range s.tools {
		if td.tool.Name != p.Name {
			continue
		}
		if p.Arguments == nil {
			p.Arguments = json.RawMessage("{}")
		}
		result, err := td.tool.Handler(ctx, p.Arguments)
		if err != nil {
			return &toolCallResult{
				Content: []contentBlock{{Type: "text", Text: err.Error()}},
				IsError: true,
			}, nil
		}
		// Serialise result to a JSON string for the text content block, so
		// agents receive a predictable { type: "text", text: "<json>" } shape.
		b, merr := json.Marshal(result)
		if merr != nil {
			return nil, &rpcError{Code: codeInternalError, Message: "Failed to serialise tool result"}
		}
		return &toolCallResult{
			Content: []contentBlock{{Type: "text", Text: string(b)}},
		}, nil
	}

	return nil, &rpcError{Code: codeMethodNotFound, Message: fmt.Sprintf("Tool not found: %s", p.Name)}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func setCORSHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// ── JSON Schema reflection ────────────────────────────────────────────────────

// inputSchema is a minimal JSON Schema object sufficient for MCP tool inputs.
type inputSchema struct {
	Type       string                    `json:"type"`
	Properties map[string]propertySchema `json:"properties,omitempty"`
	Required   []string                  `json:"required,omitempty"`
}

type propertySchema struct {
	Type        string                    `json:"type"`
	Description string                    `json:"description,omitempty"`
	Items       *propertySchema           `json:"items,omitempty"`      // for array types
	Properties  map[string]propertySchema `json:"properties,omitempty"` // for object types
}

var timeType = reflect.TypeFor[time.Time]()

func buildSchema(v any) inputSchema {
	if v == nil {
		return inputSchema{Type: "object"}
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return inputSchema{Type: "object"}
	}

	props := map[string]propertySchema{}
	var required []string

	for sf := range t.Fields() {
		if !sf.IsExported() {
			continue
		}
		if sf.Anonymous {
			// Inline embedded structs.
			inner := buildSchema(reflect.New(sf.Type).Interface())
			maps.Copy(props, inner.Properties)
			required = append(required, inner.Required...)
			continue
		}

		tag := sf.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, rest, _ := strings.Cut(tag, ",")
		if name == "" {
			name = sf.Name
		}
		omitempty := strings.Contains(rest, "omitempty")

		ft := sf.Type
		isPtr := ft.Kind() == reflect.Pointer
		if isPtr {
			ft = ft.Elem()
		}

		desc := sf.Tag.Get("description")
		ps := reflectProp(ft, desc)
		props[name] = ps

		if !isPtr && !omitempty {
			required = append(required, name)
		}
	}

	return inputSchema{
		Type:       "object",
		Properties: props,
		Required:   required,
	}
}

func reflectProp(t reflect.Type, desc string) propertySchema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType {
		return propertySchema{Type: "string", Description: coalesce(desc, "datetime (RFC 3339)")}
	}
	switch t.Kind() {
	case reflect.String:
		return propertySchema{Type: "string", Description: desc}
	case reflect.Bool:
		return propertySchema{Type: "boolean", Description: desc}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return propertySchema{Type: "integer", Description: desc}
	case reflect.Float32, reflect.Float64:
		return propertySchema{Type: "number", Description: desc}
	case reflect.Slice:
		items := reflectProp(t.Elem(), "")
		return propertySchema{Type: "array", Description: desc, Items: &items}
	case reflect.Map:
		return propertySchema{Type: "object", Description: desc}
	case reflect.Struct:
		// Nested object — recurse.
		inner := buildSchema(reflect.New(t).Interface())
		return propertySchema{Type: "object", Description: desc, Properties: inner.Properties}
	default:
		return propertySchema{Type: "string", Description: desc}
	}
}

func coalesce(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
