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
// itself. Legacy unfenced sessions retain their existing environment contract.
// Fenced sessions use the allowlist below; X search credentials stay in the broker.
func agentShellExcludedEnv() []string {
	return []string{"DEEPSEEK_API_KEY", webSearchConfigEnv}
}

// codexProcessEnv is the environment a Codex process starts with: the bridge's
// environment plus extra, without agentShellExcludedEnv. Removing a variable
// from the thread's shell_environment_policy is not enough: Codex captures a
// shell snapshot from its own environment and every command sources it, which
// exports the variable again.
func codexProcessEnv(extra []string) []string {
	brokerRequired := envValue(extra, "TOMAKO_CAPABILITY_BROKER_REQUIRED") == "1"
	excluded := map[string]bool{}
	for _, name := range agentShellExcludedEnv() {
		excluded[name] = true
	}
	env := []string{}
	for _, entry := range core.MergeEnv(os.Environ(), extra) {
		if name, _, ok := strings.Cut(entry, "="); ok && excluded[name] {
			continue
		}
		if brokerRequired {
			name, _, _ := strings.Cut(entry, "=")
			if !brokerShellEnvKeys[name] {
				continue
			}
		}
		env = append(env, entry)
	}
	return env
}

// The supervisor can gain new secrets without silently exposing them to every
// shell snapshot. Add only documented non-secret runtime settings here.
var brokerShellEnvKeys = map[string]bool{
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
	"LANG": true, "LC_ALL": true, "LC_CTYPE": true, "TZ": true, "TERM": true,
	"TMPDIR": true, "TMP": true, "TEMP": true, "CODEX_HOME": true,
	"SKILLS_OL_DIR": true, "SKILL_RESULT_API_URL": true, "TOMAKO_ENV_NAMESPACE": true,
	"TOMAKO_TASK_ENV_FILE": true, "TOMAKO_CAPABILITY_BROKER_REQUIRED": true,
	"PLAYWRIGHT_BROWSERS_PATH": true, "NODE_PATH": true, "PYTHONDONTWRITEBYTECODE": true,
}
