package scholar

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Network state belongs to the shared scholar Client, not individual features.
// Neither PDFs nor publisher pages enter the metadata cache.
const cacheEntries = 128
const cacheBytes = 16 << 20

type responseData struct {
	data                  []byte
	contentType, finalURL string
	err                   error
}
type cachedResponse struct {
	responseData
	expires time.Time
}
type pendingResponse struct {
	done      chan struct{}
	result    responseData
	abandoned bool
}
type sourceGate struct {
	slot           chan struct{}
	next, cooldown time.Time
}
type networkState struct {
	mu      sync.Mutex
	gates   map[string]*sourceGate
	cache   map[string]cachedResponse
	pending map[string]*pendingResponse
	bytes   int
	// Overridden by local tests only, before any requests start.
	interval func(string) time.Duration
}

func (c *Client) network() *networkState {
	c.networkOnce.Do(func() {
		c.networkState = &networkState{gates: map[string]*sourceGate{}, cache: map[string]cachedResponse{}, pending: map[string]*pendingResponse{}, interval: sourceInterval}
	})
	return c.networkState
}

func sourceKey(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if host == "arxiv.org" || host == "export.arxiv.org" || host == "www.arxiv.org" {
		return "arxiv.org"
	}
	return strings.ToLower(u.Host)
}
func sourceInterval(host string) time.Duration {
	if host == "arxiv.org" {
		return 3 * time.Second
	}
	if host == "api.semanticscholar.org" {
		return 1100 * time.Millisecond
	}
	// Test fixture servers need no real-service delay.
	if strings.HasPrefix(host, "127.0.0.1:") || strings.HasPrefix(host, "[::1]:") {
		return 0
	}
	return 250 * time.Millisecond
}
func waitNetwork(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type requestError struct {
	host      string
	status    int
	retryAt   time.Time
	cause     error
	detail    string
	transient bool
}

func (e *requestError) Error() string {
	if !e.retryAt.IsZero() {
		return fmt.Sprintf("%s 暂时限流或不可用，请在 %d 秒后重试", e.host, max(1, int(math.Ceil(time.Until(e.retryAt).Seconds()))))
	}
	if e.detail != "" {
		return fmt.Sprintf("%s：%s", e.host, e.detail)
	}
	return fmt.Sprintf("%s 返回 %d", e.host, e.status)
}
func (e *requestError) Unwrap() error { return e.cause }
func (e *requestError) As(target any) bool {
	if p, ok := target.(*Error); ok {
		*p = Error{e.Error()}
		return true
	}
	return false
}

func networkFailure(host string, err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	var upstream *requestError
	if errors.As(err, &upstream) {
		return upstream
	}
	var local Error
	if errors.As(err, &local) {
		return local
	}
	detail := "连接中断，请检查网络或代理"
	transient := false
	var dns *net.DNSError

	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var op *net.OpError
	var ne net.Error
	switch {
	case errors.As(err, &unknown) || errors.As(err, &invalid) || errors.As(err, &hostname):
		detail = "TLS 证书校验失败，请检查系统时间或代理证书"
	case errors.As(err, &dns):
		detail = "DNS 解析失败"
		transient = dns.IsTimeout || dns.IsTemporary
	case errors.As(err, &ne) && ne.Timeout():
		detail = "连接或读取超时"
		transient = true
	case errors.As(err, &op):
		detail = "网络或代理连接失败"
		transient = true
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		transient = true
	}
	return &requestError{host: host, cause: err, detail: detail, transient: transient}
}

// HTTP-date and delta-seconds forms; malformed values do not disable the
// conservative fallback cooldown. Large values must not overflow Duration.
func retryAfter(value string, now time.Time) time.Time {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && seconds >= 0 {
		seconds = min(seconds, int64((365*24*time.Hour)/time.Second))
		return now.Add(time.Duration(seconds) * time.Second)
	}
	if date, err := http.ParseTime(value); err == nil && date.After(now) {
		return date
	}
	return time.Time{}
}

func (n *networkState) gate(key string) *sourceGate {
	// Bound state even when a publisher page supplies many different hosts.
	if n.gates[key] == nil && len(n.gates) >= 128 {
		key = "other"
	}
	g := n.gates[key]
	if g == nil {
		g = &sourceGate{slot: make(chan struct{}, 1)}
		n.gates[key] = g
	}
	return g
}
func (n *networkState) acquire(ctx context.Context, key, host string) (*sourceGate, func(), error) {
	n.mu.Lock()
	g := n.gate(key)
	cool := g.cooldown
	n.mu.Unlock()
	if time.Now().Before(cool) {
		return nil, nil, &requestError{host: host, status: 429, retryAt: cool}
	}
	select {
	case g.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	release := func() { <-g.slot }
	n.mu.Lock()
	cool = g.cooldown
	next := g.next
	n.mu.Unlock()
	if time.Now().Before(cool) {
		release()
		return nil, nil, &requestError{host: host, status: 429, retryAt: cool}
	}
	if err := waitNetwork(ctx, time.Until(next)); err != nil {
		release()
		return nil, nil, err
	}
	n.mu.Lock()
	g.next = time.Now().Add(n.interval(key))
	n.mu.Unlock()
	return g, release, nil
}

type gatedTransport struct {
	base   http.RoundTripper
	state  *networkState
	client *Client
}
type releasingBody struct {
	io.ReadCloser
	release func()
	once    sync.Once
}

func (b *releasingBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.release); return err }
func (t gatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.client.checkURL(req.URL); err != nil {
		return nil, err
	}
	g, release, err := t.state.acquire(req.Context(), sourceKey(req.URL), req.URL.Host)
	if err != nil {
		return nil, err
	}
	res, err := t.base.RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	if res.StatusCode == 429 || res.StatusCode == 503 {
		until := retryAfter(res.Header.Get("Retry-After"), time.Now())
		if res.StatusCode == 429 && (until.IsZero() || !until.After(time.Now())) {
			until = time.Now().Add(time.Minute)
		}
		if !until.IsZero() {
			t.state.mu.Lock()
			if until.After(g.cooldown) {
				g.cooldown = until
			}
			t.state.mu.Unlock()
		}
	}
	res.Body = &releasingBody{ReadCloser: res.Body, release: release}
	return res, nil
}

func (c *Client) requestOnce(ctx context.Context, raw, accept string, limit int64) responseData {
	u, err := url.Parse(raw)
	if err != nil {
		return responseData{err: Error{"链接无效"}}
	}
	if err = c.checkURL(u); err != nil {
		return responseData{err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return responseData{err: Error{"链接无效"}}
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en,zh;q=0.8")
	client := *c.HTTP
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = gatedTransport{base: base, state: c.network(), client: c}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return responseData{err: budgetFailure(u.Host, ctx.Err())}
		}
		return responseData{err: networkFailure(u.Host, err)}
	}
	defer res.Body.Close()
	host := res.Request.URL.Host
	if res.StatusCode == 404 || res.StatusCode == 410 {
		return responseData{err: ErrNotFound}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var cool time.Time
		n := c.network()
		n.mu.Lock()
		cool = n.gate(sourceKey(res.Request.URL)).cooldown
		n.mu.Unlock()
		if !cool.After(time.Now()) {
			cool = time.Time{}
		}
		return responseData{err: &requestError{host: host, status: res.StatusCode, retryAt: cool, transient: res.StatusCode == 502 || res.StatusCode == 503 || res.StatusCode == 504}}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		if ctx.Err() != nil {
			return responseData{err: budgetFailure(host, ctx.Err())}
		}
		return responseData{err: networkFailure(host, err)}
	}
	if int64(len(data)) > limit {
		return responseData{err: Error{fmt.Sprintf("文件超过 %d MB", limit>>20)}}
	}
	return responseData{data: data, contentType: res.Header.Get("Content-Type"), finalURL: res.Request.URL.String()}
}

func budgetFailure(host string, err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return &requestError{host: host, cause: err, detail: "请求超时，请稍后重试或检查代理"}
}

func (c *Client) request(ctx context.Context, raw, accept string, limit int64) responseData {
	var result responseData
	for attempt := 0; attempt < 3; attempt++ {
		started := time.Now()
		result = c.requestOnce(ctx, raw, accept, limit)
		logRequest(raw, attempt+1, started, result.err)
		var failure *requestError
		if result.err == nil || !errors.As(result.err, &failure) || !failure.transient || !failure.retryAt.IsZero() || attempt == 2 {
			return result
		}
		if err := waitNetwork(ctx, time.Duration(1<<attempt)*time.Second+time.Duration(rand.IntN(250))*time.Millisecond); err != nil {
			return responseData{err: err}
		}
	}
	return result
}

// Only host, status and error category enter diagnostics. Never record paper
// queries, URLs, response bodies, proxy credentials or Authorization headers.
func logRequest(raw string, attempt int, started time.Time, err error) {
	u, parseErr := url.Parse(raw)
	if parseErr != nil {
		return
	}
	status, category := 200, "ok"
	if err != nil {
		status, category = 0, "network"
		var failure *requestError
		if errors.As(err, &failure) {
			status = failure.status
			category = failure.detail
			if category == "" {
				category = "http"
			}
			if !failure.retryAt.IsZero() {
				category = "cooldown"
			}
		}
		if errors.Is(err, context.Canceled) {
			category = "canceled"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			category = "timeout"
		}
		if errors.Is(err, ErrNotFound) {
			status = 404
			category = "not_found"
		}
	}
	slog.Info("scholar.request", "host", u.Hostname(), "attempt", attempt, "durationMs", time.Since(started).Milliseconds(), "status", status, "category", category)
}

func cacheable(data []byte, accept string) bool {
	if len(data) > 4<<20 {
		return false
	}
	if accept == "application/json" {
		return json.Valid(data)
	}
	if accept == "application/atom+xml" {
		var feed struct {
			XMLName xml.Name `xml:"feed"`
		}
		return xml.Unmarshal(data, &feed) == nil && !strings.Contains(string(data), "/api/errors")
	}
	return false
}
func (n *networkState) store(key string, result responseData, accept string) {
	if result.err != nil || !cacheable(result.data, accept) {
		return
	}
	for k, v := range n.cache {
		if time.Now().After(v.expires) {
			delete(n.cache, k)
			n.bytes -= len(v.data)
		}
	}
	for len(n.cache) >= cacheEntries || n.bytes+len(result.data) > cacheBytes {
		oldest := ""
		var stamp time.Time
		for k, v := range n.cache {
			if oldest == "" || v.expires.Before(stamp) {
				oldest = k
				stamp = v.expires
			}
		}
		if oldest == "" {
			break
		}
		n.bytes -= len(n.cache[oldest].data)
		delete(n.cache, oldest)
	}
	n.cache[key] = cachedResponse{responseData: result, expires: time.Now().Add(15 * time.Minute)}
	n.bytes += len(result.data)
}

func (c *Client) get(ctx context.Context, raw, accept string, limit int64) ([]byte, string, string, error) {
	result := c.getResponse(ctx, raw, accept, limit)
	// Callers must not mutate bytes owned by a cache or another caller.
	if result.err == nil && (accept == "application/json" || accept == "application/atom+xml") {
		result.data = append([]byte(nil), result.data...)
	}
	return result.data, result.contentType, result.finalURL, result.err
}
func (c *Client) getResponse(ctx context.Context, raw, accept string, limit int64) responseData {
	if err := ctx.Err(); err != nil {
		return responseData{err: err}
	}
	if accept != "application/json" && accept != "application/atom+xml" {
		return c.request(ctx, raw, accept, limit)
	}
	key := raw + "\n" + accept + "\n" + strconv.FormatInt(limit, 10)
	n := c.network()
	for {
		n.mu.Lock()
		if cached, ok := n.cache[key]; ok && time.Now().Before(cached.expires) {
			n.mu.Unlock()
			return cached.responseData
		}
		if pending := n.pending[key]; pending != nil {
			n.mu.Unlock()
			select {
			case <-ctx.Done():
				return responseData{err: ctx.Err()}
			case <-pending.done:
			}
			if ctx.Err() != nil {
				return responseData{err: ctx.Err()}
			}
			if pending.abandoned {
				continue
			}
			return pending.result
		}
		if len(n.pending) >= 128 {
			n.mu.Unlock()
			return c.request(ctx, raw, accept, limit)
		}
		pending := &pendingResponse{done: make(chan struct{})}
		n.pending[key] = pending
		n.mu.Unlock()
		result := c.request(ctx, raw, accept, limit)
		n.mu.Lock()
		pending.result = result
		pending.abandoned = ctx.Err() != nil
		n.store(key, result, accept)
		delete(n.pending, key)
		close(pending.done)
		n.mu.Unlock()
		return result
	}
}
