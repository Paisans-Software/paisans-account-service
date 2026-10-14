FROM golang:1.25.8-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /paisans-account-service ./cmd/paisans-account-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /paisans-account-service /paisans-account-service
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s CMD ["/paisans-account-service", "healthcheck"]
ENTRYPOINT ["/paisans-account-service"]
