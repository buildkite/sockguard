package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"regexp"
	"strings"
)

const (
	apiVersion = "1.32"
	ownerKey   = "com.buildkite.safety-sock.owner"
)

var (
	versionRegex = regexp.MustCompile(`^/v\d\.\d+\b`)
)

type rulesDirector struct {
	Client *http.Client
	Owner  string
}

func (r *rulesDirector) Direct(l *log.Logger, req *http.Request, upstream http.Handler) http.Handler {
	var match = func(method string, pattern string) bool {
		if method != "*" && method != req.Method {
			return false
		}
		path := req.URL.Path
		if versionRegex.MatchString(path) {
			path = versionRegex.ReplaceAllString(path, "")
		}
		re := regexp.MustCompile(pattern)
		return re.MatchString(path)
	}

	var errorHandler = func(msg string, code int) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			l.Printf("Handler returned error %q", msg)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"message": msg,
			})
			return
		})
	}

	switch {
	case match(`GET`, `^/_ping$`), match(`GET`, `^/version$`):
		return upstream

	case match(`POST`, `/containers/(create|prune)`):
		return r.addLabelsToBody(l, req, upstream)
	case match(`GET`, `/containers/json`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)
	case match(`*`, `/containers/(\w+)\b`):
		if ok, err := r.checkContainerOwner(req); ok {
			l.Printf("Container matches owner %q", r.Owner)
			return upstream
		} else if err != nil {
			return errorHandler(err.Error(), http.StatusInternalServerError)
		}
		return errorHandler("Unauthorized access to container", http.StatusUnauthorized)

	case match(`POST`, `/build`):
		return r.addLabelsToQueryStringLabels(l, req, upstream)
	case match(`POST`, `/images/prune`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)
	}

	return upstream
}

func (r *rulesDirector) checkContainerOwner(req *http.Request) (bool, error) {
	path := req.URL.Path
	if versionRegex.MatchString(path) {
		path = versionRegex.ReplaceAllString(path, "")
	}
	m := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(m) < 2 {
		return false, fmt.Errorf("Path doesn't contain a container id")
	}

	c, err := r.inspectContainer(m[1])
	if err != nil {
		return false, err
	}

	return c.HasLabel(ownerKey, r.Owner), nil
}

func (r *rulesDirector) addLabelsToBody(l *log.Logger, req *http.Request, upstream http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		l.Printf("Adding labels to Labels in request body: %s", req.URL.Path)

		err := modifyRequestBody(req, func(decoded map[string]interface{}) {
			mergeLabels("Labels", decoded, map[string]string{
				ownerKey: r.Owner,
			})
		})
		if err != nil {
			l.Printf("Err: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		upstream.ServeHTTP(w, req)
	})
}

func (r *rulesDirector) addLabelsToQueryStringFilters(l *log.Logger, req *http.Request, upstream http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		label := fmt.Sprintf("%s=%s", ownerKey, r.Owner)

		l.Printf("Adding label %s=%s to filters in querystring: %s %s",
			ownerKey, r.Owner, req.URL.Path, req.URL.RawQuery)

		err := modifyRequestFilters(req, func(filters map[string][]string) {
			for k, vals := range filters {
				if k == "label" {
					vals = append(vals, label)
					return
				}
			}
			filters["label"] = []string{label}
			return
		})
		if err != nil {
			l.Printf("Err: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		upstream.ServeHTTP(w, req)
	})
}

func (r *rulesDirector) addLabelsToQueryStringLabels(l *log.Logger, req *http.Request, upstream http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		l.Printf("Adding label %s=%s to querystring: %s %s",
			ownerKey, r.Owner, req.URL.Path, req.URL.RawQuery)

		var q = req.URL.Query()
		var labels = map[string]string{}

		if encoded := q.Get("labels"); encoded != "" {
			if err := json.NewDecoder(strings.NewReader(encoded)).Decode(&labels); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		labels[ownerKey] = r.Owner

		encoded, err := json.Marshal(labels)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		q.Set("labels", string(encoded))
		req.URL.RawQuery = q.Encode()

		upstream.ServeHTTP(w, req)
	})
}

type containerInspection struct {
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
}

func (c containerInspection) HasLabel(key, value string) bool {
	for k, val := range c.Config.Labels {
		if k == key && val == value {
			return true
		}
	}
	return false
}

func (r *rulesDirector) inspectContainer(id string) (containerInspection, error) {
	u := fmt.Sprintf("http://docker/v%s/containers/%s/json", apiVersion, id)
	resp, err := r.Client.Get(u)
	if err != nil {
		return containerInspection{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return containerInspection{}, fmt.Errorf("Request to %q failed with %s", u, resp.Status)
	}

	var result containerInspection

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return containerInspection{}, err
	}

	return result, nil
}

func modifyRequestFilters(req *http.Request, f func(filters map[string][]string)) error {
	var filters = map[string][]string{}
	var q = req.URL.Query()

	if encoded := q.Get("filters"); encoded != "" {
		if err := json.NewDecoder(strings.NewReader(encoded)).Decode(&filters); err != nil {
			return err
		}
	}

	f(filters)

	encoded, err := json.Marshal(filters)
	if err != nil {
		return err
	}

	q.Set("filters", string(encoded))
	req.URL.RawQuery = q.Encode()
	return nil
}

func modifyRequestBody(req *http.Request, f func(filters map[string]interface{})) error {
	var decoded map[string]interface{}

	if err := json.NewDecoder(req.Body).Decode(&decoded); err != nil {
		return err
	}

	f(decoded)

	encoded, err := json.Marshal(decoded)
	if err != nil {
		return err
	}

	// reset it so that upstream can read it again
	req.ContentLength = int64(len(encoded))
	req.Body = ioutil.NopCloser(bytes.NewReader(encoded))

	return nil
}

func mergeLabels(key string, into map[string]interface{}, labels map[string]string) {
	if exists, ok := into[key].(map[string]string); ok {
		for k, v := range labels {
			exists[k] = v
		}
		return
	}
	into[key] = labels
}
