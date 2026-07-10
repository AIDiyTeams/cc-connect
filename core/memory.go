package core

import "context"

// AgentMemoryFact is a structured memory fact supplied by an external system.
type AgentMemoryFact struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// AgentMemoryWriteRequest asks an agent to persist facts into the memory
// extension area for the session identified by SessionKey.
//
// WorkDir, when set by the engine/management layer, is the authoritative
// Codex cwd for this write. In multi-workspace deployments this is
// typically {base_dir}/{user_dir_prefix}{userId} (default user-{id}).
// {base_dir}/user-{userId}, matching the interactive session workspace.
// Agents must prefer WorkDir over deriving a path from SessionKey alone.
type AgentMemoryWriteRequest struct {
	SessionKey   string            `json:"session_key"`
	WorkDir      string            `json:"work_dir,omitempty"`
	SourceTaskID string            `json:"source_task_id,omitempty"`
	Title        string            `json:"title,omitempty"`
	Facts        []AgentMemoryFact `json:"facts"`
}

// AgentMemoryWriteResult describes where the agent persisted the facts.
type AgentMemoryWriteResult struct {
	File string `json:"file,omitempty"`
}

// AgentMemoryWriter is an optional interface for agents that can accept
// external memory facts and persist them in their own workspace format.
type AgentMemoryWriter interface {
	WriteMemoryFacts(ctx context.Context, req AgentMemoryWriteRequest) (*AgentMemoryWriteResult, error)
}
