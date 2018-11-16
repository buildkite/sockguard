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

// Logger is a subset of log.Logger used in a Proxy request
type Logger interface {
	Printf(format string, v ...interface{})
}

// Director returns an http.Handler that either passes through to
// an upstream handler or imposes some logic of it's own on the request.
type Director interface {
	Direct(l Logger, req *http.Request, upstream http.Handler) http.Handler
}

type DirectorFunc func(l Logger, req *http.Request, upstream http.Handler) http.Handler

func (d DirectorFunc) Direct(l Logger, req *http.Request, upstream http.Handler) http.Handler {
	return d(l, req, upstream)
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

		// TODO: deal with this
		l.Printf("Buffered: %s", rbuf)
		panic("Buffered bytes not handled")
	}

	var wg sync.WaitGroup
	wg.Add(2)

	// Copy from request to socket
	go func() {
		defer wg.Done()
		n, err := io.Copy(io.MultiWriter(sock, sockDebug), reqConn)
		if err != nil {
			l.Printf("Error copying request to socket: %v", err)
		}
		l.Printf("Copied %d bytes from downstream connection", n)
	}()

	// copy from socket to request
	go func() {
		defer wg.Done()
		n, err := io.Copy(io.MultiWriter(reqConn, connDebug), sock)
		if err != nil {
			l.Printf("Error copying socket to request: %v", err)
		}
		l.Printf("Copied %d bytes from upstream socket", n)

		if err := bufrw.Flush(); err != nil {
			l.Printf("Error flushing buffer: %v", err)
		}
		if err := reqConn.Close(); err != nil {
			l.Printf("Error closing connection: %v", err)
		}
	}()

	wg.Wait()
	l.Printf("Done, closing")
}
