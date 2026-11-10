package mcptool

import (
	"context"
	"errors"
	"net/http"
)

// Preflight validates the private local credential shape and performs exactly
// one authenticated, read-only task API request. It never creates a job or
// contacts a model. Only the API can verify the JWT signature and live session.
func Preflight(ctx context.Context, c Config) error {
	if err := validateConfig(c); err != nil {
		return err
	}
	a := &adapter{config: c, http: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	_, _, code := a.request(ctx, "GET", "/connections", nil)
	if code != "" {
		return errors.New(code)
	}
	return nil
}
