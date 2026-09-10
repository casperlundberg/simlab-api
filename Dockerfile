FROM golang:1.24-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/simlab-api ./cmd/simlab-api

# Static distroless: no shell, no package manager. This service holds the
# autoscaler's API token, which is the key to every platform credential the
# autoscaler holds.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/simlab-api /usr/local/bin/simlab-api

EXPOSE 8081

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/simlab-api"]
