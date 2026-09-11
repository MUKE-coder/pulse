# Build context: the repository root (see docker-compose.yml).
FROM golang:1.24 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The SQLite driver is pure Go, so no CGo is needed.
RUN CGO_ENABLED=0 go build -o /app ./examples/multi-instance

FROM gcr.io/distroless/static-debian12
COPY --from=build /app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
