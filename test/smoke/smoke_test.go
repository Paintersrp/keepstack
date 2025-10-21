package smoke_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/intstr"

	smoke "github.com/keepstack/keepstack/test/smoke"
)

func TestSmokeSuite(t *testing.T) {
	cfg := smoke.LoadConfig(t)
	scenario := newScenario(cfg)

	ctx := context.Background()

	t.Run("health and readiness", func(t *testing.T) {
		checkHealthEndpoint(t, ctx, cfg, "/healthz")
		checkHealthEndpoint(t, ctx, cfg, "/api/healthz")
	})

	t.Run("link crud and search", func(t *testing.T) {
		scenario.runLinkLifecycle(t, ctx)
	})

	t.Run("tag assignment and replacement", func(t *testing.T) {
		scenario.requireLink(t)
		scenario.runTagFlow(t, ctx)
	})

	t.Run("highlight verification", func(t *testing.T) {
		scenario.requireLink(t)
		scenario.runHighlightFlow(t, ctx)
	})

	t.Run("digest dry run", func(t *testing.T) {
		cfg.SkipUnlessTagged(t, "digest")
		scenario.runDigestDryRun(t, ctx)
	})

	t.Run("observability metrics", func(t *testing.T) {
		cfg.SkipUnlessTagged(t, "observability")
		scenario.runObservabilityChecks(t, ctx)
	})

	t.Run("backup job trigger", func(t *testing.T) {
		scenario.runBackupTrigger(t, ctx)
	})

	t.Run("resurfacer recommendations", func(t *testing.T) {
		cfg.SkipUnlessTagged(t, "resurfacer")
		scenario.runResurfacerChecks(t, ctx)
	})
}

type scenarioState struct {
	cfg *smoke.Config

	postPath   string
	getPath    string
	tagPath    string
	digestPath string

	linkURL   string
	linkTitle string
	query     string

	tagPrimaryName   string
	tagSecondaryName string
	tagExtraName     string

	highlightQuote string
	highlightNote  *string

	digestTransport string

	linkID string

	tagPrimaryID   int32
	tagSecondaryID int32
	tagExtraID     int32
}

func newScenario(cfg *smoke.Config) *scenarioState {
	runID := getenv("SMOKE_RUN_ID", fmt.Sprintf("smoke-%d", time.Now().UnixNano()))
	slug := fmt.Sprintf("%s-%d", runID, time.Now().UnixNano())

	note := getenvPtr("SMOKE_HIGHLIGHT_NOTE", fmt.Sprintf("Keepstack note for %s", slug))

	return &scenarioState{
		cfg:              cfg,
		postPath:         getenv("SMOKE_POST_PATH", "/api/links"),
		getPath:          getenv("SMOKE_GET_PATH", "/api/links"),
		tagPath:          getenv("SMOKE_TAG_PATH", "/api/tags"),
		digestPath:       getenv("SMOKE_DIGEST_PATH", "/api/digest/test"),
		linkURL:          getenv("SMOKE_LINK_URL", fmt.Sprintf("https://example.com/keepstack/%s", slug)),
		linkTitle:        getenv("SMOKE_LINK_TITLE", fmt.Sprintf("Keepstack Smoke %s", slug)),
		query:            getenv("SMOKE_QUERY", slug),
		tagPrimaryName:   getenv("SMOKE_TAG_NAME_PRIMARY", fmt.Sprintf("Smoke Primary %s", slug)),
		tagSecondaryName: getenv("SMOKE_TAG_NAME_SECONDARY", fmt.Sprintf("Smoke Secondary %s", slug)),
		tagExtraName:     getenv("SMOKE_TAG_NAME_EXTRA", fmt.Sprintf("Smoke Extra %s", slug)),
		highlightQuote:   getenv("SMOKE_HIGHLIGHT_QUOTE", fmt.Sprintf("Keepstack highlight for %s", slug)),
		highlightNote:    note,
		digestTransport:  getenv("SMTP_URL", "log://"),
	}
}

func (s *scenarioState) requireLink(t *testing.T) {
	t.Helper()
	if s.linkID == "" {
		t.Fatalf("link not yet created")
	}
}

func checkHealthEndpoint(t *testing.T, ctx context.Context, cfg *smoke.Config, path string) {
	t.Helper()
	status, body, err := cfg.DoJSON(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		t.Fatalf("GET %s failed: %v", path, err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET %s returned %d: %s", path, status, string(body))
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("health payload decode: %v -- %s", err, string(body))
	}
	if !strings.EqualFold(payload["status"], "ok") {
		t.Fatalf("health endpoint %s returned status=%q", path, payload["status"])
	}
}

func (s *scenarioState) runLinkLifecycle(t *testing.T, ctx context.Context) {
	payload := map[string]any{
		"url":   s.linkURL,
		"title": s.linkTitle,
	}

	status, body, err := s.cfg.DoJSON(ctx, http.MethodPost, s.postPath, nil, payload)
	if err != nil {
		t.Fatalf("create link failed: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("create link unexpected status %d: %s", status, string(body))
	}
	var linkResp linkResponse
	if err := json.Unmarshal(body, &linkResp); err != nil {
		t.Fatalf("decode link response: %v -- %s", err, string(body))
	}
	if linkResp.ID == "" {
		t.Fatalf("link response missing id: %s", string(body))
	}
	s.linkID = linkResp.ID

	err = s.cfg.Poll(ctx, s.cfg.PollInterval, s.cfg.PollTimeout, func(ctx context.Context) (bool, error) {
		query := url.Values{}
		query.Set("q", s.query)
		query.Set("limit", "5")
		status, body, err := s.cfg.DoJSON(ctx, http.MethodGet, s.getPath, query, nil)
		if err != nil {
			return false, err
		}
		if status != http.StatusOK {
			return false, fmt.Errorf("search returned %d: %s", status, string(body))
		}
		var listResp listLinksResponse
		if err := json.Unmarshal(body, &listResp); err != nil {
			return false, fmt.Errorf("decode search response: %w", err)
		}
		for _, item := range listResp.Items {
			if item.ID == s.linkID {
				return true, nil
			}
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("link search polling failed: %v", err)
	}
}

func (s *scenarioState) runTagFlow(t *testing.T, ctx context.Context) {
	primaryID, err := s.ensureTag(ctx, s.tagPrimaryName)
	if err != nil {
		t.Fatalf("ensure primary tag: %v", err)
	}
	secondaryID, err := s.ensureTag(ctx, s.tagSecondaryName)
	if err != nil {
		t.Fatalf("ensure secondary tag: %v", err)
	}
	extraID, err := s.ensureTag(ctx, s.tagExtraName)
	if err != nil {
		t.Fatalf("ensure extra tag: %v", err)
	}
	s.tagPrimaryID, s.tagSecondaryID, s.tagExtraID = primaryID, secondaryID, extraID

	query := url.Values{}
	query.Set("tags", fmt.Sprintf("%s,%s", s.tagPrimaryName, s.tagSecondaryName))
	query.Set("limit", "5")
	status, body, err := s.cfg.DoJSON(ctx, http.MethodGet, s.getPath, query, nil)
	if err != nil {
		t.Fatalf("pre-assignment tag query failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("pre-assignment tag query status %d: %s", status, string(body))
	}
	if present, err := linkPresent(body, s.linkID); err != nil {
		t.Fatalf("pre-assignment decode failed: %v -- %s", err, string(body))
	} else if present {
		t.Fatalf("link %s visible before tags applied", s.linkID)
	}

	assignBody := map[string]any{"tagIds": []int32{extraID}}
	status, body, err = s.cfg.DoJSON(ctx, http.MethodPost, fmt.Sprintf("%s/%s/tags", s.postPath, s.linkID), nil, assignBody)
	if err != nil {
		t.Fatalf("initial tag assignment failed: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("initial tag assignment status %d: %s", status, string(body))
	}
	if !tagsMatch(body, []int32{extraID}) {
		t.Fatalf("initial tag assignment payload mismatch: %s", string(body))
	}

	replaceBody := map[string]any{"tagIds": []int32{primaryID, secondaryID}}
	status, body, err = s.cfg.DoJSON(ctx, http.MethodPut, fmt.Sprintf("%s/%s/tags", s.postPath, s.linkID), nil, replaceBody)
	if err != nil {
		t.Fatalf("replace tags failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("replace tags status %d: %s", status, string(body))
	}
	if !tagsMatch(body, []int32{primaryID, secondaryID}) {
		t.Fatalf("replace tag payload mismatch: %s", string(body))
	}

	status, body, err = s.cfg.DoJSON(ctx, http.MethodPut, fmt.Sprintf("%s/%s/tags", s.postPath, s.linkID), nil, replaceBody)
	if err != nil {
		t.Fatalf("idempotent replace failed: %v", err)
	}
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("idempotent replace status %d: %s", status, string(body))
	}
	if !tagsMatch(body, []int32{primaryID, secondaryID}) {
		t.Fatalf("idempotent replace payload mismatch: %s", string(body))
	}

	status, body, err = s.cfg.DoJSON(ctx, http.MethodGet, s.getPath, query, nil)
	if err != nil {
		t.Fatalf("AND query failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("AND query status %d: %s", status, string(body))
	}
	if present, err := linkPresent(body, s.linkID); err != nil {
		t.Fatalf("AND query decode failed: %v -- %s", err, string(body))
	} else if !present {
		t.Fatalf("AND query missing link %s", s.linkID)
	}

	negative := url.Values{}
	negative.Set("tags", fmt.Sprintf("%s,%s,%s", s.tagPrimaryName, s.tagSecondaryName, s.tagExtraName))
	negative.Set("limit", "5")
	status, body, err = s.cfg.DoJSON(ctx, http.MethodGet, s.getPath, negative, nil)
	if err != nil {
		t.Fatalf("negative tag query failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("negative tag query status %d: %s", status, string(body))
	}
	if present, err := linkPresent(body, s.linkID); err != nil {
		t.Fatalf("negative query decode failed: %v -- %s", err, string(body))
	} else if present {
		t.Fatalf("link %s present when requiring third tag", s.linkID)
	}
}

func (s *scenarioState) runHighlightFlow(t *testing.T, ctx context.Context) {
	payload := map[string]any{
		"text": s.highlightQuote,
	}
	if s.highlightNote != nil {
		payload["note"] = *s.highlightNote
	}

	status, body, err := s.cfg.DoJSON(ctx, http.MethodPost, fmt.Sprintf("%s/%s/highlights", s.postPath, s.linkID), nil, payload)
	if err != nil {
		t.Fatalf("create highlight failed: %v", err)
	}
	if status != http.StatusCreated {
		t.Fatalf("create highlight status %d: %s", status, string(body))
	}
	var resp highlightResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode highlight response: %v -- %s", err, string(body))
	}
	if resp.ID == "" {
		t.Fatalf("highlight response missing id: %s", string(body))
	}

	query := url.Values{}
	query.Set("q", s.query)
	query.Set("limit", "5")
	status, body, err = s.cfg.DoJSON(ctx, http.MethodGet, s.getPath, query, nil)
	if err != nil {
		t.Fatalf("highlight verification search failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("highlight verification status %d: %s", status, string(body))
	}
	var list listLinksResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode highlight verification response: %v -- %s", err, string(body))
	}
	for _, item := range list.Items {
		if item.ID != s.linkID {
			continue
		}
		for _, highlight := range item.Highlights {
			if highlight.Text == s.highlightQuote {
				if s.highlightNote != nil && (highlight.Note == nil || *highlight.Note != *s.highlightNote) {
					t.Fatalf("highlight note mismatch: got %v want %v", highlight.Note, *s.highlightNote)
				}
				return
			}
		}
	}
	t.Fatalf("highlight %q not found for link %s", s.highlightQuote, s.linkID)
}

func (s *scenarioState) runDigestDryRun(t *testing.T, ctx context.Context) {
	payload := map[string]any{"transport": s.digestTransport}
	digestCtx, cancel := context.WithTimeout(ctx, s.cfg.DigestTimeout)
	defer cancel()

	status, body, err := s.cfg.DoJSON(digestCtx, http.MethodPost, s.digestPath, nil, payload)
	if err != nil {
		t.Fatalf("digest dry-run failed: %v", err)
	}
	switch status {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted:
	default:
		t.Fatalf("digest dry-run status %d: %s", status, string(body))
	}
	if !strings.Contains(string(body), "Keepstack Digest") {
		t.Fatalf("digest dry-run response missing marker: %s", string(body))
	}
}

func (s *scenarioState) runObservabilityChecks(t *testing.T, ctx context.Context) {
	kube := s.cfg.KubeOrSkip(t)

	selectorAPI := fmt.Sprintf("app.kubernetes.io/instance=%s,app.kubernetes.io/component=api", s.cfg.Release)
	selectorWorker := fmt.Sprintf("app.kubernetes.io/instance=%s,app.kubernetes.io/component=worker", s.cfg.Release)

	if err := s.cfg.EnsureServiceMonitorExists(ctx, kube, selectorAPI); err != nil {
		t.Fatalf("API ServiceMonitor missing: %v", err)
	}
	if err := s.cfg.EnsureServiceMonitorExists(ctx, kube, selectorWorker); err != nil {
		t.Fatalf("Worker ServiceMonitor missing: %v", err)
	}

	apiMetrics := s.scrapeMetrics(t, ctx, kube, selectorAPI, "http")
	if !strings.Contains(apiMetrics, "keepstack_api_http_requests_total") {
		t.Fatalf("API metrics missing keepstack_api_http_requests_total")
	}
	if !strings.Contains(apiMetrics, "keepstack_api_http_requests_non_2xx_total") {
		t.Fatalf("API metrics missing keepstack_api_http_requests_non_2xx_total")
	}

	workerMetrics := s.scrapeMetrics(t, ctx, kube, selectorWorker, "metrics")
	if !strings.Contains(workerMetrics, "keepstack_worker_jobs_processed_total") {
		t.Fatalf("Worker metrics missing keepstack_worker_jobs_processed_total")
	}
	if !strings.Contains(workerMetrics, "keepstack_worker_jobs_failed_total") {
		t.Fatalf("Worker metrics missing keepstack_worker_jobs_failed_total")
	}
}

func (s *scenarioState) runBackupTrigger(t *testing.T, ctx context.Context) {
	kube := s.cfg.KubeOrSkip(t)

	selector := fmt.Sprintf("app.kubernetes.io/instance=%s,app.kubernetes.io/component=backup", s.cfg.Release)
	cron, err := firstCronJob(ctx, kube, s.cfg.Namespace, selector)
	if err != nil {
		t.Fatalf("backup CronJob lookup failed: %v", err)
	}

	jobName := fmt.Sprintf("%s-%s", getenv("KS_BACKUP_JOB_PREFIX", "keepstack-backup-now"), time.Now().Format(getenv("KS_BACKUP_TIME_FORMAT", "%Y%m%d-%H%M%S")))
	job := jobFromCronTemplate(jobName, cron)

	created, err := kube.Client.BatchV1().Jobs(s.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create backup job failed: %v", err)
	}
	t.Cleanup(func() {
		_ = kube.Client.BatchV1().Jobs(s.cfg.Namespace).Delete(context.Background(), created.Name, metav1.DeleteOptions{})
	})

	timeout := durationEnv("KS_BACKUP_TIMEOUT", 10*time.Minute)
	if err := waitForJobCompletion(ctx, s.cfg, kube, created.Name, timeout); err != nil {
		t.Fatalf("backup job did not complete: %v", err)
	}

	if parseBoolEnv("KS_BACKUP_FOLLOW_LOGS", true) {
		if err := streamJobLogs(ctx, kube, s.cfg.Namespace, created.Name, t); err != nil {
			t.Fatalf("stream backup logs: %v", err)
		}
	}
}

func (s *scenarioState) runResurfacerChecks(t *testing.T, ctx context.Context) {
	kube := s.cfg.KubeOrSkip(t)

	selector := fmt.Sprintf("app.kubernetes.io/instance=%s,app.kubernetes.io/component=resurfacer", s.cfg.Release)
	cron, err := firstCronJob(ctx, kube, s.cfg.Namespace, selector)
	if err != nil {
		t.Fatalf("resurfacer CronJob lookup failed: %v", err)
	}

	jobName := fmt.Sprintf("%s-now-%d", cron.Name, time.Now().Unix())
	job := jobFromCronTemplate(jobName, cron)

	created, err := kube.Client.BatchV1().Jobs(s.cfg.Namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create resurfacer job failed: %v", err)
	}
	t.Cleanup(func() {
		_ = kube.Client.BatchV1().Jobs(s.cfg.Namespace).Delete(context.Background(), created.Name, metav1.DeleteOptions{})
	})

	timeout := durationEnv("KS_RESURF_TIMEOUT", 5*time.Minute)
	if err := waitForJobCompletion(ctx, s.cfg, kube, created.Name, timeout); err != nil {
		t.Fatalf("resurfacer job did not complete: %v", err)
	}

	if err := streamJobLogs(ctx, kube, s.cfg.Namespace, created.Name, t); err != nil {
		t.Fatalf("stream resurfacer logs: %v", err)
	}

	limit := intEnv("KS_RESURF_LIMIT", 5)
	query := url.Values{}
	query.Set("limit", strconv.Itoa(limit))
	status, body, err := s.cfg.DoJSON(ctx, http.MethodGet, "/api/recommendations", query, nil)
	if err != nil {
		t.Fatalf("fetch resurfacer recommendations failed: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("recommendations status %d: %s", status, string(body))
	}
	var resp recommendationsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode recommendations: %v -- %s", err, string(body))
	}
	if resp.Count <= 0 && len(resp.Items) == 0 {
		t.Fatalf("no resurfacer recommendations returned: %s", string(body))
	}
}

func (s *scenarioState) ensureTag(ctx context.Context, name string) (int32, error) {
	payload := map[string]any{"name": name}
	status, body, err := s.cfg.DoJSON(ctx, http.MethodPost, s.tagPath, nil, payload)
	if err != nil {
		return 0, err
	}
	switch status {
	case http.StatusOK, http.StatusCreated:
		var resp tagResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			return 0, fmt.Errorf("decode tag response: %w", err)
		}
		if resp.ID == 0 {
			return 0, fmt.Errorf("tag response missing id: %s", string(body))
		}
		return resp.ID, nil
	case http.StatusConflict:
		status, body, err = s.cfg.DoJSON(ctx, http.MethodGet, s.tagPath, nil, nil)
		if err != nil {
			return 0, fmt.Errorf("list tags after conflict: %w", err)
		}
		if status != http.StatusOK {
			return 0, fmt.Errorf("list tags status %d: %s", status, string(body))
		}
		var tags []tagResponse
		if err := json.Unmarshal(body, &tags); err != nil {
			return 0, fmt.Errorf("decode tag list: %w", err)
		}
		for _, tag := range tags {
			if strings.EqualFold(tag.Name, name) {
				return tag.ID, nil
			}
		}
		return 0, fmt.Errorf("tag %q not found after conflict", name)
	default:
		return 0, fmt.Errorf("unexpected tag status %d: %s", status, string(body))
	}
}

func (s *scenarioState) scrapeMetrics(t *testing.T, ctx context.Context, kube *smoke.Kube, selector, portName string) string {
	t.Helper()

	services, err := kube.Client.CoreV1().Services(s.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		t.Fatalf("list services failed: %v", err)
	}
	if len(services.Items) == 0 {
		t.Fatalf("no service found for selector %s", selector)
	}
	svc := services.Items[0]
	svcPort := chooseServicePort(&svc, portName)

	pods, err := kube.Client.CoreV1().Pods(s.cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		t.Fatalf("list pods failed: %v", err)
	}
	pod := pickRunningPod(t, pods.Items)
	if pod == nil {
		t.Fatalf("no running pod available for selector %s", selector)
	}

	remote, err := resolvePodPort(*pod, svcPort)
	if err != nil {
		t.Fatalf("resolve pod port: %v", err)
	}

	local, err := smoke.GetFreePort()
	if err != nil {
		t.Fatalf("allocate local port: %v", err)
	}

	handle, err := s.cfg.StartPortForward(ctx, kube, s.cfg.Namespace, pod.Name, local, remote)
	if err != nil {
		t.Fatalf("start port-forward: %v", err)
	}
	defer func() {
		if err := handle.Close(); err != nil {
			t.Fatalf("close port-forward: %v", err)
		}
	}()

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/metrics", local))
	if err != nil {
		t.Fatalf("fetch metrics: %v", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read metrics body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics endpoint returned %d: %s", resp.StatusCode, string(data))
	}
	return string(data)
}

func linkPresent(body []byte, linkID string) (bool, error) {
	var resp listLinksResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, err
	}
	for _, item := range resp.Items {
		if item.ID == linkID {
			return true, nil
		}
	}
	return false, nil
}

func tagsMatch(body []byte, expected []int32) bool {
	var resp linkTagsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return false
	}
	actual := make([]int32, 0, len(resp.Tags))
	for _, tag := range resp.Tags {
		actual = append(actual, tag.ID)
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i] < actual[j] })
	sort.Slice(expected, func(i, j int) bool { return expected[i] < expected[j] })
	if len(actual) != len(expected) {
		return false
	}
	for i := range actual {
		if actual[i] != expected[i] {
			return false
		}
	}
	return true
}

func pickRunningPod(t *testing.T, pods []corev1.Pod) *corev1.Pod {
	t.Helper()
	for i := range pods {
		if pods[i].Status.Phase == corev1.PodRunning {
			return &pods[i]
		}
	}
	return nil
}

func chooseServicePort(svc *corev1.Service, name string) corev1.ServicePort {
	for _, port := range svc.Spec.Ports {
		if port.Name == name {
			return port
		}
	}
	if len(svc.Spec.Ports) == 0 {
		return corev1.ServicePort{Port: 80, TargetPort: intstr.FromInt(80)}
	}
	return svc.Spec.Ports[0]
}

func resolvePodPort(pod corev1.Pod, svcPort corev1.ServicePort) (int, error) {
	if svcPort.TargetPort.Type == intstr.Int {
		return int(svcPort.TargetPort.IntVal), nil
	}
	if svcPort.TargetPort.Type == intstr.String && svcPort.TargetPort.StrVal != "" {
		for _, container := range pod.Spec.Containers {
			for _, port := range container.Ports {
				if port.Name == svcPort.TargetPort.StrVal {
					return int(port.ContainerPort), nil
				}
			}
		}
		return 0, fmt.Errorf("target port %q not found on pod %s", svcPort.TargetPort.StrVal, pod.Name)
	}
	return int(svcPort.Port), nil
}

func firstCronJob(ctx context.Context, kube *smoke.Kube, namespace, selector string) (*batchv1.CronJob, error) {
	list, err := kube.Client.BatchV1().CronJobs(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	if len(list.Items) == 0 {
		return nil, fmt.Errorf("no CronJob found for selector %s", selector)
	}
	return &list.Items[0], nil
}

func jobFromCronTemplate(name string, cron *batchv1.CronJob) *batchv1.Job {
	template := cron.Spec.JobTemplate.DeepCopy()
	labels := map[string]string{}
	for k, v := range template.Labels {
		labels[k] = v
	}
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: cron.Namespace,
			Labels:    labels,
		},
		Spec: template.Spec,
	}
}

func waitForJobCompletion(ctx context.Context, cfg *smoke.Config, kube *smoke.Kube, name string, timeout time.Duration) error {
	return cfg.Poll(ctx, 5*time.Second, timeout, func(ctx context.Context) (bool, error) {
		job, err := kube.Client.BatchV1().Jobs(cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		for _, condition := range job.Status.Conditions {
			if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
				return true, nil
			}
			if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
				return true, fmt.Errorf("job %s failed: %s", name, condition.Message)
			}
		}
		return false, nil
	})
}

func streamJobLogs(ctx context.Context, kube *smoke.Kube, namespace, jobName string, t *testing.T) error {
	pods, err := kube.Client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labels.Set{"job-name": jobName}.String()})
	if err != nil {
		return err
	}
	if len(pods.Items) == 0 {
		return fmt.Errorf("no pods found for job %s", jobName)
	}
	for _, pod := range pods.Items {
		req := kube.Client.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
		data, err := req.Stream(ctx)
		if err != nil {
			return err
		}
		buf, err := io.ReadAll(data)
		_ = data.Close()
		if err != nil {
			return err
		}
		t.Logf("logs for pod %s:\n%s", pod.Name, string(buf))
	}
	return nil
}

type linkResponse struct {
	ID         string              `json:"id"`
	Highlights []highlightResponse `json:"highlights"`
}

type listLinksResponse struct {
	Items []linkResponse `json:"items"`
}

type linkTagsResponse struct {
	Tags []tagResponse `json:"tags"`
}

type tagResponse struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

type highlightResponse struct {
	ID   string  `json:"id"`
	Text string  `json:"text"`
	Note *string `json:"note"`
}

type recommendationsResponse struct {
	Items []map[string]any `json:"items"`
	Count int              `json:"count"`
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvPtr(key, fallback string) *string {
	if v, ok := os.LookupEnv(key); ok {
		if v == "" {
			return nil
		}
		return &v
	}
	result := fallback
	return &result
}

func durationEnv(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func intEnv(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func parseBoolEnv(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		switch strings.ToLower(v) {
		case "true", "1", "yes", "y":
			return true
		case "false", "0", "no", "n":
			return false
		}
	}
	return fallback
}
