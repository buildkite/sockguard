package main

import (
	"bytes"
	"encoding/json"
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

	"github.com/google/go-cmp/cmp"
)

// Credit: http://hassansin.github.io/Unit-Testing-http-client-in-Go
type roundTripFunc func(req *http.Request) *http.Response

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req), nil
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

// Reusable mock rulesDirector instance - with "state" management of mocked upstream Docker daemon
// Just containers/networks initially
func mockRulesDirectorWithUpstreamState(us *upstreamState) *rulesDirector {
	return mockRulesDirector(func(req *http.Request) *http.Response {
		resp := http.Response{
			// Must be set to non-nil value or it panics
			Header: make(http.Header),
		}
		re1 := regexp.MustCompile("^/v(.*)/containers/(.*)/json$")
		re2 := regexp.MustCompile("^/v(.*)/images/(.*)/json$")
		re3 := regexp.MustCompile("^/v(.*)/networks/(.*)$")
		re4 := regexp.MustCompile("^/v(.*)/volumes/(.*)$")
		switch req.Method {
		// TODO: add basic POST + DELETE support, using upstream state
		case "GET":
			switch {
			case re1.MatchString(req.URL.Path):
				// inspect container - /containers/{id}/json
				parsePath := re1.FindStringSubmatch(req.URL.Path)
				if len(parsePath) == 3 {
					// Vary the response based on container ID (easiest option)
					// Partial JSON result, enough to satisfy the inspectLabels() struct
					if us.doesContainerExist(parsePath[2]) == false {
						resp.StatusCode = 404
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"No such container: %s\"}", parsePath[2])))
					} else {
						containerOwnerLabel := us.ownerLabelContent(us.getContainerOwner(parsePath[2]))
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Id\":\"%s\",\"Config\":{\"Labels\":{%s}}}", parsePath[2], containerOwnerLabel)))
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
					if us.doesImageExist(parsePath[2]) == false {
						resp.StatusCode = 404
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"no such image: %s: No such image: %s:latest\"}", parsePath[2], parsePath[2])))
					} else {
						imageOwnerLabel := us.ownerLabelContent(us.getImageOwner(parsePath[2]))
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Id\":\"%s\",\"Config\":{\"Labels\":{%s}}}", parsePath[2], imageOwnerLabel)))
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
					if us.doesNetworkExist(parsePath[2]) == false {
						resp.StatusCode = 404
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"network %s not found\"}", parsePath[2])))
					} else {
						networkOwnerLabel := us.ownerLabelContent(us.getNetworkOwner(parsePath[2]))
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Id\":\"%s\",\"Labels\":{%s}}", parsePath[2], networkOwnerLabel)))
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
					if us.doesVolumeExist(parsePath[2]) == false {
						resp.StatusCode = 404
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"get %s: no such volume\"}", parsePath[2])))
					} else {
						volumeOwnerLabel := us.ownerLabelContent(us.getVolumeOwner(parsePath[2]))
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"Name\":\"%s\",\"Labels\":{%s}}", parsePath[2], volumeOwnerLabel)))
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
			if req.Method != "GET" {
				t.Errorf("%s : Expected HTTP method GET got %s", uReqUrl, req.Method)
			}

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
		// Defaults + -docker-link sockguard + requesting default bridge network
		"containers_create_11": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:               "sockguard-pid-1",
				ContainerDockerLink: "asdf:zzzz",
			},
			esc: 200,
		},
		// Defaults + -docker-link sockguard flag + requesting a user defined bridge network
		"containers_create_12": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:               "sockguard-pid-1",
				ContainerDockerLink: "asdf:zzzz",
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
		// Defaults + -docker-link sockguard flag + requesting default bridge network + another arbitrary --link from client
		"containers_create_14": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:               "sockguard-pid-1",
				ContainerDockerLink: "cccc:dddd",
			},
			esc: 200,
		},
	}

	// Pre-populated simplified upstream state that "exists" before tests execute.
	// Used for a few tests (eg. -docker-link - fixtures _11, _12, _14)
	/*
		us := upstreamState{
			containers: map[string]upstreamStateContainer{
				"ciagentcontainer": upstreamStateContainer{
					// No ownership checking at this level (intentionally), due to chicken-and-egg situation
					// (CI container is a sibling/sidecar of sockguard itself, not a child)
					owner: "foreign",
					attachedNetworks: []string{
						"whatevernetwork",
					},
				},
			},
			networks: map[string]upstreamStateNetwork{
				"somenetwork": upstreamStateNetwork{
					owner: "sockguard-pid-1",
				},
				"whatevernetwork": upstreamStateNetwork{
					owner: "sockguard-pid-1",
				},
			},
		}
	*/

	reqUrl := "/v1.37/containers/create"
	expectedUrl := "/v1.37/containers/create"

	// TODOLATER: consolidate/DRY this with TestHandleNetworkCreate()?
	for k, v := range tests {

		expectedReqJson, err := loadFixtureFile(fmt.Sprintf("%s_expected", k))
		if err != nil {
			t.Fatal(err)
		}

		upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != "POST" {
				t.Errorf("%s : Expected HTTP method POST got %s", k, req.Method)
			}

			// log.Printf("%s %s", req.Method, req.URL.String())
			// Validate the request URL against expected.
			if req.URL.String() != expectedUrl {
				t.Errorf("%s : Expected URL %s got %s", k, expectedUrl, req.URL.String())
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

		// TODO: for _11 and _12, ensure network connect was performed

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

func TestSplitContainerDockerLink(t *testing.T) {
	goodTests := map[string]containerDockerLink{
		"38e5c22c7120":      containerDockerLink{Container: "38e5c22c7120", Alias: "38e5c22c7120"},
		"38e5c22c7120:asdf": containerDockerLink{Container: "38e5c22c7120", Alias: "asdf"},
		"somename":          containerDockerLink{Container: "somename", Alias: "somename"},
		"somename:zzzz":     containerDockerLink{Container: "somename", Alias: "zzzz"},
	}
	badTests := []string{
		"",
		"somename:zzzz:aaaa",
	}
	for k1, v1 := range goodTests {
		result1, err := splitContainerDockerLink(k1)
		if err != nil {
			t.Errorf("%s : %s", k1, err.Error())
		}
		if cmp.Equal(*result1, v1) != true {
			t.Errorf("'%s' : Expected %+v, got %+v\n", k1, v1, result1)
		}
	}
	for _, v2 := range badTests {
		_, err := splitContainerDockerLink(v2)
		if err == nil {
			t.Errorf("'%s' : Expected error, got nil", v2)
		}
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
		// Defaults + -docker-link enabled
		"networks_create_2": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:               "sockguard-pid-1",
				ContainerDockerLink: "bbbb:cccc",
			},
			esc: 200,
		},
	}

	// Pre-populated simplified upstream state that "exists" before tests execute.
	/*us := upstreamState{
		containers: map[string]upstreamStateContainer{
			"ciagentcontainer": upstreamStateContainer{
				// No ownership checking at this level (intentionally), due to chicken-and-egg situation
				// (CI container is a sibling/sidecar of sockguard itself, not a child)
				owner:            "foreign",
				attachedNetworks: []string{},
			},
		},
	}*/

	reqUrl := "/v1.37/networks/create"
	expectedUrl := "/v1.37/networks/create"

	// TODOLATER: consolidate/DRY this with TestHandleContainerCreate()?
	for k, v := range tests {
		expectedReqJson, err := loadFixtureFile(fmt.Sprintf("%s_expected", k))
		if err != nil {
			t.Fatal(err)
		}
		upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != "POST" {
				t.Errorf("%s : Expected HTTP method POST got %s", k, req.Method)
			}

			// log.Printf("%s %s", req.Method, req.URL.String())
			// Validate the request URL against expected.
			if req.URL.String() != expectedUrl {
				t.Errorf("%s : Expected URL %s got %s", k, expectedUrl, req.URL.String())
			}
			// Validate the body has been modified as expected
			body, err := ioutil.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != string(expectedReqJson) {
				t.Errorf("%s : Expected request body JSON:\n%s\nGot request body JSON:\n%s\n", k, string(expectedReqJson), string(body))
			}

			// Parse out request body JSON
			var decoded map[string]interface{}
			err = json.Unmarshal(body, &decoded)
			if err != nil {
				t.Fatal(err)
			}
			// TODO: manipulate "us" according to received request

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
		handler := v.rd.handleNetworkCreate(l, req, upstream)
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
		// TODO: verify the network was added to upstreamState (if applicable)

		// Don't bother checking the response, it's not relevant in mocked context. The request side is more important here.
	}
}

func TestHandleNetworkDelete(t *testing.T) {
	l := mockLogger()
	// Key = the network name that will be deleted (or attempted)
	tests := map[string]handleCreateTests{
		// Defaults (owner label matches, should pass)
		"somenetwork": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc: 200,
		},
		// Defaults (owner label does not match, should fail)
		"anothernetwork": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc: 401,
		},
		// Defaults + -docker-link enabled
		"whatevernetwork": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:               "sockguard-pid-1",
				ContainerDockerLink: "eeee:ffff",
			},
			esc: 200,
		},
	}

	// Pre-populated simplified upstream state that "exists" before tests execute.
	/*us := upstreamState{
		containers: map[string]upstreamStateContainer{
			"ciagentcontainer": upstreamStateContainer{
				// No ownership checking at this level (intentionally), due to chicken-and-egg situation
				// (CI container is a sibling/sidecar of sockguard itself, not a child)
				owner: "foreign",
				attachedNetworks: []string{
					"whatevernetwork",
				},
			},
		},
		networks: map[string]upstreamStateNetwork{
			"somenetwork": upstreamStateNetwork{
				owner: "sockguard-pid-1",
			},
			"anothernetwork": upstreamStateNetwork{
				owner: "adifferentowner",
			},
			"whatevernetwork": upstreamStateNetwork{
				owner: "sockguard-pid-1",
			},
		},
	}*/

	reqUrl := "/v1.37/networks/networktodelete"
	expectedUrl := "/v1.37/networks/networktodelete"

	pathIdRegex := regexp.MustCompile("^/v(.*)/networks/(.*)$")
	// TODOLATER: consolidate/DRY this with TestHandleContainerCreate()?
	for k, v := range tests {
		expectedReqJson, err := loadFixtureFile(fmt.Sprintf("%s_expected", k))
		if err != nil {
			t.Fatal(err)
		}
		upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != "DELETE" {
				t.Errorf("%s : Expected HTTP method DELETE got %s", k, req.Method)
			}

			// log.Printf("%s %s", req.Method, req.URL.String())
			// Validate the request URL against expected.
			if req.URL.String() != expectedUrl {
				t.Errorf("%s : Expected URL %s got %s", k, expectedUrl, req.URL.String())
			}
			// Validate the body has been modified as expected
			body, err := ioutil.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != string(expectedReqJson) {
				t.Errorf("%s : Expected request body JSON:\n%s\nGot request body JSON:\n%s\n", k, string(expectedReqJson), string(body))
			}

			// Parse out request URI
			if pathIdRegex.MatchString(req.URL.Path) == false {
				t.Fatalf("%s : URL path did not match expected /vx.xx/networks/{id|name}", k)
			}
			parsePath := pathIdRegex.FindStringSubmatch(req.URL.Path)
			if len(parsePath) != 3 {
				t.Fatalf("%s : URL path regex split mismatch, expected 3 got %d", k, len(parsePath))
			}
			// network id/name = parsePath[2]

			// TODO: manipulate "us" according to received request

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
		handler := v.rd.handleNetworkCreate(l, req, upstream)
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
		// TODO: verify the network was deleted from upstreamState (if applicable)

		// Don't bother checking the response, it's not relevant in mocked context. The request side is more important here.
	}
}

// TODOLATER: would it make more sense to implement a TestDirect, or TestDirect* (break it into variations by path or method)?
// Since that would also cover Direct() + CheckOwner(). Or do we do both...?
func TestCheckOwner(t *testing.T) {
	l := mockLogger()

	// Pre-populated simplified upstream state that "exists" before tests execute.
	us := upstreamState{
		containers: map[string]upstreamStateContainer{
			"idwithnolabel": upstreamStateContainer{
				// Empty owner = no label
				owner: "",
			},
			"idwithlabel1": upstreamStateContainer{
				owner: "test-owner",
			},
		},
		images: map[string]upstreamStateImage{
			"idwithnolabel": upstreamStateImage{
				// Empty owner = no label
				owner: "",
			},
			"idwithlabel1": upstreamStateImage{
				owner: "test-owner",
			},
		},
		networks: map[string]upstreamStateNetwork{
			"idwithnolabel": upstreamStateNetwork{
				// Empty owner = no label
				owner: "",
			},
			"idwithlabel1": upstreamStateNetwork{
				owner: "test-owner",
			},
		},
		volumes: map[string]upstreamStateVolume{
			"namewithnolabel": upstreamStateVolume{
				// Empty owner = no label
				owner: "",
			},
			"namewithlabel1": upstreamStateVolume{
				owner: "test-owner",
			},
		},
	}

	r := mockRulesDirectorWithUpstreamState(&us)

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
