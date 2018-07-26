package main

import (
	"context"
	"fmt"
	"os"

	dcl "github.com/docker/docker/client"
)

// Returns an error if there is no CgroupParent defined for this container
// (or any other issues talking to the Docker API)
func thisContainerCgroupParent(docker_socket *string) (string, error) {
	docker_cli, err := dcl.NewClientWithOpts(
		dcl.WithHost(fmt.Sprintf("unix://%s", *docker_socket)),
		dcl.WithVersion("v1.37"),
	)
	if err != nil {
		return "", err
	}

	this_hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}
	if this_hostname == "" {
		return "", fmt.Errorf("Kernel reported hostname is empty or not set, cannot use this to detect the current Container ID")
	}
	// This seems the most reliable mechanism for now, assuming 99% of use cases will just be ephemeral hostnames which default to container IDs
	// An alternative consideration was /sys/fs/cgroup but the values here can differ between container schedulers, more "grey" to parse out
	// If you define a pet hostname here, go read http://cloudscaling.com/blog/cloud-computing/the-history-of-pets-vs-cattle/ :)
	this_container_id := this_hostname

	this_container, err := docker_cli.ContainerInspect(context.Background(), this_container_id)
	if err != nil {
		return "", err
	}

	cgroup_parent := this_container.ContainerJSONBase.HostConfig.CgroupParent

	err = docker_cli.Close()
	if err != nil {
		return "", err
	}

	// Return error if it's empty
	if cgroup_parent == "" {
		return "", fmt.Errorf("CgroupParent is empty for Container ID '%s'", this_container_id)
	} else {
		return cgroup_parent, nil
	}
}
