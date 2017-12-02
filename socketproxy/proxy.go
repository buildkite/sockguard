package socketproxy

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"

	"github.com/kvz/logstreamer"
)

var (
	Debug bool
)

type SocketProxy struct {
	path     string
	sock     net.Conn
	counter  uint64
	director Director
}

// Director is a function that chooses between the default proxy behaviour or it's own handler
// depending on the content of an http.Request
type Director interface {
	Direct(l *log.Logger, req *http.Request, upstream http.Handler) http.Handler
}

// New returns a SocketProxy that proxies requests to the provided upstream unix socket
func New(upstream string, director Director) *SocketProxy {
	return &SocketProxy{
		path:     upstream,
		director: director,
	}
}

func (s *SocketProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	requestID := atomic.AddUint64(&s.counter, 1)
	path := req.URL.Path

	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}

	l := log.New(os.Stderr, fmt.Sprintf("#%d ", requestID), log.Ltime|log.Lmicroseconds)
	l.Printf("%s - %s - %db", req.Method, path, req.ContentLength)

	var passUpstream = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.ServeViaUpstreamSocket(l, w, req)
	})

	s.director.Direct(l, req, passUpstream).ServeHTTP(w, req)
}

func (s *SocketProxy) ServeViaUpstreamSocket(l *log.Logger, w http.ResponseWriter, req *http.Request) {
	var sockDebug = ioutil.Discard
	var connDebug = ioutil.Discard

	if Debug == true {
		sockStreamer := logstreamer.NewLogstreamer(l, "> ", false)
		sockDebug = sockStreamer
		defer sockStreamer.Close()

		connStreamer := logstreamer.NewLogstreamer(l, "< ", false)
		connDebug = connStreamer
		defer connStreamer.Close()
	}

	// Dial a new socket connection for this request. Re-use might be possible, but this gets
	// things working reliably to start with
	l.Printf("Dialing %s", s.path)
	sock, err := net.Dial("unix", s.path)
	if err != nil {
		http.Error(w, "Error contacting backend server.", 500)
		return
	}

	defer sock.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Not a Hijacker?", 500)
		return
	}

	reqConn, bufrw, err := hj.Hijack()
	if err != nil {
		l.Printf("Hijack error: %v", err)
		return
	}

	defer reqConn.Close()

	// This is really important, otherwise subsequent requests will be streamed in without
	// being passed via the director
	req.Header.Set("Connection", "close")

	// write the request to the remote side
	err = req.Write(io.MultiWriter(sock, sockDebug))
	if err != nil {
		l.Printf("Error copying request to target: %v", err)
		return
	}

	// handle anything already buffered from before the hijack
	if bufrw.Reader.Buffered() > 0 {
		l.Printf("Found %d bytes buffered in reader", bufrw.Reader.Buffered())
		rbuf, err := bufrw.Reader.Peek(bufrw.Reader.Buffered())
		if err != nil {
			panic(err)
		}

		l.Printf("Buffered: %s", rbuf)
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Copy from request to socket
	go func() {
		defer wg.Done()
		n, err := io.Copy(io.MultiWriter(sock, sockDebug), reqConn)
		if err != nil {
			l.Printf("Err: %v", err)
		}
		l.Printf("Copied %d bytes from connection", n)
	}()

	// copy from socket to request
	go func() {
		defer wg.Done()
		n, err := io.Copy(io.MultiWriter(reqConn, connDebug), sock)
		if err != nil {
			l.Printf("Err: %v", err)
		}
		l.Printf("Copied %d bytes from socket", n)
	}()

	wg.Wait()
	l.Printf("Done, closing")
}
