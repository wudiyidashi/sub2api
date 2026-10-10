FROM rust:1.97-slim-bookworm AS rust-build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake pkg-config libssl-dev protobuf-compiler git && rm -rf /var/lib/apt/lists/*
WORKDIR /workspace/drivers/codex-rs
COPY . .
COPY --from=host-api account_driver.proto /workspace/backend/pkg/accountdriverapi/v1/account_driver.proto
RUN --mount=type=cache,target=/usr/local/cargo/registry --mount=type=cache,target=/usr/local/cargo/git --mount=type=cache,target=/workspace/drivers/codex-rs/target cargo build --release --locked && cp target/release/sub2api-codex-account-driver /sub2api-codex-account-driver

FROM golang:1.27.2-bookworm AS verification
WORKDIR /workspace/backend
COPY --from=host-go go.mod go.sum ./
COPY --from=host-api . ./pkg/accountdriverapi/v1/
COPY --from=host-go internal/accountdriver/runner ./internal/accountdriver/runner/
COPY --from=rust-build /workspace/drivers/codex-rs/upstream-lock.json /workspace/drivers/codex-rs/upstream-lock.json
COPY --from=rust-build /sub2api-codex-account-driver /usr/local/bin/sub2api-codex-account-driver
ENV SUB2API_CODEX_DRIVER_BINARY=/usr/local/bin/sub2api-codex-account-driver
RUN go test -v -count=1 -timeout=90s ./pkg/accountdriverapi/v1 ./internal/accountdriver/runner
