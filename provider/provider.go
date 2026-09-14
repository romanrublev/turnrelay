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

// CaptchaRequiredError: the provider could not get past a captcha. Providers
// may attempt an automatic solve first; when that fails, callers cool down
// or surface it. Ts and Attempt are echoed back by the VK retry.
type CaptchaRequiredError struct {
	Sid, RedirectURI, SessionToken, Img string
	Ts, Attempt                         string
	// SolveErr is why the automatic solve failed, if one was attempted.
	SolveErr error
}

func (e *CaptchaRequiredError) Error() string {
	return fmt.Sprintf("provider: captcha required (sid %s)", e.Sid)
}

func IsCaptcha(err error) bool {
	var ce *CaptchaRequiredError
	return errors.As(err, &ce)
}
