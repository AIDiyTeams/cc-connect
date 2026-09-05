# Public progress event boundary

The Codex app-server adapter maps `agentMessage.phase=commentary` to `EventCommentary`; reasoning stays `EventThinking`, and final answers stay terminal text. Commentary deltas are withheld from terminal streaming when the native item starts with the commentary phase. Non-final buffered public messages are emitted at tool boundaries as commentary.

The engine reports commentary through `AgentTraceReporter` without accumulating it into final text. The bridge keeps its backwards-compatible `agent_thinking` envelope and adds `phase` plus the trusted dispatch `turn_no` from `SessionRuntime`. The backend owns persistence, authorization and visibility; the bridge does not infer business-specific progress, translate text or summarize reasoning.

Tests cover native phase separation, terminal text exclusion, engine forwarding and bridge phase/turn identity. Formal Test deployment uses the controller child component route with a frozen pushed revision.
