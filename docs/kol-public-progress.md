# Public progress event boundary

## Base-instruction override for application conversations

Codex's built-in base prompt is written for a terminal coding assistant. Its
"Preamble messages" and "Sharing progress updates" sections instruct the model
to announce the next tool call ("now checking the API route definitions") on
the system/instructions channel, which outranks the application's
developer-role policy on providers that map `developer` to `user`. For
conversations the trusted control plane manages (the runtime lane carries
`developer_instructions` before the thread is created), the app-server adapter
passes `baseInstructions` on `thread/start`: the vendored Codex 0.153.4 base
prompt (`agent/codex/assets/codex-default-base-instructions.md`) with only
those two sections replaced by `publicConversationSection` in
`agent/codex/public_conversation_base.go`. The first message of a turn must
confirm the user's need and the deliverable in the user's language; messages
between tool calls carry findings, decisions or limitations, never tool,
file, credential or format narration. Tool, sandbox, planning and final-answer
guidance stay byte-identical, and the unit test pins the vendored checksum so
a Codex upgrade on the bridge host refreshes the file deliberately. Plain
bridge sessions without runtime developer instructions keep the native prompt.
Resumed threads keep the base prompt they were created with.

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
