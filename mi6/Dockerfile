FROM golang:1.25-alpine AS builder
ARG VERSION=dev
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-X main.Version=${VERSION}" -o mi6-server ./cmd/mi6-server/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /build/mi6-server /usr/local/bin/mi6-server
ENV PORT=7007
EXPOSE 7007
ENTRYPOINT ["mi6-server", "--authorized-keys", "/etc/mi6/authorized_keys", "--server-key", "/etc/mi6/server_key"]

