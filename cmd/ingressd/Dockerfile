FROM haproxy:3.0-alpine AS haproxy-base

FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /ingressd ./cmd/ingressd

FROM haproxy-base
USER root
COPY --from=builder /ingressd /usr/local/bin/ingressd
RUN mkdir -p /data/ingress/certs
ENTRYPOINT ["/usr/local/bin/ingressd"]
# Add --proxy-protocol when a host HAProxy forwards via TCP passthrough (preserves real client IP).
CMD ["--config", "/data/ingress/config.json", \
     "--haproxy-cfg", "/data/ingress/haproxy.cfg", \
     "--certs-dir", "/data/ingress/certs"]
