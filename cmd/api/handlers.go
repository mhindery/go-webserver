package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"contrib.go.opencensus.io/exporter/stackdriver/propagation"
	"go.opencensus.io/plugin/ochttp"
)

var podLabels map[string]string
var environmentVariables map[string]string

// DefaultHTTPClient is a client to be used for each outgoing HTTP request.
// It adds trace propagation and timeout settings.
var DefaultHTTPClient = &http.Client{
	Transport: &ochttp.Transport{
		Base: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
			}).DialContext,

			MaxIdleConns:        200,
			MaxIdleConnsPerHost: 100,
		},
		Propagation: &propagation.HTTPFormat{},
	},
	Timeout: 0,
}

/************************** Liveness server **************************/

// healthService contains only a handler to handle health checks
type healthService struct{}

func (h *healthService) healthCheck() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}
}

/************************** Main server **************************/

// service contains the handlers of the server
type service struct {
	name string
}

func newService(name string) *service {
	return &service{
		name: name,
	}
}

// getServiceLabels returns the set of labels being applied to the service,
// reading them from a file, which in k8s's case if mounted as a volume via the downwards API.
func getServiceLabels() map[string]string {
	if podLabels == nil {
		podLabels = make(map[string]string)

		filename := "/etc/podinfo/labels"
		if IsDevelopment() {
			filename = "/tmp/podinfo/labels"
		}

		file, err := os.Open(filename)
		if err != nil {
			podLabels["error"] = fmt.Sprintf("ERROR: Error opening file %++v: %++v", filename, err)
			return podLabels
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			pair := strings.Split(scanner.Text(), "=")
			pair[1] = strings.ReplaceAll(pair[1], "\"", "")
			podLabels[pair[0]] = pair[1]
		}

		if err := scanner.Err(); err != nil {
			podLabels["error"] = fmt.Sprintf("ERROR: Error reading file: %++v", err)
		}
	}

	return podLabels
}

func getEnvironmentVariables() map[string]string {
	if environmentVariables == nil {
		doFiltering := false
		filterEnvVariables := []string{
			"DEVELOPMENT", "ENVIRONMENT", "POD_NAME", "POD_NAMESPACE", "POD_UID", "POD_IP",
			"POD_SERVICE_ACCOUNT", "NODE_NAME", "HOST_IP",
		}
		lookupMap := make(map[string]bool)
		for _, key := range filterEnvVariables {
			lookupMap[key] = true
		}
		environmentVariables = make(map[string]string)
		for _, e := range os.Environ() {
			pair := strings.Split(e, "=")
			if _, found := lookupMap[pair[0]]; !doFiltering || found {
				environmentVariables[pair[0]] = pair[1]
			}
		}
	}

	return environmentVariables
}

func getServiceInfo(s *service) map[string]interface{} {
	return map[string]interface{}{
		"name":             s.name,
		"currentTimestamp": time.Now().UTC(),
		"environment":      getEnvironmentVariables(),
		"labels":           getServiceLabels(),
	}
}

// getRequestInfo returns some info of the incoming request
func getRequestInfo(r *http.Request) map[string]interface{} {
	return map[string]interface{}{
		"headers":    r.Header,
		"method":     r.Method,
		"params":     r.URL.Query(),
		"remoteAddr": r.RemoteAddr,
		"userAgent":  r.UserAgent(),
		"url":        r.URL.String(),
		"host":       r.Host,
		"referrer":   r.Referer(),
		"protocol":   r.Proto,
	}
}

// getJSONResponse performs a call to an external call, expecting a json response and returns a map with
// that json response in it under the key "response". If no json could be decoded, the "response_raw" key will
// contain a string with the received body of the request.
func getJSONResponse(r *http.Request) map[string]interface{} {
	// Perform external call
	called := make(map[string]interface{})
	urlParams, ok := r.URL.Query()["url"]
	if !ok || len(urlParams) != 1 || len(urlParams[0]) < 1 {
		called["error"] = "ERROR: Invalid url param provided for url to call"
	} else {
		called["url"] = urlParams[0]
		resp, err := DefaultHTTPClient.Get(urlParams[0])
		if err != nil {
			called["error"] = fmt.Sprintf("ERROR: Error calling url %++v: %++v", urlParams[0], err)
		} else {
			defer resp.Body.Close()
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				called["error"] = fmt.Sprintf("ERROR: Error reading response body: %++v", err)
			} else {
				var target interface{}
				err = json.Unmarshal(body, &target)
				if err != nil {
					called["error"] = fmt.Sprintf("ERROR: Error json decoding response body: %++v", err)
					called["response_raw"] = string(body)
				} else {
					called["response"] = target
				}
			}

		}
	}
	return called
}

// indexHandler returns a json with some info about the service, the request headers, the environment
func (s *service) indexHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		response := make(map[string]interface{})
		response["service"] = getServiceInfo(s)
		response["request"] = getRequestInfo(r)

		json.NewEncoder(w).Encode(response)
	}
}

// callHandler calls a url given in the getparam and returns the json as in the indexHandler above, with the info of the call
func (s *service) callHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		response := make(map[string]interface{})
		response["service"] = getServiceInfo(s)
		response["request"] = getRequestInfo(r)
		response["called"] = getJSONResponse(r)

		json.NewEncoder(w).Encode(response)
	}
}
