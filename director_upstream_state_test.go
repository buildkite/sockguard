package main

import (
	"fmt"
)

// Simplified mocked out upstream state of Docker networks, for use in create container/create network/delete network/check owner tests
// NOTE: there is no locking around accesses in this type, assumed that each test block will have it's own instance
type upstreamState struct {
	// Key = container name/ID
	containers map[string]upstreamStateContainer
	// Key = image name/ID
	images map[string]upstreamStateImage
	// Key = network name/ID
	networks map[string]upstreamStateNetwork
	// Key = volume name
	volumes map[string]upstreamStateVolume
}

type upstreamStateContainer struct {
	owner            string
	attachedNetworks []upstreamStateContainerAttachedNetwork
}

type upstreamStateContainerAttachedNetwork struct {
	name string
	// Alias hostnames used to talk to this container via this attached network
	// Can be empty. Also more than 1 container can have the same alias, and Docker will round-robin them.
	aliases []string
}

type upstreamStateImage struct {
	owner string
}

type upstreamStateNetwork struct {
	owner string
}

type upstreamStateVolume struct {
	owner string
}

func (u *upstreamState) ownerLabelContent(owner string) string {
	ownerLabel := ""
	if owner != "" {
		ownerLabel = fmt.Sprintf("\"com.buildkite.sockguard.owner\":\"%s\"", owner)
	}
	return ownerLabel
}

//////////////
// containers

func (u *upstreamState) createContainer(idOrName string, theOwner string, networks []upstreamStateContainerAttachedNetwork) error {
	// Deny if already exists
	if u.doesContainerExist(idOrName) == true {
		return fmt.Errorf("Cannot create container with ID/Name '%s', already exists", idOrName)
	}
	// "Create" it
	u.containers[idOrName] = upstreamStateContainer{
		owner:            theOwner,
		attachedNetworks: networks,
	}
	return nil
}

func (u *upstreamState) deleteContainer(idOrName string) error {
	// Deny if does not exist
	if u.doesContainerExist(idOrName) == false {
		return fmt.Errorf("Cannot delete container with ID/Name '%s', does not exist", idOrName)
	}
	// "Delete" it
	delete(u.containers, idOrName)
	return nil
}

func (u *upstreamState) doesContainerExist(idOrName string) bool {
	_, ok := u.containers[idOrName]
	return ok
}

func (u *upstreamState) getContainerOwner(idOrName string) string {
	return u.containers[idOrName].owner
}

func (u *upstreamState) getContainerAttachedNetworks(idOrName string) []upstreamStateContainerAttachedNetwork {
	return u.containers[idOrName].attachedNetworks
}

//////////////
// images

func (u *upstreamState) createImage(idOrName string, theOwner string) error {
	// Deny if already exists
	if u.doesImageExist(idOrName) == true {
		return fmt.Errorf("Cannot create image with ID/Name '%s', already exists", idOrName)
	}
	// "Create" it
	u.images[idOrName] = upstreamStateImage{
		owner: theOwner,
	}
	return nil
}

func (u *upstreamState) deleteImage(idOrName string) error {
	// Deny if does not exist
	if u.doesImageExist(idOrName) == false {
		return fmt.Errorf("Cannot delete image with ID/Name '%s', does not exist", idOrName)
	}
	// TODOLATER: images cannot be deleted if a container is using them, add logic if/when test coverage requires it
	// "Delete" it
	delete(u.images, idOrName)
	return nil
}

func (u *upstreamState) doesImageExist(idOrName string) bool {
	_, ok := u.images[idOrName]
	return ok
}

func (u *upstreamState) getImageOwner(idOrName string) string {
	return u.images[idOrName].owner
}

//////////////
// networks

func (u *upstreamState) createNetwork(idOrName string, theOwner string) error {
	// Deny if already exists
	if _, ok := u.networks[idOrName]; ok {
		return fmt.Errorf("Cannot create network with ID/Name '%s', already exists", idOrName)
	}
	// "Create" it
	u.networks[idOrName] = upstreamStateNetwork{
		owner: theOwner,
	}
	return nil
}

func (u *upstreamState) deleteNetwork(idOrName string) error {
	// Deny if does not exist
	if _, ok := u.networks[idOrName]; ok == false {
		return fmt.Errorf("Cannot delete network with ID/Name '%s', does not exist", idOrName)
	}
	// You can't delete a network that has attached "endpoints" on a real Docker daemon, simulate
	// that for containers only for now.
	for k1, v1 := range u.containers {
		for _, v2 := range v1.attachedNetworks {
			if v2.name == idOrName {
				return fmt.Errorf("Cannot delete network with ID/Name '%s', endpoint still attached (container '%s')", idOrName, k1)
			}
		}
	}
	// "Delete" it
	delete(u.networks, idOrName)
	return nil
}

func (u *upstreamState) doesNetworkExist(idOrName string) bool {
	_, ok := u.networks[idOrName]
	return ok
}

func (u *upstreamState) getNetworkOwner(idOrName string) string {
	return u.networks[idOrName].owner
}

func (u *upstreamState) networkConnectDisconnectChecks(containerIdOrName string, networkIdOrName string) error {
	if _, ok := u.containers[containerIdOrName]; ok == false {
		return fmt.Errorf("container does not exist")
	}
	if _, ok := u.networks[networkIdOrName]; ok == false {
		return fmt.Errorf("network does not exist")
	}
	return nil
}

func (u *upstreamState) isContainerConnectedToNetwork(containerIdOrName string, networkIdOrName string) bool {
	// TODOLATER: check the container exists before proceeding? considering what's executing this, skipping duplication for now
	for _, v := range u.containers[containerIdOrName].attachedNetworks {
		if v.name == networkIdOrName {
			return true
		}
	}
	return false
}

func (u *upstreamState) connectContainerToNetwork(containerIdOrName string, networkIdOrName string, containerAliases []string) error {
	// Deny if container or network does not exist
	if err := u.networkConnectDisconnectChecks(containerIdOrName, networkIdOrName); err != nil {
		return fmt.Errorf("Cannot connect container '%s' to network '%s', %s", containerIdOrName, networkIdOrName, err.Error())
	}
	// Check if container is already attached to this network, if so deny
	if u.isContainerConnectedToNetwork(containerIdOrName, networkIdOrName) == true {
		return fmt.Errorf("Cannot connect container '%s' to network '%s', already attached", containerIdOrName, networkIdOrName)
	}
	// "Connect" the container to the network
	container := u.containers[containerIdOrName]
	containerNetwork := upstreamStateContainerAttachedNetwork{
		name:    networkIdOrName,
		aliases: containerAliases,
	}
	container.attachedNetworks = append(container.attachedNetworks, containerNetwork)
	u.containers[containerIdOrName] = container
	return nil
}

func (u *upstreamState) disconnectContainerToNetwork(containerIdOrName string, networkIdOrName string) error {
	// Deny if container or network does not exist
	if err := u.networkConnectDisconnectChecks(containerIdOrName, networkIdOrName); err != nil {
		return fmt.Errorf("Cannot disconnect container '%s' from network '%s', %s", containerIdOrName, networkIdOrName, err.Error())
	}
	// Check if container is already attached to this network, if not deny
	if u.isContainerConnectedToNetwork(containerIdOrName, networkIdOrName) == false {
		return fmt.Errorf("Cannot disconnect container '%s' from network '%s', not attached", containerIdOrName, networkIdOrName)
	}
	// "Disconnect" the container from the network
	newAttachedNetworks := []upstreamStateContainerAttachedNetwork{}
	for _, v := range u.containers[containerIdOrName].attachedNetworks {
		if v.name != networkIdOrName {
			newAttachedNetworks = append(newAttachedNetworks, v)
		}
	}
	container := u.containers[containerIdOrName]
	container.attachedNetworks = newAttachedNetworks
	u.containers[containerIdOrName] = container
	return nil
}

//////////////
// volumes

func (u *upstreamState) createVolume(name string, theOwner string) error {
	// Deny if already exists
	if u.doesVolumeExist(name) == true {
		return fmt.Errorf("Cannot create volume with Name '%s', already exists", name)
	}
	// "Create" it
	u.volumes[name] = upstreamStateVolume{
		owner: theOwner,
	}
	return nil
}

func (u *upstreamState) deleteVolume(name string) error {
	// Deny if does not exist
	if u.doesVolumeExist(name) == false {
		return fmt.Errorf("Cannot delete volume with Name '%s', does not exist", name)
	}
	// "Delete" it
	delete(u.volumes, name)
	return nil
}

func (u *upstreamState) doesVolumeExist(name string) bool {
	_, ok := u.volumes[name]
	return ok
}

func (u *upstreamState) getVolumeOwner(name string) string {
	return u.volumes[name].owner
}
