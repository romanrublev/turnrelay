package vk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"

	"github.com/romanrublev/turnrelay/provider"
)

type fakeDoer struct {
	t           *testing.T
	calls       []string
	resp        map[string]string   // url path -> body
	seq         map[string][]string // url path -> bodies consumed in order, before resp
	status      map[string]int      // url path -> HTTP status, 200 when absent
	getHeaders  map[string]fhttp.Header
	postHeaders map[string]fhttp.Header
}

func (f *fakeDoer) Do(r *fhttp.Request) (*fhttp.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	f.calls = append(f.calls, r.URL.Host+r.URL.Path+"?"+string(body)+"&"+r.URL.RawQuery)
	key := r.URL.Host + r.URL.Path
	if r.Method == "GET" {
		if f.getHeaders == nil {
			f.getHeaders = map[string]fhttp.Header{}
		}
		f.getHeaders[key] = r.Header.Clone()
	} else {
		if f.postHeaders == nil {
			f.postHeaders = map[string]fhttp.Header{}
		}
		f.postHeaders[key] = r.Header.Clone()
	}
	q := string(body) + "&" + r.URL.RawQuery
	if strings.Contains(q, "method=vchat.joinConversationByLink") {
		key += "#join"
	} else if strings.Contains(q, "method=auth.anonymLogin") {
		key += "#login"
	}
	var b string
	var ok bool
	if q := f.seq[key]; len(q) > 0 {
		b, ok = q[0], true
		f.seq[key] = q[1:]
	} else {
		b, ok = f.resp[key]
	}
	if !ok {
		f.t.Fatalf("unexpected request %s", key)
	}
	code := f.status[key]
	if code == 0 {
		code = 200
	}
	return &fhttp.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(b))}, nil
}

func newTestClient(d Doer) *Client {
	// Legacy-path tests by default; VK Calls tests flip TryVKCalls on.
	return &Client{HTTP: d, Endpoints: DefaultEndpoints, Sleep: func(time.Duration) {}, Logf: func(string, ...any) {}}
}

func TestFetchHappyPath(t *testing.T) {
	d := &fakeDoer{t: t, resp: map[string]string{
		"login.vk.ru/":                             `{"data":{"access_token":"T1"}}`,
		"api.vk.ru/method/calls.getCallPreview":    `{"response":{}}`,
		"api.vk.ru/method/calls.getAnonymousToken": `{"response":{"token":"T2"}}`,
		"calls.okcdn.ru/fb.do#login":               `{"session_key":"T3"}`,
		"calls.okcdn.ru/fb.do#join":                `{"turn_server":{"username":"u1","credential":"p1","urls":["turn:155.212.200.1:3478?transport=udp","turn:155.212.200.1:3478?transport=tcp","turn:155.212.200.2:3478"]}}`,
	}}
	c := newTestClient(d)
	cred, err := c.Fetch(context.Background(), "AbCdEf123456")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Username != "u1" || cred.Password != "p1" {
		t.Fatalf("cred %+v", cred)
	}
	if len(cred.Relays) != 2 || cred.Relays[0] != "155.212.200.1:3478" || cred.Relays[1] != "155.212.200.2:3478" {
		t.Fatalf("relays %v", cred.Relays)
	}
	if cred.Relay(3) != "155.212.200.2:3478" {
		t.Fatal("round robin")
	}
	join := d.calls[len(d.calls)-1]
	for _, want := range []string{"anonymToken=T2", "session_key=T3", "joinLink=AbCdEf123456", "protocolVersion=5"} {
		if !strings.Contains(join, want) {
			t.Fatalf("join request lacks %s: %s", want, join)
		}
	}
	tok2 := d.calls[2]
	if !strings.Contains(tok2, "vk_join_link="+url.QueryEscape("https://vk.ru/call/join/AbCdEf123456")) {
		t.Fatalf("token2 request: %s", tok2)
	}
}

func TestFetchEscapesFormValues(t *testing.T) {
	d := &fakeDoer{t: t, resp: map[string]string{
		"login.vk.ru/":                             `{"data":{"access_token":"T1&x=y"}}`,
		"api.vk.ru/method/calls.getCallPreview":    `{"response":{}}`,
		"api.vk.ru/method/calls.getAnonymousToken": `{"response":{"token":"T2"}}`,
		"calls.okcdn.ru/fb.do#login":               `{"session_key":"T3"}`,
		"calls.okcdn.ru/fb.do#join":                `{"turn_server":{"username":"u1","credential":"p1","urls":["turn:155.212.200.1:3478"]}}`,
	}}
	c := newTestClient(d)
	if _, err := c.Fetch(context.Background(), "AbCdEf123456"); err != nil {
		t.Fatal(err)
	}
	tok2 := d.calls[2]
	if !strings.Contains(tok2, "access_token=T1%26x%3Dy") {
		t.Fatalf("access_token not escaped in hop3 body: %s", tok2)
	}
	if strings.Contains(tok2, "access_token=T1&x=y") {
		t.Fatalf("access_token leaked unescaped: %s", tok2)
	}
}

func TestFetchCaptcha(t *testing.T) {
	d := &fakeDoer{t: t, resp: map[string]string{
		"login.vk.ru/":                             `{"data":{"access_token":"T1"}}`,
		"api.vk.ru/method/calls.getCallPreview":    `{"response":{}}`,
		"api.vk.ru/method/calls.getAnonymousToken": `{"error":{"error_code":14,"error_msg":"Captcha needed","captcha_sid":"123","captcha_img":"https://id.vk.ru/captcha","redirect_uri":"https://id.vk.ru/not_robot?session_token=ST&x=1"}}`,
	}}
	_, err := newTestClient(d).Fetch(context.Background(), "AbCdEf123456")
	var ce *provider.CaptchaRequiredError
	if !errors.As(err, &ce) || !provider.IsCaptcha(err) || ce.SessionToken != "ST" || ce.Sid != "123" {
		t.Fatalf("want captcha error, got %v", err)
	}
}

func TestFetchAPIError(t *testing.T) {
	d := &fakeDoer{t: t, resp: map[string]string{
		"login.vk.ru/":                             `{"data":{"access_token":"T1"}}`,
		"api.vk.ru/method/calls.getCallPreview":    `{"response":{}}`,
		"api.vk.ru/method/calls.getAnonymousToken": `{"error":{"error_code":29,"error_msg":"Rate limit reached"}}`,
	}}
	_, err := newTestClient(d).Fetch(context.Background(), "AbCdEf123456")
	if err == nil || provider.IsCaptcha(err) || !strings.Contains(err.Error(), "29") {
		t.Fatalf("got %v", err)
	}
}

// TestFetchErrorsNeverCarryResponseValues: a hop-5 answer whose urls are all
// TCP fails, and the error must not quote the response (it holds the TURN
// username and credential, and the error lands in Stats.LastError and logs).
func TestFetchErrorsNeverCarryResponseValues(t *testing.T) {
	const user, pass, sess = "leak-user-7f3a", "leak-cred-91bc", "leak-session-55d0"
	d := &fakeDoer{t: t, resp: map[string]string{
		"login.vk.ru/":                             `{"data":{"access_token":"T1"}}`,
		"api.vk.ru/method/calls.getCallPreview":    `{"response":{}}`,
		"api.vk.ru/method/calls.getAnonymousToken": `{"response":{"token":"T2"}}`,
		"calls.okcdn.ru/fb.do#login":               `{"session_key":"` + sess + `"}`,
		"calls.okcdn.ru/fb.do#join":                `{"turn_server":{"username":"` + user + `","credential":"` + pass + `","urls":["turn:155.212.200.1:3478?transport=tcp","turn:155.212.200.2:3478?transport=tcp"]},"session_id":"S"}`,
	}}
	var logged []string
	c := newTestClient(d)
	c.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	_, err := c.Fetch(context.Background(), "AbCdEf123456")
	if err == nil {
		t.Fatal("tcp-only urls accepted")
	}
	text := err.Error() + "\n" + strings.Join(logged, "\n")
	for _, secret := range []string{user, pass, sess, "T1", "T2"} {
		if strings.Contains(text, secret) {
			t.Fatalf("error or log quotes response value %q: %s", secret, text)
		}
	}
	for _, want := range []string{"turn_server", "urls", "0 usable of 2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error lacks %q: %v", want, err)
		}
	}
}

// TestFetchMissingFieldReportsKeysOnly: a hop-1 body without the token names
// the keys it did have and nothing else.
func TestFetchMissingFieldReportsKeysOnly(t *testing.T) {
	d := &fakeDoer{t: t, resp: map[string]string{
		"login.vk.ru/": `{"data":{"other":"value-should-not-appear"},"error":"nope"}`,
	}}
	_, err := newTestClient(d).Fetch(context.Background(), "AbCdEf123456")
	if err == nil {
		t.Fatal("missing access_token accepted")
	}
	if strings.Contains(err.Error(), "value-should-not-appear") || strings.Contains(err.Error(), "nope") {
		t.Fatalf("error quotes response values: %v", err)
	}
	if !strings.Contains(err.Error(), "keys: data,error") || !strings.Contains(err.Error(), "data.access_token") {
		t.Fatalf("error does not name the keys present: %v", err)
	}
}

// TestFetchHTTPStatusError: a non-2xx answer is an error naming the status,
// whatever the body says.
func TestFetchHTTPStatusError(t *testing.T) {
	d := &fakeDoer{t: t,
		resp:   map[string]string{"login.vk.ru/": `{"data":{"access_token":"T1"}}`},
		status: map[string]int{"login.vk.ru/": 429},
	}
	_, err := newTestClient(d).Fetch(context.Background(), "AbCdEf123456")
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("want an error naming 429, got %v", err)
	}
	if strings.Contains(err.Error(), "T1") {
		t.Fatalf("error quotes the body: %v", err)
	}
}

// TestNewClientWithDialer: every VK API connection goes through the given
// dialer, which sees the unresolved host:port and whose error is what Fetch
// reports. Nothing reaches the network.
func TestNewClientWithDialer(t *testing.T) {
	boom := errors.New("detour down")
	var mu sync.Mutex
	var dialed []string
	c, err := NewClientWithDialer(func(_ context.Context, network, address string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, network+" "+address)
		mu.Unlock()
		return nil, boom
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Sleep = func(time.Duration) {}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = c.Fetch(ctx, "AbCdEf123456")
	if err == nil || !strings.Contains(err.Error(), boom.Error()) {
		t.Fatalf("want the dialer's error, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) == 0 {
		t.Fatal("dialer never called")
	}
	// The VK Calls path is tried first, so the first dial is api.vk.me;
	// every dial must go through the injected dialer to a VK host on 443.
	if dialed[0] != "tcp api.vk.me:443" {
		t.Fatalf("first dial %q, want tcp api.vk.me:443", dialed[0])
	}
	for _, d := range dialed {
		if !strings.HasSuffix(d, ":443") {
			t.Fatalf("dialed %q, want a VK host on :443", d)
		}
	}
	if _, err := NewClientWithDialer(nil); err != nil {
		t.Fatalf("nil dialer: %v", err)
	}
}

// TestRedactURLErrDropsQuery: a network error wrapping a request URL must not
// carry the query string, where every VK Calls token lives, into the returned
// error (it lands in Stats.LastError and the logs).
func TestRedactURLErrDropsQuery(t *testing.T) {
	inner := errors.New("connection reset by peer")
	full := "https://api.vk.me/method/auth.getAnonymToken?v=5.276&anonymous_token=SECRETTOKEN&device_id=abc"
	got := redactURLErr(&url.Error{Op: "Post", URL: full, Err: inner})
	if strings.Contains(got.Error(), "SECRETTOKEN") || strings.Contains(got.Error(), "auth.getAnonymToken?") {
		t.Fatalf("redacted error still leaks the query: %v", got)
	}
	if !strings.Contains(got.Error(), "api.vk.me") || !strings.Contains(got.Error(), inner.Error()) {
		t.Fatalf("redacted error dropped host or cause: %v", got)
	}
	// A non-URL error passes through untouched.
	if redactURLErr(inner) != inner {
		t.Fatal("non-url error should pass through unchanged")
	}
}

func TestIsVKHost(t *testing.T) {
	for _, h := range []string{"vk.com", "vk.ru", "id.vk.ru", "id.vk.com", "login.vk.ru", "VK.RU"} {
		if !isVKHost(h) {
			t.Fatalf("%q should be a VK host", h)
		}
	}
	for _, h := range []string{"evil.com", "vk.com.evil.com", "notvk.ru", "vkru", "", "vk.ru.attacker.net"} {
		if isVKHost(h) {
			t.Fatalf("%q must not be treated as a VK host", h)
		}
	}
}
