package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

var (
	gqlLiterals   = regexp.MustCompile(`"(?:\\.|[^"\\])*"|#[^\n]*`)
	gqlQueryStart = regexp.MustCompile(`^\s*query\b`)
	gqlWrite      = regexp.MustCompile(`\b(mutation|subscription)\b`)
	linkNext      = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)
)

func checkReadOnly(query string) error {
	ops := gqlLiterals.ReplaceAllString(query, "")
	if !gqlQueryStart.MatchString(ops) || gqlWrite.MatchString(ops) {
		return ErrReadOnly
	}
	return nil
}

// GraphQL runs a read-only query and decodes its data into out. A non-empty
// errors array is a failure: partial data is unusable.
func (c *Client) GraphQL(ctx context.Context, op, query string, vars map[string]any, out any) error {
	if err := checkReadOnly(query); err != nil {
		return err
	}
	payload, err := json.Marshal(struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables,omitempty"`
	}{query, vars})
	if err != nil {
		return fmt.Errorf("github: encode %s variables: %w", op, err)
	}
	resp, err := c.do(ctx, request{kind: "graphql", label: op, method: http.MethodPost, path: "graphql", accept: "application/json", body: payload})
	if err != nil {
		return err
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(resp.body, &env); err != nil {
		return fmt.Errorf("github: graphql %s: invalid JSON: %w", op, err)
	}
	if len(env.Errors) > 0 {
		return graphQLError(resp.header, env.Errors)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return fmt.Errorf("github: graphql %s: empty data", op)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("github: graphql %s: decode: %w", op, err)
	}
	return nil
}

// Get performs a REST GET and decodes JSON into out when out is non-nil.
func (c *Client) Get(ctx context.Context, path string, out any) (http.Header, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, request{kind: "rest", label: stripQuery(path), method: http.MethodGet, path: path, accept: "application/vnd.github+json"})
	if err != nil {
		return nil, err
	}
	if out != nil {
		if err := json.Unmarshal(resp.body, out); err != nil {
			return nil, fmt.Errorf("github: GET %s: invalid JSON: %w", stripQuery(path), err)
		}
	}
	return resp.header, nil
}

// GetPages follows rel="next" links, passing each raw page body to each.
func (c *Client) GetPages(ctx context.Context, path string, each func(page []byte) error) error {
	if err := checkPath(path); err != nil {
		return err
	}
	seen := map[string]bool{}
	next := path
	for page := 0; next != ""; page++ {
		if page >= maxPages {
			return fmt.Errorf("github: GET %s: more than %d pages", stripQuery(path), maxPages)
		}
		if seen[next] {
			return fmt.Errorf("github: GET %s: pagination repeated a link", stripQuery(path))
		}
		seen[next] = true
		resp, err := c.do(ctx, request{kind: "rest", label: stripQuery(path), method: http.MethodGet, path: next, accept: "application/vnd.github+json"})
		if err != nil {
			return err
		}
		if err := each(resp.body); err != nil {
			return err
		}
		next = ""
		if m := linkNext.FindStringSubmatch(resp.header.Get("Link")); m != nil {
			next = m[1]
		}
	}
	return nil
}

// GetDiff fetches a REST resource in diff media type.
func (c *Client) GetDiff(ctx context.Context, path string) ([]byte, error) {
	if err := checkPath(path); err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, request{kind: "diff", label: stripQuery(path), method: http.MethodGet, path: path, accept: "application/vnd.github.diff"})
	if err != nil {
		return nil, err
	}
	return resp.body, nil
}

func checkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "/") || strings.Contains(p, "://") {
		return fmt.Errorf("github: invalid REST path %q", p)
	}
	return nil
}

func stripQuery(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		return p[:i]
	}
	return p
}
