FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/echo-server .

FROM alpine:3.21
RUN adduser -D -u 10001 app
USER app
COPY --from=builder /out/echo-server /usr/local/bin/echo-server
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/echo-server"]
