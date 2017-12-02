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

	"github.com/buildkite/docker-safety-sock/socketproxy"
)

var (
	debug bool
)

func init() {
	flag.BoolVar(&debug, "debug", false, "Show debugging logging for the socket")
}

func main() {
	filename := flag.String("filename", "docker-safety.sock", "The socket to create")
	upstream := flag.String("upstream-socket", "/var/run/docker.sock", "The path to the original docker socket")
	owner := flag.String("owner", "", "The string to use as the container owner")
	flag.Parse()

	if debug {
		socketproxy.Debug = true
	}

	if *owner == "" {
		*owner = fmt.Sprintf("safetysock-%d", os.Getpid())
	}

	proxy := socketproxy.New(*upstream, &rulesDirector{
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
