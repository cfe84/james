FROM golang:1.25-alpine AS hem-builder
RUN apk add --no-cache gcc musl-dev
ARG VERSION=dev
WORKDIR /build
COPY hem/go.mod hem/go.sum ./
RUN go mod download
COPY hem/ .
RUN CGO_ENABLED=1 go build -ldflags "-X main.Version=${VERSION}" -o hem ./cmd/hem/

FROM golang:1.25-alpine AS mi6-builder
ARG VERSION=dev
WORKDIR /build
COPY mi6/go.mod mi6/go.sum ./
RUN go mod download
COPY mi6/ .
RUN CGO_ENABLED=0 go build -ldflags "-X main.Version=${VERSION}" -o mi6-client ./cmd/mi6-client/

FROM golang:1.25-alpine AS qew-builder
ARG VERSION=dev
WORKDIR /build
COPY qew/go.mod qew/go.sum ./
RUN go mod download
COPY qew/ .
RUN CGO_ENABLED=0 go build -ldflags "-X main.Version=${VERSION}" -o qew ./cmd/qew/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=hem-builder /build/hem /usr/local/bin/hem
COPY --from=mi6-builder /build/mi6-client /usr/local/bin/mi6-client
COPY --from=qew-builder /build/qew /usr/local/bin/qew
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENV HEM_MI6_URL="" QEW_PASSWORD="" LISTEN=":8077"
EXPOSE 8077
ENTRYPOINT ["/entrypoint.sh"]
