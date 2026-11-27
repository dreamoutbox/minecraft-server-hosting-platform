FROM golang:1.27-alpine AS builder

WORKDIR /app
COPY go.work go.mod go.sum ./
COPY go-stripe-kit/go.* ./go-stripe-kit/
COPY fileserver/ ./fileserver/

RUN cd fileserver && CGO_ENABLED=0 GOOS=linux go build -o /app/fileserver-bin ./cmd/fileserver

FROM alpine:latest
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /app/fileserver-bin /app/fileserver

EXPOSE 9100
ENTRYPOINT ["/app/fileserver"]
