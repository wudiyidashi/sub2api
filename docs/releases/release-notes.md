# Release Notes

### 2026-10-10

- Added selectable OpenAI account drivers with managed Codex HTTP/SSE and WebSocket transport, including runtime maintenance and hot upgrade support.
- Added full request content auditing for HTTP and WebSocket traffic, including client frames, upstream frames, headers, raw bodies, JSON, SSE, and WebSocket event details.
- Unified content-audit request search with usage-style date, user, API key, account, group, model, platform, request ID, keyword, status, and sorting filters.
- Added configurable GitHub release source through `UPDATE_GITHUB_REPO`, so panel updates and deployment scripts can use an operator-owned repository.
