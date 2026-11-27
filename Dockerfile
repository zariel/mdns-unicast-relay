# syntax=docker/dockerfile:1
ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/mdns-unicast-relay .

FROM gcr.io/distroless/static-debian13:nonroot
ARG VERSION=dev
ARG VCS_REF=unknown
LABEL org.opencontainers.image.title="mdns-unicast-relay" \
      org.opencontainers.image.description="Relays a local mDNS responder's answers for chosen services as unicast" \
      org.opencontainers.image.source="https://github.com/zariel/mdns-unicast-relay" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${VCS_REF}"

COPY --from=builder /out/mdns-unicast-relay /usr/local/bin/mdns-unicast-relay

USER 65532:65532
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/mdns-unicast-relay"]
