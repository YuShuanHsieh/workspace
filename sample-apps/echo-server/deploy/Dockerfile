FROM golang:1.25.8-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /echo-server .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /echo-server /echo-server
ENTRYPOINT ["/echo-server"]
