// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package datastreams

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/tinylib/msgp/msgp"
)

const (
	defaultHostname    = "localhost"
	defaultPort        = "8126"
	defaultAddress     = defaultHostname + ":" + defaultPort
	defaultHTTPTimeout = 2 * time.Second // defines the current timeout before giving up with the send process
)

var defaultDialer = &net.Dialer{
	Timeout:   30 * time.Second,
	KeepAlive: 30 * time.Second,
	DualStack: true,
}

var defaultClient = &http.Client{
	// We copy the transport to avoid using the default one, as it might be
	// augmented with tracing and we don't want these calls to be recorded.
	// See https://golang.org/pkg/net/http/#DefaultTransport .
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           defaultDialer.DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
	Timeout: defaultHTTPTimeout,
}

type httpTransport struct {
	url     string            // the delivery URL for stats
	client  *http.Client      // the HTTP client used in the POST
	headers map[string]string // the Transport headers
}

func newHTTPTransport(addr string, site string, apiKey string, client *http.Client, agentLess bool) *httpTransport {
	// initialize the default EncoderPool with Encoder headers
	defaultHeaders := map[string]string{
		"Datadog-Meta-Lang":             "go",
		"Datadog-Meta-Lang-Version":     strings.TrimPrefix(runtime.Version(), "go"),
		"Datadog-Meta-Lang-Interpreter": runtime.Compiler + "-" + runtime.GOARCH + "-" + runtime.GOOS,
		"Content-Type":                  "application/msgpack",
		"Content-Encoding":              "gzip",
	}
	if cid := ContainerID(); cid != "" {
		defaultHeaders["Datadog-Container-ID"] = cid
	}
	var url string
	if agentLess {
		defaultHeaders["DD-API-KEY"] = apiKey
		url = fmt.Sprintf("https://trace.agent.%s/api/v0.1/pipeline_stats", site)
	} else {
		url = fmt.Sprintf("http://%s/v0.1/pipeline_stats", addr)
	}
	return &httpTransport{
		url:     url,
		client:  client,
		headers: defaultHeaders,
	}
}

func (t *httpTransport) sendPipelineStats(p *StatsPayload) error {
	var buf bytes.Buffer
	gzipWriter, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if err := msgp.Encode(gzipWriter, p); err != nil {
		return err
	}
	err = gzipWriter.Close()
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", t.url, &buf)
	if err != nil {
		return err
	}
	for header, value := range t.headers {
		req.Header.Set(header, value)
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	if code := resp.StatusCode; code >= 400 {
		// error, check the body for context information and
		// return a nice error.
		msg := make([]byte, 1000)
		n, _ := resp.Body.Read(msg)
		resp.Body.Close()
		txt := http.StatusText(code)
		if n > 0 {
			return fmt.Errorf("%s (Status: %s)", msg[:n], txt)
		}
		return fmt.Errorf("%s", txt)
	}
	return nil
}
