# Release Notes

### 2026-10-10

- Added selectable OpenAI account drivers with managed Codex HTTP/SSE and WebSocket transport, including runtime maintenance and hot upgrade support.
- Added full request content auditing for HTTP and WebSocket traffic, including client frames, upstream frames, headers, raw bodies, JSON, SSE, and WebSocket event details.
- Unified content-audit request search with usage-style date, user, API key, account, group, model, platform, request ID, keyword, status, and sorting filters.
- Added configurable GitHub release source through `UPDATE_GITHUB_REPO`, so panel updates and deployment scripts can use an operator-owned repository.
- Full GitHub releases now publish amd64 and arm64 multi-architecture images with the enhanced Codex account driver included.

### 2026-10-11

- Panel software updates now refresh the managed Codex driver together with the gateway binary when the installation includes the enhanced driver.
- Release images and update assets carry matching architecture-specific driver binaries with checksum verification and rollback support.

### 2026-10-12

- Enhanced Codex HTTP/SSE failures now show network diagnostic categories such as connection timeout, DNS, proxy, TLS, and connection reset in administrator error details.
- Preserved driver failure messages across the gateway while keeping credentials out of diagnostic messages and retaining the sending state used to prevent duplicate requests.
- Fixed the enhanced driver failing to start in GitHub release images due to incompatible system libraries; both architectures now build against the release runtime and check driver loading before publication.
- Codex Responses requests now use the official zstd request encoding before the managed upstream transport.

### 2026-10-14

- Added Gemini explicit context caching, Agent Platform (Vertex) API key authentication and model discovery, and image support in tool results.
- Added Claude Haiku 5.5 support and multilingual group names and descriptions.
- Fixed GPT-6.1 Sol subscription long-context billing and raised the generated Codex context default to 500k.
- Improved account error isolation, Responses compatibility, payment countdowns, channel monitoring, and administrator request details.
- Updated the enhanced Codex driver to Codex Proxy RS v3.26.1 while preserving managed HTTP/SSE and WebSocket requests, account lifecycle, and content auditing.
