package vk

import (
	"context"
	"net/url"
	"strings"
	"testing"
)

// The captcha-free VK Calls path (api.vk.me + VK Connect client_id) is tried
// first; on success the legacy captcha chain is never touched.
func TestFetchViaVKCalls(t *testing.T) {
	d := &fakeDoer{t: t, resp: map[string]string{
		"api.vk.me/method/auth.getAnonymToken":         `{"response":{"token":"anon.JWT","expired_at":9999999999}}`,
		"api.vk.me/method/messages.getCallPreview":     `{"response":{"user_id":12345,"secret":"SECRET-JWT"}}`,
		"api.vk.me/method/messages.getAnonymCallToken": `{"response":{"token":"OKTOKEN32"}}`,
		"calls.okcdn.ru/fb.do#login":                   `{"session_key":"SK"}`,
		"calls.okcdn.ru/fb.do#join":                    `{"turn_server":{"username":"u1","credential":"p1","urls":["turn:155.212.200.1:3478?transport=udp","turn:155.212.200.1:3478?transport=tcp","turns:155.212.200.2:3478"]}}`,
	}}
	c := newTestClient(d)
	c.TryVKCalls = true
	cred, err := c.Fetch(context.Background(), "AbCdEf123456")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Username != "u1" || cred.Password != "p1" {
		t.Fatalf("cred %+v", cred)
	}
	if len(cred.Relays) != 2 || cred.Relays[0] != "155.212.200.1:3478" || cred.Relays[1] != "155.212.200.2:3478" {
		t.Fatalf("relays %v (tcp must be dropped)", cred.Relays)
	}
	var order []string
	for _, call := range d.calls {
		order = append(order, strings.SplitN(call, "?", 2)[0])
	}
	want := []string{
		"api.vk.me/method/auth.getAnonymToken",
		"api.vk.me/method/messages.getCallPreview",
		"api.vk.me/method/messages.getAnonymCallToken",
		"calls.okcdn.ru/fb.do", "calls.okcdn.ru/fb.do",
	}
	if strings.Join(order, "\n") != strings.Join(want, "\n") {
		t.Fatalf("call order:\n%s", strings.Join(order, "\n"))
	}
	// step1 carries the vk.ru link, the VK Connect client_id and v=5.276
	s1 := d.calls[0]
	for _, w := range []string{"client_id=8093730", "v=5.276", "link=" + url.QueryEscape("https://vk.ru/call/join/AbCdEf123456"), "device_id=", "auth.getAnonymToken"} {
		if !strings.Contains(s1, w) {
			t.Fatalf("step1 lacks %s: %s", w, s1)
		}
	}
	// step3 threads user_id and secret from step2
	s3 := d.calls[2]
	for _, w := range []string{"user_id=12345", "secret=SECRET-JWT", "anonymous_token=anon.JWT"} {
		if !strings.Contains(s3, w) {
			t.Fatalf("step3 lacks %s: %s", w, s3)
		}
	}
	// no legacy hop, no captcha
	for _, call := range d.calls {
		if strings.Contains(call, "getAnonymousToken") || strings.Contains(call, "login.vk.ru") || strings.Contains(call, "captchaNotRobot") {
			t.Fatalf("legacy/captcha hop taken: %s", call)
		}
	}
}

// A captcha gate on the VK Calls path makes Fetch fall back to the legacy
// chain (which then solves or surfaces the captcha).
func TestFetchViaVKCallsFallsBackOnCaptcha(t *testing.T) {
	d := &fakeDoer{t: t, resp: map[string]string{
		"api.vk.me/method/auth.getAnonymToken":     `{"response":{"token":"anon.JWT"}}`,
		"api.vk.me/method/messages.getCallPreview": `{"error":{"error_code":14,"error_msg":"Captcha","captcha_sid":"9","redirect_uri":"https://id.vk.ru/x?session_token=ST"}}`,
		// legacy chain then runs and also hits a captcha we do not auto-solve here
		"login.vk.ru/":                             `{"data":{"access_token":"T1"}}`,
		"api.vk.ru/method/calls.getCallPreview":    `{"response":{}}`,
		"api.vk.ru/method/calls.getAnonymousToken": `{"error":{"error_code":14,"error_msg":"Captcha","captcha_sid":"1","redirect_uri":"https://id.vk.ru/x?session_token=ST2"}}`,
	}}
	c := newTestClient(d) // AutoCaptcha off in tests
	c.TryVKCalls = true
	_, err := c.Fetch(context.Background(), "AbCdEf123456")
	if err == nil {
		t.Fatal("want error")
	}
	sawLegacy := false
	for _, call := range d.calls {
		if strings.Contains(call, "login.vk.ru") {
			sawLegacy = true
		}
	}
	if !sawLegacy {
		t.Fatal("did not fall back to the legacy chain after the VK Calls captcha gate")
	}
}
