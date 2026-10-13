package main

import (
	"os"
	"path/filepath"
)

// toolHomes resolves the Claude Code and Codex homes the way the tools find them: an explicit
// flag, else $CLAUDE_CONFIG_DIR or $CODEX_HOME, else ~/.claude or ~/.codex. Every command uses
// it, so settings are configured in the same homes that history is read from. The user's home
// directory is needed only for a default.
func toolHomes(claudeFlag, codexFlag string) (claude, codex string, err error) {
	claude = firstNonEmpty(claudeFlag, os.Getenv("CLAUDE_CONFIG_DIR"))
	codex = firstNonEmpty(codexFlag, os.Getenv("CODEX_HOME"))
	if claude != "" && codex != "" {
		return claude, codex, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	claude = firstNonEmpty(claude, filepath.Join(home, ".claude"))
	codex = firstNonEmpty(codex, filepath.Join(home, ".codex"))
	return claude, codex, nil
}

// homeSource names the variable a home came from when no flag set it, for messages about it.
func homeSource(flag, env string) string {
	if flag == "" && os.Getenv(env) != "" {
		return " (from $" + env + ")"
	}
	return ""
}
