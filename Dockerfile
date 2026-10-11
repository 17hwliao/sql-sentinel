ARG GO_BUILD_IMAGE=golang:1.26.9-alpine
FROM ${GO_BUILD_IMAGE} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/kafka-relay ./cmd/kafka-relay \
 && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/kafka-worker ./cmd/kafka-worker \
 && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/api ./cmd/api \
 && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/migrate ./cmd/migrate \
 && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/sentinel ./cmd/sentinel \
 && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/verifier ./cmd/verifier \
 && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/pg-compare ./cmd/pg-compare \
 && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/grpc-probe ./cmd/grpc-probe \
 && CGO_ENABLED=0 GOOS=linux go build -buildvcs=false -trimpath -ldflags='-s -w' -o /out/write-probe ./cmd/write-probe

FROM build AS verify
RUN apk add --no-cache build-base
RUN CGO_ENABLED=1 go test -race -count=1 ./... && go vet ./...

FROM alpine:3.21
RUN apk add --no-cache ca-certificates wget
RUN adduser -D -H -u 10001 sentinel
COPY --from=build /out/ /usr/local/bin/
USER sentinel
