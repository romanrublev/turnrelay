package vk

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"

	"github.com/romanrublev/turnrelay/provider"
)

type fakeDoer struct {
	t     *testing.T
	calls []string
	resp  map[string]string // url path -> body, consumed in order per path
}

func (f *fakeDoer) Do(r *fhttp.Request) (*fhttp.Response, error) {
	body, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.URL.Host+r.URL.Path+"?"+string(body))
	key := r.URL.Host + r.URL.Path
	if strings.Contains(string(body), "method=vchat.joinConversationByLink") {
		key += "#join"
	} else if strings.Contains(string(body), "method=auth.anonymLogin") {
		key += "#login"
	}
	b, ok := f.resp[key]
	if !ok {
		f.t.Fatalf("unexpected request %s", key)
	}
	return &fhttp.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(b))}, nil
}

func newTestClient(d Doer) *Client {
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
	if !strings.Contains(tok2, "vk_join_link="+url.QueryEscape("https://vk.com/call/join/AbCdEf123456")) && !strings.Contains(tok2, "vk_join_link=https://vk.com/call/join/AbCdEf123456") {
		t.Fatalf("token2 request: %s", tok2)
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
