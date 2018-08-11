package socketproxy_test

import (
	"context"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"testing"

	"github.com/buildkite/sockguard/socketproxy"
)

func TestGetRequestOverSocketProxy(t *testing.T) {
	upstreamSock, close1 := startSocketServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("llamas"))
	}))
	defer close1()

	proxy := socketproxy.New(upstreamSock, socketproxy.DirectorFunc(func(l socketproxy.Logger, req *http.Request, upstream http.Handler) http.Handler {
		return upstream
	}))

	proxySock, close2 := startSocketServer(t, proxy)
	defer close2()

	client := createSocketClient(t, proxySock)

	res, err := client.Get("http://llamas/test")
	if err != nil {
		t.Fatal(err)
	}

	greeting, err := ioutil.ReadAll(res.Body)
	defer res.Body.Close()

	if err != nil {
		t.Fatal(err)
	}

	if string(greeting) != "llamas" {
		t.Fatalf("Unexpected response %q, expected %q", greeting, "llamas")
	}
}

func startSocketServer(t *testing.T, h http.Handler) (sock string, close func()) {
	server := http.Server{
		Handler: h,
	}

	sockFile, err := ioutil.TempFile("", "testsock")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(sockFile.Name()); err != nil {
		t.Fatal(err)
	}

	unixListener, err := net.Listen("unix", sockFile.Name())
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		_ = server.Serve(unixListener)
	}()

	return sockFile.Name(), func() {
		_ = unixListener.Close()
		_ = os.Remove(sockFile.Name())
	}
}

func createSocketClient(t *testing.T, sock string) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sock)
			},
		},
	}
}
