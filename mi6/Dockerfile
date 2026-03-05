FROM golang:1.23-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o mi6-server ./cmd/mi6-server/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /build/mi6-server /usr/local/bin/mi6-server
EXPOSE 7007
ENTRYPOINT ["mi6-server"]
