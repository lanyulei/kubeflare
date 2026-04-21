FROM golang:1.26.1 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/kubeflare ./cmd/kubeflare

FROM gcr.io/distroless/static-debian12

WORKDIR /app
COPY --from=builder /out/kubeflare /app/kubeflare
COPY configs/config.example.yaml /app/config.example.yaml

EXPOSE 8080

ENTRYPOINT ["/app/kubeflare", "serve", "--config", "/app/config.example.yaml"]
