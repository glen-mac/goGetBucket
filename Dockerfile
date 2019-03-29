# Build Container
FROM golang:1.10.3-alpine3.7 AS build-env
RUN apk add --no-cache --upgrade git openssh-client ca-certificates
RUN go get -u github.com/golang/dep/cmd/dep

# Need to use full path to handle local package imports (argo and common)
WORKDIR /go/src/github.com/anshumanbh/gogetbucket

# Cache the dependencies early
COPY Gopkg.toml Gopkg.lock ./
RUN dep ensure -vendor-only -v

# Build
COPY main.go ./
RUN go build -v -o ${GOPATH}/bin/gogetbucket

# Final Container
FROM alpine:3.7
LABEL maintainer="Anshuman Bhartiya"
COPY --from=build-env /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build-env /go/bin/gogetbucket /usr/bin/gogetbucket
COPY lists/ ./lists/

ENTRYPOINT ["/usr/bin/gogetbucket"]
