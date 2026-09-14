package vk

// Automatic solve of VK's "not a robot" captcha (id.vk.ru). The page behind
// redirect_uri embeds an obfuscated proof-of-work script; a browser solves
// it, wraps the result in a small envelope and calls the captchaNotRobot.*
// methods, then retries the original API call with the success_token.
// Sequence and page parsing follow cacggghp/vk-turn-proxy and the
// 2026-09 measurements in anton48/vk-turn-proxy-ios (both GPL-3.0):
// initSession first, Chrome header order on every call, envelope built in
// the shape the page itself uses. Image and slider challenges are not solved
// here; when VK escalates to them the captcha error is returned as is.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"

	"github.com/romanrublev/turnrelay/provider"
)

const (
	captchaAPI    = "https://api.vk.ru/method/"
	captchaAPIVer = "5.131"
	captchaOrigin = "https://id.vk.ru"
	powMaxNonce   = 10_000_000
	powMaxDiff    = 8
	powPrefix     = "v2."
	// debugInfoFallback is the widget's PACKAGE_VERSION_HASHED constant, sent
	// when the page carries no window.vk UUID.
	debugInfoFallback = "a0ac4896e9b899f78d905fd37c5adb2b768aa955eb7b2a7bcba6ee2a44a96daf"
	// desktopDeviceJSON is the device blob of a 1920x1080 desktop Chrome; the
	// same one the working Android client sends.
	desktopDeviceJSON = `{"screenWidth":1920,"screenHeight":1080,"screenAvailWidth":1920,"screenAvailHeight":1080,"innerWidth":1920,"innerHeight":951,"devicePixelRatio":1,"language":"en-US","languages":["en-US","en"],"webdriver":false,"hardwareConcurrency":8,"notificationsPermission":"denied"}`
)

// Chrome's HTTP/2 header order on captchaNotRobot.* requests; VK flags the
// default (uncontrolled) order as a bot.
var (
	chromeHeaderOrder  = []string{"host", "content-length", "sec-ch-ua-platform", "accept-language", "sec-ch-ua", "content-type", "sec-ch-ua-mobile", "user-agent", "accept", "origin", "sec-fetch-site", "sec-fetch-mode", "sec-fetch-dest", "referer", "accept-encoding", "priority"}
	chromePHeaderOrder = []string{":method", ":path", ":authority", ":scheme"}
)

type powEnvelope uint8

const (
	envelopeLegacy3    powEnvelope = iota // {hash, nonce, duration_ms}
	envelopeTelemetry5                    // + telemetry, tel_hash (bundle 1.1.1395+)
)

type captchaBootstrap struct {
	PowInput   string
	Difficulty int
	Envelope   powEnvelope
	DebugInfo  string // window.vk UUID, sent as check's debug_info
	Lang       string // window.vk.lang, sent on initSession
}

var (
	// The obfuscated IIFE ends with its arguments: }("<input>", <difficulty>, "pow_timeout"))
	rePowIIFEArgs    = regexp.MustCompile(`\}\(\s*["']([A-Za-z0-9+/=_-]{8,})["']\s*,\s*(\d+)\s*,\s*["'][^"']*["']\s*\)\s*\)`)
	rePowLegacyInput = regexp.MustCompile(`const\s+powInput\s*=\s*"([^"]+)"`)
	rePowLegacyDiff  = regexp.MustCompile(`startsWith\('0'\.repeat\((\d+)\)\)|const\s+difficulty\s*=\s*(\d+)`)
	rePowPrefix      = regexp.MustCompile(`captchaPowResult["'\]]{0,3}\s*=\s*["']([A-Za-z0-9._-]{0,8})["']\s*\+`)
	rePowTelemetry   = regexp.MustCompile(`["']tel_hash["']\s*:`)
	reVKGlobalStart  = regexp.MustCompile(`window\.vk\s*=\s*\{`)
	reVKGlobalUUID   = regexp.MustCompile(`[A-Za-z_$][\w$]*\s*:\s*"([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})"`)
	reVKGlobalLang   = regexp.MustCompile(`\blang\s*:\s*(\d+)\b`)
	errNoPow         = errors.New("vk: captcha page carries no proof-of-work parameters")
)

func parseCaptchaBootstrapHTML(html string) (captchaBootstrap, error) {
	b := captchaBootstrap{Envelope: envelopeLegacy3}
	if m := rePowPrefix.FindStringSubmatch(html); len(m) >= 2 && m[1] != powPrefix {
		return b, fmt.Errorf("vk: captcha pow envelope prefix %q is unknown", m[1])
	}
	if rePowTelemetry.MatchString(html) {
		b.Envelope = envelopeTelemetry5
	}
	b.DebugInfo, b.Lang = parseVKGlobal(html)
	if m := rePowIIFEArgs.FindStringSubmatch(html); len(m) >= 3 {
		d, err := strconv.Atoi(m[2])
		if err != nil || d <= 0 || d > powMaxDiff {
			return b, fmt.Errorf("vk: captcha pow difficulty %q out of range", m[2])
		}
		b.PowInput, b.Difficulty = m[1], d
		return b, nil
	}
	if m := rePowLegacyInput.FindStringSubmatch(html); len(m) >= 2 {
		b.PowInput, b.Difficulty = m[1], 2
		if dm := rePowLegacyDiff.FindStringSubmatch(html); len(dm) >= 2 {
			raw := dm[1]
			if raw == "" && len(dm) >= 3 {
				raw = dm[2]
			}
			if d, err := strconv.Atoi(raw); err == nil && d > 0 && d <= powMaxDiff {
				b.Difficulty = d
			}
		}
		return b, nil
	}
	return b, errNoPow
}

// vkGlobalBlock returns the balanced {...} of `window.vk = {...}`, skipping
// braces inside string literals, or "" when the page has none.
func vkGlobalBlock(html string) string {
	m := reVKGlobalStart.FindStringIndex(html)
	if m == nil {
		return ""
	}
	start := m[1] - 1
	depth := 0
	var inStr byte
	for i := start; i < len(html); i++ {
		c := html[i]
		if inStr != 0 {
			if c == '\\' {
				i++
			} else if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '"', '\'':
			inStr = c
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return html[start : i+1]
			}
		}
	}
	return ""
}

func parseVKGlobal(html string) (debugInfo, lang string) {
	block := vkGlobalBlock(html)
	if block == "" {
		return "", ""
	}
	if m := reVKGlobalUUID.FindStringSubmatch(block); len(m) >= 2 {
		debugInfo = m[1]
	}
	if m := reVKGlobalLang.FindStringSubmatch(block); len(m) >= 2 {
		lang = m[1]
	}
	return debugInfo, lang
}

// solvePoW finds a nonce such that sha256(input+nonce) starts with
// difficulty hex zeros; it returns the hash and the nonce, or "" after
// maxNonce attempts.
func solvePoW(input string, difficulty, maxNonce int) (hash string, nonce int) {
	target := strings.Repeat("0", difficulty)
	for n := 1; n <= maxNonce; n++ {
		sum := sha256.Sum256([]byte(input + strconv.Itoa(n)))
		h := hex.EncodeToString(sum[:])
		if strings.HasPrefix(h, target) {
			return h, n
		}
	}
	return "", 0
}

// buildPowResult wraps the solved hash the way the page's own success branch
// does: prefix + btoa(JSON) with the keys in the page's order. The telemetry
// probes are sent empty, which is also what the page sends when its
// collector throws.
func buildPowResult(env powEnvelope, hexHash string, nonce int, durationMs int64) string {
	var payload string
	if env == envelopeTelemetry5 {
		payload = fmt.Sprintf(`{"hash":"%s","nonce":%d,"duration_ms":%d,"telemetry":{},"tel_hash":""}`, hexHash, nonce, durationMs)
	} else {
		payload = fmt.Sprintf(`{"hash":"%s","nonce":%d,"duration_ms":%d}`, hexHash, nonce, durationMs)
	}
	return powPrefix + base64.StdEncoding.EncodeToString([]byte(payload))
}

func (c *Client) getPage(ctx context.Context, url string) ([]byte, error) {
	req, err := fhttp.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	h := req.Header
	h.Set("User-Agent", userAgent)
	h.Set("sec-ch-ua", secChUA)
	h.Set("sec-ch-ua-mobile", "?0")
	h.Set("sec-ch-ua-platform", `"Windows"`)
	h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Sec-Fetch-Site", "none")
	h.Set("Sec-Fetch-Mode", "navigate")
	h.Set("Sec-Fetch-Dest", "document")
	h.Set("Sec-Fetch-User", "?1")
	h.Set("Upgrade-Insecure-Requests", "1")
	h.Set("Priority", "u=0, i")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("vk: %s: http %d", req.URL.Host+req.URL.Path, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 4<<20))
}

// captchaAPI posts one captchaNotRobot.* call with the id.vk.ru origin and
// Chrome's header order.
func (c *Client) captchaAPI(ctx context.Context, method, form string) (map[string]any, error) {
	return c.postWith(ctx, captchaAPI+method+"?v="+captchaAPIVer, form, captchaOrigin, func(h fhttp.Header) {
		h.Set("Accept-Language", "en-US,en;q=0.9")
		h.Set("Priority", "u=1, i")
		h[fhttp.HeaderOrderKey] = chromeHeaderOrder
		h[fhttp.PHeaderOrderKey] = chromePHeaderOrder
	})
}

func randomHex(nbytes int) string {
	b := make([]byte, nbytes)
	for i := range b {
		b[i] = byte(rand.IntN(256))
	}
	return hex.EncodeToString(b)
}

// captchaShowTypeError is returned when the checkbox check refused us and VK
// announced a follow-up challenge type (e.g. "slider").
type captchaShowTypeError struct{ ShowType string }

func (e *captchaShowTypeError) Error() string {
	return "vk: captcha check refused (show_captcha_type " + strconv.Quote(e.ShowType) + ")"
}

// solveCaptcha runs the proof-of-work flow and returns the success_token for
// the retry of the failed API call.
func (c *Client) solveCaptcha(ctx context.Context, ce *provider.CaptchaRequiredError) (string, error) {
	if ce.SessionToken == "" || ce.RedirectURI == "" {
		return "", errors.New("vk: captcha error carries no redirect_uri/session_token")
	}
	domain := "vk.com"
	if u, err := neturl.Parse(ce.RedirectURI); err == nil && u.Query().Get("domain") != "" {
		domain = u.Query().Get("domain")
	}
	// A browser needs a moment before the page loads.
	c.Sleep(time.Duration(1500+rand.IntN(1000)) * time.Millisecond)
	page, err := c.getPage(ctx, ce.RedirectURI)
	if err != nil {
		return "", fmt.Errorf("vk: captcha bootstrap: %w", err)
	}
	boot, err := parseCaptchaBootstrapHTML(string(page))
	if err != nil {
		return "", err
	}
	c.Logf("vk: captcha pow difficulty %d envelope %d debug_info %v lang %q", boot.Difficulty, boot.Envelope, boot.DebugInfo != "", boot.Lang)
	base := "session_token=" + neturl.QueryEscape(ce.SessionToken) + "&domain=" + neturl.QueryEscape(domain)
	lang := boot.Lang
	if lang == "" {
		lang = "0"
	}
	if _, err := c.captchaAPI(ctx, "captchaNotRobot.initSession", base+"&lang="+lang+"&access_token="); err != nil {
		return "", fmt.Errorf("vk: captcha initSession: %w", err)
	}
	c.Sleep(time.Duration(100+rand.IntN(100)) * time.Millisecond)
	if _, err := c.captchaAPI(ctx, "captchaNotRobot.settings", base+"&adFp=&access_token="); err != nil {
		return "", fmt.Errorf("vk: captcha settings: %w", err)
	}
	c.Sleep(time.Duration(100+rand.IntN(100)) * time.Millisecond)
	fp := randomHex(16)
	if _, err := c.captchaAPI(ctx, "captchaNotRobot.componentDone", base+"&adFp=&browser_fp="+fp+"&device="+neturl.QueryEscape(desktopDeviceJSON)+"&access_token="); err != nil {
		return "", fmt.Errorf("vk: captcha componentDone: %w", err)
	}
	start := time.Now()
	hash, nonce := solvePoW(boot.PowInput, boot.Difficulty, powMaxNonce)
	if hash == "" {
		return "", fmt.Errorf("vk: captcha pow unsolved at difficulty %d", boot.Difficulty)
	}
	result := buildPowResult(boot.Envelope, hash, nonce, time.Since(start).Milliseconds())
	// The widget shows the checkbox for a while before the user clicks it.
	c.Sleep(time.Duration(1950+rand.IntN(1250)) * time.Millisecond)
	debugInfo := boot.DebugInfo
	if debugInfo == "" {
		debugInfo = debugInfoFallback
	}
	empty := neturl.QueryEscape("[]")
	check := base + "&adFp=&accelerometer=" + empty + "&gyroscope=" + empty + "&motion=" + empty + "&cursor=" + empty + "&taps=" + empty +
		"&browser_fp=" + fp + "&hash=" + neturl.QueryEscape(result) +
		"&answer=" + neturl.QueryEscape(base64.StdEncoding.EncodeToString([]byte("{}"))) +
		"&debug_info=" + debugInfo + "&access_token="
	r, err := c.captchaAPI(ctx, "captchaNotRobot.check", check)
	if err != nil {
		return "", fmt.Errorf("vk: captcha check: %w", err)
	}
	resp, _ := r["response"].(map[string]any)
	if str(resp["status"]) != "OK" || str(resp["success_token"]) == "" {
		return "", &captchaShowTypeError{ShowType: str(resp["show_captcha_type"])}
	}
	c.Sleep(200 * time.Millisecond)
	if _, err := c.captchaAPI(ctx, "captchaNotRobot.endSession", base+"&adFp=&access_token="); err != nil {
		c.Logf("vk: captcha endSession: %v", err)
	}
	return str(resp["success_token"]), nil
}
