// Package provider defines what a credential source hands to the pool.
package provider

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Credential struct {
	Username  string
	Password  string
	Relays    []string // host:port; TCP-only entries are dropped by providers
	Link      string   // provider-specific source id (call link hash, "static")
	FetchedAt time.Time
}

func (c Credential) Relay(i int) string {
	if len(c.Relays) == 0 {
		return ""
	}
	return c.Relays[i%len(c.Relays)]
}

type Fetcher func(ctx context.Context, link string) (Credential, error)

// CaptchaRequiredError: the provider needs a human. The library never solves
// captchas; callers cool down or surface it.
type CaptchaRequiredError struct {
	Sid, RedirectURI, SessionToken, Img string
}

func (e *CaptchaRequiredError) Error() string {
	return fmt.Sprintf("provider: captcha required (sid %s)", e.Sid)
}

func IsCaptcha(err error) bool {
	var ce *CaptchaRequiredError
	return errors.As(err, &ce)
}
