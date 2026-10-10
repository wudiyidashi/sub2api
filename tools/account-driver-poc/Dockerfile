FROM rust:1.97-slim-bookworm AS rust-build
RUN apt-get update && apt-get install -y --no-install-recommends build-essential cmake pkg-config libssl-dev protobuf-compiler git && rm -rf /var/lib/apt/lists/*
WORKDIR /poc
COPY . .
RUN --mount=type=cache,target=/usr/local/cargo/registry --mount=type=cache,target=/usr/local/cargo/git --mount=type=cache,target=/poc/target cargo build --locked && cp target/debug/account-driver-poc /account-driver-poc

FROM golang:1.26-bookworm AS verification
WORKDIR /poc
COPY go.mod go.sum ./
RUN go mod download
COPY pocv1 ./pocv1
COPY runner_integration_test.go ./
COPY --from=rust-build /account-driver-poc /usr/local/bin/account-driver-poc
ENV ACCOUNT_DRIVER_POC_BINARY=/usr/local/bin/account-driver-poc
RUN go test -v -count=1 -timeout=60s ./...
