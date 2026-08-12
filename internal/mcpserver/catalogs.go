package mcpserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const catalogPageSize = 2

// Resource describes readable server-managed context.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
	Text        string `json:"-"`
}

// PromptArgument describes one named prompt template argument.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

// PromptContent is one prompt message content block.
type PromptContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// PromptMessage is one rendered prompt message.
type PromptMessage struct {
	Role    string        `json:"role"`
	Content PromptContent `json:"content"`
}

// Prompt describes a reusable server-managed prompt template.
type Prompt struct {
	Name        string                                           `json:"name"`
	Description string                                           `json:"description,omitempty"`
	Arguments   []PromptArgument                                 `json:"arguments,omitempty"`
	Render      func(map[string]string) ([]PromptMessage, error) `json:"-"`
}

type resourceContent struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

type listParams struct {
	protocol.RequestParams
	Cursor string `json:"cursor,omitempty"`
}

type resourceReadParams struct {
	protocol.RequestParams
	URI string `json:"uri"`
}

type promptGetParams struct {
	protocol.RequestParams
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

type resourcesListResult struct {
	protocol.CacheableResult
	Resources  []Resource `json:"resources"`
	NextCursor string     `json:"nextCursor,omitempty"`
}

type resourceReadResult struct {
	protocol.Result
	Contents []resourceContent `json:"contents"`
}

type promptsListResult struct {
	protocol.CacheableResult
	Prompts    []Prompt `json:"prompts"`
	NextCursor string   `json:"nextCursor,omitempty"`
}

type paginatedToolsListResult struct {
	protocol.CacheableResult
	Tools      []ToolDefinition `json:"tools"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type promptGetResult struct {
	protocol.Result
	Description string          `json:"description,omitempty"`
	Messages    []PromptMessage `json:"messages"`
}

type catalogRegistry struct {
	mu        sync.RWMutex
	initOnce  sync.Once
	resources map[string]Resource
	prompts   map[string]Prompt
}

func (c *catalogRegistry) ensureDefaults() {
	c.initOnce.Do(func() {
		c.resources = map[string]Resource{
			"demo://project/guide": {
				URI:         "demo://project/guide",
				Name:        "Project guide",
				Description: "A short guide exposed as readable MCP context.",
				MIMEType:    "text/plain",
				Text:        "Use tools for actions, resources for context, and prompts for reusable workflows.",
			},
		}
		c.prompts = map[string]Prompt{
			"explain-topic": {
				Name:        "explain-topic",
				Description: "Ask for a practical explanation of a topic.",
				Arguments: []PromptArgument{
					{Name: "topic", Description: "The topic to explain.", Required: true},
				},
				Render: renderExplainTopic,
			},
		}
	})
}

func renderExplainTopic(arguments map[string]string) ([]PromptMessage, error) {
	topic := strings.TrimSpace(arguments["topic"])
	if topic == "" {
		return nil, fmt.Errorf("missing required prompt argument %q", "topic")
	}
	return []PromptMessage{{
		Role: "user",
		Content: PromptContent{
			Type: "text",
			Text: "Explain " + topic + " with one practical example.",
		},
	}}, nil
}

// RegisterResource publishes a URI-addressed resource and announces a list change.
func (s *Server) RegisterResource(resource Resource) error {
	if resource.URI == "" || resource.Name == "" {
		return fmt.Errorf("register resource: URI and name are required")
	}
	s.catalogs.ensureDefaults()
	s.catalogs.mu.Lock()
	if _, exists := s.catalogs.resources[resource.URI]; exists {
		s.catalogs.mu.Unlock()
		return fmt.Errorf("register resource: duplicate URI %q", resource.URI)
	}
	s.catalogs.resources[resource.URI] = resource
	s.catalogs.mu.Unlock()
	s.publishListChanged(resourceListChange)
	return nil
}

// RegisterPrompt publishes a named prompt template and announces a list change.
func (s *Server) RegisterPrompt(prompt Prompt) error {
	if prompt.Name == "" || prompt.Render == nil {
		return fmt.Errorf("register prompt: name and renderer are required")
	}
	s.catalogs.ensureDefaults()
	s.catalogs.mu.Lock()
	if _, exists := s.catalogs.prompts[prompt.Name]; exists {
		s.catalogs.mu.Unlock()
		return fmt.Errorf("register prompt: duplicate name %q", prompt.Name)
	}
	s.catalogs.prompts[prompt.Name] = prompt
	s.catalogs.mu.Unlock()
	s.publishListChanged(promptListChange)
	return nil
}

func (s *Server) catalogCapabilities() (map[string]any, map[string]any) {
	return map[string]any{"listChanged": true}, map[string]any{"listChanged": true}
}

func (s *Server) listTools(raw json.RawMessage) (json.RawMessage, *protocol.Error) {
	offset, rpcError := decodeListOffset(raw)
	if rpcError != nil {
		return nil, rpcError
	}
	registered := s.toolSnapshot()
	tools := make([]ToolDefinition, 0, len(registered))
	for _, entry := range registered {
		tools = append(tools, entry.definition)
	}
	page, nextCursor, rpcError := paginate(tools, offset)
	if rpcError != nil {
		return nil, rpcError
	}
	return mustMarshal(paginatedToolsListResult{
		CacheableResult: newListResult(),
		Tools:           page,
		NextCursor:      nextCursor,
	}), nil
}

func (s *Server) listResources(raw json.RawMessage) (json.RawMessage, *protocol.Error) {
	offset, rpcError := decodeListOffset(raw)
	if rpcError != nil {
		return nil, rpcError
	}
	resources := s.resourceSnapshot()
	page, nextCursor, rpcError := paginate(resources, offset)
	if rpcError != nil {
		return nil, rpcError
	}
	return mustMarshal(resourcesListResult{
		CacheableResult: newListResult(),
		Resources:       page,
		NextCursor:      nextCursor,
	}), nil
}

func (s *Server) readResource(raw json.RawMessage) (json.RawMessage, *protocol.Error) {
	var params resourceReadParams
	if err := json.Unmarshal(raw, &params); err != nil || params.URI == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "missing or invalid resource URI")
	}
	s.catalogs.ensureDefaults()
	s.catalogs.mu.RLock()
	resource, ok := s.catalogs.resources[params.URI]
	s.catalogs.mu.RUnlock()
	if ok {
		return mustMarshal(resourceReadResult{
			Result: newResult(),
			Contents: []resourceContent{{
				URI:      resource.URI,
				MIMEType: resource.MIMEType,
				Text:     resource.Text,
			}},
		}), nil
	}
	return nil, protocol.NewError(protocol.CodeInvalidParams, fmt.Sprintf("unknown resource URI %q", params.URI))
}

func (s *Server) listPrompts(raw json.RawMessage) (json.RawMessage, *protocol.Error) {
	offset, rpcError := decodeListOffset(raw)
	if rpcError != nil {
		return nil, rpcError
	}
	prompts := s.promptSnapshot()
	page, nextCursor, rpcError := paginate(prompts, offset)
	if rpcError != nil {
		return nil, rpcError
	}
	return mustMarshal(promptsListResult{
		CacheableResult: newListResult(),
		Prompts:         page,
		NextCursor:      nextCursor,
	}), nil
}

func (s *Server) getPrompt(raw json.RawMessage) (json.RawMessage, *protocol.Error) {
	var params promptGetParams
	if err := json.Unmarshal(raw, &params); err != nil || params.Name == "" {
		return nil, protocol.NewError(protocol.CodeInvalidParams, "missing or invalid prompt name")
	}
	s.catalogs.ensureDefaults()
	s.catalogs.mu.RLock()
	prompt, ok := s.catalogs.prompts[params.Name]
	s.catalogs.mu.RUnlock()
	if ok {
		messages, err := prompt.Render(params.Arguments)
		if err != nil {
			return nil, protocol.NewError(protocol.CodeInvalidParams, err.Error())
		}
		return mustMarshal(promptGetResult{
			Result:      newResult(),
			Description: prompt.Description,
			Messages:    messages,
		}), nil
	}
	return nil, protocol.NewError(protocol.CodeInvalidParams, fmt.Sprintf("unknown prompt %q", params.Name))
}

func (s *Server) resourceSnapshot() []Resource {
	s.catalogs.ensureDefaults()
	s.catalogs.mu.RLock()
	defer s.catalogs.mu.RUnlock()
	resources := make([]Resource, 0, len(s.catalogs.resources))
	for _, resource := range s.catalogs.resources {
		resources = append(resources, resource)
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].URI < resources[j].URI })
	return resources
}

func (s *Server) promptSnapshot() []Prompt {
	s.catalogs.ensureDefaults()
	s.catalogs.mu.RLock()
	defer s.catalogs.mu.RUnlock()
	prompts := make([]Prompt, 0, len(s.catalogs.prompts))
	for _, prompt := range s.catalogs.prompts {
		prompts = append(prompts, prompt)
	}
	sort.Slice(prompts, func(i, j int) bool { return prompts[i].Name < prompts[j].Name })
	return prompts
}

func decodeListOffset(raw json.RawMessage) (int, *protocol.Error) {
	var params listParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return 0, protocol.NewError(protocol.CodeInvalidParams, "invalid list params")
	}
	if params.Cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(params.Cursor)
	if err != nil || !strings.HasPrefix(string(decoded), "offset:") {
		return 0, protocol.NewError(protocol.CodeInvalidParams, "invalid cursor")
	}
	offset, err := strconv.Atoi(strings.TrimPrefix(string(decoded), "offset:"))
	if err != nil || offset < 0 {
		return 0, protocol.NewError(protocol.CodeInvalidParams, "invalid cursor")
	}
	return offset, nil
}

func paginate[T any](values []T, offset int) ([]T, string, *protocol.Error) {
	if offset > len(values) {
		return nil, "", protocol.NewError(protocol.CodeInvalidParams, "cursor is outside the current list")
	}
	end := min(offset+catalogPageSize, len(values))
	page := values[offset:end]
	if end == len(values) {
		return page, "", nil
	}
	nextCursor := base64.RawURLEncoding.EncodeToString([]byte("offset:" + strconv.Itoa(end)))
	return page, nextCursor, nil
}

func newListResult() protocol.CacheableResult {
	return protocol.CacheableResult{
		Result:     newResult(),
		TTLMillis:  toolsListTTLMillis,
		CacheScope: protocol.CacheScopePublic,
	}
}
