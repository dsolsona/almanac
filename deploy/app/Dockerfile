# Published application image: the embedded UI and the almanac executable on a minimal runtime.
# Base images are pinned by their multi-architecture index digest; scripts/check-image-pins.py keeps
# every Dockerfile and Compose file on the same digest for a given image.

# Build the browser assets with the pinned Node toolchain.
FROM node:26.8.1-bookworm-slim@sha256:367679cf9792759492a486e4aa4b421764d71a9546a6dae8aab81a99eb797b3e AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Compile Almanac and embed the production UI.
FROM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS build
WORKDIR /src
ENV GOTOOLCHAIN=local GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/assets.go ./web/assets.go
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/almanac ./cmd/almanac

# Small runtime with TLS, certificate validation, and volume locking tools.
FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3 AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates openssl curl util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 almanac \
    && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin almanac
COPY --from=build /out/almanac /usr/local/bin/almanac
COPY deploy/local/init.sh /usr/local/lib/almanac/init.sh
COPY deploy/local/init-test.sh /usr/local/lib/almanac/init-test.sh
USER 10001:10001
ENTRYPOINT []
CMD ["/usr/local/bin/almanac", "version"]
