package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ciReaderTimeout bounds one credential command invocation.
const ciReaderTimeout = 30 * time.Second

// CIReader is the optional global github_ci_reader command. It supplies a
// short-lived read-only credential used for exactly one call: reading the
// head commit's check contexts. Fine-grained personal access tokens have no
// Checks permission, so without it check contexts on a private repository are
// unreadable. Everything else, including head binding, workflow runs, logs and
// reruns, keeps the ordinary gh identity. See github_ci_reader in
// reference/global-config.md.
type CIReader struct {
	Command string
	Args    []string
}

// WithCIReader enables the CI reader for check-context reads.
func (h *Host) WithCIReader(reader *CIReader) *Host {
	h.ciReader = reader
	return h
}

// rollupIDQuery resolves the exact head commit's check rollup with the
// ordinary login. The rollup ID encodes the commit, so reading contexts by
// node ID keeps the read bound to headSHA without the reader traversing
// commits (an App without Contents permission cannot).
const rollupIDQuery = `query($owner:String!,$name:String!,$oid:String!){repository(owner:$owner,name:$name){object(expression:$oid){... on Commit{statusCheckRollup{id}}}}}`

const rollupChecksQuery = `query($id:ID!,$cursor:String){node(id:$id){... on StatusCheckRollup{id ` + checkContextsSelection + `}}}`

func (h *Host) headRollupID(ctx context.Context, owner, name, headSHA string) (string, error) {
	args := []string{"api"}
	if h.host != "" {
		args = append(args, "--hostname", h.host)
	}
	args = append(args, "graphql", "-f", "query="+rollupIDQuery,
		"-F", "owner="+owner, "-F", "name="+name, "-F", "oid="+headSHA)
	out, err := h.cmd(ctx, "gh", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh api check rollup for head commit: %s: %w", strings.TrimSpace(string(out)), err)
	}
	var response struct {
		Data struct {
			Repository *struct {
				Object *struct {
					Rollup *struct {
						ID string `json:"id"`
					} `json:"statusCheckRollup"`
				} `json:"object"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		return "", fmt.Errorf("parse check rollup for head commit: %w", err)
	}
	if response.Data.Repository == nil || response.Data.Repository.Object == nil {
		return "", errors.New("head commit check discovery returned no commit")
	}
	if response.Data.Repository.Object.Rollup == nil {
		return "", nil
	}
	if response.Data.Repository.Object.Rollup.ID == "" {
		return "", errors.New("head commit check discovery returned a rollup without an id")
	}
	return response.Data.Repository.Object.Rollup.ID, nil
}

// ciReadToken runs the configured command as `<command> <args...> <host>
// <owner/repo>` and validates the credential it prints. A refusal is an
// error: the read never falls back to the ordinary identity. The output is
// never echoed because it carries the token.
func (h *Host) ciReadToken(ctx context.Context) (string, error) {
	host := h.host
	if host == "" {
		host = "github.com"
	}
	ctx, cancel := context.WithTimeout(ctx, ciReaderTimeout)
	defer cancel()
	args := append(append([]string(nil), h.ciReader.Args...), host, h.repoSlug())
	out, err := h.cmd(ctx, h.ciReader.Command, args...).Output()
	if err != nil {
		detail := ""
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			detail = strings.TrimSpace(string(exitErr.Stderr))
		}
		return "", fmt.Errorf("github_ci_reader command failed: %s: %w", detail, err)
	}
	var credential struct {
		Token       string            `json:"token"`
		ExpiresAt   time.Time         `json:"expires_at"`
		Permissions map[string]string `json:"permissions"`
	}
	if json.Unmarshal(out, &credential) != nil {
		return "", errors.New("github_ci_reader command printed an unreadable credential")
	}
	if credential.Token == "" || strings.IndexFunc(credential.Token, func(r rune) bool { return r <= ' ' || r > '~' }) >= 0 {
		return "", errors.New("github_ci_reader command printed an invalid token")
	}
	if time.Until(credential.ExpiresAt) < time.Minute {
		return "", errors.New("github_ci_reader credential is expired or expires within a minute")
	}
	if credential.Permissions["checks"] != "read" || credential.Permissions["statuses"] != "read" {
		return "", errors.New("github_ci_reader credential must grant checks and statuses read")
	}
	for permission, access := range credential.Permissions {
		if access != "read" {
			return "", fmt.Errorf("github_ci_reader credential is not read-only (%s: %s)", permission, access)
		}
	}
	return credential.Token, nil
}
