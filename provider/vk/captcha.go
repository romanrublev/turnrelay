package vk

import (
	"fmt"
	neturl "net/url"

	"github.com/romanrublev/turnrelay/provider"
)

// vkAPIError turns the "error" object of a VK API response into an error.
// Error 14 on calls.getAnonymousToken is VK Smart Captcha.
func vkAPIError(obj map[string]any) error {
	code, _ := obj["error_code"].(float64)
	msg, _ := obj["error_msg"].(string)
	if int(code) == 14 {
		ce := &provider.CaptchaRequiredError{Img: str(obj["captcha_img"]), RedirectURI: str(obj["redirect_uri"])}
		switch v := obj["captcha_sid"].(type) {
		case string:
			ce.Sid = v
		case float64:
			ce.Sid = fmt.Sprintf("%.0f", v)
		}
		if u, err := neturl.Parse(ce.RedirectURI); err == nil {
			ce.SessionToken = u.Query().Get("session_token")
		}
		ce.Ts = numOrString(obj["captcha_ts"])
		ce.Attempt = numOrString(obj["captcha_attempt"])
		return ce
	}
	return fmt.Errorf("vk: API error %d: %s", int(code), msg)
}

func str(v any) string { s, _ := v.(string); return s }

// numOrString renders a JSON field VK sends either as a number or a string.
func numOrString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return fmt.Sprintf("%.0f", x)
	}
	return ""
}
