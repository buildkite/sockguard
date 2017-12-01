package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/buildkite/docker-safety-sock/socketproxy"
)

var (
	debug bool = true
)

func main() {
	filename := flag.String("filename", "docker-safety.sock", "The socket to create")
	upstream := flag.String("upstream-socket", "/var/run/docker.sock", "The path to the original docker socket")
	flag.Parse()

	proxy := socketproxy.New(*upstream)

	l, err := net.Listen("unix", *filename)
	if err = os.Chmod(*filename, 0600); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	debugf("Listening on " + *filename)

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, os.Kill, syscall.SIGTERM)
	go func(c chan os.Signal) {
		sig := <-c
		debugf("Caught signal %s: shutting down.", sig)
		_ = l.Close()
		os.Exit(0)
	}(sigc)

	if err != nil {
		panic(err)
	} else {
		err := http.Serve(l, proxy)
		if err != nil {
			panic(err)
		}
	}
}

func debugf(format string, v ...interface{}) {
	if debug {
		fmt.Printf(format+"\n", v...)
	}
}
