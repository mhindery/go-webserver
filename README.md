# Generic Go HTTP server on k8s

## What is it

This is a small web server created for experimenting with istio on k8s. I needed a simple service which I could deploy with some simple topologies and configurations that talk to each other, in order to inspect istio's (traffic management, monitoring) features. The existing examples (BookStore, Isotope) were quite convoluted and I couldn't modify their behaviour easily. So, I put this one together to use; it's a very simple server written in Go. As it has some generic functionality which you nearly always need for any Go HTTP server anyways, I decided to put it here, so it can be re-used as a start for future projects needing a go webserver on k8s.

The service itself has a few endpoints:
- `/_ah/health/` and `/_ah/ready/`: return just an empty HTTP 200 response.
- `/`: will return a json with information about the incomfing request (headers, params) and itself (labels, environment).
- `/call/?url=<service>`: will call the provided url (expecting a json response) and return the same json as the previous endpoint with an additional entry for the called service.

The server has the following generic features on it:
- (structured) logging with a configurable log level to stderr / stdout (via [Zap](https://github.com/uber-go/zap))
- skeleton to easily add middleware via the well-known adapter method (see [this post describing the pattern](https://medium.com/@matryer/writing-middleware-in-golang-and-how-go-makes-it-so-much-fun-4375c1246e81)). The included middleware is provided for request timeouts and access logging.
- access logging in Apache format.
- separate liveness server and endpoints to be used with k8s health/readiness probes. (see [this Medium post explaining why](https://medium.com/over-engineering/graceful-shutdown-with-go-http-servers-and-kubernetes-rolling-updates-6697e7db17cf))
- graceful shutdown handling.
- propagation of tracing headers (via [OpenCensus](https://github.com/census-instrumentation/opencensus-go) using the GoogleCloudFormat header propagation).
- sensible defaults for timeouts on the server and a client for outgoing requests.

Thus, the server can easily be re-used as a starting point, avoiding having to re-implement the boilerplate for the features above. Just copy this one and add your own handler functions.

There is no fancy structure with packages and modules, as there isn't any need for it here. It has one `main` package with 3 files to have a little bit separation / overview; one with the endpoints implementations, one with the middleware stuff, and one `main.go` to do setup and link everything together.

## Quick start

Install Go (1.11+), e.g.:

```bash
brew install go
```

Run the server:

```bash
cd cmd/api
go run *.go
```

To run with the log level increased to debug:

```bash
go run *.go -log -1
```

The port at which the server is listening can also be changed via a flag:

```bash
go run *.go -log -1 -listen-addr ":80"
```

With the command from above running, the server is listening, you can go to [http://localhost:80/](http://localhost:80/) or [http://localhost:80/call/?url=http%3A%2F%2Fdate.jsontest.com%2F](http://localhost:80/call/?url=http%3A%2F%2Fdate.jsontest.com%2F).

The service will have these logs (after two requests `ctrl+c` is used):

```bash
Mathieus-MacBook-Pro:api mhindery$ go run *.go -log -1 -listen-addr ":80"
2019-07-13T14:21:31.670+0200    DEBUG   api/main.go:59  liveness server listening on :9000
2019-07-13T14:21:31.670+0200    INFO    api/main.go:192 server listening on :80
2019-07-13T14:21:42.109+0200    INFO    api/middleware.go:51    [::1]:60217 - - [13/Jul/2019:12:21:42] "GET /call/?url=http%3A%2F%2Fdate.jsontest.com%2F HTTP/1.1" 200 3628 217
2019-07-13T14:21:46.479+0200    INFO    api/middleware.go:51    [::1]:60217 - - [13/Jul/2019:12:21:46] "GET / HTTP/1.1" 200 3380 0
^C2019-07-13T14:21:51.411+0200  DEBUG   api/main.go:176 received shutdown signal
2019-07-13T14:21:51.411+0200    DEBUG   api/main.go:180 server shutting down...
2019-07-13T14:21:51.411+0200    INFO    api/main.go:198 server shut down cleanly
2019-07-13T14:21:51.411+0200    DEBUG   api/main.go:69  liveness server shutting down...
2019-07-13T14:21:51.411+0200    DEBUG   api/main.go:76  liveness server shut down cleanly
Mathieus-MacBook-Pro:api mhindery$ 
```


## Build into docker container

From the root of the repo:

```bash
docker build -f Dockerfile -t <desired_tag> .
```

Run it:

```bash
docker run -it --rm -p 8282:8282 -p 9000:9000 <desired_tag> /app/api -log -1
```

Note that now the `DEVELOPMENT` env variable is not set, so the logger will output in structured format, and upon sending the shutdown signal, it will wait 10 seconds before shutting down.

## Kubernetes service and deployment

The `k8s_config` contains a deployment and service yaml for the server. The deployment contains the directive to pass the labels and pod information through to the pod as environment variables as discussed on [the k8s docs](https://kubernetes.io/docs/tasks/inject-data-application/downward-api-volume-expose-pod-information/). It also has health / readiness probes defined, and a podAntiAffinity to spread out pods over nodes. Other than that, they are very standard yamls.