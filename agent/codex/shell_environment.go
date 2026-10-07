package codex

import (
	"os"
	"strings"

	"github.com/chenhg5/cc-connect/core"
)

// agentShellExcludedEnv names bridge environment variables that Codex, and so
// every Agent command, shell snapshot and MCP server it starts, must not
// inherit. Provider credentials in the bridge environment ended up in command
// output and model context; the bridge reads the web search credentials path
// itself. XAI_API_KEY is still inherited because signals-x-search.py reads it
// directly; it can join this list once X search runs as a platform tool.
func agentShellExcludedEnv() []string {
	return []string{"DEEPSEEK_API_KEY", webSearchConfigEnv}
}

// codexProcessEnv is the environment a Codex process starts with: the bridge's
// environment plus extra, without agentShellExcludedEnv. Removing a variable
// from the thread's shell_environment_policy is not enough: Codex captures a
// shell snapshot from its own environment and every command sources it, which
// exports the variable again.
func codexProcessEnv(extra []string) []string {
	excluded := map[string]bool{}
	for _, name := range agentShellExcludedEnv() {
		excluded[name] = true
	}
	env := []string{}
	for _, entry := range core.MergeEnv(os.Environ(), extra) {
		if name, _, ok := strings.Cut(entry, "="); ok && excluded[name] {
			continue
		}
		env = append(env, entry)
	}
	return env
}
