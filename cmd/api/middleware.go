package main

import (
	"context"
	"net/http"
	"time"
)

// adapter type is a wrapper to construct middleware.
// It takes in a http.Handler and returns a wrapped http.Handler.
type adapter func(http.Handler) http.Handler

// adapt takes an http.Handler and applies a set of middleware (in the form of adapters) to it.
// Note: all middleware is executed in reverse order of their appearance in the arguments to adapt().
func adapt(h http.Handler, middleware ...adapter) http.Handler {
	for _, middlewareFn := range middleware {
		h = middlewareFn(h)
	}
	return h
}

// statusWriter is a struct implementing the ResponseWriter interface to record some metrics for logging purposes.
type statusWriter struct {
	http.ResponseWriter
	status int
	length int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.length += n
	return n, err
}

// logHTTPRequest logs a request in Apache log format, with as additional last number the amount of milliseconds the request took
func logHTTPRequest() adapter {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := statusWriter{ResponseWriter: w}
			h.ServeHTTP(&sw, r)
			durationInMilliSeconds := time.Since(start).Nanoseconds() / (int64(time.Millisecond) / int64(time.Nanosecond))
			logger.Infof("%s - - [%s] \"%s %v %s\" %d %d %d", r.RemoteAddr, time.Now().UTC().Format("02/Jan/2006:03:04:05"), r.Method, r.URL, r.Proto, sw.status, sw.length, durationInMilliSeconds)
		})
	}
}

// addRequestTimeout will bind a context with timeout to the request to timeout the request after a specified time.
func addRequestTimeout() adapter {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Create a context with deadline.
			ctx, cancel := context.WithTimeout(r.Context(), requestTimeoutDuration)
			defer cancel()

			r = r.WithContext(ctx)
			h.ServeHTTP(w, r)
		})
	}
}
