package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

var extensionIDPattern = regexp.MustCompile(
	`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)+/[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`,
)

// Extensions maps namespaced extension identifiers to opaque settings objects.
type Extensions map[string]json.RawMessage

// Validate checks extension identifiers and the settings object boundary without
// interpreting extension-specific fields.
func (extensions Extensions) Validate() error {
	for id, settings := range extensions {
		if !extensionIDPattern.MatchString(id) {
			return fmt.Errorf("invalid extension identifier %q", id)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(settings, &object); err != nil || object == nil {
			return fmt.Errorf("extension %q settings must be a JSON object", id)
		}
	}
	return nil
}

// IntersectExtensions returns server settings for extensions offered by both
// peers. Unknown settings remain byte-for-byte opaque to the core protocol.
func IntersectExtensions(client, server Extensions) Extensions {
	negotiated := make(Extensions)
	for id, serverSettings := range server {
		if _, ok := client[id]; !ok {
			continue
		}
		negotiated[id] = bytes.Clone(serverSettings)
	}
	return negotiated
}
