# Migration to ClaudeBuddy

The ClaudeBuddy release completes the product and module rename. No legacy
product-name alias is retained, so imports, environment variables, context files,
and automation that referenced the former names must be updated.

| Former name | Current name |
| --- | --- |
| `github.com/codeany-ai/open-agent-sdk-go` | `github.com/claudebuddy/claudebuddy-agent-sdk-go` |
| `CODEANY_API_KEY` | `CLAUDEBUDDY_API_KEY` |
| `CODEANY_AUTH_TOKEN` | `CLAUDEBUDDY_AUTH_TOKEN` |
| `CODEANY_BASE_URL` | `CLAUDEBUDDY_BASE_URL` |
| `CODEANY_MODEL` | `CLAUDEBUDDY_MODEL` |
| `CODEANY_CUSTOM_HEADERS` | `CLAUDEBUDDY_CUSTOM_HEADERS` |
| `CODEANY.md` | `CLAUDEBUDDY.md` |

Update imports and dependencies:

```bash
go get github.com/claudebuddy/claudebuddy-agent-sdk-go
go mod tidy
```

Then replace old import prefixes in source code and rename environment variables
in deployment secrets, CI configuration, and local shell profiles. Rename project
or user context files separately. The SDK does not read the former aliases.

The concurrent runtime is additive. Existing `agent.New`, `Agent.Query`, and
`Agent.Prompt` calls continue through the default Session. Services that handle
many independent conversations should create one shared Agent and one Session per
conversation. The concurrency restriction is one active Run per Session, not one
Run per Agent.

