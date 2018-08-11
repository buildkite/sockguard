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

There is also an option to set `cgroup-parent` on container creation. This is useful for restricting CPU/Memory resources of containers spawned via this proxy (eg. when using a container scheduler).

## How is this solved elsewhere?

Docker provides an ACL system in their Enterprise product, and also provides a plugin API with authorization hooks. At this stage the plugin eco-system is still pretty new. The advantage of using a local socket is that you can use filesystem permissions to control access to it.

Another approach is Docker-in-docker, which is unfortunately [slow and fraught with issues](https://jpetazzo.github.io/2015/09/03/do-not-use-docker-in-docker-for-ci/).

## Implementation status

Very alpha! Most of the high risk endpoints are covered decently. Not yet ready for production usage.

Based off https://docs.docker.com/engine/api/v1.32.

### Containers (Done)

- [x] GET /containers/json (filtered)
- [x] POST /containers/create (label added)
- [x] GET /containers/{id}/json (ownership check)
- [x] GET /containers/{id}/top (ownership check)
- [x] GET /containers/{id}/logs (ownership check)
- [x] GET /containers/{id}/changes (ownership check)
- [x] GET /containers/{id}/export (ownership check)
- [x] GET /containers/{id}/stats (ownership check)
- [x] POST /containers/{id}/resize (ownership check)
- [x] POST /containers/{id}/start (ownership check)
- [x] POST /containers/{id}/stop (ownership check)
- [x] POST /containers/{id}/restart (ownership check)
- [x] POST /containers/{id}/kill (ownership check)
- [x] POST /containers/{id}/update (ownership check)
- [x] POST /containers/{id}/rename (ownership check)
- [x] POST /containers/{id}/pause (ownership check)
- [x] POST /containers/{id}/unpause (ownership check)
- [x] POST /containers/{id}/attach (ownership check)
- [x] GET /containers/{id}/attach/ws (ownership check)
- [x] POST /containers/{id}/wait (ownership check)
- [x] DELETE /containers/{id} (ownership check)
- [x] HEAD /containers/{id}/archive (ownership check)
- [x] GET /containers/{id}/archive (ownership check)
- [x] PUT /containers/{id}/archive (ownership check)
- [x] POST /containers/{id}/exec (ownership check)
- [x] POST /containers/prune (filtered)
- [x] POST /exec/{id}/start
- [x] POST /exec/{id}/resize
- [x] GET /exec/{id}/json

### Images (Partial)

- [x] GET /images/json (filtered)
- [x] POST /build (label added)
- [x] POST /build/prune  (filtered)
- [ ] POST /images/create
- [x] GET /images/{name}/json
- [x] GET /images/{name}/history
- [x] PUSH /images/{name}/push
- [x] POST  /images/{name}/tag
- [x] REMOVE /images/{name}
- [ ] GET /images/search
- [x] POST /images/prune
- [ ] POST /commit
- [x] POST /images/{name}/get
- [ ] GET /images/get
- [ ] POST /images/load

### Networks (Done)

- [x] GET /networks
- [x] GET /networks/{id}
- [x] POST /networks/create
- [x] POST /networks/{id}/connect
- [x] POST /networks/{id}/disconnect
- [x] POST /networks/prune

### Volumes

- [x] GET /volumes
- [x] POST /volumes/create
- [x] GET /volumes/{name}
- [x] DELETE /volumes/{name}
- [x] POST /volumes/prune

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
- [x] POST /info
- [ ] GET /version
- [x] GET /_ping (direct)
- [x] GET /events
- [ ] GET /system/df
- [ ] GET /distribution/{name}/json
- [ ] POST /session

### Configs

- [ ] GET /configs
- [ ] POST /configs/create
- [ ] GET /configs/{id}
- [ ] DELETE /configs/{id}
- [ ] POST /configs/{id}/update

## Example: Running in Amazon ECS with CgroupParent

Let's say you are spawning a `sockguard` instance per ECS task, to pass through a guarded Docker socker to some worker (eg. a CI worker). You may want to apply the same CPU/Memory constraints as the ECS task. This can be done via a bash wrapper to `/sockguard` in a sidecar container (ensure you have `bash`, `curl` and `jq` available):

```
#!/bin/bash

set -euo pipefail

###########################

# Detect CgroupParent first

# A) Use the container ID from /proc/self/cgroup
# (note: this works fine on a systemd based system, need to adjust the grep on pre-systemd? fine for us right now)
container_id=$(cat /proc/self/cgroup | grep "1:name=systemd" | rev | cut -d/ -f1 | rev)

# B) Use the hostname
# (note: works, as long as someone doesnt start the container with --hostname. A) preferred for now)
# container_id="$HOSTNAME"

if [ -z "$container_id" ]; then
  echo "sockguard/start.sh: container_id empty?"
  exit 1
fi

# Get the CgroupParent via the Docker API
container_inspect_url="http:/v1.37/containers/${container_id}/json"
cgroup_parent=$(curl -s --unix-socket /var/run/docker.sock "$container_inspect_url" | jq -r .HostConfig.CgroupParent)

if [ -z "$cgroup_parent" ]; then
  echo "sockguard/start.sh: cgroup_parent empty? (from Docker API)"
  exit 1
fi

###########################

# Start sockguard with some args
exec /sockguard -cgroup-parent '${cgroup_parent}' -owner-label '${cgroup_parent}' ...other args...
```
