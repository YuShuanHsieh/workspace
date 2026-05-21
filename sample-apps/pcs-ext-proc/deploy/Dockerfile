FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 go build -o /out/pcs-ext-proc .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=65532:65532 /out/pcs-ext-proc /pcs-ext-proc
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/pcs-ext-proc"]
