# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build

WORKDIR /src

RUN apk add --no-cache git ca-certificates tzdata

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /out/telegram-api ./

FROM alpine:3.20 AS runtime

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=build /out/telegram-api /app/telegram-api

ENTRYPOINT ["/app/telegram-api"]
