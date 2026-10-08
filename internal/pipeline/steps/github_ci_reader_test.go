package steps

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

// TestBuildHost_GitHubCIReaderReadsChecksThroughTheConfiguredCommand pins the
// config -> provider wiring: a configured github_ci_reader is the only route
// to check contexts, and a refusing command fails the read instead of falling
// back to the ordinary login.
func TestBuildHost_GitHubCIReaderReadsChecksThroughTheConfiguredCommand(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	credential, err := json.Marshal(map[string]any{
		"token":       "ghs_fakeReader",
		"expires_at":  time.Now().Add(time.Hour),
		"permissions": map[string]string{"checks": "read", "statuses": "read", "metadata": "read"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		credential string
		wantErr    bool
	}{
		"reader":  {credential: string(credential)},
		"refusal": {credential: "not json", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			binDir := fakeCLIBinDir(t)
			linkTestBinary(t, binDir, "gh")
			linkTestBinary(t, binDir, "fake-ci-reader")
			sctx := newTestContext(t, nil, dir, baseSHA, headSHA, config.Commands{})
			sctx.Env = fakeCLIEnv(binDir, map[string]string{
				"FAKE_CLI_MODE":                 "ci-gh",
				"FAKE_CLI_STATE":                "OPEN",
				"FAKE_CLI_CHECKS":               `[{"name":"build","state":"FAILURE","bucket":"fail"}]`,
				"FAKE_CLI_PR_HEAD_SHA":          "deadbeef",
				"FAKE_CLI_CI_READER_CREDENTIAL": tc.credential,
			})
			sctx.Repo.UpstreamURL = "https://github.com/test/repo.git"
			sctx.Config.GitHubCIReader = &config.GitHubCIReader{Command: "fake-ci-reader"}
			host, skip := buildHost(sctx, scm.ProviderGitHub)
			if host == nil {
				t.Fatalf("buildHost returned nil: %s", skip)
			}
			checks, err := host.GetChecks(context.Background(), &scm.PR{Number: "42", HeadSHA: "deadbeef"})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("GetChecks() = %+v, want the refusing reader to fail the read", checks)
				}
				return
			}
			if err != nil || len(checks) != 1 || checks[0].Bucket != scm.CheckBucketFail {
				t.Fatalf("GetChecks() = %+v, %v; want the failed check read through the reader", checks, err)
			}
		})
	}
}
