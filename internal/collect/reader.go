package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/thingzio/devbox/internal/classify"
	"github.com/thingzio/devbox/internal/github"
)

// ErrTooManyFiles means a comparison lists 300 or more files, so the list may be truncated.
var ErrTooManyFiles = errors.New("comparison lists 300 or more files")

const (
	compareFileLimit = 300
	maxGraphQLPages  = 100
)

// Observed is one open PR seen by a fingerprint poll.
type Observed struct {
	Number      int
	Fingerprint string
	HeadOID     string
}

// Reader is every GitHub read collection needs.
type Reader interface {
	Fingerprints(ctx context.Context, owner, name string) ([]Observed, error)
	Detail(ctx context.Context, owner, name string, number int, viewer string) (*Detail, error)
	Checks(ctx context.Context, owner, name, head string, number int) ([]classify.Check, error)
	AwaitingRuns(ctx context.Context, owner, name, head string) (int, error)
	MarkerConclusions(ctx context.Context, owner, name, head, check string) ([]string, error)
	ChangedFiles(ctx context.Context, owner, name, base, head string) ([]string, error)
	Diff(ctx context.Context, owner, name, base, commit string) ([]byte, error)
	CommentBody(ctx context.Context, owner, name string, id int64) (string, error)
	RulesetTypes(ctx context.Context, owner, name, branch string) ([]string, error)
	ClassicSignatures(ctx context.Context, owner, name, branch string) (bool, error)
}

// GitHubReader implements Reader with the read-only GitHub client.
type GitHubReader struct {
	Client *github.Client
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type fingerprintNode struct {
	Number           int       `json:"number"`
	UpdatedAt        time.Time `json:"updatedAt"`
	BaseRefOid       string    `json:"baseRefOid"`
	HeadRefOid       string    `json:"headRefOid"`
	MergeStateStatus string    `json:"mergeStateStatus"`
	ReviewDecision   string    `json:"reviewDecision"`
	IsDraft          bool      `json:"isDraft"`
	Commits          Nodes[struct {
		Commit struct {
			StatusCheckRollup *Rollup `json:"statusCheckRollup"`
		} `json:"commit"`
	}] `json:"commits"`
}

func (n fingerprintNode) fields() FingerprintFields {
	rollup := ""
	if c := n.Commits.Nodes; len(c) > 0 && c[len(c)-1].Commit.StatusCheckRollup != nil {
		rollup = c[len(c)-1].Commit.StatusCheckRollup.State
	}
	return FingerprintFields{
		UpdatedAt: n.UpdatedAt, BaseOID: n.BaseRefOid, HeadOID: n.HeadRefOid, MergeState: n.MergeStateStatus,
		ReviewDecision: n.ReviewDecision, Draft: n.IsDraft, Rollup: rollup,
	}
}

// Fingerprints lists every open PR with its fingerprint; a repeated cursor is an error (§4.2).
func (g GitHubReader) Fingerprints(ctx context.Context, owner, name string) ([]Observed, error) {
	var out []Observed
	seen := map[string]bool{}
	var cursor *string
	for page := 0; page < maxGraphQLPages; page++ {
		var data struct {
			Repository *struct {
				PullRequests struct {
					PageInfo pageInfo          `json:"pageInfo"`
					Nodes    []fingerprintNode `json:"nodes"`
				} `json:"pullRequests"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": owner, "name": name, "endCursor": cursor}
		if err := g.Client.GraphQL(ctx, "Fingerprints", fingerprintsQuery, vars, &data); err != nil {
			return nil, err
		}
		if data.Repository == nil {
			return nil, fmt.Errorf("repository %s/%s not found", owner, name)
		}
		conn := data.Repository.PullRequests
		for _, n := range conn.Nodes {
			out = append(out, Observed{Number: n.Number, Fingerprint: Fingerprint(n.fields()), HeadOID: n.HeadRefOid})
		}
		if !conn.PageInfo.HasNextPage {
			return out, nil
		}
		if seen[conn.PageInfo.EndCursor] {
			return nil, fmt.Errorf("pull request pagination repeated cursor %s", conn.PageInfo.EndCursor)
		}
		seen[conn.PageInfo.EndCursor] = true
		next := conn.PageInfo.EndCursor
		cursor = &next
	}
	return nil, fmt.Errorf("pull requests of %s/%s exceed %d pages", owner, name, maxGraphQLPages)
}

// Detail reads one PR's PullRequestDetail node.
func (g GitHubReader) Detail(ctx context.Context, owner, name string, number int, viewer string) (*Detail, error) {
	var data struct {
		Repository *struct {
			PullRequest *Detail `json:"pullRequest"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": owner, "name": name, "number": number, "viewer": viewer}
	if err := g.Client.GraphQL(ctx, "PullRequestDetail", detailQuery, vars, &data); err != nil {
		return nil, err
	}
	if data.Repository == nil || data.Repository.PullRequest == nil {
		return nil, fmt.Errorf("pull request %s/%s#%d not found", owner, name, number)
	}
	return data.Repository.PullRequest, nil
}

type checkNode struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	IsRequired bool   `json:"isRequired"`
	CheckSuite *struct {
		WorkflowRun *struct {
			DatabaseID int64 `json:"databaseId"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
	Context string `json:"context"`
	State   string `json:"state"`
}

func (n checkNode) check() classify.Check {
	if n.Typename != "CheckRun" {
		return classify.Check{Name: n.Context, State: n.State, Required: n.IsRequired}
	}
	state := n.Conclusion
	if state == "" {
		state = n.Status
	}
	var run int64
	if n.CheckSuite != nil && n.CheckSuite.WorkflowRun != nil {
		run = n.CheckSuite.WorkflowRun.DatabaseID
	}
	return classify.Check{Name: n.Name, State: state, Required: n.IsRequired, RunID: run}
}

// Checks reads the head commit's status contexts with required flags for the PR.
func (g GitHubReader) Checks(ctx context.Context, owner, name, head string, number int) ([]classify.Check, error) {
	out := []classify.Check{}
	seen := map[string]bool{}
	var cursor *string
	for page := 0; page < maxGraphQLPages; page++ {
		var data struct {
			Repository struct {
				Object *struct {
					StatusCheckRollup *struct {
						Contexts struct {
							PageInfo pageInfo    `json:"pageInfo"`
							Nodes    []checkNode `json:"nodes"`
						} `json:"contexts"`
					} `json:"statusCheckRollup"`
				} `json:"object"`
			} `json:"repository"`
		}
		vars := map[string]any{"owner": owner, "name": name, "oid": head, "pr": number, "endCursor": cursor}
		if err := g.Client.GraphQL(ctx, "Checks", checksQuery, vars, &data); err != nil {
			return nil, err
		}
		obj := data.Repository.Object
		if obj == nil || obj.StatusCheckRollup == nil {
			return out, nil
		}
		conn := obj.StatusCheckRollup.Contexts
		for _, n := range conn.Nodes {
			out = append(out, n.check())
		}
		if !conn.PageInfo.HasNextPage {
			return out, nil
		}
		if seen[conn.PageInfo.EndCursor] {
			return nil, fmt.Errorf("check pagination repeated cursor %s", conn.PageInfo.EndCursor)
		}
		seen[conn.PageInfo.EndCursor] = true
		next := conn.PageInfo.EndCursor
		cursor = &next
	}
	return nil, fmt.Errorf("checks exceed %d pages", maxGraphQLPages)
}

func repoPath(owner, name string) string {
	return "repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func escapeBranch(branch string) string {
	parts := strings.Split(branch, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// AwaitingRuns counts workflow runs on head waiting for approval.
func (g GitHubReader) AwaitingRuns(ctx context.Context, owner, name, head string) (int, error) {
	var out struct {
		TotalCount int `json:"total_count"`
	}
	path := fmt.Sprintf("%s/actions/runs?head_sha=%s&status=action_required&per_page=1", repoPath(owner, name), url.QueryEscape(head))
	if _, err := g.Client.Get(ctx, path, &out); err != nil {
		return 0, err
	}
	return out.TotalCount, nil
}

// MarkerConclusions lists the conclusions of runs of one check on head ("" while running).
func (g GitHubReader) MarkerConclusions(ctx context.Context, owner, name, head, check string) ([]string, error) {
	var out struct {
		CheckRuns []struct {
			Conclusion *string `json:"conclusion"`
		} `json:"check_runs"`
	}
	path := fmt.Sprintf("%s/commits/%s/check-runs?check_name=%s&per_page=100", repoPath(owner, name), url.PathEscape(head), url.QueryEscape(check))
	if _, err := g.Client.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	conclusions := make([]string, 0, len(out.CheckRuns))
	for _, r := range out.CheckRuns {
		c := ""
		if r.Conclusion != nil {
			c = *r.Conclusion
		}
		conclusions = append(conclusions, c)
	}
	return conclusions, nil
}

// ChangedFiles lists changed and previous file names of base...head.
func (g GitHubReader) ChangedFiles(ctx context.Context, owner, name, base, head string) ([]string, error) {
	var out struct {
		Files *[]struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
		} `json:"files"`
	}
	path := fmt.Sprintf("%s/compare/%s...%s", repoPath(owner, name), url.PathEscape(base), url.PathEscape(head))
	if _, err := g.Client.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	if out.Files == nil {
		return nil, errors.New("comparison has no file list")
	}
	if len(*out.Files) >= compareFileLimit {
		return nil, ErrTooManyFiles
	}
	var names []string
	for _, f := range *out.Files {
		names = append(names, f.Filename)
		if f.PreviousFilename != "" {
			names = append(names, f.PreviousFilename)
		}
	}
	return names, nil
}

// Diff fetches the three-dot diff base...commit.
func (g GitHubReader) Diff(ctx context.Context, owner, name, base, commit string) ([]byte, error) {
	return g.Client.GetDiff(ctx, fmt.Sprintf("%s/compare/%s...%s", repoPath(owner, name), url.PathEscape(base), url.PathEscape(commit)))
}

// CommentBody reads one issue comment body.
func (g GitHubReader) CommentBody(ctx context.Context, owner, name string, id int64) (string, error) {
	var out struct {
		Body string `json:"body"`
	}
	if _, err := g.Client.Get(ctx, fmt.Sprintf("%s/issues/comments/%d", repoPath(owner, name), id), &out); err != nil {
		return "", err
	}
	return out.Body, nil
}

// RulesetTypes lists rule types that apply to a branch across all ruleset pages.
func (g GitHubReader) RulesetTypes(ctx context.Context, owner, name, branch string) ([]string, error) {
	var types []string
	path := fmt.Sprintf("%s/rules/branches/%s?per_page=100", repoPath(owner, name), escapeBranch(branch))
	err := g.Client.GetPages(ctx, path, func(page []byte) error {
		var rulesOnPage []struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(page, &rulesOnPage); err != nil {
			return fmt.Errorf("decode rulesets: %w", err)
		}
		for _, r := range rulesOnPage {
			types = append(types, r.Type)
		}
		return nil
	})
	return types, err
}

// ClassicSignatures reads classic branch protection's required-signatures flag.
func (g GitHubReader) ClassicSignatures(ctx context.Context, owner, name, branch string) (bool, error) {
	var out struct {
		Enabled bool `json:"enabled"`
	}
	path := fmt.Sprintf("%s/branches/%s/protection/required_signatures", repoPath(owner, name), escapeBranch(branch))
	if _, err := g.Client.Get(ctx, path, &out); err != nil {
		return false, err
	}
	return out.Enabled, nil
}
