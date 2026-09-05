# Codex Brand Workspace Fence

## Goal

Each Codex session may:

- read and write files in its resolved brand workspace;
- write its private `.codex/memories` tree;
- read shared Skills-OL files and shared platform facts through symlinks;
- use a workspace-local temporary directory.

It must not:

- modify shared Skills-OL or platform facts;
- read or modify another brand workspace;
- modify `.codex/config.toml`, auth, session state, `.agents`, or `.git` metadata;
- widen its own permissions through `/mode`, an approval request, or a prompt;
- write arbitrary system paths or the host `/tmp`.

## Why the old configuration was unsafe

The production combination below disables the filesystem boundary:

```toml
# cc-connect
mode = "bypassPermissions"

# ~/.codex/config.toml
sandbox_mode = "danger-full-access"
approval_policy = "never"
```

A brand workspace contains symlinks into the shared Skill library. With full
disk write access, writing `workspace/skills/example/SKILL.md` follows the
symlink and mutates the shared source seen by every customer. A prompt such as
"only edit the current workspace" is not a security control.

## Selected design

The fence uses three independent layers.

### 1. Codex permission profile

cc-connect sends a fixed named `permissions` profile on both `thread/start`
and every `turn/start`, together with the resolved brand `cwd`. It also sends
`approvalPolicy = "never"`, so a denied path cannot be reopened by approval.
When `permissions_profile` is configured, cc-connect ignores runtime mode
changes, including `/mode yolo`.

Add this to the host Codex config at `~/.codex/config.toml`:

```toml
default_permissions = "tomako-brand-fence"

[permissions.tomako-brand-fence]
description = "Tomako brand workspace fence"

[permissions.tomako-brand-fence.filesystem]
":minimal" = "read"
"/home/ubuntu/Skills-OL" = "read"

[permissions.tomako-brand-fence.filesystem.":workspace_roots"]
"." = "write"
".codex" = "read"
".codex/memories" = "write"

[permissions.tomako-brand-fence.network]
enabled = true
```

`:minimal` exposes the system binaries and libraries required to run tools.
The current brand workspace is the only customer-data root mounted into the
sandbox. The shared Skill library is mounted read-only. A narrower
`.codex/memories` write rule overrides the `.codex` read-only rule without
making config, auth, and session state writable.

cc-connect copies the host-managed provider and `[permissions.*]` sections into
each brand-specific `CODEX_HOME` on session start. Per-brand `[projects.*]`
trust entries remain private.

Configure the project in cc-connect:

```toml
[[projects]]
name = "tomako-studio"
mode = "multi-workspace"
base_dir = "/home/ubuntu/workspaces"
workspace_namespace = "prod"
skip_git = true

[projects.agent]
type = "codex"

[projects.agent.options]
backend = "app_server"
app_server_url = "stdio"
permissions_profile = "tomako-brand-fence"
model = "deepseek-v4-flash"
```

The profile is fail-closed: an unknown profile causes thread creation to fail.
`permissions_profile` is rejected on the legacy exec backend.

### 2. Host filesystem permissions

The shared Skill checkout should be writable only by the deployment account:

```bash
chown -R root:root /home/ubuntu/Skills-OL
chmod -R u=rwX,go=rX /home/ubuntu/Skills-OL
```

Deploy Skills-OL as `root`, then restart cc-connect. The `ubuntu` service user
can read and execute the library but cannot mutate it, even if a future runtime
regression accidentally weakens the Codex sandbox.

Brand workspaces remain owned by the cc-connect service user. Cross-brand read
is prevented by the bubblewrap filesystem view, not by Unix mode bits, because
all current Codex processes run as the same service user.

### 3. Workspace construction

Each brand gets one real directory under:

```text
/home/ubuntu/workspaces/<environment>/workspace-<workspace-id>/brand-<brand-id>/
```

Run production and test as separate cc-connect processes and give each project
a distinct `workspace_namespace` such as `prod` or `test`. The namespace is a
validated single path segment, so independently generated matching IDs cannot
share files even when both processes use the same host and `base_dir`.

Private outputs, `.codex`, `.codex/memories`, and `.tmp` are real directories.
Shared Skills, dependencies, package metadata, and platform facts may remain
symlinks. The sandbox evaluates the target mount: a symlink under a writable
workspace does not make its external target writable.

cc-connect sets `TMPDIR=<brand-workspace>/.tmp` with mode `0700`. Tooling that
needs temporary files therefore does not require write access to host `/tmp`.

Per-session execution authority is staged separately under
`.codex/cc-connect-task-runtime-*/machine.env`. The trusted bridge atomically
rotates this file between turns and removes it on session close. Tools can read
it through the existing `.codex` read-only mount but cannot overwrite it. Host
`/tmp` is intentionally hidden by the fence and cannot hold a tool-readable
authority file.
The file path is bound both in the app-server process environment (for Node/MCP
child tools) and in the thread shell policy. Credentials remain in the protected
file and are not embedded in either configuration or prompts.

## Acceptance tests

Run the fence probe as the cc-connect service user. It must produce this matrix:

| Operation | Expected |
|---|---|
| create `own.txt` in current brand workspace | allowed |
| create `.codex/memories/fact.md` | allowed |
| read `skills/.../SKILL.md` through symlink | allowed |
| write `skills/.../SKILL.md` through symlink | denied, shared file unchanged |
| read shared platform fact through memory symlink | allowed |
| write shared platform fact through memory symlink | denied |
| read sibling brand workspace | hidden or denied |
| write sibling brand workspace | hidden or denied |
| write `.codex/config.toml` | denied |
| switch to yolo at runtime | ignored |

Before deployment, also verify:

```bash
go test ./agent/codex -count=1
go test ./core -run 'TestBrandWorkspace|TestResolveMemoryWorkDir' -count=1
go test ./...
```

## Residual boundaries

- MCP servers and external daemons are separate principals. Any MCP tool that
  writes files must receive the same brand root explicitly or run inside an
  equivalent sandbox.
- The cc-connect supervisor itself is trusted and remains able to create brand
  workspaces and write memory files.
- Per-brand Unix users or containers can be added later for stronger kernel
  isolation, but they are not required for the current single-host scale once
  the bubblewrap profile and read-only shared checkout are both enforced.
