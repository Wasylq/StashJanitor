package stash

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestServer wires an httptest.Server whose handler asserts the request
// shape and replies with the supplied body+status. Returns a Client pointing
// at the server.
func newTestServer(t *testing.T, status int, body string, assertReq func(*http.Request, []byte)) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		if assertReq != nil {
			assertReq(r, buf)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "")
}

func TestExecuteSuccess(t *testing.T) {
	c := newTestServer(t, 200, `{"data":{"version":{"version":"v0.31.0"}}}`,
		func(r *http.Request, body []byte) {
			if r.URL.Path != "/graphql" {
				t.Errorf("expected POST to /graphql, got %s %s", r.Method, r.URL.Path)
			}
			if r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("missing/incorrect Content-Type: %q", r.Header.Get("Content-Type"))
			}
			if r.Header.Get("ApiKey") != "" {
				t.Errorf("expected no ApiKey header for client with empty key, got %q", r.Header.Get("ApiKey"))
			}
			var env map[string]any
			if err := json.Unmarshal(body, &env); err != nil {
				t.Errorf("request body is not JSON: %v", err)
			}
			if _, ok := env["query"]; !ok {
				t.Error("request body missing 'query' field")
			}
		},
	)

	var out struct {
		Version struct {
			Version string `json:"version"`
		} `json:"version"`
	}
	if err := c.Execute(context.Background(), `{ version { version } }`, nil, &out); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.Version.Version != "v0.31.0" {
		t.Errorf("Version = %q, want v0.31.0", out.Version.Version)
	}
}

func TestExecuteSetsAPIKeyHeaderWhenPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("ApiKey"); got != "secret" {
			t.Errorf("ApiKey header = %q, want %q", got, "secret")
		}
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	t.Cleanup(srv.Close)

	c := NewClient(srv.URL, "secret")
	if err := c.Execute(context.Background(), `{}`, nil, nil); err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestExecuteHTTPError(t *testing.T) {
	c := newTestServer(t, 500, `internal server error`, nil)
	err := c.Execute(context.Background(), `{}`, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected *HTTPError, got %T: %v", err, err)
	}
	if httpErr.Status != 500 {
		t.Errorf("Status = %d, want 500", httpErr.Status)
	}
	if !strings.Contains(httpErr.Body, "internal server error") {
		t.Errorf("body = %q, expected to contain 'internal server error'", httpErr.Body)
	}
}

func TestExecuteGraphQLError(t *testing.T) {
	c := newTestServer(t, 200, `{"errors":[{"message":"unauthenticated"},{"message":"second error"}]}`, nil)
	err := c.Execute(context.Background(), `{}`, nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var gqlErr *GraphQLError
	if !errors.As(err, &gqlErr) {
		t.Fatalf("expected *GraphQLError, got %T: %v", err, err)
	}
	if len(gqlErr.Errors) != 2 {
		t.Errorf("len(Errors) = %d, want 2", len(gqlErr.Errors))
	}
	if !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("error message missing 'unauthenticated': %v", err)
	}
	if !strings.Contains(err.Error(), "second error") {
		t.Errorf("error message missing 'second error': %v", err)
	}
}

func TestExecuteAcceptsNilOut(t *testing.T) {
	c := newTestServer(t, 200, `{"data":{"sceneUpdate":{"id":"42"}}}`, nil)
	if err := c.Execute(context.Background(), `mutation { sceneUpdate(...) }`, nil, nil); err != nil {
		t.Fatalf("Execute with nil out: %v", err)
	}
}

func TestExecutePassesVariables(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &captured)
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "")

	vars := map[string]any{"id": "42", "distance": 4}
	if err := c.Execute(context.Background(), `query($id: ID!, $distance: Int!) { ... }`, vars, nil); err != nil {
		t.Fatal(err)
	}
	gotVars, ok := captured["variables"].(map[string]any)
	if !ok {
		t.Fatalf("variables not present in request, got: %v", captured)
	}
	if gotVars["id"] != "42" {
		t.Errorf("variables.id = %v, want 42", gotVars["id"])
	}
	if gotVars["distance"].(float64) != 4 {
		t.Errorf("variables.distance = %v, want 4", gotVars["distance"])
	}
}
