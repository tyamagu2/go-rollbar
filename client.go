// Copyright 2017 The go-rollbar Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rollbar

import (
	"bytes"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/pkg/errors"
	api "github.com/zchee/go-rollbar/api/v1"
	"golang.org/x/net/context"
	"golang.org/x/net/context/ctxhttp"
)

// Client represents a first Client methods.
type Client interface {
	Debug(error) Call
	Info(error) Call
	Error(error) Call
	Warn(error) Call
	Critical(error) Call
}

type client struct {
	debugClient    *httpClient
	infoClient     *httpClient
	errorClient    *httpClient
	warnClient     *httpClient
	criticalClient *httpClient
}

type httpClient struct {
	token        string
	client       *http.Client
	endpoint     string
	debug        bool
	logger       Logger
	environment  string
	platform     string
	codeVersion  string
	serverHost   string
	serverRoot   string
	serverBranch string
	stackskip    int
}

var defaultHTTPClient = httpClient{
	client:      http.DefaultClient,
	endpoint:    api.DefaultEndpoint,
	logger:      nilLogger{},
	environment: "development",
	platform:    runtime.GOOS,
	stackskip:   3, // default is 3
}

// Level level of stack trace.
type Level string

const (
	// DebugLevel logs are typically voluminous, and are usually disabled in production.
	DebugLevel Level = "debug"
	// InfoLevel is the default logging priority.
	InfoLevel Level = "info"
	// WarnLevel logs are more important than Info, but don't need individual human review.
	WarnLevel Level = "warning"
	// ErrorLevel logs are high-priority. If an application is running smoothly, it shouldn't generate any error-level logs.
	ErrorLevel Level = "error"
	// CriticalLevel logs are particularly important errors. In development the logger panics after writing the message.
	CriticalLevel Level = "critical"
)

// New creates a new REST rollbar API client.
//
// The `token` is required, other optional parameters can be passed using the
// various `With...` functions.
func New(token string, options ...Option) Client {
	cl := defaultHTTPClient
	cl.token = token
	if debug, err := strconv.ParseBool(os.Getenv("ROLLBAR_DEBUG")); err == nil && debug {
		cl.debug = debug
	}

	for _, o := range options {
		o(&cl)
	}
	if _, ok := cl.logger.(nilLogger); cl.debug && ok {
		cl.logger = traceLogger{os.Stderr}
	}
	if cl.serverHost == "" {
		cl.serverHost, _ = os.Hostname()
	}

	return &client{
		debugClient:    &cl,
		infoClient:     &cl,
		errorClient:    &cl,
		warnClient:     &cl,
		criticalClient: &cl,
	}
}

// payload creates the rollbar payload data.
func (c *httpClient) payload(level Level, err error) *api.Payload {
	title := "<nil>"
	if err != nil {
		title = err.Error()
	}
	stack := CreateStack(c.stackskip)

	data := &api.Data{
		Environment: c.environment,
		Body:        errorBody(err, stack),
		Level:       string(level),
		Timestamp:   time.Now().Unix(),
		Platform:    c.platform,
		CodeVersion: c.codeVersion,
		Language:    language,
		Server: &api.Server{
			Host:   c.serverHost,
			Root:   c.serverRoot,
			Branch: c.serverBranch,
		},
		Fingerprint: stack.Fingerprint(),
		Title:       title,
		Notifier: &api.Notifier{
			Name:    Name,
			Version: Version,
		},
	}

	return &api.Payload{
		AccessToken: c.token,
		Data:        data,
	}
}

// newRequest creates new http.Request from payload.
func (c *httpClient) newRequest(payload *api.Payload) (*http.Request, error) {
	if c.token == "" {
		return nil, errors.New("empty token")
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.Wrap(err, "failed to encode payload")
	}

	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, errors.Wrap(err, "failed to create new POST request")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)

	return req, nil
}

// Do posts payload to rollbar.
// The returns rollbar response into res.
func (c *httpClient) Do(ctx context.Context, req *http.Request, res *api.Response) error {
	resp, err := ctxhttp.Do(ctx, c.client, req)
	if err != nil {
		select {
		default:
		case <-ctx.Done():
			return ctx.Err()
		}
		return errors.Wrap(err, "failed to POST to rollbar")
	}

	defer func() {
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return errors.Errorf("received response: %s", resp.Status)
	}

	return c.parseResponse(ctx, resp.Body, res)
}

// parseResponse parses the rollbar API response.
func (c *httpClient) parseResponse(ctx context.Context, rdr io.Reader, resp *api.Response) error {
	if c.debug {
		buf := new(bytes.Buffer)
		io.Copy(buf, rdr)

		c.logger.Debugf(ctx, "-----> %s (response)\n", c.endpoint)
		var m map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			c.logger.Debugf(ctx, "failed to unmarshal payload: %v", err)
		} else {
			formatted, _ := json.MarshalIndent(m, "", "  ")
			c.logger.Debugf(ctx, "%s\n", formatted)
		}
		c.logger.Debugf(ctx, "<----- %s (response)\n", c.endpoint)
		rdr = buf
	}

	return json.NewDecoder(rdr).Decode(resp)
}
