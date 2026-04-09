// Package stash is the GraphQL client for Stash (stashapp/stash).
//
// The client is intentionally minimal: it speaks raw GraphQL queries via
// net/http, supports the optional `ApiKey` header for authenticated
// instances, and maps GraphQL `errors` arrays into typed Go errors so
// callers can detect schema or auth problems explicitly.
//
// The query catalog (findDuplicateScenes, findScenes, sceneMerge, …) lives
// in queries.go and uses Execute. The streaming variant for very large
// findDuplicateScenes responses is added later in scan/scenes.go.
package stash

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Client is a Stash GraphQL client. Construct with NewClient. Safe for
// concurrent use across goroutines (the embedded http.Client is, and we
// add no per-request shared state).
type Client struct {
	// Endpoint is the fully-qualified GraphQL URL, e.g.
	// "http://localhost:9999/graphql".
	Endpoint string

	// APIKey is sent in the `ApiKey` header on every request when non-empty.
	// Stash instances without an API key configured can leave this blank.
	APIKey string

	// HTTP is the underlying transport. NewClient sets a sensible default
	// timeout; override after construction if you need something else.
	HTTP *http.Client
}

// NewClient builds a Client pointed at the given Stash base URL. The base
// URL should be the root of the Stash web UI (e.g. "http://host:9999"); the
// "/graphql" suffix is appended automatically.
//
// apiKey is the literal API key, NOT the env var name. Resolve the env var
// at the call site (typically via config.Config.StashAPIKey).
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		Endpoint: strings.TrimRight(baseURL, "/") + "/graphql",
		APIKey:   apiKey,
		HTTP: &http.Client{
			// Default timeout protects against a hung Stash instance.
			// findDuplicateScenes at 60k scenes can be slow — five minutes
			// is generous but bounded.
			Timeout: 5 * time.Minute,
		},
	}
}

// Execute sends a GraphQL query and decodes the `data` field into out (which
// must be a non-nil pointer to a struct or map matching the query's response
// shape). variables may be nil.
//
// Returns:
//   - *HTTPError      — non-2xx response from Stash
//   - *GraphQLError   — Stash returned a `errors` array
//   - error           — network failure, JSON decode failure, etc.
//
// out may be nil if the caller doesn't need the data (e.g. for a mutation
// whose return value is ignored).
func (c *Client) Execute(ctx context.Context, query string, variables map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("marshalling graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building graphql request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("ApiKey", c.APIKey)
	}

	slog.Debug("stash graphql request",
		"endpoint", c.Endpoint,
		"bytes", len(body),
		"has_api_key", c.APIKey != "",
	)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("stash request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read the body up front so we can include it in HTTP error messages
	// even when the response isn't valid JSON. The size cap prevents a
	// pathological 1GB error page from blowing up memory.
	const maxBody = 64 << 20 // 64 MiB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return fmt.Errorf("reading stash response: %w", err)
	}

	slog.Debug("stash graphql response",
		"status", resp.StatusCode,
		"bytes", len(respBody),
	)

	if resp.StatusCode/100 != 2 {
		return &HTTPError{
			Status: resp.StatusCode,
			Body:   truncateForError(respBody, 512),
		}
	}

	var envelope struct {
		Data   json.RawMessage    `json:"data"`
		Errors []GraphQLErrorItem `json:"errors,omitempty"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decoding stash response: %w (body: %s)", err, truncateForError(respBody, 256))
	}

	if len(envelope.Errors) > 0 {
		return &GraphQLError{Errors: envelope.Errors}
	}

	if out == nil {
		return nil
	}
	if len(envelope.Data) == 0 {
		// Stash returned 200 with neither data nor errors — treat as empty.
		return nil
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("unmarshalling stash data field: %w", err)
	}
	return nil
}

// HTTPError is returned when Stash responds with a non-2xx status code.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("stash returned HTTP %d", e.Status)
	}
	return fmt.Sprintf("stash returned HTTP %d: %s", e.Status, e.Body)
}

// GraphQLError aggregates the items from a GraphQL `errors` array.
type GraphQLError struct {
	Errors []GraphQLErrorItem
}

// GraphQLErrorItem mirrors a single entry in the GraphQL `errors` array.
type GraphQLErrorItem struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

func (e *GraphQLError) Error() string {
	switch len(e.Errors) {
	case 0:
		return "stash graphql: empty error array (this is a bug)"
	case 1:
		return "stash graphql: " + e.Errors[0].Message
	default:
		msgs := make([]string, len(e.Errors))
		for i, item := range e.Errors {
			msgs[i] = item.Message
		}
		return "stash graphql: " + strings.Join(msgs, "; ")
	}
}

// truncateForError shortens a byte slice to a printable string suitable for
// error messages. Long bodies become "...<truncated>" so logs stay readable.
func truncateForError(b []byte, max int) string {
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "...<truncated>"
}
