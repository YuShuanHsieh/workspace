FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/dashboard-client .

FROM alpine:3.21
RUN adduser -D -u 10001 app
USER app
COPY --from=builder /out/dashboard-client /usr/local/bin/dashboard-client
ENTRYPOINT ["/usr/local/bin/dashboard-client"]
