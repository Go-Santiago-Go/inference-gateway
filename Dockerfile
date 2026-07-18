FROM golang:1.26 AS build
WORKDIR /src
COPY . .
# CGO_ENABLED=0 forces a fully static binary, so it runs on a base with no libc.
RUN CGO_ENABLED=0 go build -o /app ./cmd/server

# distroless/static carries CA certificates for the outbound TLS calls to Bedrock,
# which a scratch base would lack. It has no shell and no package manager.
FROM gcr.io/distroless/static
COPY --from=build /app /app

# The service binds 8080, so it needs no privilege; UID 65532 ships with distroless.
USER nonroot:nonroot
ENTRYPOINT ["/app"]
