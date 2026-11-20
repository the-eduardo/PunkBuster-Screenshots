# Build Stage
FROM golang:1.26-alpine AS builder

LABEL authors="the-eduardo"

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 go build -ldflags="-w -s" -o /go/bin/app ./cmd/bot

# Final stage
# alpine:3.20 saiu de suporte em 01/04/2026; 3.24 tem suporte ate 01/06/2028
# (alpinelinux.org/releases, conferido em 20/11/2026). Mesmos pacotes do apk
# abaixo existem com o mesmo nome na 3.24 (APKINDEX main/aarch64).
FROM alpine:3.24

# ca-certificates é obrigatório aqui: sem ele o handshake TLS com a API/gateway
# do Discord falha (a imagem "scratch" usada antes não garantia isso).
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
VOLUME ["/data"]

COPY --from=builder /go/bin/app /app/app

CMD ["./app"]
