package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// Reusable mock rulesDirector instance
func mockRulesDirector() *rulesDirector {
	return &rulesDirector{
		Client: &http.Client{},
		Owner:  "test-owner",
		AllowHostModeNetworking: false,
	}
}

// Reusable mock log.Logger instance
func mockLogger() *log.Logger {
	return log.New(os.Stderr, "MOCK: ", log.Ltime|log.Lmicroseconds)
}

func TestAddLabelsToQueryStringFilters(t *testing.T) {
	r := mockRulesDirector()
	l := mockLogger()

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

	for c_req_url, u_req_url := range tests {
		upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// log.Printf("%s %s", req.Method, req.URL.String())
			// Validate the request URL against expected.
			if req.URL.String() != u_req_url {
				decode_u_req_url, err1 := url.QueryUnescape(u_req_url)
				decode_in_req_url, err2 := url.QueryUnescape(req.URL.String())
				if err1 == nil && err2 == nil {
					t.Errorf("Expected:\n%s\ngot:\n%s\n\n(URL decoded) Expected:\n%s\ngot:\n%s\n", u_req_url, req.URL.String(), decode_u_req_url, decode_in_req_url)
				} else {
					t.Errorf("Expected:\n%s\ngot:\n%s\n\n(errors trying to URL decode)\n", u_req_url, req.URL.String())
				}
			}

			// Return empty JSON, the request is whats important not the response
			fmt.Fprintf(w, `{}`)
		})

		// Credit: https://blog.questionable.services/article/testing-http-handlers-go/
		// Create a request to pass to our handler
		req, err := http.NewRequest("GET", c_req_url, nil)
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
			t.Errorf("%s : handler returned wrong status code: got %v want %v", c_req_url, status, http.StatusOK)
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

func TestHandleContainerCreate(t *testing.T) {
	l := mockLogger()

	rd1 := &rulesDirector{
		Client: &http.Client{},
		Owner:  "test-owner",
		AllowHostModeNetworking: false,
		ContainerCgroupParent:   "some-cgroup",
		ContainerDockerLink:     "asdf:zzzz",
	}

	expectedReqJson, err := loadFixtureFile("containers_create_1_expected")
	if err != nil {
		t.Fatal(err)
	}

	upstream := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// log.Printf("%s %s", req.Method, req.URL.String())
		// Validate the request URL against expected.
		expectedUrl := "/v1.37/containers/create"
		if req.URL.String() != expectedUrl {
			t.Error("Expected URL", expectedUrl, "got", req.URL.String())
		}
		// Validate the body has been modified as expected
		body, err := ioutil.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != string(expectedReqJson) {
			t.Errorf("Expected request body JSON:\n%s\nGot request body JSON:\n%s\n", string(expectedReqJson), string(body))
		}

		// Return empty JSON, the request is whats important not the response
		fmt.Fprintf(w, `{}`)
	})

	// Credit: https://blog.questionable.services/article/testing-http-handlers-go/
	// Create a request to pass to our handler
	containerCreateJson, err := loadFixtureFile("containers_create_1_in")
	if err != nil {
		t.Fatal(err)
	}
	reqUrl := "/v1.37/containers/create"
	req, err := http.NewRequest("POST", reqUrl, strings.NewReader(containerCreateJson))
	if err != nil {
		t.Fatal(err)
	}
	// We create a ResponseRecorder (which satisfies http.ResponseWriter) to record the response.
	rr := httptest.NewRecorder()
	handler := rd1.handleContainerCreate(l, req, upstream)

	// Our handlers satisfy http.Handler, so we can call their ServeHTTP method
	// directly and pass in our Request and ResponseRecorder.
	handler.ServeHTTP(rr, req)

	// Check the status code is what we expect.
	if status := rr.Code; status != http.StatusOK {
		// Get the body out of the response to return with the error
		respBody, err := ioutil.ReadAll(rr.Body)
		if err == nil {
			t.Errorf("%s : handler returned wrong status code: got %v want %v. Response body: %s", reqUrl, status, http.StatusOK, string(respBody))
		} else {
			t.Errorf("%s : handler returned wrong status code: got %v want %v. Error reading response body: %s", reqUrl, status, http.StatusOK, err.Error())
		}
	}

	// Don't bother checking the response, it's not relevant in mocked context. The request side is more important here.
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
