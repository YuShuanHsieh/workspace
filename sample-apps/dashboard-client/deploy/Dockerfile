FROM golang:1.25.8-alpine AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /dashboard-client .

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /dashboard-client /dashboard-client
ENTRYPOINT ["/dashboard-client"]
