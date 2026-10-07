# Image generated on 2026-10-06
ARG GO_IMAGE=golang:1.27.1-trixie@sha256:8f58fd67ea075142d947a60e0caa4317746a55118d312f027793d382c7741734
FROM ${GO_IMAGE} AS builder

WORKDIR /build

COPY . .

ARG GOFLAGS="-ldflags=-w -ldflags=-s"
RUN CGO_ENABLED=0 go build -o k6build -trimpath ./cmd/k6build/main.go

# k6build server requires golang toolchain
FROM ${GO_IMAGE}

RUN addgroup --gid 1000 k6build && \
    adduser --uid 1000 --ingroup k6build \
    --home /home/k6build --shell /bin/sh \
    --disabled-password --gecos "" k6build

COPY --from=builder /build/k6build /usr/local/bin/

WORKDIR /home/k6build

USER k6build

ENTRYPOINT ["/usr/local/bin/k6build"]
