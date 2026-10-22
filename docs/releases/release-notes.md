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

- Fixed loading the built-in team dashboard through its embedded directory URL.
- Added an administrator-only team consumption dashboard with overview, virtual groups, rankings, activity insights, and usage details. It shares the existing login and supports importing previous dashboard group configurations.
- Troubleshooting content recording now retains at most one day by default and enforces a configurable 512 MiB logical budget, evicting whole oldest requests when needed.
- Full HTTP and WebSocket content recording is now disabled by default and can be enabled independently for troubleshooting; security moderation and usage/error logs remain available, and retained audit records stay queryable within the storage policy.
- Added Gemini explicit context caching, Agent Platform (Vertex) API key authentication and model discovery, and image support in tool results.
- Added Claude Haiku 5.5 support and multilingual group names and descriptions.
- Fixed GPT-6.1 Sol subscription long-context billing and raised the generated Codex context default to 500k.
- Improved account error isolation, Responses compatibility, payment countdowns, channel monitoring, and administrator request details.
- Updated the enhanced Codex driver to Codex Proxy RS v3.26.1 while preserving managed HTTP/SSE and WebSocket requests, account lifecycle, and content auditing.

### 2026-10-19

- Added Gemini 4 Argon discovery and pricing, with thinking-level forwarding and streamed thought summaries on compatibility endpoints.
- Added a configurable minimum output-token allowance for compatibility requests.
- Fixed composite-group OAuth routing and adapted model availability to the GPT-5.5 retirement on ChatGPT subscription accounts.
- Improved payment confirmation, Stripe order resumption, and pending refund accounting.
- Improved Responses compatibility for strict clients, corrected Zhipu quota windows, and isolated Codex imports by workspace.
- Restored inline account filters, including enhanced-driver filtering.
- Updated the enhanced Codex transport to CPR v3.29.3 with TLS and archive dependency security fixes.

### 2026-10-22

- Codex accounts now detect location through their actual outbound proxy and align request timezone, local date, region, and search location for HTTP and WebSocket requests.
- Account editing now previews the selected exit IP and timezone, with manual refresh. Failed detection stops Codex requests instead of using an outdated location.
- Use a fixed or sticky proxy exit for consistent location; operating system, workspace paths, and language preferences retain their actual values.
