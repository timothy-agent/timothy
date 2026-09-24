// Package redact scrubs secrets out of error text before it reaches a
// log or an API response.
package redact

import "strings"

// Token rebuilds err with every occurrence of secret in its message
// replaced by "REDACTED". errors.Is and errors.As still reach every
// error of err's chain whose own text and chain never mention secret,
// so sentinels survive while the errors that leak it stay unreachable.
func Token(err error, secret string) error {
	if err == nil || secret == "" {
		return err
	}
	return &redacted{msg: strings.ReplaceAll(err.Error(), secret, "REDACTED"), keep: clean(err, secret)}
}

type redacted struct {
	msg  string
	keep []error
}

func (e *redacted) Error() string   { return e.msg }
func (e *redacted) Unwrap() []error { return e.keep }

// clean returns the largest parts of err's chain free of secret.
func clean(err error, secret string) []error {
	if safe(err, secret) {
		return []error{err}
	}
	var out []error
	for _, c := range children(err) {
		out = append(out, clean(c, secret)...)
	}
	return out
}

func safe(err error, secret string) bool {
	if strings.Contains(err.Error(), secret) {
		return false
	}
	for _, c := range children(err) {
		if !safe(c, secret) {
			return false
		}
	}
	return true
}

func children(err error) []error {
	switch u := err.(type) {
	case interface{ Unwrap() error }:
		if c := u.Unwrap(); c != nil {
			return []error{c}
		}
	case interface{ Unwrap() []error }:
		return u.Unwrap()
	}
	return nil
}
