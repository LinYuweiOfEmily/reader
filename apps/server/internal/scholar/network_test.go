package scholar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func networkFixture(t *testing.T, handler http.HandlerFunc) (*Client, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := New(1 << 20)
	c.AllowPrivateHosts = true
	return c, server.URL
}

func TestNetworkCoalescesAndCachesMetadata(t *testing.T) {
	var hits atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(entered)
		}
		<-release
		fmt.Fprint(w, `{"title":"paper"}`)
	})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, _, err := c.get(context.Background(), u, "application/json", 1024); err != nil {
				t.Error(err)
			}
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	_, _, _, err := c.get(context.Background(), u, "application/json", 1024)
	if err != nil || hits.Load() != 1 {
		t.Fatalf("requests=%d err=%v", hits.Load(), err)
	}
}

func TestNetworkHonorsSharedCooldown(t *testing.T) {
	var hits atomic.Int32
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	})
	for _, path := range []string{"/one", "/two"} {
		_, _, _, err := c.get(context.Background(), u+path, "application/json", 1024)
		if err == nil || !strings.Contains(err.Error(), "60") {
			t.Errorf("missing cooldown: %v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("cooldown sent %d requests", hits.Load())
	}
}

func TestNetworkRetriesTransientStatus(t *testing.T) {
	var hits atomic.Int32
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{"ok":true}`)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _, err := c.get(ctx, u, "application/json", 1024)
	if err != nil || hits.Load() != 2 {
		t.Fatalf("requests=%d err=%v", hits.Load(), err)
	}
}

func TestNetworkDoesNotCachePDFOrErrors(t *testing.T) {
	for _, status := range []int{200, 403} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var hits atomic.Int32
			c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				w.WriteHeader(status)
				w.Write([]byte(pdfBytes))
			})
			for i := 0; i < 2; i++ {
				c.get(context.Background(), u, "application/pdf", 1024)
			}
			if hits.Load() != 2 {
				t.Fatalf("requests=%d", hits.Load())
			}
		})
	}
}

func TestNetworkCanceledWaiterDoesNotCancelOwner(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var hits atomic.Int32
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(entered)
		}
		<-release
		fmt.Fprint(w, `{"ok":true}`)
	})
	done := make(chan error, 1)
	go func() { _, _, _, err := c.get(context.Background(), u, "application/json", 1024); done <- err }()
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := c.get(ctx, u, "application/json", 1024)
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter: %v", err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatalf("requests=%d", hits.Load())
	}
}

func TestArxivRateLimitDoesNotDownloadOtherVersion(t *testing.T) {
	var hits atomic.Int32
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(429) })
	c.ArXivPDF = u + "/"
	_, err := c.arxivPDF(context.Background(), "1706.03762v7")
	if err == nil || hits.Load() != 1 {
		t.Fatalf("requests=%d err=%v", hits.Load(), err)
	}
}

func TestNetworkSerializesAndSpacesDistinctRequests(t *testing.T) {
	var mu sync.Mutex
	var times []time.Time
	var active, peak atomic.Int32
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		fmt.Fprint(w, `{}`)
	})
	c.network().interval = func(string) time.Duration { return 40 * time.Millisecond }
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, _, err := c.get(context.Background(), fmt.Sprintf("%s/%d", u, i), "application/json", 1024)
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if peak.Load() != 1 || len(times) != 3 {
		t.Fatalf("peak=%d times=%d", peak.Load(), len(times))
	}
	for i := 1; i < len(times); i++ {
		if times[i].Sub(times[i-1]) < 30*time.Millisecond {
			t.Fatalf("requests too close: %v", times)
		}
	}
}

func TestNetworkQueueCancellation(t *testing.T) {
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{}`) })
	c.network().interval = func(string) time.Duration { return time.Minute }
	c.get(context.Background(), u+"/first", "application/json", 1024)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, _, err := c.get(ctx, u+"/second", "application/json", 1024)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestNetworkRetryAfterDateAndRedirect(t *testing.T) {
	var hits atomic.Int32
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/blocked", 302)
			return
		}
		hits.Add(1)
		w.Header().Set("Retry-After", time.Now().Add(time.Minute).UTC().Format(http.TimeFormat))
		w.WriteHeader(503)
	})
	for _, path := range []string{"/start", "/blocked"} {
		_, _, _, err := c.get(context.Background(), u+path, "application/json", 1024)
		if err == nil {
			t.Fatal("missing error")
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("cooldown bypassed after redirect: %d", hits.Load())
	}
}

func TestNetworkCacheLimitsExpiryAndIsolation(t *testing.T) {
	c := New(1024)
	n := c.network()
	payload := []byte(`{"data":"` + strings.Repeat("x", 1<<20) + `"}`)
	for i := 0; i < 20; i++ {
		n.store(fmt.Sprint(i), responseData{data: payload}, "application/json")
	}
	if n.bytes > cacheBytes || len(n.cache) > cacheEntries {
		t.Fatalf("unbounded cache bytes=%d count=%d", n.bytes, len(n.cache))
	}
	for k, v := range n.cache {
		v.expires = time.Now().Add(-time.Second)
		n.cache[k] = v
	}
	n.store("new", responseData{data: []byte(`{}`)}, "application/json")
	if n.bytes != 2 || len(n.cache) != 1 {
		t.Fatalf("expired entries retained: %d %d", n.bytes, len(n.cache))
	}
	var hits atomic.Int32
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); fmt.Fprint(w, `{"ok":true}`) })
	b, _, _, _ := c.get(context.Background(), u, "application/json", 1024)
	b[0] = '!'
	b, _, _, _ = c.get(context.Background(), u, "application/json", 1024)
	if b[0] != '{' || hits.Load() != 1 {
		t.Fatal("cache bytes leaked to caller")
	}
	_, _, _, err := c.get(context.Background(), u, "application/json", 2)
	if err == nil {
		t.Fatal("cache bypassed response size limit")
	}
}

func TestNetworkCanceledOwnerAllowsFollower(t *testing.T) {
	entered := make(chan struct{})
	var hits atomic.Int32
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(entered)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{}`)
	})
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, _, _, err := c.get(ctx, u, "application/json", 1024); first <- err }()
	<-entered
	second := make(chan error, 1)
	go func() { _, _, _, err := c.get(context.Background(), u, "application/json", 1024); second <- err }()
	cancel()
	if !errors.Is(<-first, context.Canceled) {
		t.Fatal("owner not canceled")
	}
	select {
	case err := <-second:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("follower hung")
	}
	if hits.Load() != 2 {
		t.Fatalf("requests=%d", hits.Load())
	}
}

func TestNetworkRetriesHeaderTimeoutWithinBudget(t *testing.T) {
	var hits atomic.Int32
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{}`)
	})
	c.HTTP.Transport.(*http.Transport).ResponseHeaderTimeout = 30 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	_, _, _, err := c.get(ctx, u, "application/json", 1024)
	if err != nil || hits.Load() != 2 {
		t.Fatalf("requests=%d err=%v", hits.Load(), err)
	}
}

type networkRoundTripFunc func(*http.Request) (*http.Response, error)

func (f networkRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNetworkFollowersShareExhaustedTimeout(t *testing.T) {
	c := New(1024)
	var hits atomic.Int32
	entered := make(chan struct{})
	c.HTTP.Transport = networkRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if hits.Add(1) == 1 {
			close(entered)
		}
		return nil, context.DeadlineExceeded
	})
	first, second := make(chan error, 1), make(chan error, 1)
	go func() {
		_, _, _, err := c.get(context.Background(), "https://example.org/metadata", "application/json", 1024)
		first <- err
	}()
	<-entered
	go func() {
		_, _, _, err := c.get(context.Background(), "https://example.org/metadata", "application/json", 1024)
		second <- err
	}()
	if !errors.Is(<-first, context.DeadlineExceeded) || !errors.Is(<-second, context.DeadlineExceeded) {
		t.Fatal("lost timeout")
	}
	if hits.Load() != 3 {
		t.Fatalf("followers started new retries: %d", hits.Load())
	}
}

func TestNetworkRejectsMalformedMetadataCache(t *testing.T) {
	var hits atomic.Int32
	c, u := networkFixture(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, "<html>Temporary error</html>")
	})
	for i := 0; i < 2; i++ {
		c.get(context.Background(), u, "application/json", 1024)
	}
	if hits.Load() != 2 {
		t.Fatal("cached invalid API response")
	}
}
