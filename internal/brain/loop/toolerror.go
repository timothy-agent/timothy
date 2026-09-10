package loop

import (
	"encoding/json"
	"strings"
)

// D-104: a failed tool call reaches the model as a small JSON object
// instead of prose, so the retry decision is a field rather than
// something the model has to infer from wording. Codes name the class
// of failure; retryable says whether re-issuing the same call could
// ever succeed. The wrap happens at one boundary (executeOne), and
// only for the model-facing content: the digest, the audit line, the
// stream event and the mission event all keep the raw prose.
type toolError struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// Tool error codes. The first group cannot succeed on a retry: the
// call is refused by policy, names nothing, or was turned down by a
// human. The second group can: the failure is transient or external.
const (
	codePolicyDenied       = "policy_denied"
	codeUnknownTool        = "unknown_tool"
	codeCallCap            = "call_cap"
	codeUserDenied         = "user_denied"
	codeTimeout            = "timeout"
	codeNetwork            = "network"
	codeGatewayUnavailable = "gateway_unavailable"
	codeToolError          = "tool_error"
)

// retryableFor reports whether a call that failed with code could
// succeed if the model issued it again unchanged. An unknown code is
// treated as retryable: a new failure class is more often transient
// than permanent, and a wrong "do not retry" costs the model a
// recovery it could have made.
func retryableFor(code string) bool {
	switch code {
	case codePolicyDenied, codeUnknownTool, codeCallCap, codeUserDenied:
		return false
	default:
		return true
	}
}

// toolErrorEnvelopeBytes is the room wrapToolError reserves for the
// JSON scaffolding around the message: the field names, the code, the
// retryable flag, and worst-case escaping of the message itself.
const toolErrorEnvelopeBytes = 256

// wrapToolError renders a failed call's message as the structured
// object. capBytes is the turn's tool-result cap: the message is
// trimmed to fit inside it BEFORE marshaling, because capToolResult
// runs on the finished content and would otherwise cut the object
// mid-JSON. A marshal failure falls back to the raw message so a
// failure never becomes an empty result.
func wrapToolError(code, msg string, capBytes int) string {
	if capBytes > toolErrorEnvelopeBytes {
		if budget := capBytes - toolErrorEnvelopeBytes; len(msg) > budget {
			msg = msg[:budget] + "\n[truncated]"
		}
	}
	b, err := json.Marshal(toolError{Error: code, Message: msg, Retryable: retryableFor(code)})
	if err != nil {
		return msg
	}
	return string(b)
}

// alreadyStructured reports whether s is already a JSON object with an
// "error" key, in which case the loop passes it through unwrapped
// rather than nesting one error object inside another. See the result
// convention on tools.Tool.
func alreadyStructured(s string) bool {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "{") {
		return false
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &obj); err != nil {
		return false
	}
	_, ok := obj["error"]
	return ok
}
