FROM alpine:3.8

# Install docker so we can test the unix socket
RUN apk add --no-cache bash docker

COPY ./start.sh /start.sh

RUN chmod +x /start.sh && \
    ln -sf /var/run/docker/sockguard.sock /var/run/docker.sock

CMD [ "/start.sh" ]
