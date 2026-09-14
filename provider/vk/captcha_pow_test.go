package vk

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	fhttp "github.com/bogdanfinn/fhttp"

	"github.com/romanrublev/turnrelay/provider"
)

// captchaHTML mimics the obfuscated BFF page: window.vk with a UUID under a
// per-build key, an IIFE whose arguments carry the PoW input and difficulty,
// a telemetry-shaped envelope and the v2. prefix.
const captchaHTML = `<html><script>
window.vk = {
  apiConfigDomains: {"loginDomain":"login.vk.ru","domain":"vk.ru"},
  brlefapmjnpg: "450ef2ba-1610-4248-b4f8-2d85dbad252c",
  ex: {}, lang: 3, ts: 1789387692
};
window.lang = {"a":"b"};
</script><script>
(function(_0x1,_0x2,_0x3){var r={'hash':_0xa,'nonce':_0xb,'duration_ms':_0xc,'telemetry':_0xd,'tel_hash':_0xe};
window['captchaPowResult']='v2.'+btoa(JSON['stringify'](r));if(h['startsWith']('0'['repeat'](_0x2))){}
}("pow-seed-42",1,"pow_timeout"));
</script></html>`

func TestParseCaptchaBootstrapHTML(t *testing.T) {
	b, err := parseCaptchaBootstrapHTML(captchaHTML)
	if err != nil || b.PowInput != "pow-seed-42" || b.Difficulty != 1 {
		t.Fatalf("got %+v %v", b, err)
	}
	if b.Envelope != envelopeTelemetry5 || b.DebugInfo != "450ef2ba-1610-4248-b4f8-2d85dbad252c" || b.Lang != "3" {
		t.Fatalf("page globals: %+v", b)
	}
	if _, err := parseCaptchaBootstrapHTML("<html>nothing</html>"); err == nil {
		t.Fatal("missing pow accepted")
	}
	b, _ = parseCaptchaBootstrapHTML(`const powInput = "x"; const difficulty = 3;`)
	if b.Difficulty != 3 || b.Envelope != envelopeLegacy3 || b.DebugInfo != "" {
		t.Fatalf("legacy page: %+v", b)
	}
	b, _ = parseCaptchaBootstrapHTML(`const powInput = "x";`)
	if b.Difficulty != 2 {
		t.Fatalf("default difficulty %d", b.Difficulty)
	}
	if _, err := parseCaptchaBootstrapHTML(`window['captchaPowResult']='v3.'+x; }("abcdefgh",2,"e"))`); err == nil {
		t.Fatal("unknown envelope prefix accepted")
	}
	if _, err := parseCaptchaBootstrapHTML(`}("abcdefgh",9,"e"))`); err == nil {
		t.Fatal("difficulty 9 accepted")
	}
}

func TestBuildPowResult(t *testing.T) {
	r := buildPowResult(envelopeTelemetry5, "00ab", 7, 12)
	if !strings.HasPrefix(r, "v2.") {
		t.Fatalf("prefix: %s", r)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(r, "v2."))
	if err != nil || string(raw) != `{"hash":"00ab","nonce":7,"duration_ms":12,"telemetry":{},"tel_hash":""}` {
		t.Fatalf("payload %q %v", raw, err)
	}
	raw, _ = base64.StdEncoding.DecodeString(strings.TrimPrefix(buildPowResult(envelopeLegacy3, "00ab", 7, 12), "v2."))
	if string(raw) != `{"hash":"00ab","nonce":7,"duration_ms":12}` {
		t.Fatalf("legacy payload %q", raw)
	}
}

func TestSolvePoW(t *testing.T) {
	h, nonce := solvePoW("pow-seed-42", 2, 1<<20)
	if h == "" || !strings.HasPrefix(h, "00") {
		t.Fatalf("hash %q", h)
	}
	sum := sha256.Sum256([]byte("pow-seed-42" + strconv.Itoa(nonce)))
	if hex.EncodeToString(sum[:]) != h {
		t.Fatal("hash does not match input+nonce")
	}
	if h, _ := solvePoW("x", 64, 10); h != "" {
		t.Fatal("impossible difficulty should give up")
	}
}

// captchaFlow wires a fake VK where the first getAnonymousToken answers with
// a captcha and the second with the token, plus the id.vk.ru bootstrap page
// and the four captchaNotRobot methods.
func captchaFlow(t *testing.T, checkBody string) *fakeDoer {
	return &fakeDoer{t: t,
		resp: map[string]string{
			"login.vk.ru/":                                   `{"data":{"access_token":"T1"}}`,
			"api.vk.ru/method/calls.getCallPreview":          `{"response":{}}`,
			"calls.okcdn.ru/fb.do#login":                     `{"session_key":"T3"}`,
			"calls.okcdn.ru/fb.do#join":                      `{"turn_server":{"username":"u1","credential":"p1","urls":["turn:155.212.200.1:3478"]}}`,
			"id.vk.ru/not_robot_captcha":                     captchaHTML,
			"api.vk.ru/method/captchaNotRobot.initSession":   `{"response":{"show_captcha_type":"not_robot","content_settings":[]}}`,
			"api.vk.ru/method/captchaNotRobot.settings":      `{"response":{"show_captcha_type":"not_robot"}}`,
			"api.vk.ru/method/captchaNotRobot.componentDone": `{"response":1}`,
			"api.vk.ru/method/captchaNotRobot.check":         checkBody,
			"api.vk.ru/method/captchaNotRobot.endSession":    `{"response":1}`,
		},
		seq: map[string][]string{
			"api.vk.ru/method/calls.getAnonymousToken": {
				`{"error":{"error_code":14,"error_msg":"Captcha needed","captcha_sid":"777","captcha_ts":1700000000,"captcha_attempt":0,"captcha_img":"https://id.vk.ru/captcha","redirect_uri":"https://id.vk.ru/not_robot_captcha?domain=vk.com&session_token=ST-1&variant=popup"}}`,
				`{"response":{"token":"T2"}}`,
			},
		},
	}
}

func TestFetchSolvesCaptchaAutomatically(t *testing.T) {
	d := captchaFlow(t, `{"response":{"status":"OK","success_token":"SUCCESS-9"}}`)
	c := newTestClient(d)
	c.AutoCaptcha = true
	cred, err := c.Fetch(context.Background(), "AbCdEf123456")
	if err != nil {
		t.Fatal(err)
	}
	if cred.Username != "u1" {
		t.Fatalf("cred %+v", cred)
	}
	var order []string
	for _, call := range d.calls {
		order = append(order, strings.SplitN(call, "?", 2)[0])
	}
	want := []string{
		"login.vk.ru/", "api.vk.ru/method/calls.getCallPreview", "api.vk.ru/method/calls.getAnonymousToken",
		"id.vk.ru/not_robot_captcha",
		"api.vk.ru/method/captchaNotRobot.initSession",
		"api.vk.ru/method/captchaNotRobot.settings", "api.vk.ru/method/captchaNotRobot.componentDone",
		"api.vk.ru/method/captchaNotRobot.check", "api.vk.ru/method/captchaNotRobot.endSession",
		"api.vk.ru/method/calls.getAnonymousToken", "calls.okcdn.ru/fb.do", "calls.okcdn.ru/fb.do",
	}
	if strings.Join(order, "\n") != strings.Join(want, "\n") {
		t.Fatalf("call order:\n%s", strings.Join(order, "\n"))
	}
	init := d.calls[4]
	if !strings.Contains(init, "session_token=ST-1&domain=vk.com&lang=3") {
		t.Fatalf("initSession body: %s", init)
	}
	check := d.calls[7]
	for _, w := range []string{"session_token=ST-1", "domain=vk.com", "hash=v2.", "answer=" + url.QueryEscape("e30="), "cursor=", "browser_fp=", "debug_info=450ef2ba-1610-4248-b4f8-2d85dbad252c"} {
		if !strings.Contains(check, w) {
			t.Fatalf("check body lacks %s: %s", w, check)
		}
	}
	hv := check[strings.Index(check, "hash=")+5:]
	hv = hv[:strings.Index(hv, "&")]
	hv, _ = url.QueryUnescape(hv)
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(hv, "v2."))
	if err != nil || !strings.Contains(string(raw), `"tel_hash":""`) || !strings.Contains(string(raw), `"hash":"0`) {
		t.Fatalf("envelope %q %v", raw, err)
	}
	hdr := d.postHeaders["api.vk.ru/method/captchaNotRobot.check"]
	if got := hdr[fhttp.HeaderOrderKey]; len(got) == 0 || got[0] != "host" {
		t.Fatalf("chrome header order not pinned: %v", got)
	}
	retry := d.calls[9]
	for _, w := range []string{"success_token=SUCCESS-9", "captcha_sid=777", "captcha_ts=1700000000", "captcha_attempt=1", "is_sound_captcha=0", "captcha_key=&", "access_token=T1"} {
		if !strings.Contains(retry, w) {
			t.Fatalf("retry body lacks %s: %s", w, retry)
		}
	}
	if d.getHeaders["id.vk.ru/not_robot_captcha"].Get("Sec-Fetch-Mode") != "navigate" {
		t.Fatal("bootstrap page must be fetched as a navigation")
	}
	if hdr.Get("Origin") != "https://id.vk.ru" {
		t.Fatal("captcha API calls must carry the id.vk.ru origin")
	}
}

func TestFetchCaptchaSolveFailureSurfacesCaptcha(t *testing.T) {
	d := captchaFlow(t, `{"response":{"status":"BOT","show_captcha_type":"slider"}}`)
	c := newTestClient(d)
	c.AutoCaptcha = true
	_, err := c.Fetch(context.Background(), "AbCdEf123456")
	var ce *provider.CaptchaRequiredError
	if !provider.IsCaptcha(err) {
		t.Fatalf("want captcha error, got %v", err)
	}
	_ = ce
	if !strings.Contains(err.Error(), "777") {
		t.Fatalf("error should name the sid: %v", err)
	}
	errors.As(err, &ce)
	var st *captchaShowTypeError
	if !errors.As(ce.SolveErr, &st) || st.ShowType != "slider" {
		t.Fatalf("solve error should carry show_captcha_type: %v", ce.SolveErr)
	}
	for _, call := range d.calls {
		if strings.HasPrefix(call, "api.vk.ru/method/calls.getAnonymousToken") && strings.Contains(call, "success_token") {
			t.Fatal("must not retry getAnonymousToken after a failed solve")
		}
	}
}

func TestFetchCaptchaDisabled(t *testing.T) {
	d := captchaFlow(t, `{"response":{"status":"OK","success_token":"S"}}`)
	c := newTestClient(d) // AutoCaptcha false
	_, err := c.Fetch(context.Background(), "AbCdEf123456")
	if !provider.IsCaptcha(err) {
		t.Fatalf("want captcha error, got %v", err)
	}
	for _, call := range d.calls {
		if strings.HasPrefix(call, "id.vk.ru") || strings.Contains(call, "captchaNotRobot") {
			t.Fatalf("solver ran while disabled: %s", call)
		}
	}
}
