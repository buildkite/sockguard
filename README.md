# Docker Safety Sock

Safely providing access to a docker daemon to untrusted containers is challenging. By design docker doesn't provide any sort of access control over what can be done over that socket, so anything which has the socket has the same influence over your system as the user that docker is running as. This includes the host filesystem via mounts. To compound this, the default configuration of most docker installations has docker running with root privileges.

In a CI environment, builds need to be able to create containers, networks and volumes with access to a limit set of filesystem directories on the host. They need to have access to the resources they create and be able to destroy them as makes sense in the build.

## How it works

Docker Safety Sock (dsm) provides a proxy around the [docker socket]() that is passed to the container that safely runs the build. The proxied socket adds restrictions around what can be accessed via the socket.

## How is this solved elsewhere?

Docker provides an ACL system in their Enterprise product, and also provides a plugin API with authorization hooks. At this stage the plugin eco-system is still pretty new.

Another approach is Docker-in-docker, which is unfortunately slow and fraught with issues.


## Endpoints Implemented

https://docs.docker.com/engine/api/v1.32

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
- [*] POST /containers/prune (filtered)
- [*] GET /images/json (filtered)
- [*] POST /build (label added)
- [*] POST /build/prune  (filtered)
- [ ] POST /images/create
- [ ] GET /images/{name}/json
- [ ] GET /images/{name}/history
- [ ] PUSH /images/{name}/push
- [ ] POST  /images/{name}/tag
- [ ] REMOVE /images/{name}
- [ ] GET /images/search
- [ ] POST /images/prune
- [ ] POST /commit
- [ ] POST /images/{name}/get
- [ ] GET /images/get
- [ ] POST /images/load
- [ ] GET /networks
- [ ] GET /networks/{id}
- [ ] GET /networks/{id}
- [ ] POST /networks/create
- [ ] POST /networks/{id}/connect
- [ ] POST /networks/{id}/disconnect
- [ ] POST /networks/prune
- [ ] GET /volumes
- [ ] POST /volumes/create
- [ ] GET /volumes/{name}
- [ ] DELETE /volumes/{name}
- [ ] POST /volumes/prune
- [ ] POST /containers/{id}/exec
- [ ] POST /exec/{id}/start
- [ ] POST /exec/{id}/resize
- [ ] GET /exec/{id}/json
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
- [ ] GET /secrets
- [ ] POST /secrets/create
- [ ] GET /secrets/{id}
- [ ] DELETE /secrets/{id}
- [ ] POST /secrets/{id}/update
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
- [ ] POST /auth
- [ ] POST /info
- [ ] GET /version
- [*] GET /_ping (direct)
- [ ] GET /events
- [ ] GET /system/df
- [ ] GET /configs
- [ ] POST /configs/create
- [ ] GET /configs/{id}
- [ ] DELETE /configs/{id}
- [ ] POST /configs/{id}/update
- [ ] GET /distribution/{name}/json
- [ ] POST /session
- [ ] GET /tasks/{id}/logs
