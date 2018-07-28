package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	apiVersion = "1.32"
)

var (
	versionRegex = regexp.MustCompile(`^/v\d\.\d+\b`)
)

type rulesDirector struct {
	Client                  *http.Client
	LabelName               string
	LabelValue              string
	AllowBinds              []string
	AllowHostModeNetworking bool
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": msg,
	})
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
			writeError(w, msg, code)
			return
		})
	}

	switch {
	case match(`GET`, `^/(_ping|version|info)$`):
		return upstream
	case match(`GET`, `^/events$`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)

	// Container related endpoints
	case match(`POST`, `^/containers/create$`):
		return r.handleContainerCreate(l, req, upstream)
	case match(`POST`, `^/containers/prune$`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)
	case match(`GET`, `^/containers/json$`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)
	case match(`*`, `^/(containers|exec)/(\w+)\b`):
		if ok, err := r.checkLabel(l, "containers", false, req); ok {
			return upstream
		} else if err == errInspectNotFound {
			l.Printf("Container not found, allowing")
			return upstream
		} else if err != nil {
			return errorHandler(err.Error(), http.StatusInternalServerError)
		}
		return errorHandler("Unauthorized access to container", http.StatusUnauthorized)

	// Build related endpoints
	case match(`POST`, `^/build$`):
		return r.addLabelsToQueryStringLabels(l, req, upstream)

	// Image related endpoints
	case match(`GET`, `^/images/json$`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)
	case match(`POST`, `^/images/create$`):
		return upstream
	case match(`POST`, `^/images/(create|search|get|load)$`):
		break
	case match(`POST`, `^/images/prune$`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)
	case match(`*`, `^/images/(\w+)\b`):
		if ok, err := r.checkLabel(l, "images", true, req); ok {
			return upstream
		} else if err == errInspectNotFound {
			l.Printf("Image not found, allowing")
			return upstream
		} else if err != nil {
			return errorHandler(err.Error(), http.StatusInternalServerError)
		}
		return errorHandler("Unauthorized access to image", http.StatusUnauthorized)

	// Network related endpoints
	case match(`GET`, `^/networks$`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)
	case match(`POST`, `^/networks/create$`):
		return r.addLabelsToBody(l, req, upstream)
	case match(`POST`, `^/networks/prune$`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)
	case match(`GET`, `^/networks/(\w+)$`),
		match(`DELETE`, `^/networks/(\w+)$`),
		match(`POST`, `^/networks/(\w+)/(connect|disconnect)$`):
		if ok, err := r.checkLabel(l, "networks", true, req); ok {
			return upstream
		} else if err == errInspectNotFound {
			l.Printf("Network not found, allowing")
			return upstream
		} else if err != nil {
			return errorHandler(err.Error(), http.StatusInternalServerError)
		}
		return errorHandler("Unauthorized access to network", http.StatusUnauthorized)

	// Volumes related endpoints
	case match(`GET`, `^/volumes$`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)
	case match(`POST`, `^/volumes/create$`):
		return r.addLabelsToBody(l, req, upstream)
	case match(`POST`, `^/volumes/prune$`):
		return r.addLabelsToQueryStringFilters(l, req, upstream)
	case match(`GET`, `^/volumes/(\w+)$`), match(`DELETE`, `^/volumes/(\w+)$`):
		if ok, err := r.checkLabel(l, "volumes", true, req); ok {
			return upstream
		} else if err == errInspectNotFound {
			l.Printf("Volume not found, allowing")
			return upstream
		} else if err != nil {
			return errorHandler(err.Error(), http.StatusInternalServerError)
		}
		return errorHandler("Unauthorized access to volume", http.StatusUnauthorized)

	}

	return errorHandler(req.Method+" "+req.URL.Path+" not implemented yet", http.StatusNotImplemented)
}

var identifierPatterns = []*regexp.Regexp{
	regexp.MustCompile(`^/containers/(\w+?)(?:/\w+)?$`),
	regexp.MustCompile(`^/networks/(\w+?)(?:/\w+)?$`),
	regexp.MustCompile(`^/volumes/(\w+?)(?:/\w+)?$`),
	regexp.MustCompile(`^/images/(.+?)/(?:json|history|push|tag)$`),
	regexp.MustCompile(`^/images/([^/]+)$`),
	regexp.MustCompile(`^/images/(\w+/[^/]+)$`),
}

// Check label takes a request for /vx.x/{kind}/{id} and uses inspect to see if it's
// got the correct label name/value.
func (r *rulesDirector) checkLabel(l *log.Logger, kind string, allowEmpty bool, req *http.Request) (bool, error) {
	path := req.URL.Path
	if versionRegex.MatchString(path) {
		path = versionRegex.ReplaceAllString(path, "")
	}

	var identifier string

	for _, re := range identifierPatterns {
		if m := re.FindStringSubmatch(path); len(m) > 0 {
			identifier = m[1]
		}
	}

	if identifier == "" {
		return false, fmt.Errorf("Unable to find an identifier in %s", path)
	}

	l.Printf("Looking up identifier %q", identifier)

	labels, err := r.inspectLabels(kind, identifier)
	if err != nil {
		return false, err
	}

	l.Printf("Labels for %s: %v", path, labels)

	if val, exists := labels[r.LabelName]; exists && val == r.LabelValue {
		l.Printf("Allow, %s matches %q=%q", path, r.LabelName, r.LabelValue)
		return true, nil
	} else if !exists && allowEmpty {
		l.Printf("Allow, %s has no %q label", path, r.LabelName)
		return true, nil
	} else {
		l.Printf("Deny, %s has %q label %q, wanted %q", path, r.LabelName, val, r.LabelValue)
		return false, nil
	}
}

func (r *rulesDirector) handleContainerCreate(l *log.Logger, req *http.Request, upstream http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var decoded map[string]interface{}

		if err := json.NewDecoder(req.Body).Decode(&decoded); err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}

		// first we add our labels
		addLabel(r.LabelName, r.LabelValue, decoded["Labels"])

		l.Printf("Labels: %#v", decoded["Labels"])

		// prevent privileged mode
		privileged, ok := decoded["HostConfig"].(map[string]interface{})["Privileged"].(bool)
		if ok && privileged {
			l.Printf("Denied privileged on container create")
			writeError(w, "Containers aren't allowed to run as privileged", http.StatusUnauthorized)
			return
		}

		// filter binds, don't allow host binds
		binds, ok := decoded["HostConfig"].(map[string]interface{})["Binds"].([]interface{})
		if ok {
			for _, bind := range binds {
				if !isBindAllowed(bind.(string), r.AllowBinds) {
					l.Printf("Denied host bind %q", bind)
					writeError(w, "Host binds aren't allowed", http.StatusUnauthorized)
					return
				}
			}
		}

		// prevent host and container network mode
		networkMode, ok := decoded["HostConfig"].(map[string]interface{})["NetworkMode"].(string)
		if ok && networkMode == "host" && (!r.AllowHostModeNetworking) {
			l.Printf("Denied host network mode on container create")
			writeError(w, "Containers aren't allowed to use host networking", http.StatusUnauthorized)
			return
		}

		encoded, err := json.Marshal(decoded)
		if err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}

		// reset it so that upstream can read it again
		req.ContentLength = int64(len(encoded))
		req.Body = ioutil.NopCloser(bytes.NewReader(encoded))

		upstream.ServeHTTP(w, req)
	})
}

func isBindAllowed(bind string, allowed []string) bool {
	chunks := strings.Split(bind, ":")

	// host-src:container-dest
	// host-src:container-dest:ro
	// volume-name:container-dest
	// volume-name:container-dest:ro

	// TODO: better heuristic for host-src vs volume-name
	if strings.ContainsAny(chunks[0], ".\\/") {
		hostSrc := filepath.FromSlash(path.Clean("/" + chunks[0]))

		for _, allowedPath := range allowed {
			if strings.HasPrefix(hostSrc, allowedPath) {
				return true
			}
		}

		return false
	}

	return true
}

func addLabel(label, value string, into interface{}) {
	switch t := into.(type) {
	case map[string]interface{}:
		t[label] = value
	default:
		log.Printf("Found unhandled label type %T: %v", into, t)
	}
}

func (r *rulesDirector) addLabelsToBody(l *log.Logger, req *http.Request, upstream http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		err := modifyRequestBody(req, func(decoded map[string]interface{}) {
			addLabel(r.LabelName, r.LabelValue, decoded["Labels"])
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		upstream.ServeHTTP(w, req)
	})
}

func (r *rulesDirector) addLabelsToQueryStringFilters(l *log.Logger, req *http.Request, upstream http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		err := modifyRequestFilters(req, func(filters map[string][]interface{}) {
			label := r.LabelName + "=" + r.LabelValue
			l.Printf("Adding label %v to label filters %v", label, filters["label"])
			filters["label"] = []interface{}{label}
			for _, val := range filters["label"] {
				if valString, ok := val.(string); ok {
					if valString != r.LabelName && !strings.HasPrefix(valString, r.LabelName+"=") {
						filters["label"] = append(filters["label"], valString)
					}
				}
			}
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		upstream.ServeHTTP(w, req)
	})
}

func (r *rulesDirector) addLabelsToQueryStringLabels(l *log.Logger, req *http.Request, upstream http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		l.Printf("Adding label %s=%s to querystring: %s %s",
			r.LabelName, r.LabelValue, req.URL.Path, req.URL.RawQuery)

		var q = req.URL.Query()
		var labels = map[string]string{}

		if encoded := q.Get("labels"); encoded != "" {
			if err := json.NewDecoder(strings.NewReader(encoded)).Decode(&labels); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}

		labels[r.LabelName] = r.LabelValue

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

var errInspectNotFound = errors.New("Not found")

func (r *rulesDirector) getInto(into interface{}, path string, arg ...interface{}) error {
	u := fmt.Sprintf("http://docker/v%s%s", apiVersion, fmt.Sprintf(path, arg...))

	resp, err := r.Client.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return errInspectNotFound
	} else if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Request to %q failed: %s", u, resp.Status)
	}

	return json.NewDecoder(resp.Body).Decode(into)
}

func (r *rulesDirector) inspectLabels(kind, id string) (map[string]string, error) {
	switch kind {
	case "containers", "images":
		var result struct {
			Config struct {
				Labels map[string]string
			}
		}

		if err := r.getInto(&result, "/"+kind+"/%s/json", id); err != nil {
			return nil, err
		}

		return result.Config.Labels, nil
	case "networks", "volumes":
		var result struct {
			Labels map[string]string
		}

		if err := r.getInto(&result, "/"+kind+"/%s", id); err != nil {
			return nil, err
		}

		return result.Labels, nil
	}

	return nil, fmt.Errorf("Unknown kind %q", kind)
}

func modifyRequestFilters(req *http.Request, f func(filters map[string][]interface{})) error {
	var filters = map[string][]interface{}{}
	var q = req.URL.Query()

	if encoded := q.Get("filters"); encoded != "" {
		var generic map[string]interface{}

		log.Printf("filters=%q", encoded)
		if err := json.NewDecoder(strings.NewReader(encoded)).Decode(&generic); err != nil {
			return err
		}
		for k, v := range generic {
			switch tv := v.(type) {
			case map[string]interface{}:
				for mk, mv := range tv {
					log.Printf("Adding %s = %v", mk, mv)
					filters[k] = []interface{}{mk}
				}
			default:
				log.Printf("[%s] Got type %T: %v", k, v, tv)
			}
		}

		log.Printf("%#v", filters)
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
