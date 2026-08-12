package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

// legacyProtocolVersion is the historical stdio lifecycle kept at the
// transport boundary. The modern dispatcher never observes this state.
const legacyProtocolVersion = "2025-06-18"

type stdioEra uint8

const (
	stdioEraUnknown stdioEra = iota
	stdioEraModern
	stdioEraLegacy
)

type stdioCompatibilityState struct {
	era               stdioEra
	legacyInitialized bool
}

func (s *Server) handleStdioCompatibility(
	ctx context.Context,
	request protocol.Request,
	state *stdioCompatibilityState,
) (protocol.Response, bool) {
	if state.era == stdioEraUnknown {
		state.era = classifyStdioEra(request)
	}

	switch state.era {
	case stdioEraModern:
		if request.Method == "initialize" {
			return modernInitializeDiagnostic(request.ID), true
		}
		return protocol.Response{}, false
	case stdioEraLegacy:
		return s.handleLegacyStdio(ctx, request, state), true
	default:
		return protocol.Response{}, false
	}
}

func classifyStdioEra(request protocol.Request) stdioEra {
	// An initialize request carrying modern metadata is an accidental use of
	// the removed lifecycle, so keep it in the modern path for an actionable
	// method-not-found diagnostic. A metadata-free initialize starts legacy.
	if request.Method == "initialize" {
		if hasModernMetadata(request.Params) {
			return stdioEraModern
		}
		return stdioEraLegacy
	}
	return stdioEraModern
}

func hasModernMetadata(raw json.RawMessage) bool {
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil || params == nil {
		return false
	}
	metaRaw, ok := params["_meta"]
	if !ok {
		return false
	}
	var meta protocol.RequestMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return false
	}
	return meta.ProtocolVersion != ""
}

func modernInitializeDiagnostic(id *protocol.RequestID) protocol.Response {
	return protocol.Response{
		JSONRPC: "2.0",
		ID:      id,
		Error: protocol.NewErrorWithData(protocol.CodeMethodNotFound,
			"initialize is unavailable for modern MCP; call server/discover instead",
			map[string]any{
				"modernMethod":             "server/discover",
				"legacyLifecycle":          "initialize",
				"supportedProtocolVersion": protocol.Version20260728,
			}),
	}
}

func (s *Server) handleLegacyStdio(
	ctx context.Context,
	request protocol.Request,
	state *stdioCompatibilityState,
) protocol.Response {
	response := protocol.Response{JSONRPC: "2.0", ID: request.ID}
	if !state.legacyInitialized && request.Method == "server/discover" {
		response.Error = protocol.NewError(protocol.CodeMethodNotFound, "method not found")
		return response
	}

	switch request.Method {
	case "server/discover":
		response.Error = protocol.NewError(protocol.CodeMethodNotFound, "legacy server does not implement server/discover; use initialize")
		return response
	case "initialize":
		if state.legacyInitialized {
			response.Error = protocol.NewError(protocol.CodeInvalidRequest, "legacy server is already initialized")
			return response
		}
		state.legacyInitialized = true
		response.Result = mustMarshal(s.legacyInitializeResult())
		return response
	case "notifications/initialized":
		if !state.legacyInitialized {
			response.Error = protocol.NewError(protocol.CodeInvalidRequest, "legacy server is not initialized")
		}
		return response
	}

	if !state.legacyInitialized {
		response.Error = protocol.NewErrorWithData(
			protocol.CodeInvalidRequest,
			"legacy server requires initialize before business requests",
			map[string]any{"requiredMethod": "initialize"},
		)
		return response
	}

	switch request.Method {
	case "tools/list", "tools/call", "resources/list", "resources/read", "prompts/list", "prompts/get":
		return s.handleLegacyBusiness(ctx, request)
	default:
		response.Error = protocol.NewError(protocol.CodeMethodNotFound, "method not found")
		return response
	}
}

// ServeLegacy runs the historical initialize lifecycle on a dedicated stdio
// connection. It is useful for compatibility fixtures and keeps legacy state
// out of the modern stateless Server.Handle path.
func (s *Server) ServeLegacy(ctx context.Context, input io.Reader, output io.Writer) error {
	return s.serveStdio(ctx, input, output, stdioCompatibilityState{era: stdioEraLegacy})
}

type legacyInitializeResult struct {
	ProtocolVersion string                  `json:"protocolVersion"`
	Capabilities    map[string]any          `json:"capabilities"`
	ServerInfo      protocol.Implementation `json:"serverInfo"`
}

func (s *Server) legacyInitializeResult() legacyInitializeResult {
	resources, prompts := s.catalogCapabilities()
	return legacyInitializeResult{
		ProtocolVersion: legacyProtocolVersion,
		Capabilities: map[string]any{
			"tools":     map[string]any{"listChanged": true},
			"resources": resources,
			"prompts":   prompts,
		},
		ServerInfo: protocol.Implementation{Name: serverName, Version: serverVersion},
	}
}

func (s *Server) handleLegacyBusiness(ctx context.Context, request protocol.Request) protocol.Response {
	response := protocol.Response{JSONRPC: "2.0", ID: request.ID}
	params, err := legacyParamsAsModern(request.Params)
	if err != nil {
		response.Error = protocol.NewError(protocol.CodeInvalidParams, err.Error())
		return response
	}
	modernResponse := s.Handle(ctx, protocol.Request{
		JSONRPC: "2.0",
		ID:      request.ID,
		Method:  request.Method,
		Params:  params,
	})
	response.Error = modernResponse.Error
	if modernResponse.Error != nil {
		return response
	}
	response.Result, err = stripModernResultEnvelope(modernResponse.Result)
	if err != nil {
		response.Error = protocol.NewError(protocol.CodeInternalError, fmt.Sprintf("encode legacy result: %v", err))
	}
	return response
}

func legacyParamsAsModern(raw json.RawMessage) (json.RawMessage, error) {
	params := make(map[string]json.RawMessage)
	if len(raw) != 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, fmt.Errorf("legacy params must be an object: %w", err)
		}
	}
	meta, err := json.Marshal(protocol.RequestMeta{
		ProtocolVersion:    protocol.Version20260728,
		ClientCapabilities: map[string]any{},
	})
	if err != nil {
		return nil, fmt.Errorf("encode compatibility metadata: %w", err)
	}
	params["_meta"] = json.RawMessage(meta)
	return json.Marshal(params)
}

func stripModernResultEnvelope(raw json.RawMessage) (json.RawMessage, error) {
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		if err == nil {
			err = fmt.Errorf("result must be an object")
		}
		return nil, err
	}
	delete(result, "resultType")
	delete(result, "_meta")
	delete(result, "ttlMs")
	delete(result, "cacheScope")
	return json.Marshal(result)
}
