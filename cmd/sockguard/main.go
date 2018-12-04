package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/buildkite/sockguard"
	"github.com/buildkite/sockguard/socketproxy"
)

var (
	debug bool
)

func init() {
	flag.BoolVar(&debug, "debug", false, "Show debugging logging for the socket")
}

func main() {
	filename := flag.String("filename", "sockguard.sock", "The guarded socket to create")
	socketMode := flag.String("mode", "0600", "Permissions of the guarded socket")
	socketUid := flag.Int("uid", -1, "The UID (owner) of the guarded socket (defaults to -1 - process owner)")
	socketGid := flag.Int("gid", -1, "The GID (group) of the guarded socket (defaults to -1 - process group)")
	upstream := flag.String("upstream-socket", "/var/run/docker.sock", "The path to the original docker socket")
	owner := flag.String("owner-label", "", "The value to use as the owner of the socket, defaults to the process id")
	allowBind := flag.String("allow-bind", "", "A path to allow host binds to occur under")
	allowHostModeNetworking := flag.Bool("allow-host-mode-networking", false, "Allow containers to run with --net host")
	cgroupParent := flag.String("cgroup-parent", "", "Set CgroupParent to an arbitrary value on new containers")
	user := flag.String("user", "", "Forces --user on containers")
	dockerLink := flag.String("docker-link", "", "Add a Docker --link from any spawned containers to another container")
	containerJoinNetwork := flag.String("container-join-network", "", "Always connect this container to new user defined bridge networks (and disconnect on delete)")
	containerJoinNetworkAlias := flag.String("container-join-network-alias", "", "Alias for network connection of specified container (Requires -container-join-network)")
	flag.Parse()

	if debug {
		socketproxy.Debug = true
	}

	if *socketUid == -1 {
		// Default to the process UID
		sockUid := os.Getuid()
		socketUid = &sockUid
	}
	if *socketGid == -1 {
		// Default to the process GID
		sockGid := os.Getgid()
		socketGid = &sockGid
	}

	useSocketMode, err := strconv.ParseUint(*socketMode, 0, 32)
	if err != nil {
		log.Fatal(err)
	}

	if *owner == "" {
		*owner = fmt.Sprintf("sockguard-pid-%d", os.Getpid())
	}

	var allowBinds []string

	if *allowBind != "" {
		allowBinds = strings.Split(*allowBind, ",")
	}

	if *cgroupParent != "" {
		debugf("Setting CgroupParent on new containers to '%s'", *cgroupParent)
	}

	// These should not be used together, one or the other
	if *dockerLink != "" && *containerJoinNetwork != "" {
		log.Fatal("Error: -docker-link and -join-network should not be used together.")
	}

	// Make sure -container-join-network-alias is only specified if -container-join-network is set
	if *containerJoinNetworkAlias != "" && *containerJoinNetwork == "" {
		log.Fatal("Error: -container-join-network-alias requires -container-join-network")
	}

	proxyHttpClient := http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				debugf("Dialing directly")
				return net.Dial("unix", *upstream)
			},
		},
	}

	if *dockerLink != "" {
		container, _, err := parseDockerLink(*dockerLink)
		if err != nil {
			log.Fatal(err)
		}
		dockerLinkContainerExists, err := sockguard.CheckContainerExists(&proxyHttpClient, container)
		if err != nil {
			log.Fatal(err.Error())
		}
		if dockerLinkContainerExists == false {
			log.Fatalf("Error: -docker-link '%s' specified but this container does not exist", container)
		}
		debugf("Adding a Docker --link to new containers: '%s'", *dockerLink)
	}

	if *containerJoinNetwork != "" {
		// TODOLATER: how much does it matter that this container is running?
		joinNetworkContainerExists, err := sockguard.CheckContainerExists(&proxyHttpClient, *containerJoinNetwork)
		if err != nil {
			log.Fatal(err.Error())
		}
		if joinNetworkContainerExists == false {
			log.Fatalf("Error: -container-join-network '%s' specified but this container does not exist", *containerJoinNetwork)
		}
		debugContainerJoinNetworkAlias := ""
		if *containerJoinNetworkAlias != "" {
			debugContainerJoinNetworkAlias = fmt.Sprintf(" (using alias '%s')", *containerJoinNetworkAlias)
		}
		debugf("Container '%s'%s will always be connected to user defined bridged networks created via sockguard", *containerJoinNetwork, debugContainerJoinNetworkAlias)
	}

	proxy := socketproxy.New(*upstream, &sockguard.RulesDirector{
		AllowBinds:                allowBinds,
		AllowHostModeNetworking:   *allowHostModeNetworking,
		ContainerCgroupParent:     *cgroupParent,
		ContainerDockerLink:       *dockerLink,
		ContainerJoinNetwork:      *containerJoinNetwork,
		ContainerJoinNetworkAlias: *containerJoinNetworkAlias,
		Owner:                     *owner,
		User:                      *user,
		Client:                    &proxyHttpClient,
	})
	listener, err := net.Listen("unix", *filename)
	if err != nil {
		log.Fatal(err)
	}

	if *socketUid >= 0 && *socketGid >= 0 {
		if err = os.Chown(*filename, *socketUid, *socketGid); err != nil {
			_ = listener.Close()
			log.Fatal(err)
		}
	}

	if err = os.Chmod(*filename, os.FileMode(useSocketMode)); err != nil {
		_ = listener.Close()
		log.Fatal(err)
	}

	fmt.Printf("Listening on %s (socket UID %d GID %d permissions %s), upstream is %s\n", *filename, *socketUid, *socketGid, *socketMode, *upstream)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, os.Kill, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		debugf("Caught signal %s: shutting down.", sig)
		_ = listener.Close()
		os.Exit(0)
	}()

	if err = http.Serve(listener, proxy); err != nil {
		log.Fatal(err)
	}
}

// extractd from director.go, to be refactored out
func parseDockerLink(input string) (string, string, error) {
	if splitInput := strings.Split(input, ":"); len(splitInput) == 1 {
		// container
		return splitInput[0], splitInput[0], nil
	} else if len(splitInput) == 2 {
		// container:alias
		return splitInput[0], splitInput[1], nil
	}
	return "", "", fmt.Errorf(
		"Unable to parse docker link %q, expected container:alias", input)
}

func debugf(format string, v ...interface{}) {
	if debug {
		fmt.Printf(format+"\n", v...)
	}
}
