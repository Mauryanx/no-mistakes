package config

import "fmt"

// GitHubCIReader is the optional github_ci_reader command, which prints a
// short-lived read-only credential for GitHub check-context reads. It is
// global-only, like provider_plugins: it names an executable the daemon runs,
// so no repository layer can set it. Semantics live in
// reference/global-config.md.
type GitHubCIReader struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

func normalizeGitHubCIReader(raw *GitHubCIReader) (*GitHubCIReader, error) {
	if raw == nil {
		return nil, nil
	}
	command, err := normalizeProviderPluginCommand(raw.Command)
	if err != nil {
		return nil, fmt.Errorf("invalid github_ci_reader.command: %w", err)
	}
	return &GitHubCIReader{Command: command, Args: append([]string(nil), raw.Args...)}, nil
}
