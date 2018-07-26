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
	"syscall"

	"github.com/CpuID/sockguard/socketproxy"
)

var (
	debug bool
)

func init() {
	flag.BoolVar(&debug, "debug", false, "Show debugging logging for the socket")
}

func main() {
	filename := flag.String("filename", "sockguard.sock", "The guarded socket to create")
	upstream := flag.String("upstream-socket", "/var/run/docker.sock", "The path to the original docker socket")
	owner := flag.String("owner-label", "", "The value to use as the owner of the socket, defaults to the process id")
	allowBind := flag.String("allow-bind", "", "A path to allow host binds to occur under")
	allowHostModeNetworking := flag.Bool("allow-host-mode-networking", false, "Allow containers to run with --net host")
	setCgroupParent := flag.String("set-cgroup-parent", "", "Set CgroupParent. Arbitrary string or 'this-container' to match the CgroupParent of the container running this process")
	flag.Parse()

	if debug {
		socketproxy.Debug = true
	}

	if *owner == "" {
		*owner = fmt.Sprintf("sockguard-pid-%d", os.Getpid())
	}

	var allowBinds []string

	if *allowBind != "" {
		allowBinds = []string{*allowBind}
	}

	var cgroupParentValue string
	var err error
	// 2 options:
	// - this-container = detect the CgroupParent of the container running this process
	// - custom string = passthrough arbitrary value for CgroupParent
	if *setCgroupParent == "this-container" {
		cgroupParentValue, err = thisContainerCgroupParent(upstream)
		if err != nil {
			log.Fatal(err)
		}
	} else if *setCgroupParent != "" {
		cgroupParentValue = *setCgroupParent
	}

	proxy := socketproxy.New(*upstream, &rulesDirector{
		AllowBinds:              allowBinds,
		AllowHostModeNetworking: *allowHostModeNetworking,
		ContainerCgroupParent:   cgroupParentValue,
		Owner: *owner,
		Client: &http.Client{
			Transport: &http.Transport{
				DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
					debugf("Dialing directly")
					return net.Dial("unix", *upstream)
				},
			},
		},
	})

	listener, err := net.Listen("unix", *filename)
	if err != nil {
		log.Fatal(err)
	}

	if err = os.Chmod(*filename, 0600); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Listening on %s, upstream is %s\n", *filename, *upstream)

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
