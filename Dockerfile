# Build the browser assets with the pinned Node toolchain.
FROM node:26.8.1-bookworm-slim@sha256:367679cf9792759492a486e4aa4b421764d71a9546a6dae8aab81a99eb797b3e AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Compile Almanac and embed the production UI.
FROM golang:1.27.3-bookworm@sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5 AS build
WORKDIR /src
ENV GOTOOLCHAIN=local GOFLAGS=-mod=readonly
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/assets.go ./web/assets.go
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/almanac ./cmd/almanac
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/almanac-agent ./cmd/almanac-agent

# Small local runtime with TLS, certificate validation, and volume locking tools.
FROM debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8 AS runtime
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates openssl curl util-linux \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 almanac \
    && useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin almanac
COPY --from=build /out/almanac /usr/local/bin/almanac
COPY --from=build /out/almanac-agent /usr/local/bin/almanac-agent
COPY deploy/local/init.sh /usr/local/lib/almanac/init.sh
COPY deploy/local/init-test.sh /usr/local/lib/almanac/init-test.sh
USER 10001:10001
ENTRYPOINT []
CMD ["/usr/local/bin/almanac", "version"]
