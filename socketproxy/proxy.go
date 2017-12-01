package socketproxy

import (
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
)

type SocketProxy struct {
	path    string
	sock    net.Conn
	counter uint64
}

func New(path string) *SocketProxy {
	return &SocketProxy{
		path: path,
	}
}

func (s *SocketProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	log.Printf("Dialing %s", s.path)
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
		log.Printf("Hijack error: %v", err)
		return
	}

	defer reqConn.Close()

	if bufrw.Reader.Buffered() > 0 {
		panic("buffered")
	}

	requestID := atomic.AddUint64(&s.counter, 1)
	path := req.URL.Path

	if req.URL.RawQuery != "" {
		path += "?" + req.URL.RawQuery
	}

	log.Printf("[#%d] %-5s - %s %d",
		requestID,
		req.Method,
		path,
		req.ContentLength,
	)

	// sockPrefixer := prefixer.New(sock, "sock> ")
	// connPrefixer := prefixer.New(reqCon, "sock> ")

	log.Printf("Writing request to sock")
	err = req.Write(io.MultiWriter(sock, os.Stderr))
	if err != nil {
		log.Printf("Error copying request to target: %v", err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		log.Printf("Copying from client to socket")
		n, err := io.Copy(io.MultiWriter(sock, os.Stderr), reqConn)
		if err != nil {
			log.Printf("Err: %v", err)
		}
		log.Printf("Copied %d bytes from client", n)
	}()

	go func() {
		defer wg.Done()
		log.Printf("Copying from socket to client")
		n, err := io.Copy(io.MultiWriter(reqConn, os.Stderr), sock)
		if err != nil {
			log.Printf("Err: %v", err)
		}
		log.Printf("Copied %d bytes from socket", n)
	}()

	wg.Wait()
	log.Printf("Done serving")
}
