FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /meshrouterd ./cmd/meshrouterd \
 && mkdir -p /data/mesh

FROM scratch
COPY --from=builder /meshrouterd /meshrouterd
COPY --from=builder /data/mesh /data/mesh
ENTRYPOINT ["/meshrouterd"]
