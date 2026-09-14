package vk

// Captcha-free anonymous-join flow through VK Calls' own API (api.vk.me with
// VK Connect's public client_id 8093730), discovered and documented by
// anton48/vk-turn-proxy-ios (GPL-3.0). VK gates anon flows per (FQDN, method,
// client_id); the legacy calls.getAnonymousToken path is captcha-gated and,
// as of 2026-09, rejects freshly created call links with error 9008, while
// this path is captcha-free because VK treats VK Connect's anonymous tokens
// as already-trusted identity. Expect VK to gate it eventually; the legacy
// path (client.go) with the PoW solver stays as the fallback.

import (
	"context"
	"fmt"
	neturl "net/url"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/google/uuid"
)

const (
	vkCallsAPIHost    = "api.vk.me"
	vkConnectClientID = "8093730"
	vkCallsAPIVersion = "5.276"
	// iosUA identifies the native VK Calls app; this path sends no
	// Origin/Referer (it is not a WebView).
	iosUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
)

// vkCallsGet issues one bodiless POST (VK puts every param in the URL) with
// the native-app header set and returns the parsed JSON.
func (c *Client) vkCallsGet(ctx context.Context, url string) (map[string]any, error) {
	req, err := fhttp.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set("User-Agent", iosUA)
	h.Set("Accept", "*/*")
	doer := c.VKCallsHTTP
	if doer == nil {
		doer = c.HTTP // tests inject one Doer
	}
	resp, err := doer.Do(req)
	if err != nil {
		// VK frequently resets a pooled HTTP/2 connection; drop idle
		// connections and retry once on a fresh one before giving up.
		if ci, ok := doer.(interface{ CloseIdleConnections() }); ok {
			ci.CloseIdleConnections()
		}
		req2, _ := fhttp.NewRequestWithContext(ctx, "POST", url, nil)
		req2.Header = req.Header.Clone()
		resp, err = doer.Do(req2)
		if err != nil {
			return nil, err
		}
	}
	defer resp.Body.Close()
	return decodeJSON(resp)
}

// fetchViaVKCalls runs the five-step captcha-free flow for one call link hash.
// A captcha or a VK API error is returned so Fetch can fall back to legacy.
func (c *Client) fetchViaVKCalls(ctx context.Context, link string) (Credential, error) {
	device := uuid.New().String()
	linkURL := neturl.QueryEscape("https://vk.ru/call/join/" + link)
	name := neturl.QueryEscape(randomName())

	// 1. anonymous_token from VK Connect
	r, err := c.vkCallsGet(ctx, fmt.Sprintf("https://%s/method/auth.getAnonymToken?v=%s&client_id=%s&link=%s&device_id=%s&anonymName=%s&lang=en",
		vkCallsAPIHost, vkCallsAPIVersion, vkConnectClientID, linkURL, device, name))
	if err != nil {
		return Credential{}, err
	}
	if e := vkErr(r); e != nil {
		return Credential{}, fmt.Errorf("vkcalls step1: %w", e)
	}
	resp, _ := r["response"].(map[string]any)
	anon := str(resp["token"])
	if anon == "" {
		return Credential{}, missing("auth.getAnonymToken", "response.token", r)
	}
	anonEsc := neturl.QueryEscape(anon)
	c.Sleep(120000000) // 120 ms

	// 2. call preview -> user_id + secret
	r, err = c.vkCallsGet(ctx, fmt.Sprintf("https://%s/method/messages.getCallPreview?v=%s&anonymous_token=%s&device_id=%s&extended=1&fields=first_name&lang=en&link=%s",
		vkCallsAPIHost, vkCallsAPIVersion, anonEsc, device, linkURL))
	if err != nil {
		return Credential{}, err
	}
	if e := vkErr(r); e != nil {
		return Credential{}, e
	}
	// getCallPreview confirms the call exists; VK no longer returns
	// user_id/secret here and getAnonymCallToken no longer needs them.
	resp, _ = r["response"].(map[string]any)
	uidParam := ""
	if uid, ok := resp["user_id"].(float64); ok {
		uidParam = fmt.Sprintf("&user_id=%.0f", uid)
	}
	if secret := str(resp["secret"]); secret != "" {
		uidParam += "&secret=" + neturl.QueryEscape(secret)
	}
	c.Sleep(120000000)

	// 3. anon call token (the gate that captcha'd the legacy client ids)
	r, err = c.vkCallsGet(ctx, fmt.Sprintf("https://%s/method/messages.getAnonymCallToken?v=%s&anonymous_token=%s&device_id=%s&link=%s&name=%s%s&lang=en",
		vkCallsAPIHost, vkCallsAPIVersion, anonEsc, device, linkURL, name, uidParam))
	if err != nil {
		return Credential{}, err
	}
	if e := vkErr(r); e != nil {
		return Credential{}, fmt.Errorf("vkcalls step3: %w", e)
	}
	resp, _ = r["response"].(map[string]any)
	okToken := str(resp["token"])
	if okToken == "" {
		return Credential{}, missing("messages.getAnonymCallToken", "response.token", r)
	}
	c.Sleep(120000000)

	// 4. OK anonymLogin -> session_key
	okDevice := uuid.New().String()
	session := neturl.QueryEscape(fmt.Sprintf(`{"version":2,"device_id":"%s","client_version":"1.0.1"}`, okDevice))
	r, err = c.vkCallsGet(ctx, c.Endpoints.OK+"?session_data="+session+"&method=auth.anonymLogin&format=JSON&application_key="+okAppKey)
	if err != nil {
		return Credential{}, err
	}
	sk := str(r["session_key"])
	if sk == "" {
		return Credential{}, missing("auth.anonymLogin", "session_key", r)
	}
	c.Sleep(120000000)

	// 5. join -> TURN credentials
	r, err = c.vkCallsGet(ctx, c.Endpoints.OK+"?joinLink="+neturl.QueryEscape(link)+"&isVideo=false&protocolVersion=5&anonymToken="+neturl.QueryEscape(okToken)+"&method=vchat.joinConversationByLink&format=JSON&application_key="+okAppKey+"&session_key="+neturl.QueryEscape(sk))
	if err != nil {
		return Credential{}, err
	}
	return turnCredFromJoin(link, r)
}
