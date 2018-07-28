package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"

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
	cgroupParent := flag.String("cgroup-parent", "", "Set CgroupParent to an arbitrary value. Cannot be used with --inherit-cgroup-parent")
	inheritCgroupParent := flag.Bool("inherit-cgroup-parent", false, "Set CgroupParent on new containers to match the CgroupParent of the container running this process")
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
		allowBinds = []string{*allowBind}
	}

	if *cgroupParent != "" && *inheritCgroupParent == true {
		log.Fatal("--cgroup-parent and --inherit-cgroup-parent cannot be used together. Pick one")
	}
	var cgroupParentValue string
	if *inheritCgroupParent {
		cgroupParentValue, err = thisContainerCgroupParent(upstream)
		if err != nil {
			log.Fatal(err)
		}
	} else if *cgroupParent != "" {
		cgroupParentValue = *cgroupParent
	}
	if cgroupParentValue != "" {
		debugf("Setting CgroupParent on new containers to '%s'", cgroupParentValue)
	}

	proxy := socketproxy.New(*upstream, &rulesDirector{
		AllowBinds:              allowBinds,
		AllowHostModeNetworking: *allowHostModeNetworking,
		ContainerCgroupParent:   cgroupParentValue,
		Owner:  *owner,
		Client: dockerApiClient(upstream),
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

func debugf(format string, v ...interface{}) {
	if debug {
		fmt.Printf(format+"\n", v...)
	}
}
