package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// Config aggregates the environment-driven knobs required by the smoke suite.
type Config struct {
	BaseURL       string
	HTTPClient    *http.Client
	PollInterval  time.Duration
	PollTimeout   time.Duration
	DigestTimeout time.Duration

	Namespace string
	Release   string

	Tags map[string]struct{}

	kube      *Kube
	kubeError error
}

// Kube bundles Kubernetes clients used by the suite.
type Kube struct {
	Config  *rest.Config
	Client  kubernetes.Interface
	Dynamic dynamic.Interface
}

// LoadConfig builds a Config based on environment variables and sensible defaults.
func LoadConfig(tb testing.TB) *Config {
	tb.Helper()

	timeout := durationFromEnv("SMOKE_TIMEOUT", 15*time.Second)
	pollInterval := durationFromEnv("SMOKE_POLL_INTERVAL", 2*time.Second)
	pollTimeout := durationFromEnv("SMOKE_POLL_TIMEOUT", 60*time.Second)
	digestTimeout := durationFromEnv("SMOKE_DIGEST_TIMEOUT", 20*time.Second)

	client := &http.Client{Timeout: timeout}

	cfg := &Config{
		BaseURL:       getEnv("SMOKE_BASE_URL", "http://keepstack.localtest.me:18080"),
		HTTPClient:    client,
		PollInterval:  pollInterval,
		PollTimeout:   pollTimeout,
		DigestTimeout: digestTimeout,
		Namespace:     getEnv("KS_NAMESPACE", "keepstack"),
		Release:       getEnv("KS_RELEASE", "keepstack"),
		Tags:          parseTags(os.Getenv("SMOKE_TAGS")),
	}

	kube, err := newKube()
	if err != nil {
		cfg.kubeError = err
	} else {
		cfg.kube = kube
	}

	return cfg
}

func (c *Config) HasTag(tag string) bool {
	_, ok := c.Tags[strings.TrimSpace(strings.ToLower(tag))]
	return ok
}

// SkipUnlessTagged skips the calling test when the required tag is absent.
func (c *Config) SkipUnlessTagged(t *testing.T, tag string) {
	t.Helper()
	if !c.HasTag(tag) {
		t.Skipf("skipping %s; enable by including tag %q in SMOKE_TAGS", t.Name(), tag)
	}
}

// KubeOrSkip returns the Kubernetes clients or skips the test if they are unavailable.
func (c *Config) KubeOrSkip(t *testing.T) *Kube {
	t.Helper()
	if c.kube != nil {
		return c.kube
	}
	if c.kubeError != nil {
		t.Skipf("skipping %s; unable to initialise kubernetes client: %v", t.Name(), c.kubeError)
	}
	t.Skipf("skipping %s; kubernetes client not configured", t.Name())
	return nil
}

// Poll executes fn until it returns true or the timeout elapses.
func (c *Config) Poll(ctx context.Context, interval, timeout time.Duration, fn func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		done, err := fn(ctx)
		if done {
			return err
		}
		if err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("poll timeout after %s", timeout)
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// DoJSON issues an HTTP request against the Keepstack API returning the status code and response body.
func (c *Config) DoJSON(ctx context.Context, method, p string, query url.Values, body any) (int, []byte, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid base url %q: %w", c.BaseURL, err)
	}

	rel := &url.URL{Path: p, RawQuery: query.Encode()}
	target := base.ResolveReference(rel)

	var reader io.Reader
	var contentType string
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal request body: %w", err)
		}
		reader = strings.NewReader(string(payload))
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return 0, nil, fmt.Errorf("create request: %w", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read response: %w", err)
	}
	return resp.StatusCode, data, nil
}

// StartPortForward establishes a port-forward to the provided pod and returns a handle to stop it.
func (c *Config) StartPortForward(ctx context.Context, kube *Kube, namespace, pod string, localPort, remotePort int) (*PortForwardHandle, error) {
	transport, upgrader, err := spdy.RoundTripperFor(kube.Config)
	if err != nil {
		return nil, fmt.Errorf("build port-forward transport: %w", err)
	}

	hostURL, err := url.Parse(kube.Config.Host)
	if err != nil {
		return nil, fmt.Errorf("parse kubernetes host: %w", err)
	}
	hostURL.Path = path.Join("api", "v1", "namespaces", namespace, "pods", pod, "portforward")

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport, Timeout: kube.Config.Timeout}, "POST", hostURL)

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	errCh := make(chan error, 1)

	ports := []string{fmt.Sprintf("%d:%d", localPort, remotePort)}
	pf, err := portforward.New(dialer, ports, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("start port-forward: %w", err)
	}

	go func() {
		err := pf.ForwardPorts()
		errCh <- err
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		close(stopCh)
		return nil, fmt.Errorf("port-forward context cancelled: %w", ctx.Err())
	case err := <-errCh:
		return nil, fmt.Errorf("port-forward error: %w", err)
	case <-readyCh:
	}

	return &PortForwardHandle{stopCh: stopCh, errCh: errCh, once: &sync.Once{}}, nil
}

// PortForwardHandle manages lifecycle of a port forward.
type PortForwardHandle struct {
	stopCh chan struct{}
	errCh  <-chan error
	once   *sync.Once
}

// Close terminates the port-forward session.
func (h *PortForwardHandle) Close() error {
	if h == nil {
		return nil
	}
	h.once.Do(func() {
		close(h.stopCh)
	})
	if h.errCh != nil {
		if err, ok := <-h.errCh; ok && err != nil {
			return err
		}
	}
	return nil
}

// GetFreePort discovers an available local TCP port.
func GetFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func newKube() (*Kube, error) {
	restConfig, err := loadKubeConfig()
	if err != nil {
		return nil, err
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	return &Kube{Config: restConfig, Client: clientset, Dynamic: dynamicClient}, nil
}

func loadKubeConfig() (*rest.Config, error) {
	loadingRules := &clientcmd.ClientConfigLoadingRules{}
	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, overrides)

	if restConfig, err := clientConfig.ClientConfig(); err == nil {
		return restConfig, nil
	}

	if restConfig, err := rest.InClusterConfig(); err == nil {
		return restConfig, nil
	} else {
		return nil, err
	}
}

func parseTags(raw string) map[string]struct{} {
	tags := map[string]struct{}{}
	for _, tag := range strings.Split(raw, ",") {
		tag = strings.TrimSpace(strings.ToLower(tag))
		if tag != "" {
			tags[tag] = struct{}{}
		}
	}
	return tags
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationFromEnv(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// EnsureServiceMonitorExists validates that a ServiceMonitor with the provided selector is present.
func (c *Config) EnsureServiceMonitorExists(ctx context.Context, kube *Kube, selector string) error {
	gvr := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "servicemonitors"}
	list, err := kube.Dynamic.Resource(gvr).Namespace(c.Namespace).List(ctx, v1.ListOptions{LabelSelector: selector})
	if err != nil {
		return err
	}
	if len(list.Items) == 0 {
		return fmt.Errorf("no ServiceMonitor found for selector %q", selector)
	}
	return nil
}
