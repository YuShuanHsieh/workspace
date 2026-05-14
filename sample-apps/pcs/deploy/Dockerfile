FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/pcs .

FROM alpine:3.21
RUN adduser -D -u 10001 app
USER app
COPY --from=builder /out/pcs /usr/local/bin/pcs
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/pcs"]
