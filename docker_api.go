package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
)

type containerInspect struct {
	Id         string                     `json:"Id"`
	HostConfig containerInspectHostConfig `json:"HostConfig"`
}

type containerInspectHostConfig struct {
	CgroupParent string `json:"CgroupParent"`
}

func dockerApiClient(docker_socket *string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				debugf("Dialing directly")
				return net.Dial("unix", *docker_socket)
			},
		},
	}
}

// Returns an error if there is no CgroupParent defined for this container
// (or any other issues talking to the Docker API)
func thisContainerCgroupParent(docker_socket *string) (string, error) {
	httpc := dockerApiClient(docker_socket)

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
	//this_container_id := this_hostname
	this_container_id := "355221589ed8"

	resp, err := httpc.Get(fmt.Sprintf("http://unix/v1.37/containers/%s/json", this_container_id))
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var decoded_resp containerInspect
	err = json.Unmarshal(body, &decoded_resp)
	if err != nil {
		return "", err
	}

	cgroup_parent := decoded_resp.HostConfig.CgroupParent

	// Return error if it's empty
	if cgroup_parent == "" {
		return "", fmt.Errorf("CgroupParent is empty for Container ID '%s'", this_container_id)
	} else {
		return cgroup_parent, nil
	}
}
