# Bridge Platform Protocol Specification

> Version: 1.0-draft  
> Status: Draft — subject to change before implementation

## Overview

### Native constrained output (optional runtime capability)

Successful `register_ack` responses advertise `runtime_capabilities: ["output_schema_v1", "turn_budget_v1"]`.
This advertises protocol support; the selected Agent session is checked independently before
dispatch. Adapters requiring constrained output must refuse an older Bridge without the capability.

An authenticated adapter may include `runtime.output_schema`, a JSON Schema object (root
`type: "object"`, at most 128 KiB). It is trusted control-plane metadata, never inferred from
the prompt. Invalid schemas and unsupported Agent sessions fail explicitly; there is no
prompt-only fallback. Omit it for ordinary turns. Each turn replaces the prior schema.

The Codex app-server backend sends the unchanged object as `turn/start.outputSchema`.
The exec backend writes a private per-process schema file, passes `--output-schema`, and removes
the file after process exit or launch failure. Task-scoped `reasoning_effort` is applied by both
backends; thread creation must not replace it with a model default. Existing permissions profiles
remain in force, and this capability grants no additional file, network, or publishing permission.

The authenticated adapter can also supply `runtime.turn_budget_seconds` with
`turn_budget_v1`: 1–3600 seconds selects that turn's bounded wall-clock budget;
0 or omission keeps the engine default. Invalid budgets fail before Agent.Send.
It is replaced on every queued or foreground turn and does not alter ordinary
chat defaults. Adapters requiring this budget must refuse an older Bridge.

The Bridge Protocol allows **external platform adapters** written in any programming language to connect to cc-connect at runtime via WebSocket. This eliminates the requirement to write Go code and recompile the binary for every new platform integration.

### Architecture

Task authority belongs in the authenticated `runtime` fields
`machine_capability_token`, `image_capability_token`, `task_authority_envelope_b64`
and `task_id`. It is never derived from user text. Exact matching legacy markers
at the beginning of a prompt are removed before Skill routing. Sessions that
implement `ToolAuthoritySession` pass authority to tools outside model prompts;
other adapters retain the legacy marker fallback after routing.

Codex app-server sessions bind a protected, initially empty `TOMAKO_TASK_ENV_FILE`
before their first thread start/resume. Its path stays fixed; each turn atomically
replaces the contents, and an unscoped turn clears old credentials. Closing the
session removes the file. Re-resuming an already loaded Codex thread cannot add
shell config, so delaying this binding until after history recovery is invalid.

```
┌──────────────────────────────────────────────────────┐
│                    cc-connect                        │
│                                                      │
│   ┌────────────┐ ┌────────────┐ ┌────────────────┐  │
│   │  Telegram   │ │   Feishu   │ │ BridgePlatform │  │
│   │  (native)   │ │  (native)  │ │  (WebSocket)   │  │
│   └─────┬──────┘ └─────┬──────┘ └───────┬────────┘  │
│         │              │                │            │
│         └──────────────┴────────────────┘            │
│                        │                             │
│                  ┌─────┴─────┐                       │
│                  │   Engine   │                       │
│                  └───────────┘                       │
└──────────────────────────────────────────────────────┘
                         │ WebSocket
              ┌──────────┴───────────┐
              │                      │
   ┌──────────┴──────┐  ┌───────────┴─────┐
   │  Python Adapter  │  │ Node.js Adapter  │
   │ (WeChat, Line…)  │  │ (Custom Chat…)   │
   └─────────────────┘  └─────────────────┘
```

The `BridgePlatform` is a built-in platform inside cc-connect that:

1. Exposes a WebSocket endpoint for external adapters to connect.
2. Translates WebSocket messages into `core.Platform` interface calls.
3. Routes engine replies back to the adapter over the same WebSocket connection.

---

## Connection

### Endpoint

```
ws://<host>:<port>/bridge/ws
```

The port and path are configured in `config.toml`:

```toml
[bridge]
enabled = true
port = 9810
host = "127.0.0.1"        # optional; omit to preserve all-interface binding
path = "/bridge/ws"       # optional, default "/bridge/ws"
token = "your-secret"     # required for authentication
```

### Authentication

The adapter must authenticate on connection using one of:

| Method | Example |
|--------|---------|
| Query parameter | `ws://host:9810/bridge/ws?token=your-secret` |
| Header | `Authorization: Bearer your-secret` |
| Header | `X-Bridge-Token: your-secret` |

Unauthenticated connections are rejected with HTTP 401.

### Connection Lifecycle

```
Adapter                          cc-connect
  │                                  │
  │──── WebSocket Connect ──────────→│  (with token)
  │                                  │
  │──── register ──────────────────→│  (declare platform name & capabilities)
  │←─── register_ack ──────────────│  (confirm or reject)
  │                                  │
  │←──→ message / reply exchange ──→│  (bidirectional)
  │                                  │
  │──── ping ──────────────────────→│  (keepalive, every 30s recommended)
  │←─── pong ──────────────────────│
  │                                  │
  │──── close ─────────────────────→│  (graceful disconnect)
```

---

## Message Protocol

All messages are JSON objects with a required `type` field. The protocol uses newline-delimited JSON over WebSocket text frames (one JSON object per frame).

### Adapter → cc-connect

#### `register`

Must be the first message after connection. Declares the adapter identity and capabilities.

```json
{
  "type": "register",
  "platform": "wechat",
  "capabilities": ["text", "image", "file", "audio", "card", "buttons", "typing", "update_message", "preview"],
  "metadata": {
    "version": "1.0.0",
    "description": "WeChat Official Account adapter"
  }
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | `"register"` |
| `platform` | string | yes | Unique platform name (lowercase, alphanumeric + hyphens). Used in session keys. |
| `capabilities` | string[] | yes | List of supported capabilities (see [Capabilities](#capabilities)). |
| `metadata` | object | no | Free-form metadata for logging/debugging. |

#### `message`

Delivers an incoming user message to the engine.

```json
{
  "type": "message",
  "msg_id": "msg-001",
  "session_key": "wechat:user123:user123",
  "user_id": "user123",
  "user_name": "Alice",
  "content": "Hello, what can you do?",
  "reply_ctx": "conv-abc-123",
  "images": [],
  "files": [],
  "audio": null
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | `"message"` |
| `msg_id` | string | yes | Platform-specific message ID for tracing. |
| `session_key` | string | yes | Unique session identifier. Format: `{platform}:{scope}:{user}`. The adapter defines how to compose this. |
| `user_id` | string | yes | User identifier on the platform. |
| `user_name` | string | no | Display name. |
| `content` | string | yes | Text content. |
| `reply_ctx` | string | yes | Opaque context string the adapter needs to route replies back. cc-connect echoes this in every reply. |
| `images` | Image[] | no | Attached images (see [Image Object](#image-object)). |
| `files` | File[] | no | Attached files (see [File Object](#file-object)). |
| `audio` | Audio | no | Voice message (see [Audio Object](#audio-object)). |

#### `card_action`

User clicked a button or selected an option on a card.

```json
{
  "type": "card_action",
  "session_key": "wechat:user123:user123",
  "action": "cmd:/new",
  "reply_ctx": "conv-abc-123"
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | `"card_action"` |
| `session_key` | string | yes | Session that triggered the action. |
| `action` | string | yes | The callback value from the button (e.g., `"cmd:/new"`, `"nav:/model"`, `"act:/heartbeat pause"`). |
| `reply_ctx` | string | yes | Reply context for routing the response. |

#### `preview_ack`

Acknowledges a preview start and returns a handle for subsequent updates.

```json
{
  "type": "preview_ack",
  "ref_id": "preview-req-001",
  "preview_handle": "platform-msg-id-789"
}
```

#### `ping`

Keepalive. cc-connect responds with `pong`.

```json
{
  "type": "ping",
  "ts": 1710000000000
}
```

---

### cc-connect → Adapter

#### `register_ack`

Confirms or rejects registration.

```json
{
  "type": "register_ack",
  "ok": true,
  "error": ""
}
```

#### `reply`

A complete reply message to send to the user.

```json
{
  "type": "reply",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "content": "I can help you with coding tasks!",
  "format": "text"
}
```

**Fields:**

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `type` | string | yes | `"reply"` |
| `session_key` | string | yes | Target session. |
| `reply_ctx` | string | yes | Echoed from the original message. |
| `content` | string | yes | Reply text content. |
| `format` | string | no | `"text"` (default) or `"markdown"`. |

#### `reply_stream`

Streaming delta for real-time typing preview. Only sent if the adapter declared `"preview"` capability.

```json
{
  "type": "reply_stream",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "delta": "partial content...",
  "full_text": "accumulated full text so far...",
  "preview_handle": "platform-msg-id-789",
  "done": false
}
```

| Field | Type | Description |
|-------|------|-------------|
| `delta` | string | New text since last stream message. |
| `full_text` | string | Full accumulated text. Adapters can use this for "replace entire message" updates. |
| `preview_handle` | string | Handle returned by `preview_ack`. Empty on first stream message. |
| `done` | bool | `true` on the final stream message. |
| `status` | object | Optional structured footer on the final frame (see below). Never inlined into `full_text` / `content`. |
| `usage` | object | Optional token usage on the final frame. |

#### `status` (structured reply footer)

Sent on final `reply` / `reply_stream(done=true)` instead of appending italic markdown to the body.

```json
{
  "context": "[ctx: ~6%]",
  "context_pct": 6,
  "model": "deepseek-v4-flash",
  "effort": "medium",
  "workdir": "~/workspaces/user-6",
  "session_name": "Greeting & product ideas",
  "text": "[ctx: ~6%] · deepseek-v4-flash · medium · ~/workspaces/user-6"
}
```

| Field | Description |
|-------|-------------|
| `context` | Context usage label, e.g. `[ctx: ~6%]`. |
| `context_pct` | Numeric percent when parseable. |
| `model` | Model id (diagnostic; Studio UI hides it). |
| `effort` | Reasoning effort (e.g. `medium`). |
| `workdir` | Workspace path (diagnostic; Studio shows `session_name` instead). |
| `session_name` | Human-readable session title (Codex summary / custom name). |
| `text` | Original footer string (fallback for display). |

#### `preview_start`

Requests the adapter to create an initial preview message (for streaming).

```json
{
  "type": "preview_start",
  "ref_id": "preview-req-001",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "content": "Thinking..."
}
```

The adapter should send the message and respond with `preview_ack` containing the platform message ID.

#### `update_message`

Requests the adapter to edit an existing message in-place. Used for streaming preview updates.

```json
{
  "type": "update_message",
  "session_key": "wechat:user123:user123",
  "preview_handle": "platform-msg-id-789",
  "content": "Updated text content..."
}
```

#### `delete_message`

Requests the adapter to delete a message (e.g., cleaning up preview messages).

```json
{
  "type": "delete_message",
  "session_key": "wechat:user123:user123",
  "preview_handle": "platform-msg-id-789"
}
```

#### `card`

Send a structured card to the user. Only sent if the adapter declared `"card"` capability; otherwise cc-connect falls back to `reply` with `card.RenderText()`.

```json
{
  "type": "card",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "card": {
    "header": {
      "title": "Model Selection",
      "color": "blue"
    },
    "elements": [
      {
        "type": "markdown",
        "content": "Choose a model:"
      },
      {
        "type": "actions",
        "buttons": [
          {"text": "GPT-4", "btn_type": "primary", "value": "cmd:/model switch gpt-4"},
          {"text": "Claude", "btn_type": "default", "value": "cmd:/model switch claude"}
        ],
        "layout": "row"
      },
      {
        "type": "divider"
      },
      {
        "type": "note",
        "text": "Current: gpt-4"
      }
    ]
  }
}
```

See [Card Schema](#card-schema) for the full card element reference.

#### `buttons`

Send a message with inline buttons. Only sent if the adapter declared `"buttons"` capability.

```json
{
  "type": "buttons",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "content": "Allow tool execution: bash(rm -rf /tmp/old)?",
  "buttons": [
    [
      {"text": "✅ Allow", "data": "perm:req-123:allow"},
      {"text": "❌ Deny", "data": "perm:req-123:deny"}
    ]
  ]
}
```

`buttons` is a 2D array: each inner array is one row.

#### `typing_start`

Requests the adapter to show a typing indicator.

```json
{
  "type": "typing_start",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123"
}
```

#### `typing_stop`

Requests the adapter to hide the typing indicator.

```json
{
  "type": "typing_stop",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123"
}
```

#### `audio`

Send a voice/audio message. Only sent if the adapter declared `"audio"` capability.

```json
{
  "type": "audio",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "data": "<base64-encoded-audio>",
  "format": "mp3"
}
```

#### `image`

Send an image to the user. Only sent if the adapter declared `"image"` capability.

```json
{
  "type": "image",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "data": "<base64-encoded-image>",
  "mime_type": "image/png",
  "file_name": "screenshot.png"
}
```

#### `file`

Send a file to the user. Only sent if the adapter declared `"file"` capability.

```json
{
  "type": "file",
  "session_key": "wechat:user123:user123",
  "reply_ctx": "conv-abc-123",
  "data": "<base64-encoded-file>",
  "mime_type": "application/pdf",
  "file_name": "report.pdf"
}
```

#### `pong`

Response to `ping`.

```json
{
  "type": "pong",
  "ts": 1710000000000
}
```

#### `agent_thinking`

Full model thinking text for a backend task (`llm-` reply contexts, requires the
`agent_trace` capability). Emitted once per completed reasoning chunk; content is
byte-capped at 64KB, never summarized. The adapter is responsible for gating any
user-facing exposure of this data.

```json
{
  "type": "agent_thinking",
  "session_key": "feishu:ou_123",
  "reply_ctx": "llm-ab12cd34",
  "trace_id": "trace-001",
  "content": "Full reasoning text...",
  "occurred_at": "2026-09-03T12:00:00.123456789Z"
}
```

#### `agent_trace`

Redacted tool trace for a backend task (`llm-` reply contexts, requires the
`agent_trace` capability). Pairing `tool_use` / `tool_result` frames share a
`trace_id`; `duration_ms` appears on the `tool_result` frame when the pair is
matched.

```json
{
  "type": "agent_trace",
  "session_key": "feishu:ou_123",
  "reply_ctx": "llm-ab12cd34",
  "trace_id": "trace-001",
  "event_type": "tool_use",
  "tool_name": "shell",
  "input": "truncated tool input (8KB cap)",
  "output": "truncated tool output (2KB cap)",
  "status": "success",
  "occurred_at": "2026-09-03T12:00:00.123456789Z"
}
```

#### `error`

Notify the adapter of a server-side error.

```json
{
  "type": "error",
  "code": "session_not_found",
  "message": "No active session for the given key"
}
```

---

## Data Schemas

### Capabilities

| Capability | Description | Enables |
|------------|-------------|---------|
| `text` | Basic text messaging (required) | `message`, `reply` |
| `image` | Sending/receiving images | `message.images`, `image` reply |
| `file` | Sending/receiving files | `message.files`, `file` reply |
| `audio` | Sending/receiving voice messages | `message.audio`, `audio` reply |
| `card` | Structured rich card rendering | `card` reply |
| `buttons` | Inline clickable buttons | `buttons` reply, `card_action` |
| `typing` | Typing indicator | `typing_start`, `typing_stop` |
| `update_message` | Edit existing messages | `update_message` |
| `preview` | Streaming preview (requires `update_message` or `token_stream`) | `preview_start`, `reply_stream` |
| `token_stream` | Prefer by-token `reply_stream` for Studio chat (`reply_ctx` prefix `cmsg-`). LLM Task (`llm-`) keeps coarse `preview_start` / `update_message` + default throttle. | `reply_stream` (Studio only) |
| `delete_message` | Delete messages | `delete_message` |
| `reconstruct_reply` | Can reconstruct reply context from session_key | Enables cron/heartbeat messages |
| `agent_trace` | Trusted control-plane diagnostics and validated staged results for `llm-` reply contexts | `agent_trace`, `agent_structured_result` |

If a capability is not declared, cc-connect will automatically degrade:
- No `card` → cards are rendered as plain text via `RenderText()`.
- No `buttons` → buttons are omitted or rendered as text hints.
- No `preview` → streaming is disabled; only the final reply is sent.
- No `typing` → typing indicators are skipped.

### Trusted Agent control-plane events

Adapters that declare `agent_trace` may receive two internal event types for
LLM tasks. These are control-plane messages, not user-visible chat output.

`agent_trace` contains bounded tool/lifecycle metadata. Lifecycle events use
`event_type: "lifecycle"`, `tool_name: "AgentLifecycle"`, a stable stage name
in `input`, and the measured child duration in `duration_ms`. Prompt text,
chain-of-thought, and unrestricted tool output must not be placed in this
channel.

`agent_structured_result` transports one result that was validated by a
scene-specific dynamic tool:

```json
{
  "type": "agent_structured_result",
  "session_key": "tomako:workspace:user",
  "reply_ctx": "llm-123",
  "stage": "core",
  "result": {},
  "occurred_at": "2026-08-13T08:00:00Z"
}
```

cc-connect only emits structured results for `llm-` reply contexts and only to
an authenticated adapter that declared `agent_trace`. The receiving control
plane remains responsible for authority validation and durable persistence.

For the `brand_analysis` scene, cc-connect also enforces the staged protocol at
the dynamic-tool boundary. Evidence must complete before `core`; `core` may be
published once; a ready `competitors` stage requires completion of the first
native Web Search observed after `core`; and no stage may be published twice.
An `unavailable` competitor stage is the explicit fallback when native search
cannot run. The crawler's full deterministic result is emitted to the control
plane, while the Agent receives a bounded semantic-only evidence projection so
logos, colors, raw CSS, and large asset lists do not inflate model context.

### Image Object

```json
{
  "mime_type": "image/png",
  "data": "<base64-encoded>",
  "file_name": "screenshot.png"
}
```

### File Object

```json
{
  "mime_type": "application/pdf",
  "data": "<base64-encoded>",
  "file_name": "report.pdf"
}
```

### Audio Object

```json
{
  "mime_type": "audio/ogg",
  "data": "<base64-encoded>",
  "format": "ogg",
  "duration": 5
}
```

### Card Schema

A card consists of an optional header and a list of elements:

```json
{
  "header": {
    "title": "Card Title",
    "color": "blue"
  },
  "elements": [ ... ]
}
```

**Supported colors:** `blue`, `green`, `red`, `orange`, `purple`, `grey`, `turquoise`, `violet`, `indigo`, `wathet`, `yellow`, `carmine`.

#### Element Types

**Markdown**
```json
{"type": "markdown", "content": "**Bold** and _italic_"}
```

**Divider**
```json
{"type": "divider"}
```

**Actions (Button Row)**
```json
{
  "type": "actions",
  "buttons": [
    {"text": "Click Me", "btn_type": "primary", "value": "cmd:/do-something"}
  ],
  "layout": "row"
}
```

`btn_type`: `"primary"`, `"default"`, `"danger"`.  
`layout`: `"row"` (default), `"equal_columns"`.

**List Item (Description + Button)**
```json
{
  "type": "list_item",
  "text": "GPT-4 — Most capable model",
  "btn_text": "Select",
  "btn_type": "primary",
  "btn_value": "cmd:/model switch gpt-4"
}
```

**Select (Dropdown)**
```json
{
  "type": "select",
  "placeholder": "Choose a model",
  "options": [
    {"text": "GPT-4", "value": "cmd:/model switch gpt-4"},
    {"text": "Claude", "value": "cmd:/model switch claude"}
  ],
  "init_value": "cmd:/model switch gpt-4"
}
```

**Note (Footnote)**
```json
{
  "type": "note",
  "text": "Tip: use /help to see all commands",
  "tag": "optional-machine-tag"
}
```

---

## Session Key Format

Session keys follow the pattern:

```
{platform}:{scope}:{user_id}
```

- **platform**: The `platform` name from registration (e.g., `wechat`).
- **scope**: A grouping scope — could be a group/channel ID, or the same as `user_id` for 1-on-1 chats.
- **user_id**: The unique user identifier.

Examples:
- `wechat:user123:user123` — personal DM
- `wechat:group456:user123` — user in a group chat
- `matrix:room789:alice` — Matrix room

The adapter is responsible for constructing consistent session keys.

Business adapters may append a stable Brand scope:

```
{platform}:{workspace_id}:{user_id}:brand:{brand_id}:{session_type}:{session_id}
```

In multi-workspace mode, `:brand:` sessions resolve to
`{base_dir}/{workspace_namespace}/workspace-{workspace_id}/brand-{brand_id}`
when `workspace_namespace` is configured; otherwise the legacy path remains
`{base_dir}/workspace-{workspace_id}/brand-{brand_id}`.
Different users in the same Workspace and Brand therefore share the working
directory while retaining separate conversation session keys.

---

## Session Management REST API

In addition to the WebSocket protocol for real-time messaging, the Bridge Server exposes HTTP REST endpoints on the same port for session management. This allows adapters to list, create, switch, and delete sessions without requiring the separate Management API.

### Authentication

The same token used for WebSocket connections applies to REST endpoints:

| Method | Example |
|--------|---------|
| Header | `Authorization: Bearer your-secret` |
| Query param | `?token=your-secret` |

### Response Format

All responses use the same envelope as the Management API:

```json
{"ok": true, "data": { ... }}
{"ok": false, "error": "message"}
```

### Endpoints

All endpoints are relative to the Bridge Server base URL (e.g., `http://localhost:9810`).

#### GET /bridge/sessions

Lists sessions for a given session key prefix (typically `platform:chatId`).

**Query parameters:**

| Param | Type | Required | Description |
|-------|------|----------|-------------|
| `session_key` | string | yes | The session key to list sessions for (e.g., `wechat:user123:user123`). |

**Response:**

```json
{
  "ok": true,
  "data": {
    "sessions": [
      {
        "id": "s1",
        "name": "default",
        "history_count": 12
      },
      {
        "id": "s2",
        "name": "work",
        "history_count": 5
      }
    ],
    "active_session_id": "s1"
  }
}
```

---

#### POST /bridge/sessions

Creates a new named session.

**Request body:**

```json
{
  "session_key": "wechat:user123:user123",
  "name": "work"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `session_key` | string | yes | Session key for the user. |
| `name` | string | no | Human-readable session name. Defaults to `"default"`. |

**Response:**

```json
{
  "ok": true,
  "data": {
    "id": "s3",
    "name": "work",
    "message": "session created"
  }
}
```

---

#### GET /bridge/sessions/{id}

Returns session detail with message history.

**Query parameters:**

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| `session_key` | string | (required) | Session key to identify the project context. |
| `history_limit` | int | 50 | Max history entries to return. |

**Response:**

```json
{
  "ok": true,
  "data": {
    "id": "s1",
    "name": "default",
    "history": [
      {"role": "user", "content": "Hello"},
      {"role": "assistant", "content": "Hi! How can I help?"}
    ]
  }
}
```

---

#### DELETE /bridge/sessions/{id}

Deletes a session and its history.

**Query parameters:**

| Param | Type | Required | Description |
|-------|------|----------|-------------|
| `session_key` | string | yes | Session key to identify the project context. |

**Response:**

```json
{
  "ok": true,
  "data": {
    "message": "session deleted"
  }
}
```

---

#### POST /bridge/sessions/switch

Switches the active session for a session key.

**Request body:**

```json
{
  "session_key": "wechat:user123:user123",
  "target": "s2"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `session_key` | string | yes | Session key. |
| `target` | string | yes | Session ID or name to switch to. |

**Response:**

```json
{
  "ok": true,
  "data": {
    "message": "session switched",
    "active_session_id": "s2"
  }
}
```

---

## Error Handling

### Reconnection

If the WebSocket connection drops, the adapter should:

1. Wait with exponential backoff (starting at 1s, max 60s).
2. Reconnect and send a new `register` message.
3. Resume normal operation — cc-connect maintains session state independently of the connection.

### Message Ordering

Messages within a single WebSocket connection are ordered. cc-connect processes adapter messages sequentially per session key.

### Timeouts

- **Ping interval**: Adapters should send `ping` at least every 30 seconds.
- **Connection timeout**: cc-connect closes idle connections after 90 seconds without a ping.
- **Reply timeout**: If an agent takes too long, cc-connect may send an error reply. The adapter does not need to handle this specially.

---

## Configuration Example

```toml
[bridge]
enabled = true
port = 9810
token = "a-strong-random-secret"

# Optional: restrict which adapters can connect (by platform name).
# Default: allow all registered adapters.
# allow_platforms = ["wechat", "matrix"]
```

No per-adapter project configuration is needed — adapters are associated with the **default project** or specify a `project` field in the `register` message to bind to a specific project.

---

## SDK Guidelines

When building an adapter, follow these guidelines:

1. **Keep it stateless** — the adapter should be a thin translation layer. All session state lives in cc-connect.
2. **Handle reconnection** — network failures are normal. Implement exponential backoff.
3. **Declare capabilities honestly** — only declare capabilities your platform actually supports.
4. **Use `reply_ctx` faithfully** — always echo back the `reply_ctx` from the original message.
5. **Base64 for binary data** — images, files, and audio are transferred as base64-encoded strings.
6. **Log errors, don't crash** — if you receive an unknown message type, log it and continue.

### Minimal Adapter Example (Python pseudocode)

```python
import asyncio
import json
import websockets

async def main():
    uri = "ws://localhost:9810/bridge/ws?token=your-secret"
    async with websockets.connect(uri) as ws:
        # 1. Register
        await ws.send(json.dumps({
            "type": "register",
            "platform": "my-chat",
            "capabilities": ["text", "buttons"]
        }))
        ack = json.loads(await ws.recv())
        assert ack["ok"], f"Registration failed: {ack['error']}"

        # 2. Start message loop
        async def recv_loop():
            async for raw in ws:
                msg = json.loads(raw)
                if msg["type"] == "reply":
                    send_to_chat_platform(msg["reply_ctx"], msg["content"])
                elif msg["type"] == "buttons":
                    send_buttons_to_chat(msg["reply_ctx"], msg["content"], msg["buttons"])
                # ... handle other types

        async def send_loop():
            while True:
                chat_msg = await get_next_chat_message()
                await ws.send(json.dumps({
                    "type": "message",
                    "msg_id": chat_msg.id,
                    "session_key": f"my-chat:{chat_msg.user_id}:{chat_msg.user_id}",
                    "user_id": chat_msg.user_id,
                    "user_name": chat_msg.user_name,
                    "content": chat_msg.text,
                    "reply_ctx": chat_msg.conversation_id
                }))

        await asyncio.gather(recv_loop(), send_loop())

asyncio.run(main())
```

---

## Versioning

The protocol version is declared in the `register` message via `metadata.protocol_version`. The current version is `1`. cc-connect will reject connections with incompatible versions and respond with a `register_ack` containing an error.

```json
{
  "type": "register",
  "platform": "my-chat",
  "capabilities": ["text"],
  "metadata": {
    "protocol_version": 1
  }
}
```
