// Package awscreds holds the secret-store JSON shape for static AWS IAM
// keys, shared by every component that authenticates to AWS with a
// resolved secret: the gateway's bedrock driver and brain's aws
// connector.
package awscreds

import (
	"encoding/json"
	"fmt"
)

// StaticCredentials is the secret-store JSON shape for static IAM keys:
// {"access_key_id":"...","secret_access_key":"...","session_token":"(optional)","region":"(optional)"}.
// Unknown keys are ignored; AccessKeyID/SecretAccessKey are required.
type StaticCredentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
	Region          string `json:"region,omitempty"`
}

// ErrMissingKeys reports a parsed secret that carries no usable key
// pair. Callers wrap it with their own prefix.
var ErrMissingKeys = fmt.Errorf("static credentials missing access_key_id or secret_access_key")

// Parse reads a secret-store value as static credentials JSON. A
// missing access_key_id or secret_access_key is an error: a resolved
// secret that isn't usable credentials must fail loudly. Errors never
// echo the raw value.
func Parse(raw string) (*StaticCredentials, error) {
	var c StaticCredentials
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("parse static credentials: %w", err)
	}
	if c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return nil, ErrMissingKeys
	}
	return &c, nil
}
