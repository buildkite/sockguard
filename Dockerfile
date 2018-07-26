FROM golang:1.10.3-alpine3.8

RUN mkdir -p /go/src/app
WORKDIR /go/src/app

RUN apk add --no-cache git

COPY *.go /go/src/app/

RUN go get -d -v ./...
RUN go install -v ./...

# Single binary in the final image
FROM alpine:3.8

COPY --from=0 /go/bin/app /sockguard

CMD [ "/sockguard" ]
