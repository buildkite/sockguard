# Sockguard

Safely providing access to a docker daemon to untrusted containers is [challenging](https://docs.docker.com/engine/security/https/). By design docker doesn't provide any sort of access control over what can be done over that socket, so anything which has the socket has the same influence over your system as the user that docker is running as. This includes the host filesystem via mounts. To compound this, the default configuration of most docker installations has docker running with root privileges.

In a CI environment, builds need to be able to create containers, networks and volumes with access to a limit set of filesystem directories on the host. They need to have access to the resources they create and be able to destroy them as makes sense in the build.

## Usage

This runs a guarded socket that is then passed into a container for Docker outside of Docker usage.

```
sockguard --upstream-socket /var/run/docker.sock --allow-bind "$PWD" &
docker -H unix://$PWD/sockguard.sock run --rm -v $PWD/sockguard.sock:/var/lib/docker.sock buildkite/agent:3
```

## How it works

Sockguard provides a proxy around the docker socket that is passed to the container that safely runs the build. The proxied socket adds restrictions around what can be accessed via the socket.

When an image, container, volume or network is created it gets given a label of `com.buildkite.sockguard.owner={identifier}`, which is the identifier of the specific instance of the socket proxy. Each subsequent operation is checked against this ownership socket and only a match (or in the case of images, the lack of an owner), is allowed to proceed for read or write operations.

In addition, creation of containers imposes certain restrictions to ensure that containers are contained:

* No `privileged` mode is allowed
* By default no host bind mounts are allowed, but certain paths can be white-listed with `--allow-bind`
* No `host` network mode is allowed

## How is this solved elsewhere?

Docker provides an ACL system in their Enterprise product, and also provides a plugin API with authorization hooks. At this stage the plugin eco-system is still pretty new. The advantage of using a local socket is that you can use filesystem permissions to control access to it.

Another approach is Docker-in-docker, which is unfortunately [slow and fraught with issues](https://jpetazzo.github.io/2015/09/03/do-not-use-docker-in-docker-for-ci/).

## Implementation status

Very alpha! Most of the high risk endpoints are covered decently. Not yet ready for production usage.

Based off https://docs.docker.com/engine/api/v1.32.

### Containers (Done)

- [*] GET /containers/json (filtered)
- [*] POST /containers/create (label added)
- [*] GET /containers/{id}/json (ownership check)
- [*] GET /containers/{id}/top (ownership check)
- [*] GET /containers/{id}/logs (ownership check)
- [*] GET /containers/{id}/changes (ownership check)
- [*] GET /containers/{id}/export (ownership check)
- [*] GET /containers/{id}/stats (ownership check)
- [*] POST /containers/{id}/resize (ownership check)
- [*] POST /containers/{id}/start (ownership check)
- [*] POST /containers/{id}/stop (ownership check)
- [*] POST /containers/{id}/restart (ownership check)
- [*] POST /containers/{id}/kill (ownership check)
- [*] POST /containers/{id}/update (ownership check)
- [*] POST /containers/{id}/rename (ownership check)
- [*] POST /containers/{id}/pause (ownership check)
- [*] POST /containers/{id}/unpause (ownership check)
- [*] POST /containers/{id}/attach (ownership check)
- [*] GET /containers/{id}/attach/ws (ownership check)
- [*] POST /containers/{id}/wait (ownership check)
- [*] DELETE /containers/{id} (ownership check)
- [*] HEAD /containers/{id}/archive (ownership check)
- [*] GET /containers/{id}/archive (ownership check)
- [*] PUT /containers/{id}/archive (ownership check)
- [*] POST /containers/{id}/exec (ownership check)
- [*] POST /containers/prune (filtered)
- [*] POST /exec/{id}/start
- [*] POST /exec/{id}/resize
- [*] GET /exec/{id}/json

### Images (Partial)

- [*] GET /images/json (filtered)
- [*] POST /build (label added)
- [*] POST /build/prune  (filtered)
- [ ] POST /images/create
- [*] GET /images/{name}/json
- [*] GET /images/{name}/history
- [*] PUSH /images/{name}/push
- [*] POST  /images/{name}/tag
- [*] REMOVE /images/{name}
- [ ] GET /images/search
- [*] POST /images/prune
- [ ] POST /commit
- [*] POST /images/{name}/get
- [ ] GET /images/get
- [ ] POST /images/load

### Networks (Done)

- [*] GET /networks
- [*] GET /networks/{id}
- [*] POST /networks/create
- [*] POST /networks/{id}/connect
- [*] POST /networks/{id}/disconnect
- [*] POST /networks/prune

### Volumes

- [*] GET /volumes
- [*] POST /volumes/create
- [*] GET /volumes/{name}
- [*] DELETE /volumes/{name}
- [()] POST /volumes/prune

### Swarm (Disabled)

- [ ] GET /swarm
- [ ] POST /swarm/init
- [ ] POST /swarm/join
- [ ] POST  /swarm/leave
- [ ] POST /swarm/update
- [ ] GET /swarm/unlockkey
- [ ] POST /swarm/unlock
- [ ] GET /nodes
- [ ] GET /nodes/{id}
- [ ] DELETE /nodes/{id}
- [ ] POST /nodes/{id}/update
- [ ] GET /services
- [ ] POST /services/create
- [ ] GET /services/{id}
- [ ] DELETE /services/{id}
- [ ] POST /services/{id}/update
- [ ] GET /services/{id}/logs
- [ ] GET /tasks
- [ ] GET /tasks/{id}
- [ ] GET /tasks/{id}/logs
- [ ] GET /secrets
- [ ] POST /secrets/create
- [ ] GET /secrets/{id}
- [ ] DELETE /secrets/{id}
- [ ] POST /secrets/{id}/update

### Plugins (Disabled)

- [ ] GET /plugins
- [ ] GET /plugins/privileges
- [ ] POST /plugins/pull
- [ ] GET /plugins/{name}/json
- [ ] DELETE /plugins/{name}
- [ ] POST /plugins/{name}/enable
- [ ] POST /plugins/{name}/disable
- [ ] POST /plugins/{name}/upgrade
- [ ] POST /plugins/create
- [ ] POST /plugins/{name}/set

### System

- [ ] POST /auth
- [*] POST /info
- [ ] GET /version
- [*] GET /_ping (direct)
- [ ] GET /events
- [ ] GET /system/df
- [ ] GET /distribution/{name}/json
- [ ] POST /session

### Configs

- [ ] GET /configs
- [ ] POST /configs/create
- [ ] GET /configs/{id}
- [ ] DELETE /configs/{id}
- [ ] POST /configs/{id}/update

