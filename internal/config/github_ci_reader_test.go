package config

import (
	"strings"
	"testing"
)

func TestLoadGlobalParsesGitHubCIReader(t *testing.T) {
	cfg, err := LoadGlobalFromBytes([]byte("github_ci_reader:\n  command: /opt/bin/reader\n  args: [token]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubCIReader == nil || cfg.GitHubCIReader.Command != "/opt/bin/reader" || strings.Join(cfg.GitHubCIReader.Args, " ") != "token" {
		t.Fatalf("GitHubCIReader = %#v", cfg.GitHubCIReader)
	}
	unset, err := LoadGlobalFromBytes([]byte(defaultConfigYAML))
	if err != nil {
		t.Fatal(err)
	}
	if unset.GitHubCIReader != nil {
		t.Fatalf("default config enables the CI reader: %#v", unset.GitHubCIReader)
	}
	for _, raw := range []string{"github_ci_reader:\n  args: [token]\n", "github_ci_reader:\n  command: bin/reader\n"} {
		if _, err := LoadGlobalFromBytes([]byte(raw)); err == nil || !strings.Contains(err.Error(), "github_ci_reader.command") {
			t.Fatalf("LoadGlobalFromBytes(%q) error = %v, want a github_ci_reader.command error", raw, err)
		}
	}
}

func TestMergeTakesGitHubCIReaderFromGlobalOnly(t *testing.T) {
	repo, err := LoadRepoFromBytes([]byte("github_ci_reader:\n  command: /tmp/evil\n"))
	if err != nil {
		t.Fatalf("LoadRepoFromBytes: %v", err)
	}
	if merged := Merge(DefaultGlobalConfig(), repo); merged.GitHubCIReader != nil {
		t.Fatalf("repository config configured a CI reader: %#v", merged.GitHubCIReader)
	}
	global, err := LoadGlobalFromBytes([]byte("github_ci_reader:\n  command: /opt/bin/reader\n"))
	if err != nil {
		t.Fatal(err)
	}
	if merged := Merge(global, repo); merged.GitHubCIReader == nil || merged.GitHubCIReader.Command != "/opt/bin/reader" {
		t.Fatalf("merged GitHubCIReader = %#v, want the global command", merged.GitHubCIReader)
	}
}
