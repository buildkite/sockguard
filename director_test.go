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
func mockRulesDirector() *rulesDirector {
	return &rulesDirector{
		Client:                  &http.Client{},
		Owner:                   "test-owner",
		AllowHostModeNetworking: false,
	}
}

// Reusable mock rulesDirector instance - with "state" management of mocked upstream Docker daemon
// Just containers/networks initially
func mockRulesDirectorWithUpstreamState(us *upstreamState) *rulesDirector {
	rd := mockRulesDirector()
	rd.Client = mockRulesDirectorHttpClientWithUpstreamState(us)
	return rd
}

func mockRulesDirectorHttpClientWithUpstreamState(us *upstreamState) *http.Client {
	return &http.Client{
		Transport: roundTripFunc(func(req *http.Request) *http.Response {
			resp := http.Response{
				// Must be set to non-nil value or it panics
				Header: make(http.Header),
			}
			re1 := regexp.MustCompile("^/v(.*)/containers/(.*)/json$")
			// TODOLATER: adjust re2 to make /json suffix optional, for non-GET?
			re2 := regexp.MustCompile("^/v(.*)/images/(.*)/json$")
			// NOTE: this regex may not cover all name variations, but will cover enough to fulfil tests
			re3 := regexp.MustCompile("^/v(.*)/networks/([A-Za-z0-9]+)(/connect|/disconnect)?$")
			re4 := regexp.MustCompile("^/v(.*)/volumes/(.*)$")
			switch {
			case re1.MatchString(req.URL.Path):
				if req.Method == "GET" {
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
				} else {
					resp.StatusCode = 501
					resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Unsupported HTTP method %s for %s\n", req.Method, req.URL.Path)))
				}
			case re2.MatchString(req.URL.Path):
				switch req.Method {
				case "GET":
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
				default:
					resp.StatusCode = 501
					resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Unsupported HTTP method %s for %s\n", req.Method, req.URL.Path)))
				}
			case re3.MatchString(req.URL.Path):
				parsePath := re3.FindStringSubmatch(req.URL.Path)
				if len(parsePath) != 4 {
					resp.StatusCode = 501
					resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Failure parsing network ID/target from path - %s\n", req.URL.Path)))
					return &resp
				}
				switch req.Method {
				case "GET":
					// inspect network - /networks/{id}
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
				case "POST":
					switch parsePath[3] {
					case "/connect", "/disconnect":
						// connect container to network - /networks/{id}/connect
						// disconnect container to network - /networks/{id}/disconnect
						// Verify the Content-Type = application/json, will 400 without it on Docker daemon
						contentType := req.Header.Get("Content-Type")
						if contentType != "application/json" {
							resp.StatusCode = 400
							resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"Content-Type specified (%s) must be 'application/json'\"}", contentType)))
							return &resp
						}
						// Parse out the Container from request body
						var decoded map[string]interface{}
						if err := json.NewDecoder(req.Body).Decode(&decoded); err != nil {
							resp.StatusCode = 500
							resp.Body = ioutil.NopCloser(bytes.NewBufferString(err.Error()))
							return &resp
						}
						useContainer := decoded["Container"].(string)
						// Bare minimum response format here, mostly response code
						if us.doesNetworkExist(parsePath[2]) == false {
							resp.StatusCode = 404
							resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"network %s not found\"}", parsePath[2])))
						} else {
							var err error
							if parsePath[3] == "/connect" {
								useContainerAliases := []string{}
								// If there are Aliases specified, pass them in.
								parseContainerEndpointConfig, ok := decoded["EndpointConfig"]
								if ok {
									parseContainerAliases, ok2 := parseContainerEndpointConfig.(map[string]interface{})["Aliases"].([]interface{})
									if ok2 {
										for _, parseContainerAlias := range parseContainerAliases {
											parsedContainerAlias := parseContainerAlias.(string)
											if parsedContainerAlias != "" {
												useContainerAliases = append(useContainerAliases, parsedContainerAlias)
											}
										}
									}
								}
								err = us.connectContainerToNetwork(useContainer, parsePath[2], useContainerAliases)
							} else if parsePath[3] == "/disconnect" {
								err = us.disconnectContainerToNetwork(useContainer, parsePath[2])
							}
							if err != nil {
								resp.StatusCode = 500
								resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"error %sing container '%s' to/from network '%s': %s\"}", parsePath[3], useContainer, parsePath[2], err.Error())))
								return &resp
							}
							resp.StatusCode = 200
							resp.Body = ioutil.NopCloser(bytes.NewBufferString("OK"))
						}
					default:
						// unknown
						resp.StatusCode = 501
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("POST not supported for %s\n", req.URL.Path)))
					}
				case "DELETE":
					// delete network - /networks/{id}
					// Bare minimum response format here, mostly response code
					if us.doesNetworkExist(parsePath[2]) == false {
						resp.StatusCode = 404
						resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("{\"message\":\"network %s not found\"}", parsePath[2])))
					} else {
						us.deleteNetwork(parsePath[2])
						resp.StatusCode = 200
						resp.Body = ioutil.NopCloser(bytes.NewBufferString("OK"))
					}
				default:
					resp.StatusCode = 501
					resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Unsupported HTTP method %s for %s\n", req.Method, req.URL.Path)))
				}
			case re4.MatchString(req.URL.Path):
				switch req.Method {
				case "GET":
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
					resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Unsupported HTTP method %s for %s\n", req.Method, req.URL.Path)))
				}
			default:
				resp.StatusCode = 501
				resp.Body = ioutil.NopCloser(bytes.NewBufferString(fmt.Sprintf("Path %s not implemented\n", req.URL.Path)))
			}
			return &resp
		}),
	}
}

// Reusable mock log.Logger instance
func mockLogger() *log.Logger {
	return log.New(os.Stderr, "MOCK: ", log.Ltime|log.Lmicroseconds)
}

func TestAddLabelsToQueryStringFilters(t *testing.T) {
	l := mockLogger()
	r := mockRulesDirector()

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
				Owner:                   "sockguard-pid-1",
				AllowHostModeNetworking: true,
			},
			esc: 200,
		},
		// Defaults + Host Mode Networking disabled + request with NetworkMode=host (should fail)
		"containers_create_7": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:                   "sockguard-pid-1",
				AllowHostModeNetworking: false,
			},
			esc: 401,
		},
		// Defaults + Cgroup Parent
		"containers_create_8": handleCreateTests{
			rd: &rulesDirector{
				Client: &http.Client{},
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:                 "sockguard-pid-1",
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

			// TODOLATER: append to "us" (upstream state) the new container, and any connected networks? we only check the ciagentcontainer
			// when verifying state further down right now, which is the key consideration.

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

		// State of ciagentcontainer network attachments is not relevant for a general container creation call,
		// only matters for network create/delete.

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

	// Pre-populated simplified upstream state that "exists" before tests execute.
	us := upstreamState{
		containers: map[string]upstreamStateContainer{
			"ciagentcontainer": upstreamStateContainer{
				// No ownership checking at this level (intentionally), due to chicken-and-egg situation
				// (CI container is a sibling/sidecar of sockguard itself, not a child)
				owner:            "foreign",
				attachedNetworks: []upstreamStateContainerAttachedNetwork{},
			},
		},
		networks: map[string]upstreamStateNetwork{},
	}

	// For each of the tests below, there will be 2 files in the fixtures/ dir:
	// - <key>_in.json - the client request sent to the director
	// - <key>_expected.json - the expected request sent to the upstream
	tests := map[string]handleCreateTests{
		// Defaults
		"networks_create_1": handleCreateTests{
			rd: &rulesDirector{
				Client: mockRulesDirectorHttpClientWithUpstreamState(&us),
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc: 200,
		},
		// Defaults + -docker-link enabled
		"networks_create_2": handleCreateTests{
			rd: &rulesDirector{
				Client: mockRulesDirectorHttpClientWithUpstreamState(&us),
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:               "sockguard-pid-1",
				ContainerDockerLink: "ciagentcontainer:cccc",
			},
			esc: 200,
		},
		// Defaults + -container-join-network enabled
		"networks_create_3": handleCreateTests{
			rd: &rulesDirector{
				Client: mockRulesDirectorHttpClientWithUpstreamState(&us),
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:                "sockguard-pid-1",
				ContainerJoinNetwork: "ciagentcontainer",
			},
			esc: 200,
		},
		// Defaults + -container-join-network + -container-join-network-alias enabled
		"networks_create_4": handleCreateTests{
			rd: &rulesDirector{
				Client: mockRulesDirectorHttpClientWithUpstreamState(&us),
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:                     "sockguard-pid-1",
				ContainerJoinNetwork:      "ciagentcontainer",
				ContainerJoinNetworkAlias: "ciagentalias",
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

			var decoded map[string]interface{}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatal(err)
			}
			newNetworkName := decoded["Name"].(string)
			newNetworkOwner := ""
			switch lab := decoded["Labels"].(type) {
			case map[string]interface{}:
				newNetworkOwner = lab["com.buildkite.sockguard.owner"].(string)
			default:
				t.Fatal("Error: Cannot parse Labels from request JSON on network create")
			}
			if us.doesNetworkExist(newNetworkName) == true {
				t.Fatalf("Network '%s' already exists", newNetworkName)
			}
			us.createNetwork(newNetworkName, newNetworkOwner)

			// Return empty JSON, the request is whats important not the response
			fmt.Fprintf(w, `{}`)
		})
		// Credit: https://blog.questionable.services/article/testing-http-handlers-go/
		// Create a request to pass to our handler
		containerCreateJson, err := loadFixtureFile(fmt.Sprintf("%s_in", k))
		if err != nil {
			t.Fatal(err)
		}

		// Parse out the new network name from containerCreateJson, for use in further checks below
		var decodedIn map[string]interface{}
		if err := json.Unmarshal([]byte(containerCreateJson), &decodedIn); err != nil {
			t.Fatal(err)
		}
		inNewNetworkName := decodedIn["Name"].(string)

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

		// Verify the network was added to upstreamState
		if rr.Code == 200 && us.doesNetworkExist(inNewNetworkName) == false {
			t.Errorf("%s : %d response code, but network '%s' does not exist, should have been created in mock upstream state", k, rr.Code, inNewNetworkName)
		} else if rr.Code != 200 && us.doesNetworkExist(inNewNetworkName) == true {
			t.Errorf("%s : %d response code, but network '%s' exists, should not have been created", k, rr.Code, inNewNetworkName)
		}

		// Verify the ciagentcontainer was connected to the new network (if applicable)
		if v.rd.ContainerDockerLink != "" || v.rd.ContainerJoinNetwork != "" {
			ciAgentAttachedNetworks := us.getContainerAttachedNetworks("ciagentcontainer")
			ciAgentAttachedToNetwork := false
			ciAgentAttachedToNetworkWithAlias := false
			for _, vn := range ciAgentAttachedNetworks {
				if vn.name == inNewNetworkName {
					ciAgentAttachedToNetwork = true
					if v.rd.ContainerJoinNetworkAlias == "" {
						// No alias set, consider this a success
						ciAgentAttachedToNetworkWithAlias = true
					} else if cmp.Equal(vn.aliases, []string{v.rd.ContainerJoinNetworkAlias}) == true {
						// Should also have the correct alias set
						ciAgentAttachedToNetworkWithAlias = true
					}
					break
				}
			}
			if ciAgentAttachedToNetwork == false {
				t.Errorf("%s : network '%s' exists (or should exist), but ciagentcontainer is not attached", k, inNewNetworkName)
			}
			if ciAgentAttachedToNetworkWithAlias == false {
				t.Errorf("%s : network '%s' exists (or should exist), but ciagentcontainer does not have the alias '%s'", k, inNewNetworkName, v.rd.ContainerJoinNetworkAlias)
			}
		}

		// Don't bother checking the response, it's not relevant in mocked context. The request side is more important here.
	}
}

func TestHandleNetworkDelete(t *testing.T) {
	l := mockLogger()

	// Pre-populated simplified upstream state that "exists" before tests execute.
	us := upstreamState{
		containers: map[string]upstreamStateContainer{
			"ciagentcontainer": upstreamStateContainer{
				// No ownership checking at this level (intentionally), due to chicken-and-egg situation
				// (CI container is a sibling/sidecar of sockguard itself, not a child)
				owner: "foreign",
				attachedNetworks: []upstreamStateContainerAttachedNetwork{
					upstreamStateContainerAttachedNetwork{
						name: "whatevernetwork",
					},
					upstreamStateContainerAttachedNetwork{
						name: "alwaysjoinnetwork",
					},
					upstreamStateContainerAttachedNetwork{
						name:    "alwaysjoinnetworkwithalias",
						aliases: []string{"ciagentalias"},
					},
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
			"alwaysjoinnetwork": upstreamStateNetwork{
				owner: "sockguard-pid-1",
			},
			"alwaysjoinnetworkwithalias": upstreamStateNetwork{
				owner: "sockguard-pid-1",
			},
		},
	}

	// Key = the network name that will be deleted (or attempted)
	tests := map[string]handleCreateTests{
		// Defaults (owner label matches, should pass)
		"somenetwork": handleCreateTests{
			rd: &rulesDirector{
				Client: mockRulesDirectorHttpClientWithUpstreamState(&us),
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc: 200,
		},
		// Defaults (owner label does not match, should fail)
		"anothernetwork": handleCreateTests{
			rd: &rulesDirector{
				Client: mockRulesDirectorHttpClientWithUpstreamState(&us),
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner: "sockguard-pid-1",
			},
			esc: 401,
		},
		// Defaults + -docker-link enabled
		"whatevernetwork": handleCreateTests{
			rd: &rulesDirector{
				Client: mockRulesDirectorHttpClientWithUpstreamState(&us),
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:               "sockguard-pid-1",
				ContainerDockerLink: "ciagentcontainer:ffff",
			},
			esc: 200,
		},
		// Defaults + -container-join-network enabled
		"alwaysjoinnetwork": handleCreateTests{
			rd: &rulesDirector{
				Client: mockRulesDirectorHttpClientWithUpstreamState(&us),
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:                "sockguard-pid-1",
				ContainerJoinNetwork: "ciagentcontainer",
			},
			esc: 200,
		},
		// Defaults + -container-join-network + -container-join-network-alias enabled
		// Technically we don't do anything different to the prior here, but added for completeness
		"alwaysjoinnetworkwithalias": handleCreateTests{
			rd: &rulesDirector{
				Client: mockRulesDirectorHttpClientWithUpstreamState(&us),
				// This is what's set in main() as the default, assuming running in a container so PID 1
				Owner:                     "sockguard-pid-1",
				ContainerJoinNetwork:      "ciagentcontainer",
				ContainerJoinNetworkAlias: "ciagentalias",
			},
			esc: 200,
		},
	}

	pathIdRegex := regexp.MustCompile("^/v(.*)/networks/(.*)$")
	// TODOLATER: consolidate/DRY this with TestHandleContainerCreate()?
	for k, v := range tests {
		reqUrl := fmt.Sprintf("/v1.37/networks/%s", k)
		expectedUrl := fmt.Sprintf("/v1.37/networks/%s", k)
		upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != "DELETE" {
				t.Errorf("%s : Expected HTTP method DELETE got %s", k, req.Method)
			}

			// log.Printf("%s %s", req.Method, req.URL.String())
			// Validate the request URL against expected.
			if req.URL.String() != expectedUrl {
				t.Errorf("%s : Expected URL %s got %s", k, expectedUrl, req.URL.String())
			}

			// No request body for these DELETE calls

			// Parse out request URI
			if pathIdRegex.MatchString(req.URL.Path) == false {
				t.Fatalf("%s : URL path did not match expected /vx.xx/networks/{id|name}", k)
			}
			parsePath := pathIdRegex.FindStringSubmatch(req.URL.Path)
			if len(parsePath) != 3 {
				t.Fatalf("%s : URL path regex split mismatch, expected 3 got %d", k, len(parsePath))
			}

			// "Delete" the network (from mocked upstream state)
			err := us.deleteNetwork(parsePath[2])
			if err != nil {
				t.Fatal(err)
			}

			// Return empty JSON, the request is whats important not the response
			fmt.Fprintf(w, `{}`)
		})
		// Credit: https://blog.questionable.services/article/testing-http-handlers-go/
		// Create a request to pass to our handler
		req, err := http.NewRequest("DELETE", reqUrl, nil)
		if err != nil {
			t.Fatal(err)
		}

		// We create a ResponseRecorder (which satisfies http.ResponseWriter) to record the response.
		rr := httptest.NewRecorder()
		handler := v.rd.handleNetworkDelete(l, req, upstream)

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

		// Verify the network was deleted from mock upstream state (or not deleted on error)
		if rr.Code == 200 && us.doesNetworkExist(k) == true {
			t.Errorf("%s : %d response code, but network still exists, should have been deleted from mock upstream state", k, rr.Code)
		} else if rr.Code != 200 && us.doesNetworkExist(k) == false {
			t.Errorf("%s : %d response code, but network does not exist, should not have been deleted", k, rr.Code)
		}

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
				Owner:                 "sockguard-pid-1",
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
