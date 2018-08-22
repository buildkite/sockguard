# Don't duplicate the build process
FROM buildkite/sockguard:latest as built

# So we can get a shell if we need to dig around for local testing. Main Dockerfile uses scratch
FROM alpine:3.8
COPY --from=built /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=built /sockguard /sockguard
ENTRYPOINT [ "/sockguard" ]
