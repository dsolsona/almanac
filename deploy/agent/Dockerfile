# Published agent image: the outbound-only almanac-agent executable on a minimal runtime.
# Base images are pinned by their multi-architecture index digest; scripts/check-image-pins.py keeps
# every Dockerfile and Compose file on the same digest for a given image.

FROM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS build
WORKDIR /src
ENV GOTOOLCHAIN=local GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/almanac-agent ./cmd/almanac-agent

# The agent only needs certificate roots: it dials the backend and the datasources it is assigned.
FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3 AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 almanac \
    && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin almanac
COPY --from=build /out/almanac-agent /usr/local/bin/almanac-agent
USER 10001:10001
# The executable has one command: run --backend-url=… --token-file=… [--ca-file=…]; the token file is mounted privately.
ENTRYPOINT ["/usr/local/bin/almanac-agent"]
CMD ["run"]
