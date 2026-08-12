package host

import (
	"encoding/json"
	"fmt"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

func negotiatedExtensionsFromDiscovery(
	offered protocol.Extensions,
	discoveryResult json.RawMessage,
) (protocol.Extensions, error) {
	if err := offered.Validate(); err != nil {
		return nil, fmt.Errorf("validate client extensions: %w", err)
	}
	var discovery struct {
		Capabilities struct {
			Extensions protocol.Extensions `json:"extensions"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(discoveryResult, &discovery); err != nil {
		return nil, fmt.Errorf("decode server extensions: %w", err)
	}
	if err := discovery.Capabilities.Extensions.Validate(); err != nil {
		return nil, fmt.Errorf("validate server extensions: %w", err)
	}
	return protocol.IntersectExtensions(offered, discovery.Capabilities.Extensions), nil
}

func capabilitiesWithExtensions(
	base map[string]any,
	extensions protocol.Extensions,
) (map[string]any, error) {
	if err := extensions.Validate(); err != nil {
		return nil, fmt.Errorf("validate client extensions: %w", err)
	}
	capabilities := make(map[string]any, len(base)+1)
	for name, settings := range base {
		capabilities[name] = settings
	}
	if len(extensions) > 0 {
		capabilities["extensions"] = extensions
	}
	return capabilities, nil
}
