// Package vk obtains TURN credentials from a VK Calls link, the way the VK
// web client does it (anonymous join). Chain and app ids follow
// cacggghp/vk-turn-proxy (GPL-3.0).
package vk

import (
	"bytes"
	"context"
	"encoding/json"
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
}

// publicResolverDialer bypasses the system resolver, which whitelisted
// networks often break first.
func publicResolverDialer() net.Dialer {
	return net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second, Resolver: &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			var last error
			for _, s := range []string{"77.88.8.8:53", "77.88.8.1:53", "8.8.8.8:53", "1.1.1.1:53"} {
				c, err := d.DialContext(ctx, "udp", s)
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
	}}
}

func NewClient() (*Client, error) {
	hc, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(),
		tlsclient.WithTimeoutSeconds(20),
		tlsclient.WithClientProfile(profiles.Chrome_146),
		tlsclient.WithCookieJar(tlsclient.NewCookieJar()),
		tlsclient.WithDialer(publicResolverDialer()),
	)
	if err != nil {
		return nil, fmt.Errorf("vk: http client: %w", err)
	}
	return &Client{HTTP: hc, Endpoints: DefaultEndpoints, Sleep: time.Sleep, Logf: func(string, ...any) {}}, nil
}

func (c *Client) post(ctx context.Context, url, form string) (map[string]any, error) {
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
	h.Set("Origin", "https://vk.ru")
	h.Set("Referer", "https://vk.ru/")
	h.Set("Sec-Fetch-Site", "same-site")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Dest", "empty")
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
	joinURL := neturl.QueryEscape("https://vk.com/call/join/" + link)
	// 2. preview (best effort, mirrors the web client)
	_, _ = c.post(ctx, e.API+"calls.getCallPreview?v=5.275&client_id="+a.id, "vk_join_link="+joinURL+"&fields=photo_200&access_token="+token1Esc)
	c.Sleep(300 * time.Millisecond)
	// 3. anonymous call token; captcha shows up here
	r, err = c.post(ctx, e.API+"calls.getAnonymousToken?v=5.275&client_id="+a.id, "vk_join_link="+joinURL+"&name="+neturl.QueryEscape(randomName())+"&access_token="+token1Esc)
	if err != nil {
		return Credential{}, err
	}
	if eo, ok := r["error"].(map[string]any); ok {
		return Credential{}, vkAPIError(eo)
	}
	resp, _ := r["response"].(map[string]any)
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
