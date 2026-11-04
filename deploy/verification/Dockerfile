FROM victoriametrics/victoria-metrics:v1.155.0@sha256:f60dc268b54ecf87f6e09d740c2b732838aa9193090cc741417c9f6386a130df AS victoria-metrics

FROM node:26.8.1-bookworm-slim@sha256:367679cf9792759492a486e4aa4b421764d71a9546a6dae8aab81a99eb797b3e AS node

FROM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9

ENV PATH=/usr/local/go/bin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
	PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright \
	GOTOOLCHAIN=local \
	GOWORK=off

COPY --from=node /usr/local /usr/local

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates curl gcc git libc6-dev make postgresql-client python3 \
	&& rm -rf /var/lib/apt/lists/*

RUN printf '%s\n' 'F2-T22-linux-verifier-v1' > /etc/almanac-linux-verifier

WORKDIR /opt/almanac/web
COPY web/package.json web/package-lock.json ./
RUN npm ci \
	&& ./node_modules/.bin/playwright install --with-deps chromium

WORKDIR /opt/almanac/tools/openapi
COPY tools/openapi/package.json tools/openapi/package-lock.json ./
RUN npm ci

WORKDIR /opt/almanac/go
COPY go.mod go.sum ./
RUN go mod download
RUN GOPROXY=https://proxy.golang.org GOBIN=/usr/local/bin go install github.com/JetBrains/go-modern-guidelines@v0.1.1 \
	&& GOPROXY=https://proxy.golang.org GOBIN=/usr/local/bin go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0 \
	&& GOPROXY=https://proxy.golang.org GOBIN=/usr/local/bin go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1

# Smoke checks use ripgrep and procps; keep these after the cached tool layers.
RUN apt-get update \
	&& apt-get install -y --no-install-recommends ripgrep procps \
	&& rm -rf /var/lib/apt/lists/*

COPY --from=victoria-metrics /victoria-metrics-prod /usr/local/bin/victoria-metrics-prod
WORKDIR /workspace
ARG SETUP_FINGERPRINT
COPY setup-fingerprint /etc/almanac-linux-verifier-fingerprint
LABEL org.almanac.verification.setup-fingerprint=${SETUP_FINGERPRINT}

ENTRYPOINT []
CMD ["/bin/sh"]
