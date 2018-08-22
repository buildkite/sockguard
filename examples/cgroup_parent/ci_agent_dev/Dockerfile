FROM debian:stretch

RUN apt-get update && \
    apt-get install -y curl && \
    rm -rf /var/run/apt/lists/*

# Install docker so we can test the unix socket
ENV DOCKER_VERSION 18.06.1
RUN curl -O "https://download.docker.com/linux/static/stable/x86_64/docker-${DOCKER_VERSION}-ce.tgz" && \
    tar xzf "docker-${DOCKER_VERSION}-ce.tgz" && \
    mv /docker/docker /usr/bin/docker && \
    rm -rf /docker && \
    rm -rf "docker-${DOCKER_VERSION}-ce.tgz"

# Also install docker-compose, for testing/misc
ENV DOCKER_COMPOSE_VERSION 1.22.0
RUN curl -L "https://github.com/docker/compose/releases/download/${DOCKER_COMPOSE_VERSION}/docker-compose-$(uname -s)-$(uname -m)" -o /usr/bin/docker-compose && \
    chmod +x /usr/bin/docker-compose

COPY ./start.sh /start.sh

RUN chmod +x /start.sh && \
    ln -sf /var/run/docker/sockguard.sock /var/run/docker.sock

CMD [ "/start.sh" ]
