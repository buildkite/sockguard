# Docker Safety Sock

Safely providing access to a docker daemon to untrusted containers is challenging. By design docker doesn't provide any sort of access control over what can be done over that socket, so anything which has the socket has the same influence over your system as the user that docker is running as. This includes the host filesystem via mounts. To compound this, the default configuration of most docker installations has docker running with root privileges.

In a CI environment, builds need to be able to create containers, networks and volumes with access to a limit set of filesystem directories on the host. They need to have access to the resources they create and be able to destroy them as makes sense in the build.

## How it works

Docker Safety Sock (dsm) provides a proxy around the [docker socket]() that is passed to the container that safely runs the build. The proxied socket adds restrictions around what can be accessed via the socket.

## How is this solved elsewhere?

Docker provides an ACL system in their Enterprise product, and also provides a plugin API with authorization hooks. At this stage the plugin eco-system is still pretty new.

Another approach is Docker-in-docker, which is unfortunately slow and fraught with issues.




