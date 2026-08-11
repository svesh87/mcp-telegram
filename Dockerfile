# Build on the builder's own architecture and cross-compile, which is what Go is good at.
# Emulating the target architecture instead would make the arm64 image take minutes for no
# gain.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

# Dependencies first, so a change to the code does not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/mcp-telegram .

# The result is one static binary, so the image holds nothing else: no shell to reach the
# Telegram session with, and nothing to update.
FROM scratch

# The Bot API is HTTPS, so the roots have to be in the image. The stack mounts the host
# bundle over this one, because a bundle baked in at build time goes stale.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/mcp-telegram /mcp-telegram

# A user that is not root by default. Whoever mounts a session directory overrides this
# with their own identifier, since the directory has to stay writable for them.
USER 65532:65532

EXPOSE 8815

ENTRYPOINT ["/mcp-telegram"]
