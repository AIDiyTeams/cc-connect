# Public progress event boundary

The Codex app-server adapter maps `agentMessage.phase=commentary` to `EventCommentary`; reasoning stays `EventThinking`, and final answers stay terminal text. Commentary deltas are withheld from terminal streaming when the native item starts with the commentary phase. Non-final buffered public messages are emitted at tool boundaries as commentary.

The engine reports commentary through `AgentTraceReporter` without accumulating it into final text. The bridge keeps its backwards-compatible `agent_thinking` envelope and adds `phase` plus the trusted dispatch `turn_no` from `SessionRuntime`. The backend owns persistence, authorization and visibility; the bridge does not infer business-specific progress, translate text or summarize reasoning.

Tests cover native phase separation, terminal text exclusion, engine forwarding and bridge phase/turn identity. Formal Test deployment uses the controller child component route with a frozen pushed revision.

Adapters may negotiate `commentary_stream` for `cmsg-` turns. Explicit native
commentary then sends cumulative snapshots (`content_version`, `content_done`)
under the same `trace_id`, at most once per second plus the completed tail. The
adapter upserts one message-scoped note and persists its version/seal to reject
replays after restart. Unclassified phases still wait for classification; private
reasoning is never reclassified. Older adapters, task consumers and messaging
platforms receive only the completed note. No translation/model call is added.

Connections that negotiate `token_stream` use 50 ms / 1-character preview updates without the 2,000-character IM preview cap, so long final answers keep growing before the terminal frame. Consumers without that capability retain their configured cadence and length limit. This changes transport previews only: commentary and unclassified native items still cannot enter final-answer streaming, and a preview does not confirm business completion.
