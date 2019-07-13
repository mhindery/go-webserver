package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"contrib.go.opencensus.io/exporter/stackdriver"
	"contrib.go.opencensus.io/exporter/stackdriver/propagation"
	"go.opencensus.io/plugin/ochttp"
	"go.opencensus.io/trace"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/gorilla/mux"
)

var (
	listenAddr             string
	livenessListenAddr     string
	logLevel               = flag.Int("log", 0, "-1=debug+, 0=info+, 1=warn+, 2=error+")
	serviceName            = ""
	logger                 *zap.SugaredLogger
	environmentName        = "local"
	requestTimeoutDuration = 60 * time.Second
)

// IsDevelopment returns if we are running in development mode
func IsDevelopment() bool {
	if envVar := os.Getenv("DEVELOPMENT"); envVar != "" {
		isDevelopment, err := strconv.Atoi(envVar)
		return (err == nil && isDevelopment == 1)
	}
	return false
}

// startLivenessServer fires up a server on the specified listen address which exclusively answers health checks
func startLivenessServer(address string) *http.Server {
	r := mux.NewRouter()
	r.HandleFunc("/_ah/health/", (&healthService{}).healthCheck())

	srv := http.Server{
		Addr:         address,
		Handler:      r,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  5 * time.Second,
	}

	go func() {
		logger.Debugf("liveness server listening on %v", address)
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			logger.Fatalf("failed to start liveness server: %v", err)
		}
	}()

	return &srv
}

func shutdownLivenessServer(srv *http.Server) {
	logger.Debugf("liveness server shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Errorf("failed to gracefully shutdown: %v", err)
	} else {
		logger.Debugf("liveness server shut down cleanly")
	}
}

/************************** Main server **************************/

// getRouter creates a router (which is a handler) for the server to use in serving traffic.
// It links paths to services, handlers and middleware.
func getRouter() *mux.Router {
	healthServerHandlers := &healthService{}
	mainServerHandlers := newService("Inspector")

	router := mux.NewRouter()
	router.Handle("/_ah/health/", adapt(healthServerHandlers.healthCheck(), addRequestTimeout(), logHTTPRequest()))
	router.Handle("/_ah/ready/", adapt(healthServerHandlers.healthCheck(), addRequestTimeout(), logHTTPRequest()))
	router.Handle("/call/", adapt(mainServerHandlers.callHandler(), addRequestTimeout(), logHTTPRequest()))
	router.Handle("/", adapt(mainServerHandlers.indexHandler(), addRequestTimeout(), logHTTPRequest()))

	return router
}

// setupLogger configures a logger with the desired log level
func setupLogger(loggingLevel int) {
	encoding := "console"
	encoderConfig := zap.NewDevelopmentEncoderConfig()
	if !IsDevelopment() {
		encoding = "json"
		encoderConfig = zap.NewProductionEncoderConfig()
	}
	zapLogger, _ := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapcore.Level(loggingLevel)),
		Development:      IsDevelopment(),
		Encoding:         encoding,
		EncoderConfig:    encoderConfig,
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}.Build()
	logger = zapLogger.Sugar()
}

func main() {
	flag.StringVar(&listenAddr, "listen-addr", ":8282", "server listen address")
	flag.StringVar(&livenessListenAddr, "liveness-listen-addr", ":9000", "liveness check listen address")
	flag.Parse()

	environmentName = os.Getenv("ENVIRONMENT")

	setupLogger(*logLevel)
	defer logger.Sync()

	// Liveness checks handles by separate server to avoid premature killing by k8s during srv shutdown
	livenessSrv := startLivenessServer(livenessListenAddr)
	defer shutdownLivenessServer(livenessSrv)

	// Telemetry with OpenCensus
	if projectName := os.Getenv("GCP_PROJECT"); projectName != "" {
		exporter, err := stackdriver.NewExporter(stackdriver.Options{ProjectID: projectName})
		if err != nil {
			logger.Fatalf("could not set up tracing stackdriver exporter: %v", err)
		}
		trace.RegisterExporter(exporter)
	}
	trace.ApplyConfig(trace.Config{DefaultSampler: trace.ProbabilitySampler(0)})

	tracingWrapper := func(handler http.Handler) http.Handler {
		incomingSpanNamer := func(req *http.Request) string {
			return fmt.Sprintf("Recv.%s.%s: %s", serviceName, environmentName, req.URL.Path)
		}

		ocHandler := &ochttp.Handler{
			Propagation:    &propagation.HTTPFormat{},
			Handler:        handler,
			FormatSpanName: incomingSpanNamer,
		}
		return fixTracingHeader(ocHandler)
	}

	// Make the server with some sensible default timeouts.
	srv := http.Server{
		Addr:         listenAddr,
		Handler:      tracingWrapper(getRouter()),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  15 * time.Second,
	}

	// Handle graceful shutdown:
	// Listen for shutdown signals. If received, wait a few seconds (not during development)
	// so the upstream k8s service has taken the pod out of rotation and stops sending traffic,
	// then initiate the server shutdown with some timeout. The server will then finish in-flight
	// requests during that time, but not accept any new ones. Afterwards, exit the program.
	allConsClosed := make(chan struct{})
	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt, syscall.SIGTERM)
		defer func() {
			signal.Stop(sigint)
		}()
		<-sigint
		logger.Debugf("received shutdown signal")
		if !IsDevelopment() {
			time.Sleep(10 * time.Second)
		}
		logger.Debugf("server shutting down...")

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		if err := srv.Shutdown(ctx); err != nil {
			logger.Errorf("failed to shut down gracefully: %v", err)
		}
		close(allConsClosed)
	}()

	// Run server
	logger.Infof("server listening on %v", listenAddr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		logger.Fatalf("failed to start server: %v", err)
	}

	<-allConsClosed
	logger.Infof("server shut down cleanly")

}

// fixTracingHeader fixes the possibly-incompatible tracing header
// # See https://github.com/census-ecosystem/opencensus-go-exporter-stackdriver/pull/169
// # If span_id in the incoming header is a hexadecimal representation, convert it to integer for the go library
func fixTracingHeader(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpHeader := "X-Cloud-Trace-Context"
		if h := r.Header.Get(httpHeader); h != "" {

			// Parse the trace id field.
			slash := strings.Index(h, `/`)
			if slash != -1 {
				tracestr, h := h[:slash], h[slash+1:]

				// Parse the span id field.
				spanstr := h
				semicolon := strings.Index(h, `;`)
				if semicolon != -1 {
					spanstr, h = h[:semicolon], h[semicolon+1:]

					_, err := strconv.ParseUint(spanstr, 10, 64)
					// If integer parsing failed, it's hex -> decode it
					if err != nil {
						n, err := strconv.ParseUint(spanstr, 16, 64)
						if err == nil {
							spanstr = strconv.FormatUint(uint64(uint32(n)), 10)
						}
					}

					// Set the new header
					convertedHeader := fmt.Sprintf("%s/%s;%s", tracestr, spanstr, h)
					// incomingHeader := r.Header.Get(httpHeader)
					// logger.Infof("convertedHeader: (%++v) %++v", incomingHeader, convertedHeader)
					r.Header.Set(httpHeader, convertedHeader)
				}

			}
		}
		h.ServeHTTP(w, r)
	})
}
