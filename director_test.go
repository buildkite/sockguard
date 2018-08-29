package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Credit: http://hassansin.github.io/Unit-Testing-http-client-in-Go
type roundTripFunc func(req *http.Request) *http.Response

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
}

// Mock upstream Docker daemon, to test features that require
// upstream state to validate.
func mockUpstreamDocker() *httptest.Server {
	re1 := regexp.MustCompile("^/containers/(.*)/json$")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			switch {
			case re1.MatchString(r.URL.Path):
				// inspect container - /containers/{id}/json
				parsePath := re1.FindStringSubmatch(r.URL.Path)
				if len(parsePath) != 2 {
					http.Error(w, fmt.Sprintf("Failure parsing container ID from path - %s\n", r.URL.Path), 501)
				}
				containerId := parsePath[1]
				// Vary the response based on container ID (easiest option)
				// Partial JSON result, enough to satisfy the inspectLabels() struct
				switch containerId {
				case "idwithnolabel":
					w.Write([]byte(fmt.Sprintf("{\"Id\":\"%s\",\"Config\":{\"Labels\":{}}}", containerId)))
				case "idwithlabel1":
					w.Write([]byte(fmt.Sprintf("{\"Id\":\"%s\",\"Config\":{\"Labels\":{\"com.buildkite.sockguard.owner\":\"sockguard-pid-1\"}}}", containerId)))
				default:
					w.Write([]byte(fmt.Sprintf("{\"message\":\"No such container: %s\"}", containerId)))
				}
			default:
				http.Error(w, fmt.Sprintf("Unhandled GET path - %s\n", r.URL.Path), 501)
			}
		default:
			http.Error(w, fmt.Sprintf("Unhandled method - %s to %s\n", r.Method, r.URL.Path), 501)
		}
	}))
}

// Reusable mock rulesDirector instance
func mockRulesDirector(tfn roundTripFunc) *rulesDirector {
	return &rulesDirector{
		Client: &http.Client{
			Transport: roundTripFunc(tfn),
		},
		Owner: "test-owner",
		AllowHostModeNetworking: false,
	}
}

// Reusable mock log.Logger instance
func mockLogger() *log.Logger {
	return log.New(os.Stderr, "MOCK: ", log.Ltime|log.Lmicroseconds)
}

func TestAddLabelsToQueryStringFilters(t *testing.T) {
	l := mockLogger()
	r := mockRulesDirector(func(req *http.Request) *http.Response { return &http.Response{} })

	// key = client side URL (inc query params)
	// value = expected request URL on upstream side (inc query params)
	// TODOLATER: would it be more elegant to write these as URL decoded for readability? will need to change the map[string]string to still send the full docker-compose ps URLs
	tests := map[string]string{
		// docker ps - without any filters
		"/v1.32/containers/json": "/v1.32/containers/json?filters=%7B%22label%22%3A%5B%22com.buildkite.sockguard.owner%3Dtest-owner%22%5D%7D",
		// docker ps - with a key=value: true filter
		"/v1.32/containers/json?filters=%7B%22label%22%3A%7B%22test%3Dblah%22%3Atrue%7D%7D": "/v1.32/containers/json?filters=%7B%22label%22%3A%5B%22test%3Dblah%22%2C%22com.buildkite.sockguard.owner%3Dtest-owner%22%5D%7D",
		// docker-compose ps - first list API call
		"/v1.32/containers/json?limit=-1&all=1&size=0&trunc_cmd=0&filters=%7B%22label%22%3A+%5B%22com.docker.compose.project%3Dblah%22%2C+%22com.docker.compose.oneoff%3DFalse%22%5D%7D": "/v1.32/containers/json?all=1&filters=%7B%22label%22%3A%5B%22com.docker.compose.project%3Dblah%22%2C%22com.docker.compose.oneoff%3DFalse%22%2C%22com.buildkite.sockguard.owner%3Dtest-owner%22%5D%7D&limit=-1&size=0&trunc_cmd=0",
		// docker-compose ps - second list API call
		"/v1.32/containers/json?limit=-1&all=0&size=0&trunc_cmd=0&filters=%7B%22label%22%3A+%5B%22com.docker.compose.project%3Dblah%22%2C+%22com.docker.compose.oneoff%3DTrue%22%5D%7D": "/v1.32/containers/json?all=0&filters=%7B%22label%22%3A%5B%22com.docker.compose.project%3Dblah%22%2C%22com.docker.compose.oneoff%3DTrue%22%2C%22com.buildkite.sockguard.owner%3Dtest-owner%22%5D%7D&limit=-1&size=0&trunc_cmd=0",
	}

	for cReqUrl, uReqUrl := range tests {
		upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// log.Printf("%s %s", req.Method, req.URL.String())
			// Validate the request URL against expected.
			if req.URL.String() != uReqUrl {
				decodeUReqUrl, err1 := url.QueryUnescape(uReqUrl)
				decodeInReqUrl, err2 := url.QueryUnescape(req.URL.String())
				if err1 == nil && err2 == nil {
					t.Errorf("Expected:\n%s\ngot:\n%s\n\n(URL decoded) Expected:\n%s\ngot:\n%s\n", uReqUrl, req.URL.String(), decodeUReqUrl, decodeInReqUrl)
				} else {
					t.Errorf("Expected:\n%s\ngot:\n%s\n\n(errors trying to URL decode)\n", uReqUrl, req.URL.String())
				}
			}

			// Return empty JSON, the request is whats important not the response
			fmt.Fprintf(w, `{}`)
		})

		// Credit: https://blog.questionable.services/article/testing-http-handlers-go/
		// Create a request to pass to our handler
		req, err := http.NewRequest("GET", cReqUrl, nil)
		if err != nil {
			t.Fatal(err)
		}
		// We create a ResponseRecorder (which satisfies http.ResponseWriter) to record the response.
		rr := httptest.NewRecorder()
		handler := r.addLabelsToQueryStringFilters(l, req, upstream)

		// Our handlers satisfy http.Handler, so we can call their ServeHTTP method
		// directly and pass in our Request and ResponseRecorder.
		handler.ServeHTTP(rr, req)

		// Check the status code is what we expect.
		if status := rr.Code; status != http.StatusOK {
			t.Errorf("%s : handler returned wrong status code: got %v want %v", cReqUrl, status, http.StatusOK)
		}

		// Don't bother checking the response, it's not relevant in mocked context. The request side is more important here.
	}
}

func loadFixtureFile(filename_part string) (string, error) {
	data, err := ioutil.ReadFile(fmt.Sprintf("./fixtures/%s.json", filename_part))
	if err != nil {
		return "", err
	}
	// Remove any whitespace/newlines from the start/end of the file
	return strings.TrimSpace(string(data)), nil
}

// Used for handleContainerCreate(), handleNetworkCreate(), and friends
type handleCreateTests struct {
	rd *rulesDirector
	// Expected StatusCode
	esc int
}

func TestHandleContainerCreate(t *testing.T) {
	l := mockLogger()
	// For each of the tests below, there will be 2 files in the fixtures/ dir:
	// - <key>_in.json - the client request sent to the director
	// - <key>_expected.json - the expected request sent to the upstream
	tests := map[string]handleCreateTests{
		// Defaults
		"containers_create_1": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc: 200,
		},
		// Defaults + custom Owner
		"containers_create_2": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				Owner:  "test-owner",
			},
			esc: 200,
		},
		// Defaults with Binds disabled, and a bind sent (should fail)
		"containers_create_3": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:      "sockguard-pid-1",
				AllowBinds: []string{},
			},
			esc: 401,
		},
		// Defaults + Binds enabled + a matching bind (should pass)
		"containers_create_4": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:      "sockguard-pid-1",
				AllowBinds: []string{"/tmp"},
			},
			esc: 200,
		},
		// Defaults + Binds enabled + a non-matching bind (should fail)
		"containers_create_5": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:      "sockguard-pid-1",
				AllowBinds: []string{"/tmp"},
			},
			esc: 401,
		},
		// Defaults + Host Mode Networking + request with NetworkMode=host (should pass)
		"containers_create_6": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
				AllowHostModeNetworking: true,
			},
			esc: 200,
		},
		// Defaults + Host Mode Networking disabled + request with NetworkMode=host (should fail)
		"containers_create_7": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
				AllowHostModeNetworking: false,
			},
			esc: 401,
		},
		// Defaults + Cgroup Parent
		"containers_create_8": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
				ContainerCgroupParent: "some-cgroup",
			},
			esc: 200,
		},
		// Defaults + Force User
		"containers_create_9": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
				User:  "someuser",
			},
			esc: 200,
		},
		// Defaults + a custom label on request
		"containers_create_10": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc: 200,
		},
		// Defaults + try set a CgroupParent (should fail, only permitted if sockguard started with -cgroup-parent)
		"containers_create_13": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc: 401,
		},
	}
	reqUrl := "/v1.37/containers/create"
	expectedUrl := "/v1.37/containers/create"
	// TODOLATER: consolidate/DRY this with TestHandleNetworkCreate()?
	for k, v := range tests {
		expectedReqJson, err := loadFixtureFile(fmt.Sprintf("%s_expected", k))
		if err != nil {
			t.Fatal(err)
		}
		upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// log.Printf("%s %s", req.Method, req.URL.String())
			// Validate the request URL against expected.
			if req.URL.String() != expectedUrl {
				t.Error("Expected URL", expectedUrl, "got", req.URL.String())
			}
			// Validate the body has been modified as expected
			body, err := ioutil.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != string(expectedReqJson) {
				t.Errorf("%s : Expected request body JSON:\n%s\nGot request body JSON:\n%s\n", k, string(expectedReqJson), string(body))
			}
			// Return empty JSON, the request is whats important not the response
			fmt.Fprintf(w, `{}`)
		})
		// Credit: https://blog.questionable.services/article/testing-http-handlers-go/
		// Create a request to pass to our handler
		containerCreateJson, err := loadFixtureFile(fmt.Sprintf("%s_in", k))
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest("POST", reqUrl, strings.NewReader(containerCreateJson))
		if err != nil {
			t.Fatal(err)
		}
		// We create a ResponseRecorder (which satisfies http.ResponseWriter) to record the response.
		rr := httptest.NewRecorder()
		handler := v.rd.handleContainerCreate(l, req, upstream)
		// Our handlers satisfy http.Handler, so we can call their ServeHTTP method
		// directly and pass in our Request and ResponseRecorder.
		handler.ServeHTTP(rr, req)
		// Check the status code is what we expect.
		//fmt.Printf("%s : SC %d ESC %d\n", k, rr.Code, v.esc)
		if status := rr.Code; status != v.esc {
			// Get the body out of the response to return with the error
			respBody, err := ioutil.ReadAll(rr.Body)
			if err == nil {
				t.Errorf("%s : handler returned wrong status code: got %v want %v. Response body: %s", k, status, v.esc, string(respBody))
			} else {
				t.Errorf("%s : handler returned wrong status code: got %v want %v. Error reading response body: %s", k, status, v.esc, err.Error())
			}
		}
		// Don't bother checking the response, it's not relevant in mocked context. The request side is more important here.
	}
}

func TestHandleNetworkCreate(t *testing.T) {
	l := mockLogger()
	// For each of the tests below, there will be 2 files in the fixtures/ dir:
	// - <key>_in.json - the client request sent to the director
	// - <key>_expected.json - the expected request sent to the upstream
	tests := map[string]handleCreateTests{
		// Defaults
		"networks_create_1": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc: 200,
		},
	}
	reqUrl := "/v1.37/networks/create"
	expectedUrl := "/v1.37/networks/create"
	// TODOLATER: consolidate/DRY this with TestHandleContainerCreate()?
	for k, v := range tests {
		expectedReqJson, err := loadFixtureFile(fmt.Sprintf("%s_expected", k))
		if err != nil {
			t.Fatal(err)
		}
		upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// log.Printf("%s %s", req.Method, req.URL.String())
			// Validate the request URL against expected.
			if req.URL.String() != expectedUrl {
				t.Error("Expected URL", expectedUrl, "got", req.URL.String())
			}
			// Validate the body has been modified as expected
			body, err := ioutil.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != string(expectedReqJson) {
				t.Errorf("%s : Expected request body JSON:\n%s\nGot request body JSON:\n%s\n", k, string(expectedReqJson), string(body))
			}
			// Return empty JSON, the request is whats important not the response
			fmt.Fprintf(w, `{}`)
		})
		// Credit: https://blog.questionable.services/article/testing-http-handlers-go/
		// Create a request to pass to our handler
		containerCreateJson, err := loadFixtureFile(fmt.Sprintf("%s_in", k))
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest("POST", reqUrl, strings.NewReader(containerCreateJson))
		if err != nil {
			t.Fatal(err)
		}
		// We create a ResponseRecorder (which satisfies http.ResponseWriter) to record the response.
		rr := httptest.NewRecorder()
		// TODOLATER: in Direct(), call a handleNetworkCreate() instead?
		handler := v.rd.addLabelsToBody(l, req, upstream)
		// Our handlers satisfy http.Handler, so we can call their ServeHTTP method
		// directly and pass in our Request and ResponseRecorder.
		handler.ServeHTTP(rr, req)
		// Check the status code is what we expect.
		//fmt.Printf("%s : SC %d ESC %d\n", k, rr.Code, v.esc)
		if status := rr.Code; status != v.esc {
			// Get the body out of the response to return with the error
			respBody, err := ioutil.ReadAll(rr.Body)
			if err == nil {
				t.Errorf("%s : handler returned wrong status code: got %v want %v. Response body: %s", k, status, v.esc, string(respBody))
			} else {
				t.Errorf("%s : handler returned wrong status code: got %v want %v. Error reading response body: %s", k, status, v.esc, err.Error())
			}
		}
		// Don't bother checking the response, it's not relevant in mocked context. The request side is more important here.
	}
}

// TODOLATER: would it make more sense to implement a TestDirect, or TestDirect* (break it into variations by path or method)?
// Since that would also cover Direct() + CheckOwner(). Or do we do both...?
func TestCheckOwner(t *testing.T) {
	l := mockLogger()
	r := mockRulesDirector(func(req *http.Request) *http.Response {
		resp := http.Response{
			// Must be set to non-nil value or it panics
			Header: make(http.Header),
		}
		re1 := regexp.MustCompile("^/v(.*)/containers/(.*)/json$")
		re2 := regexp.MustCompile("^/v(.*)/images/(.*)/json$")
		re3 := regexp.MustCompile("^/v(.*)/networks/(.*)$")
		re4 := regexp.MustCompile("^/v(.*)/volumes/(.*)$")
		switch req.Method {
		case "GET":
			switch {
			case re1.MatchString(req.URL.Path):
				// inspect container - /containers/{id}/json
				parsePath := re1.FindStringSubmatch(req.URL.Path)
				if len(parsePath) == 3 {
					// Vary the response based on container ID (easiest option)
					// Partial JSON result, enough to satisfy the inspectLabels() struct
					switch parsePath[2] {
					case "idwithnolabel":
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Id\":\"%s\",\"Config\":{\"Labels\":{}}}", parsePath[2])))
					case "idwithlabel1":
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Id\":\"%s\",\"Config\":{\"Labels\":{\"com.buildkite.sockguard.owner\":\"test-owner\"}}}", parsePath[2])))
					default:
						resp.StatusCode = 404
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"No such container: %s\"}", parsePath[2])))
					}
				} else {
					resp.StatusCode = 501
					resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Failure parsing container ID from path - %s\n", req.URL.Path)))
				}
			case re2.MatchString(req.URL.Path):
				// inspect image - /images/{id}/json
				parsePath := re2.FindStringSubmatch(req.URL.Path)
				if len(parsePath) == 3 {
					// Vary the response based on image ID (easiest option)
					// Partial JSON result, enough to satisfy the inspectLabels() struct
					switch parsePath[2] {
					case "idwithnolabel":
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Id\":\"%s\",\"Config\":{\"Labels\":{}}}", parsePath[2])))
					case "idwithlabel1":
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Id\":\"%s\",\"Config\":{\"Labels\":{\"com.buildkite.sockguard.owner\":\"test-owner\"}}}", parsePath[2])))
					default:
						resp.StatusCode = 404
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"no such image: %s: No such image: %s:latest\"}", parsePath[2], parsePath[2])))
					}
				} else {
					resp.StatusCode = 501
					resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Failure parsing image ID from path - %s\n", req.URL.Path)))
				}
			case re3.MatchString(req.URL.Path):
				// inspect network - /networks/{id}
				parsePath := re3.FindStringSubmatch(req.URL.Path)
				if len(parsePath) == 3 {
					// Vary the response based on network ID (easiest option)
					// Partial JSON result, enough to satisfy the inspectLabels() struct
					switch parsePath[2] {
					case "idwithnolabel":
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Id\":\"%s\",\"Labels\":{}}", parsePath[2])))
					case "idwithlabel1":
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Id\":\"%s\",\"Labels\":{\"com.buildkite.sockguard.owner\":\"test-owner\"}}", parsePath[2])))
					default:
						resp.StatusCode = 404
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"network %s not found\"}", parsePath[2])))
					}
				} else {
					resp.StatusCode = 501
					resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Failure parsing network ID from path - %s\n", req.URL.Path)))
				}
			case re4.MatchString(req.URL.Path):
				// inspect volume - /volume/{name}
				parsePath := re4.FindStringSubmatch(req.URL.Path)
				if len(parsePath) == 3 {
					// Vary the response based on volume name (easiest option)
					// Partial JSON result, enough to satisfy the inspectLabels() struct
					switch parsePath[2] {
					case "namewithnolabel":
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Name\":\"%s\",\"Labels\":{}}", parsePath[2])))
					case "namewithlabel1":
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Name\":\"%s\",\"Labels\":{\"com.buildkite.sockguard.owner\":\"test-owner\"}}", parsePath[2])))
					default:
						resp.StatusCode = 404
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"get %s: no such volume\"}", parsePath[2])))
					}
				} else {
					resp.StatusCode = 501
					resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Failure parsing volume name from path - %s\n", req.URL.Path)))
				}
			default:
				resp.StatusCode = 501
				resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Unhandled GET path - %s\n", req.URL.Path)))
			}
		default:
			resp.StatusCode = 501
			resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Unhandled method - %s to %s\n", req.Method, req.URL.Path)))
		}
		return &resp
	})

	tests := map[string]struct {
		Type      string
		ExpResult bool
	}{
		// A container that will match
		"/v1.37/containers/idwithlabel1/logs": {"containers", true},
		// A container that won't match
		"/v1.37/containers/idwithnolabel/logs": {"containers", false},
		// An image that will match
		"/v1.37/images/idwithlabel1/json": {"images", true},
		// An image that won't match
		"/v1.37/images/idwithnolabel/json": {"images", false},
		// A network that will match
		"/v1.37/networks/idwithlabel1": {"networks", true},
		// A network that won't match
		"/v1.37/networks/idwithnolabel": {"networks", false},
		// A volume that will match
		"/v1.37/volumes/namewithlabel1": {"volumes", true},
		// A volume that won't match
		"/v1.37/volumes/namewithnolabel": {"volumes", false},
	}

	for k, v := range tests {
		kReq, err := http.NewRequest("GET", k, nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := r.checkOwner(l, v.Type, false, kReq)
		if err != nil {
			t.Errorf("%s : Error - %s", kReq.URL.String(), err.Error())
		}
		if v.ExpResult != result {
			t.Errorf("%s : Expected %t, got %t", kReq.URL.String(), v.ExpResult, result)
		}
	}
}

type handleBuildTest struct {
	rd *rulesDirector
	// Expected StatusCode
	esc int

	// These are short enough, store inline rather than in fixtures files
	inQueryString       string
	expectedQueryString string
}

func TestHandleBuild(t *testing.T) {
	l := mockLogger()
	tests := []handleBuildTest{
		// Defaults
		handleBuildTest{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc:                 200,
			inQueryString:       `buildargs={}&cachefrom=[]&cgroupparent=&cpuperiod=0&cpuquota=0&cpusetcpus=&cpusetmems=&cpushares=0&dockerfile=Dockerfile&labels={}&memory=0&memswap=0&networkmode=default&rm=1&shmsize=0&target=&ulimits=null&version=1`,
			expectedQueryString: `buildargs={}&cachefrom=[]&cgroupparent=&cpuperiod=0&cpuquota=0&cpusetcpus=&cpusetmems=&cpushares=0&dockerfile=Dockerfile&labels={"com.buildkite.sockguard.owner":"sockguard-pid-1"}&memory=0&memswap=0&networkmode=default&rm=1&shmsize=0&target=&ulimits=null&version=1`,
		},
		// Defaults + custom label
		handleBuildTest{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc:                 200,
			inQueryString:       `buildargs={}&cachefrom=[]&cgroupparent=&cpuperiod=0&cpuquota=0&cpusetcpus=&cpusetmems=&cpushares=0&dockerfile=Dockerfile&labels={"somelabel":"somevalue"}&memory=0&memswap=0&networkmode=default&rm=1&shmsize=0&target=&ulimits=null&version=1`,
			expectedQueryString: `buildargs={}&cachefrom=[]&cgroupparent=&cpuperiod=0&cpuquota=0&cpusetcpus=&cpusetmems=&cpushares=0&dockerfile=Dockerfile&labels={"com.buildkite.sockguard.owner":"sockguard-pid-1","somelabel":"somevalue"}&memory=0&memswap=0&networkmode=default&rm=1&shmsize=0&target=&ulimits=null&version=1`,
		},
		// Defaults + CgroupParent in config (should pass)
		handleBuildTest{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
				ContainerCgroupParent: "somecgroup",
			},
			esc:                 200,
			inQueryString:       `buildargs={}&cachefrom=[]&cgroupparent=&cpuperiod=0&cpuquota=0&cpusetcpus=&cpusetmems=&cpushares=0&dockerfile=Dockerfile&labels={}&memory=0&memswap=0&networkmode=default&rm=1&shmsize=0&target=&ulimits=null&version=1`,
			expectedQueryString: `buildargs={}&cachefrom=[]&cgroupparent=somecgroup&cpuperiod=0&cpuquota=0&cpusetcpus=&cpusetmems=&cpushares=0&dockerfile=Dockerfile&labels={"com.buildkite.sockguard.owner":"sockguard-pid-1"}&memory=0&memswap=0&networkmode=default&rm=1&shmsize=0&target=&ulimits=null&version=1`,
		},
		// Defaults + CgroupParent in API request (should fail)
		handleBuildTest{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc:                 401,
			inQueryString:       `buildargs={}&cachefrom=[]&cgroupparent=anothercgroup&cpuperiod=0&cpuquota=0&cpusetcpus=&cpusetmems=&cpushares=0&dockerfile=Dockerfile&labels={}&memory=0&memswap=0&networkmode=default&rm=1&shmsize=0&target=&ulimits=null&version=1`,
			expectedQueryString: `<should fail and never get here>`,
		},
	}
	reqUrlPath := "/v1.37/build"
	expectedUrlPath := "/v1.37/build"
	for _, v := range tests {
		upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// log.Printf("%s %s", req.Method, req.URL.Path)
			// Validate the request URL path against expected.
			if req.URL.Path != expectedUrlPath {
				t.Error("Expected URL path", expectedUrlPath, "got", req.URL.Path)
			}

			// Validate the query string matches expected
			unescapeQueryString, err := url.QueryUnescape(req.URL.RawQuery)
			if err != nil {
				t.Fatal(err)
			}
			if unescapeQueryString != v.expectedQueryString {
				t.Errorf("Expected URL query string:\n%s\nGot:\n%s\n\n", v.expectedQueryString, unescapeQueryString)
			}

			// We don't validate the request body here, as it is a build context tar (which isn't modified), not relevant

			// Return empty JSON, the request is whats important not the response
			fmt.Fprintf(w, `{}`)
		})
		// Credit: https://blog.questionable.services/article/testing-http-handlers-go/
		// Create a request to pass to our handler, using an empty request body for now (not relevant)
		r, err := http.NewRequest("POST", fmt.Sprintf("%s?%s", reqUrlPath, v.inQueryString), nil)
		if err != nil {
			t.Fatal(err)
		}
		// We create a ResponseRecorder (which satisfies http.ResponseWriter) to record the response.
		rr := httptest.NewRecorder()
		handler := v.rd.handleBuild(l, r, upstream)
		// Our handlers satisfy http.Handler, so we can call their ServeHTTP method
		// directly and pass in our Request and ResponseRecorder.
		handler.ServeHTTP(rr, r)
		// Check the status code is what we expect.
		//fmt.Printf("%s : SC %d ESC %d\n", k, rr.Code, v.esc)
		if status := rr.Code; status != v.esc {
			// Get the body out of the response to return with the error
			respBody, err := ioutil.ReadAll(rr.Body)
			if err == nil {
				t.Errorf("%s : handler returned wrong status code: got %v want %v. Response body: %s", v.inQueryString, status, v.esc, string(respBody))
			} else {
				t.Errorf("%s : handler returned wrong status code: got %v want %v. Error reading response body: %s", v.inQueryString, status, v.esc, err.Error())
			}
		}
		// Don't bother checking the response, it's not relevant in mocked context. The request side is more important here.
	}
}
