# Image generated on 2026-11-05
ARG GO_IMAGE=golang:1.27.3-trixie@sha256:af1bfc2614ebb1dbd31af514b78740fd678850e76498762b425ecee4c5c6ea80
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
