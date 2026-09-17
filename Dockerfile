FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/kafka-relay ./cmd/kafka-relay \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/kafka-worker ./cmd/kafka-worker

FROM alpine:3.21
RUN adduser -D -H -u 10001 sentinel
COPY --from=build /out/kafka-relay /usr/local/bin/kafka-relay
COPY --from=build /out/kafka-worker /usr/local/bin/kafka-worker
USER sentinel
