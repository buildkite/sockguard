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

	"github.com/buildkite/sockguard/socketproxy"
)

const (
	apiVersion = "1.32"
	ownerKey   = "com.buildkite.sockguard.owner"
)

var (
	versionRegex = regexp.MustCompile(`^/v\d\.\d+\b`)
)

type rulesDirector struct {
	Client                  *http.Client
	Owner                   string
	AllowBinds              []string
	AllowHostModeNetworking bool
	ContainerCgroupParent   string
	User                    string
}

func writeError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": msg,
	})
}

func (r *rulesDirector) Direct(l socketproxy.Logger, req *http.Request, upstream http.Handler) http.Handler {
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
		if ok, err := r.checkOwner(l, "containers", false, req); ok {
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
		if ok, err := r.checkOwner(l, "images", true, req); ok {
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
	case match(`GET`, `^/networks/(.+)$`),
		match(`DELETE`, `^/networks/(.+)$`),
		match(`POST`, `^/networks/(.+)/(connect|disconnect)$`):
		if ok, err := r.checkOwner(l, "networks", true, req); ok {
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
		if ok, err := r.checkOwner(l, "volumes", true, req); ok {
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
	regexp.MustCompile(`^/containers/(.+?)(?:/\w+)?$`),
	regexp.MustCompile(`^/networks/(.+?)(?:/\w+)?$`),
	regexp.MustCompile(`^/volumes/(\w+?)(?:/\w+)?$`),
	regexp.MustCompile(`^/images/(.+?)/(?:json|history|push|tag)$`),
	regexp.MustCompile(`^/images/([^/]+)$`),
	regexp.MustCompile(`^/images/(\w+/[^/]+)$`),
}

// Check owner takes a request for /vx.x/{kind}/{id} and uses inspect to see if it's
// got the correct owner label.
func (r *rulesDirector) checkOwner(l socketproxy.Logger, kind string, allowEmpty bool, req *http.Request) (bool, error) {
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

	if val, exists := labels[ownerKey]; exists && val == r.Owner {
		l.Printf("Allow, %s matches owner %q", path, r.Owner)
		return true, nil
	} else if !exists && allowEmpty {
		l.Printf("Allow, %s has no owner", path)
		return true, nil
	} else {
		l.Printf("Deny, %s has owner %q, wanted %q", path, val, r.Owner)
		return false, nil
	}
}

func (r *rulesDirector) handleContainerCreate(l socketproxy.Logger, req *http.Request, upstream http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var decoded map[string]interface{}

		if err := json.NewDecoder(req.Body).Decode(&decoded); err != nil {
			writeError(w, err.Error(), http.StatusBadRequest)
			return
		}

		// first we add our labels
		addLabel(ownerKey, r.Owner, decoded["Labels"])

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

		// apply CgroupParent if enabled
		if r.ContainerCgroupParent != "" {
			// If a CgroupParent is already specified, bug out
			cgroupParent, ok := decoded["HostConfig"].(map[string]interface{})["CgroupParent"].(string)
			if ok {
				if cgroupParent != "" {
					l.Printf("Denied container create due to existing CgroupParent '%s' (override not permitted)", cgroupParent)
					writeError(w, fmt.Sprintf("Cannot override CgroupParent value '%s' on container create", cgroupParent), http.StatusUnauthorized)
					return
				}
				decoded["HostConfig"].(map[string]interface{})["CgroupParent"] = r.ContainerCgroupParent
			}
		}

		// force user
		if r.User != "" {
			decoded["User"] = r.User
			l.Printf("Forcing user to '%s'", r.User)
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

func (r *rulesDirector) addLabelsToBody(l socketproxy.Logger, req *http.Request, upstream http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		err := modifyRequestBody(req, func(decoded map[string]interface{}) {
			addLabel(ownerKey, r.Owner, decoded["Labels"])
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		upstream.ServeHTTP(w, req)
	})
}

func (r *rulesDirector) addLabelsToQueryStringFilters(l socketproxy.Logger, req *http.Request, upstream http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var q = req.URL.Query()
		var filters = map[string][]interface{}{}

		// parse existing filters from querystring
		if qf := q.Get("filters"); qf != "" {
			var existing map[string]interface{}

			if err := json.NewDecoder(strings.NewReader(qf)).Decode(&existing); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}

			// different docker implementations send us different data structures
			for k, v := range existing {
				switch tv := v.(type) {
				// sometimes we get a map of value=true
				case map[string]interface{}:
					for mk := range tv {
						filters[k] = append(filters[k], mk)
					}
				// sometimes we get a slice of values (from docker-compose)
				case []interface{}:
					filters[k] = append(filters[k], tv...)
				default:
					http.Error(w, fmt.Sprintf("Unhandled filter type of %T", v), http.StatusBadRequest)
					return
				}
			}
		}

		// add an label slice if none exists
		if _, exists := filters["label"]; !exists {
			filters["label"] = []interface{}{}
		}

		// add an owner label
		label := ownerKey + "=" + r.Owner
		l.Printf("Adding label %v to label filters %v", label, filters["label"])
		filters["label"] = append(filters["label"], label)

		// encode back into json
		encoded, err := json.Marshal(filters)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		q.Set("filters", string(encoded))
		req.URL.RawQuery = q.Encode()

		upstream.ServeHTTP(w, req)
	})
}

func (r *rulesDirector) addLabelsToQueryStringLabels(l socketproxy.Logger, req *http.Request, upstream http.Handler) http.Handler {
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
