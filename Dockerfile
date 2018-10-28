FROM golang:1.11-alpine as builder
RUN apk add --no-cache ca-certificates git
ENV GO111MODULE=on
WORKDIR /go/src/github.com/buildkite/sockguard
ADD go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -a -installsuffix cgo -ldflags="-w -s" -o /go/bin/sockguard

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /go/bin/sockguard /sockguard
ENTRYPOINT [ "/sockguard" ]
