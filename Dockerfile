# syntax=docker/dockerfile:1
# Same shape as aeman's image: the SPA and the Go binary are built on the
# build host's architecture (the binary cross-compiled), so a multi-arch
# image costs one build and no emulation.

# 1. The SPA into web/dist (architecture-independent).
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
COPY web/scripts/ ./scripts/
RUN npm ci
COPY web/ ./
RUN npm run build

# 2. The self-contained Go binary (embeds web/dist via go:embed).
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG VERSION=docker
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist ./web/dist
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /aeboard ./cmd/aeboard
RUN mkdir -p /data && chown 65532:65532 /data

# 3. Runtime: the static binary plus git (for file:// remotes and store
#    maintenance) and CA certificates for HTTPS to the forge; non-root, the
#    same uid as aeman's image.
FROM alpine:3.21
RUN apk add --no-cache git ca-certificates \
 && addgroup -g 65532 nonroot \
 && adduser -D -H -u 65532 -G nonroot nonroot
COPY --from=build /aeboard /aeboard
COPY --from=build --chown=65532:65532 /data /data
USER 65532:65532
ENV AEBOARD_DATA=/data
EXPOSE 8766
ENTRYPOINT ["/aeboard"]
CMD ["serve", "--addr=0.0.0.0:8766", "--identity=none"]
