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
	upstream := flag.String("upstream-socket", "/var/run/docker.sock", "The path to the original docker socket")
	owner := flag.String("owner-label", "", "(DEPRECATED) Use --label-value")
	labelName := flag.String("label-name", "com.buildkite.sockguard.owner", "Label name to apply/filter for Docker daemon calls, defaults to 'com.buildkite.sockguard.owner'")
	labelValue := flag.String("label-value", "", "The label value to use, defaults to 'sockguard-pid-#' (# as the process id)")
	labelValueAsCgroupParent := flag.Bool("label-value-cgroup-parent", false, "Use the CgroupParent value as the label value")
	allowBind := flag.String("allow-bind", "", "A path to allow host binds to occur under")
	allowHostModeNetworking := flag.Bool("allow-host-mode-networking", false, "Allow containers to run with --net host")
	flag.Parse()

	if debug {
		socketproxy.Debug = true
	}

	if *owner != "" {
		log.Fatal("--owner-label is deprecated, use --label-value instead")
	}
	if *labelValue != "" && *labelValueAsCgroupParent {
		log.Fatal("--label-value and --label-value-cgroup-parent cannot be used together. Pick one")
	}
	if *labelValueAsCgroupParent {
		useLabelValue, err := thisContainerCgroupParent(upstream)
		if err != nil {
			log.Fatal(err)
		}
		*labelValue = useLabelValue
	} else if *labelValue == "" {
		*labelValue = fmt.Sprintf("sockguard-pid-%d", os.Getpid())
	}

	var allowBinds []string

	if *allowBind != "" {
		allowBinds = []string{*allowBind}
	}

	proxy := socketproxy.New(*upstream, &rulesDirector{
		AllowBinds:              allowBinds,
		AllowHostModeNetworking: *allowHostModeNetworking,
		LabelName:               *labelName,
		LabelValue:              *labelValue,
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
