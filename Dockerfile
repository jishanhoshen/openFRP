# syntax=docker/dockerfile:1
# Multi-stage build for TunnelHub binaries.
#   targets: control (relay control plane), agent (Linux agent image), e2e (test tools)

FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tunnelhub-control ./cmd/control \
 && go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tunnelhub-agent ./cmd/agent \
 && go build -trimpath -o /out/e2e-tool ./test/e2e/tool \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot AS control
COPY --from=build /out/tunnelhub-control /usr/local/bin/tunnelhub-control
USER nonroot
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/tunnelhub-control"]
CMD ["serve"]

FROM gcr.io/distroless/static-debian12:nonroot AS agent
COPY --from=build /out/tunnelhub-agent /usr/local/bin/tunnelhub-agent
USER nonroot
ENTRYPOINT ["/usr/local/bin/tunnelhub-agent"]
CMD ["run", "-config=/etc/tunnelhub/agent.yaml"]

FROM gcr.io/distroless/static-debian12:nonroot AS e2e
COPY --from=build /out/e2e-tool /usr/local/bin/e2e-tool
COPY --from=build /out/tunnelhub-agent /usr/local/bin/tunnelhub-agent
# Shared volume for agent config; a named volume inherits this ownership.
COPY --from=build --chown=65532:65532 /out/data /data
USER nonroot
ENTRYPOINT ["/usr/local/bin/e2e-tool"]
