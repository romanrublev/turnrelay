// Package vk obtains TURN credentials from a VK Calls link, the way the VK
// web client does it (anonymous join). Chain and app ids follow
// cacggghp/vk-turn-proxy (GPL-3.0).
package vk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	neturl "net/url"
	"sort"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/google/uuid"

	"github.com/romanrublev/turnrelay/provider"
)

type Credential = provider.Credential

type Doer interface {
	Do(*fhttp.Request) (*fhttp.Response, error)
}

type Endpoints struct{ Login, API, OK string }

var DefaultEndpoints = Endpoints{
	Login: "https://login.vk.ru/?act=get_anonym_token",
	API:   "https://api.vk.ru/method/",
	OK:    "https://calls.okcdn.ru/fb.do",
}

type app struct{ id, secret string }

var apps = []app{
	{"6287487", "QbYic1K3lEV5kTGiqlq2"},  // VK_WEB_APP_ID
	{"7879029", "aR5NKGmm03GYrCiNKsaw"},  // VK_MVK_APP_ID
	{"52461373", "o557NLIkAErNhakXrQ7A"}, // VK_WEB_VKVIDEO_APP_ID
	{"52649896", "WStp4ihWG4l3nmXZgIbC"}, // VK_MVK_VKVIDEO_APP_ID
	{"51781872", "IjjCNl4L4Tf5QZEXIHKK"}, // VK_ID_AUTH_APP
}

const (
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	secChUA   = `"Not(A:Brand";v="99", "Google Chrome";v="146", "Chromium";v="146"`
	okAppKey  = "CGMMEJLGDIHBABABA"
)

type Client struct {
	HTTP      Doer
	Endpoints Endpoints
	Sleep     func(time.Duration)
	Logf      func(string, ...any)
	// AutoCaptcha solves VK's proof-of-work captcha in place (see
	// captcha_pow.go) up to CaptchaAttempts times before giving up.
	AutoCaptcha     bool
	CaptchaAttempts int
}

const defaultCaptchaAttempts = 2

// defaultResolver is shared by every client so cached answers survive
// credential refreshes.
var defaultResolver = newRacingResolver(publicResolvers, nil)

// NewClient builds the VK API client with the Chrome TLS fingerprint and a
// dialer that resolves names through public resolvers.
func NewClient() (*Client, error) {
	return NewClientWithDialer(nil)
}

// NewClientWithDialer is NewClient with every TCP connection to the VK API
// opened by dial (network "tcp", address host:port, name unresolved). The
// racing public resolver of NewClient is then not used: resolving the name
// is dial's job, which is what a sing-box detour or bind_interface dialer
// expects. A nil dial is NewClient.
func NewClientWithDialer(dial func(ctx context.Context, network, address string) (net.Conn, error)) (*Client, error) {
	opts := []tlsclient.HttpClientOption{
		tlsclient.WithTimeoutSeconds(20),
		tlsclient.WithClientProfile(profiles.Chrome_146),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
	}
	if dial == nil {
		dial = defaultResolver.DialContext
	}
	opts = append(opts, tlsclient.WithDialContext(dial))
	hc, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), opts...)
	if err != nil {
		return nil, fmt.Errorf("vk: http client: %w", err)
	}
	return &Client{HTTP: hc, Endpoints: DefaultEndpoints, Sleep: time.Sleep, Logf: func(string, ...any) {}, AutoCaptcha: true, CaptchaAttempts: defaultCaptchaAttempts}, nil
}

func (c *Client) post(ctx context.Context, url, form string) (map[string]any, error) {
	return c.postWith(ctx, url, form, "https://vk.ru", nil)
}

// postWith is post with the Origin/Referer of the page that would issue the
// request in a browser (vk.ru for the calls API, id.vk.ru for the captcha)
// and an optional hook that adjusts the headers before sending.
func (c *Client) postWith(ctx context.Context, url, form, origin string, adjust func(fhttp.Header)) (map[string]any, error) {
	req, err := fhttp.NewRequestWithContext(ctx, "POST", url, bytes.NewBufferString(form))
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set("User-Agent", userAgent)
	h.Set("sec-ch-ua", secChUA)
	h.Set("sec-ch-ua-mobile", "?0")
	h.Set("sec-ch-ua-platform", `"Windows"`)
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Accept", "*/*")
	h.Set("Origin", origin)
	h.Set("Referer", origin+"/")
	h.Set("Sec-Fetch-Site", "same-site")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Dest", "empty")
	if adjust != nil {
		adjust(h)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("vk: %s: http %d", req.URL.Host+req.URL.Path, resp.StatusCode)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("vk: %s: bad json: %w", req.URL.Host+req.URL.Path, err)
	}
	return m, nil
}

// missing describes a response that lacks the field a hop needs. Only the
// names of the top-level keys go into the error, never their values: these
// responses carry tokens and, on the last hop, the TURN username and
// credential, and the error ends up in Stats.LastError and in logs.
func missing(hop, field string, r map[string]any) error {
	return fmt.Errorf("vk: %s: no %s in response (keys: %s)", hop, field, strings.Join(keys(r), ","))
}

func keys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// Fetch runs the anonymous-join chain for one call link hash. It tries each
// known VK app id; a captcha aborts immediately.
func (c *Client) Fetch(ctx context.Context, link string) (Credential, error) {
	var last error
	for _, a := range apps {
		cred, err := c.fetchWith(ctx, link, a)
		if err == nil {
			return cred, nil
		}
		if provider.IsCaptcha(err) {
			return Credential{}, err
		}
		c.Logf("vk: app %s: %v", a.id, err)
		last = err
	}
	return Credential{}, fmt.Errorf("vk: all app ids failed: %w", last)
}

func (c *Client) fetchWith(ctx context.Context, link string, a app) (Credential, error) {
	e := c.Endpoints
	// 1. anonymous access token
	r, err := c.post(ctx, e.Login, fmt.Sprintf("client_id=%s&token_type=messages&client_secret=%s&version=1&app_id=%s", a.id, a.secret, a.id))
	if err != nil {
		return Credential{}, err
	}
	data, _ := r["data"].(map[string]any)
	token1 := str(data["access_token"])
	if token1 == "" {
		return Credential{}, missing("get_anonym_token", "data.access_token", r)
	}
	token1Esc := neturl.QueryEscape(token1)
	c.Sleep(120 * time.Millisecond)
	joinURL := neturl.QueryEscape("https://vk.ru/call/join/" + link)
	// 2. preview (best effort, mirrors the web client)
	_, _ = c.post(ctx, e.API+"calls.getCallPreview?v=5.275&client_id="+a.id, "vk_join_link="+joinURL+"&fields=photo_200&access_token="+token1Esc)
	c.Sleep(300 * time.Millisecond)
	// 3. anonymous call token; captcha shows up here
	tokenForm := "vk_join_link=" + joinURL + "&name=" + neturl.QueryEscape(randomName())
	var resp map[string]any
	for attempt := 0; ; attempt++ {
		r, err = c.post(ctx, e.API+"calls.getAnonymousToken?v=5.275&client_id="+a.id, tokenForm+"&access_token="+token1Esc)
		if err != nil {
			return Credential{}, err
		}
		eo, isErr := r["error"].(map[string]any)
		if !isErr {
			resp, _ = r["response"].(map[string]any)
			break
		}
		apiErr := vkAPIError(eo)
		var ce *provider.CaptchaRequiredError
		if !errors.As(apiErr, &ce) {
			return Credential{}, apiErr
		}
		attempts := c.CaptchaAttempts
		if attempts <= 0 {
			attempts = defaultCaptchaAttempts
		}
		if !c.AutoCaptcha || attempt >= attempts {
			return Credential{}, ce
		}
		success, serr := c.solveCaptcha(ctx, ce)
		if serr != nil {
			c.Logf("vk: captcha auto-solve failed: %v", serr)
			ce.SolveErr = serr
			return Credential{}, ce
		}
		c.Logf("vk: captcha solved, retrying getAnonymousToken")
		if ce.Attempt == "" || ce.Attempt == "0" {
			ce.Attempt = "1"
		}
		tokenForm = "vk_join_link=" + joinURL + "&name=" + neturl.QueryEscape(randomName()) +
			"&captcha_key=&captcha_sid=" + neturl.QueryEscape(ce.Sid) + "&is_sound_captcha=0" +
			"&success_token=" + neturl.QueryEscape(success) + "&captcha_ts=" + neturl.QueryEscape(ce.Ts) +
			"&captcha_attempt=" + neturl.QueryEscape(ce.Attempt)
	}
	token2 := str(resp["token"])
	if token2 == "" {
		return Credential{}, missing("calls.getAnonymousToken", "response.token", r)
	}
	token2Esc := neturl.QueryEscape(token2)
	c.Sleep(120 * time.Millisecond)
	// 4. OK calls session
	session := fmt.Sprintf(`{"version":2,"device_id":"%s","client_version":1.1,"client_type":"SDK_JS"}`, uuid.New())
	r, err = c.post(ctx, e.OK, "session_data="+neturl.QueryEscape(session)+"&method=auth.anonymLogin&format=JSON&application_key="+okAppKey)
	if err != nil {
		return Credential{}, err
	}
	token3 := str(r["session_key"])
	if token3 == "" {
		return Credential{}, missing("auth.anonymLogin", "session_key", r)
	}
	token3Esc := neturl.QueryEscape(token3)
	c.Sleep(120 * time.Millisecond)
	// 5. join -> TURN credentials
	r, err = c.post(ctx, e.OK, "joinLink="+neturl.QueryEscape(link)+"&isVideo=false&protocolVersion=5&capabilities=2F7F&anonymToken="+token2Esc+"&method=vchat.joinConversationByLink&format=JSON&application_key="+okAppKey+"&session_key="+token3Esc)
	if err != nil {
		return Credential{}, err
	}
	ts, _ := r["turn_server"].(map[string]any)
	cred := Credential{Username: str(ts["username"]), Password: str(ts["credential"]), Link: link, FetchedAt: time.Now()}
	urls, _ := ts["urls"].([]any)
	for _, u := range urls {
		s := str(u)
		if !strings.HasPrefix(s, "turn:") && !strings.HasPrefix(s, "turns:") {
			continue
		}
		if strings.Contains(s, "transport=tcp") {
			continue
		}
		s = strings.TrimPrefix(strings.TrimPrefix(strings.SplitN(s, "?", 2)[0], "turn:"), "turns:")
		cred.Relays = append(cred.Relays, s)
	}
	if cred.Username == "" || cred.Password == "" || len(cred.Relays) == 0 {
		return Credential{}, fmt.Errorf("vk: vchat.joinConversationByLink: incomplete turn_server (keys: %s; turn_server keys: %s; %d usable of %d urls)",
			strings.Join(keys(r), ","), strings.Join(keys(ts), ","), len(cred.Relays), len(urls))
	}
	return cred, nil
}
