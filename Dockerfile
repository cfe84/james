FROM golang:1.25-alpine AS hem-builder
RUN apk add --no-cache gcc musl-dev
ARG VERSION=dev
WORKDIR /build/hem
COPY hem/go.mod hem/go.sum ./
COPY moneypenny/go.mod moneypenny/go.sum /build/moneypenny/
RUN go mod download
COPY hem/ .
COPY moneypenny/ /build/moneypenny/
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

FROM golang:1.25-alpine AS gadgets-builder
ARG VERSION=dev
WORKDIR /build
COPY gadgets/ .
RUN CGO_ENABLED=0 go build -ldflags "-X main.Version=${VERSION}" -o gadgets ./cmd/gadgets/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
ENV TZ=America/Denver
COPY --from=hem-builder /build/hem/hem /usr/local/bin/hem
COPY --from=mi6-builder /build/mi6-client /usr/local/bin/mi6-client
COPY --from=qew-builder /build/qew /usr/local/bin/qew
COPY --from=gadgets-builder /build/gadgets /usr/local/bin/gadgets
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENV HEM_MI6_URL="" MI6_SERVER_FINGERPRINT="" QEW_PASSWORD="" LISTEN=":8077"
EXPOSE 8077
ENTRYPOINT ["/entrypoint.sh"]
