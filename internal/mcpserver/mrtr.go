package mcpserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/shychee/mcp-from-scratch/internal/protocol"
)

const (
	confirmPreviewInputKey = "confirm_preview"
	demoRequestStateKey    = "mcp-from-scratch-confirm-preview-v1"
)

type inputRequest struct {
	Method string            `json:"method"`
	Params elicitationParams `json:"params"`
}

type elicitationParams struct {
	Mode            string         `json:"mode"`
	Message         string         `json:"message"`
	RequestedSchema map[string]any `json:"requestedSchema"`
}

type elicitationResult struct {
	Action  string `json:"action"`
	Content struct {
		Confirm bool `json:"confirm"`
	} `json:"content,omitempty"`
}

type confirmPreviewArguments struct {
	Preview string `json:"preview"`
}

type confirmPreviewTool struct{}

func (confirmPreviewTool) Definition() tool {
	return tool{
		Name:        confirmPreviewInputKey,
		Description: "Preview an inert action and complete only after explicit confirmation.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"preview": map[string]any{
					"type":        "string",
					"description": "Description of the inert action to confirm.",
				},
			},
			"required": []string{"preview"},
		},
	}
}

func (confirmPreviewTool) Call(invocation toolCallInvocation) (toolCallResult, error) {
	if !supportsFormElicitation(invocation.ClientCapabilities) {
		return toolCallResult{}, protocol.NewErrorWithData(
			protocol.CodeMissingRequiredClientCapability,
			"missing required client capability",
			map[string]any{
				"requiredCapabilities": map[string]any{
					"elicitation": map[string]any{
						"form": map[string]any{},
					},
				},
			},
		)
	}

	var args confirmPreviewArguments
	if err := json.Unmarshal(invocation.Arguments, &args); err != nil {
		return toolCallResult{}, fmt.Errorf("decode confirm_preview arguments: %w", err)
	}

	if len(invocation.InputResponses) == 0 {
		if invocation.RequestState != "" {
			return toolCallResult{}, fmt.Errorf("requestState requires inputResponses")
		}
		return newConfirmPreviewInputRequired(args.Preview), nil
	}
	if invocation.RequestState == "" {
		return toolCallResult{}, fmt.Errorf("missing requestState")
	}
	if !validConfirmPreviewState(args.Preview, invocation.RequestState) {
		return toolCallResult{}, fmt.Errorf("invalid requestState")
	}

	rawResponse, ok := invocation.InputResponses[confirmPreviewInputKey]
	if !ok {
		return newConfirmPreviewInputRequired(args.Preview), nil
	}
	var response elicitationResult
	if err := json.Unmarshal(rawResponse, &response); err != nil {
		return toolCallResult{}, fmt.Errorf("decode confirm_preview input response: %w", err)
	}

	message := "preview was not confirmed"
	switch response.Action {
	case "accept":
		if response.Content.Confirm {
			message = "confirmed preview: " + args.Preview
		}
	case "decline":
		message = "preview declined"
	case "cancel":
		message = "preview canceled"
	default:
		return toolCallResult{}, fmt.Errorf("invalid elicitation action %q", response.Action)
	}

	return toolCallResult{
		Content: []contentBlock{{Type: "text", Text: message}},
	}, nil
}

func newConfirmPreviewInputRequired(preview string) toolCallResult {
	return toolCallResult{
		Result: protocol.Result{ResultType: protocol.ResultTypeInputRequired},
		InputRequests: map[string]inputRequest{
			confirmPreviewInputKey: {
				Method: "elicitation/create",
				Params: elicitationParams{
					Mode:    "form",
					Message: "Confirm this preview: " + preview,
					RequestedSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"confirm": map[string]any{
								"type":        "boolean",
								"description": "Confirm the preview.",
							},
						},
						"required": []string{"confirm"},
					},
				},
			},
		},
		RequestState: confirmPreviewState(preview),
	}
}

func supportsFormElicitation(capabilities map[string]any) bool {
	raw, ok := capabilities["elicitation"]
	if !ok {
		return false
	}
	elicitation, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	if len(elicitation) == 0 {
		return true
	}
	_, ok = elicitation["form"]
	return ok
}

func confirmPreviewState(preview string) string {
	return base64.RawURLEncoding.EncodeToString(confirmPreviewStateDigest(preview))
}

func confirmPreviewStateDigest(preview string) []byte {
	mac := hmac.New(sha256.New, []byte(demoRequestStateKey))
	_, _ = mac.Write([]byte(confirmPreviewInputKey))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(preview))
	return mac.Sum(nil)
}

func validConfirmPreviewState(preview, state string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return false
	}
	return hmac.Equal(decoded, confirmPreviewStateDigest(preview))
}
