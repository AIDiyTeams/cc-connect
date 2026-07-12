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
	Name string `json:"name,omitempty"`
}

// AgentMemoryListRequest lists fact markdown files for a user workspace.
type AgentMemoryListRequest struct {
	SessionKey string `json:"session_key"`
	WorkDir    string `json:"work_dir,omitempty"`
}

// AgentMemoryFactMeta is a lightweight listing entry for one fact file.
type AgentMemoryFactMeta struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

// AgentMemoryListResult is the list of fact files under a user workspace.
type AgentMemoryListResult struct {
	SessionKey string               `json:"session_key"`
	WorkDir    string               `json:"work_dir,omitempty"`
	FactsDir   string               `json:"facts_dir,omitempty"`
	Facts      []AgentMemoryFactMeta `json:"facts"`
}

// AgentMemoryGetRequest reads one fact markdown file by basename.
type AgentMemoryGetRequest struct {
	SessionKey string `json:"session_key"`
	WorkDir    string `json:"work_dir,omitempty"`
	Name       string `json:"name"`
}

// AgentMemoryUpdateRequest replaces the full markdown body of one fact file.
type AgentMemoryUpdateRequest struct {
	SessionKey string `json:"session_key"`
	WorkDir    string `json:"work_dir,omitempty"`
	Name       string `json:"name"`
	Content    string `json:"content"`
}

// AgentMemoryDeleteRequest removes one fact markdown file by basename.
type AgentMemoryDeleteRequest struct {
	SessionKey string `json:"session_key"`
	WorkDir    string `json:"work_dir,omitempty"`
	Name       string `json:"name"`
}

// AgentMemoryFactFile is the editable markdown body of a fact file.
type AgentMemoryFactFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time,omitempty"`
	Path    string `json:"path,omitempty"`
}

// AgentMemoryWriter is an optional interface for agents that can accept
// external memory facts and persist them in their own workspace format.
type AgentMemoryWriter interface {
	WriteMemoryFacts(ctx context.Context, req AgentMemoryWriteRequest) (*AgentMemoryWriteResult, error)
}

// AgentMemoryManager extends AgentMemoryWriter with list / get / update /
// delete of per-user fact markdown files. Management API routes by
// session_key → user workspace before calling these methods.
type AgentMemoryManager interface {
	AgentMemoryWriter
	ListMemoryFacts(ctx context.Context, req AgentMemoryListRequest) (*AgentMemoryListResult, error)
	GetMemoryFact(ctx context.Context, req AgentMemoryGetRequest) (*AgentMemoryFactFile, error)
	UpdateMemoryFact(ctx context.Context, req AgentMemoryUpdateRequest) (*AgentMemoryFactFile, error)
	DeleteMemoryFact(ctx context.Context, req AgentMemoryDeleteRequest) error
}
