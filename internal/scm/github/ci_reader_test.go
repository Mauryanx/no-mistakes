package github

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/scm"
)

const testReaderToken = "ghs_readerToken"

// readerTestCmdFactory serves sequential responses per command and has the
// helper process assert which credential each command ran with: the reader
// token for keys in readerKeys, no token at all for everything else.
func readerTestCmdFactory(t *testing.T, responses map[string][]githubTestResponse, readerKeys map[string]bool) (CmdFactory, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	flat := map[string]githubTestResponse{}
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
			key := strings.TrimSpace(name + " " + strings.Join(args, " "))
			mu.Lock()
			calls = append(calls, key)
			if queue := responses[key]; len(queue) > 0 {
				flat[key] = queue[0]
				if len(queue) > 1 {
					responses[key] = queue[1:]
				}
			}
			response, ok := flat[key]
			mu.Unlock()
			if !ok {
				response = githubTestResponse{stderr: "unexpected command: " + key, code: 1}
			}
			cmd := githubTestCmdFactory(map[string]githubTestResponse{key: response})(ctx, name, args...)
			want := ""
			if readerKeys[key] {
				want = testReaderToken
			}
			cmd.Env = append(cmd.Env, "GH_TOKEN=", "GH_ENTERPRISE_TOKEN=", "GITHUB_TEST_WANT_GH_TOKEN="+want)
			return cmd
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), calls...)
		}
}

func readerCredential(token string, expires time.Duration, permissions map[string]string) string {
	encoded, _ := json.Marshal(map[string]any{"token": token, "expires_at": time.Now().Add(expires).UTC().Format(time.RFC3339), "permissions": permissions})
	return string(encoded)
}

var readerPermissions = map[string]string{"checks": "read", "statuses": "read", "metadata": "read", "pull_requests": "read", "actions": "read"}

const (
	readerHeadView    = "gh pr view 123 --repo test/repo --json headRefOid --jq .headRefOid"
	readerSupplier    = "/opt/reader token github.com test/repo"
	readerWorkflowRun = "gh api --method GET repos/test/repo/actions/runs -f head_sha=h1 -f per_page=100 --paginate --slurp"
)

func readerRollupIDCommand(headSHA string) string {
	return strings.Join([]string{"gh", "api", "graphql", "-f", "query=" + rollupIDQuery, "-F", "owner=test", "-F", "name=repo", "-F", "oid=" + headSHA}, " ")
}

func readerRollupChecksCommand(id, cursor string) string {
	args := []string{"gh", "api", "graphql", "-f", "query=" + rollupChecksQuery, "-F", "id=" + id}
	if cursor != "" {
		args = append(args, "-F", "cursor="+cursor)
	}
	return strings.Join(args, " ")
}

func readerRollupPage(id, nodes string, next string) string {
	return `{"data":{"node":{"id":"` + id + `","contexts":{"nodes":` + nodes + `,"pageInfo":{"hasNextPage":` + map[bool]string{true: "true", false: "false"}[next != ""] + `,"endCursor":"` + next + `"}}}}}`
}

func readerHost(factory CmdFactory) *Host {
	return New(factory, nil, "", "test/repo").WithCIReader(&CIReader{Command: "/opt/reader", Args: []string{"token"}})
}

func TestGetChecksCIReaderReadsOnlyRollupContextsWithReaderToken(t *testing.T) {
	t.Parallel()

	page1, page2 := readerRollupChecksCommand("R1", ""), readerRollupChecksCommand("R1", "c1")
	factory, calls := readerTestCmdFactory(t, map[string][]githubTestResponse{
		readerHeadView:              {{stdout: "h1\n"}},
		readerRollupIDCommand("h1"): {{stdout: `{"data":{"repository":{"object":{"statusCheckRollup":{"id":"R1"}}}}}`}},
		readerSupplier:              {{stdout: readerCredential(testReaderToken, time.Hour, readerPermissions)}},
		page1:                       {{stdout: readerRollupPage("R1", `[{"__typename":"CheckRun","databaseId":7,"name":"build","status":"COMPLETED","conclusion":"FAILURE"}]`, "c1")}},
		page2:                       {{stdout: readerRollupPage("R1", `[{"__typename":"StatusContext","id":"S1","context":"legacy","state":"SUCCESS"}]`, "")}},
		readerWorkflowRun:           {{stdout: `[{"total_count":0,"workflow_runs":[]}]`}},
	}, map[string]bool{page1: true, page2: true})

	// An absent recorded head still binds the current head: the reader cannot read `gh pr checks`.
	pr := &scm.PR{Number: "123"}
	checks, err := readerHost(factory).GetChecks(context.Background(), pr)
	if err != nil {
		t.Fatalf("GetChecks() error = %v", err)
	}
	if len(checks) != 2 || checks[0].Name != "build" || checks[0].Bucket != scm.CheckBucketFail || checks[1].Name != "legacy" || checks[1].Bucket != scm.CheckBucketPass {
		t.Fatalf("checks = %+v, want the failed check run and the legacy status from both pages", checks)
	}
	if pr.HeadSHA != "h1" {
		t.Fatalf("pr.HeadSHA = %q, want the bound head h1", pr.HeadSHA)
	}
	if got := strings.Join(calls(), "\n"); strings.Count(got, readerSupplier) != 1 || strings.Count(got, readerHeadView) != 2 {
		t.Fatalf("calls = %s, want one credential per discovery and head binding before and after", got)
	}
}

func TestGetChecksCIReaderRefusesWithoutFallingBack(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		supplier githubTestResponse
		want     string
	}{
		"refused":        {githubTestResponse{stderr: "repository is outside the configured installation", code: 1}, "outside the configured installation"},
		"malformed":      {githubTestResponse{stdout: `{"token":"ghs_secret","expires_at":"soon"}`}, "unreadable credential"},
		"empty token":    {githubTestResponse{stdout: readerCredential("", time.Hour, readerPermissions)}, "invalid token"},
		"expired":        {githubTestResponse{stdout: readerCredential("ghs_secret", 30*time.Second, readerPermissions)}, "expires within a minute"},
		"no statuses":    {githubTestResponse{stdout: readerCredential("ghs_secret", time.Hour, map[string]string{"checks": "read"})}, "checks and statuses read"},
		"write grant":    {githubTestResponse{stdout: readerCredential("ghs_secret", time.Hour, map[string]string{"checks": "read", "statuses": "read", "contents": "write"})}, "not read-only (contents: write)"},
		"no permissions": {githubTestResponse{stdout: readerCredential("ghs_secret", time.Hour, nil)}, "checks and statuses read"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			factory, calls := readerTestCmdFactory(t, map[string][]githubTestResponse{
				readerHeadView:              {{stdout: "h1\n"}},
				readerRollupIDCommand("h1"): {{stdout: `{"data":{"repository":{"object":{"statusCheckRollup":{"id":"R1"}}}}}`}},
				readerSupplier:              {tc.supplier},
			}, nil)
			_, err := readerHost(factory).GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "h1"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("GetChecks() error = %v, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "ghs_secret") {
				t.Fatalf("GetChecks() error leaked the credential: %v", err)
			}
			for _, call := range calls() {
				if strings.Contains(call, "contexts(first:100") {
					t.Fatalf("checks were read after the reader refused: %s", call)
				}
			}
		})
	}
}

func TestGetChecksCIReaderNeverAcceptsAnotherRollup(t *testing.T) {
	t.Parallel()

	for name, page := range map[string]string{
		"other rollup": readerRollupPage("R2", `[]`, ""),
		"null node":    `{"data":{"node":null},"errors":[{"type":"FORBIDDEN","message":"Resource not accessible by integration"}]}`,
		"wrong type":   `{"data":{"node":{}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			page1 := readerRollupChecksCommand("R1", "")
			factory, _ := readerTestCmdFactory(t, map[string][]githubTestResponse{
				readerHeadView:              {{stdout: "h1\n"}},
				readerRollupIDCommand("h1"): {{stdout: `{"data":{"repository":{"object":{"statusCheckRollup":{"id":"R1"}}}}}`}},
				readerSupplier:              {{stdout: readerCredential(testReaderToken, time.Hour, readerPermissions)}},
				page1:                       {{stdout: page}},
			}, map[string]bool{page1: true})
			checks, err := readerHost(factory).GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "h1"})
			if err == nil || !strings.Contains(err.Error(), "returned no rollup") {
				t.Fatalf("GetChecks() = %+v, %v; want an unreadable-rollup error, never an empty green page", checks, err)
			}
		})
	}
}

func TestGetChecksCIReaderDiscardsAMovedHead(t *testing.T) {
	t.Parallel()

	page1 := readerRollupChecksCommand("R1", "")
	factory, _ := readerTestCmdFactory(t, map[string][]githubTestResponse{
		readerHeadView:              {{stdout: "h1\n"}, {stdout: "h2\n"}},
		readerRollupIDCommand("h1"): {{stdout: `{"data":{"repository":{"object":{"statusCheckRollup":{"id":"R1"}}}}}`}},
		readerSupplier:              {{stdout: readerCredential(testReaderToken, time.Hour, readerPermissions)}},
		page1:                       {{stdout: readerRollupPage("R1", `[{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"}]`, "")}},
		readerWorkflowRun:           {{stdout: `[{"total_count":0,"workflow_runs":[]}]`}},
	}, map[string]bool{page1: true})
	_, err := readerHost(factory).GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "h1"})
	if err == nil || !strings.Contains(err.Error(), "PR head changed during check discovery from h1 to h2") {
		t.Fatalf("GetChecks() error = %v, want the moved head to discard the observation", err)
	}
}

func TestGetChecksCIReaderSkipsTheCredentialWhenTheHeadHasNoRollup(t *testing.T) {
	t.Parallel()

	factory, calls := readerTestCmdFactory(t, map[string][]githubTestResponse{
		readerHeadView:              {{stdout: "h1\n"}},
		readerRollupIDCommand("h1"): {{stdout: `{"data":{"repository":{"object":{"statusCheckRollup":null}}}}`}},
		readerWorkflowRun:           {{stdout: `[{"total_count":1,"workflow_runs":[{"id":101,"name":"build","status":"completed","conclusion":"failure"}]}]`}},
	}, nil)
	checks, err := readerHost(factory).GetChecks(context.Background(), &scm.PR{Number: "123", HeadSHA: "h1"})
	if err != nil {
		t.Fatalf("GetChecks() error = %v", err)
	}
	if len(checks) != 1 || checks[0].Bucket != scm.CheckBucketFail {
		t.Fatalf("checks = %+v, want the workflow run that never reached the rollup", checks)
	}
	if strings.Contains(strings.Join(calls(), "\n"), readerSupplier) {
		t.Fatal("a head without a rollup still minted a credential")
	}
}
