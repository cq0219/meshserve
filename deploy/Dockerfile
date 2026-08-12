# MeshServe 多阶段构建镜像
FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/meshserve ./cmd/meshserve

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=builder /out/meshserve /usr/local/bin/meshserve
ENTRYPOINT ["meshserve"]
