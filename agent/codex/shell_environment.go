package codex

// agentShellExcludedEnv names bridge environment variables that Agent commands
// must not inherit. The bridge's own provider credentials stay with the bridge;
// a Skill that needs an external service reaches it through a platform tool.
// XAI_API_KEY is still inherited because signals-x-search.py reads it directly;
// it can join this list once X search runs as a platform tool.
func agentShellExcludedEnv() []string {
	return []string{"DEEPSEEK_API_KEY", webSearchConfigEnv}
}
