ARG GO_VERSION=1.27.4
FROM golang:${GO_VERSION}-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -mod=readonly -trimpath -o /out/github-archive ./cmd/github-archive && \
    CGO_ENABLED=0 go build -mod=readonly -trimpath -o /out/github-archive-panel ./cmd/github-archive-panel

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/github-archive /usr/local/bin/github-archive
COPY --from=build /out/github-archive-panel /usr/local/bin/github-archive-panel
WORKDIR /workspace
CMD ["github-archive", "-config", "/workspace/config.yaml"]
