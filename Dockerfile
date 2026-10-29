FROM golang:1.26 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /chainpulse .

# Static binary on distroless: CA certificates for HTTPS, no shell, runs as nonroot.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /chainpulse /chainpulse
EXPOSE 9090
ENTRYPOINT ["/chainpulse"]
