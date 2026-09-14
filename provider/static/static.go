// Package static returns one fixed credential: self-hosted coturn, or a
// relay whose credentials were obtained elsewhere.
package static

import (
	"context"
	"time"

	"github.com/romanrublev/turnrelay/provider"
)

func New(server, username, password string) provider.Fetcher {
	return func(context.Context, string) (provider.Credential, error) {
		return provider.Credential{Username: username, Password: password, Relays: []string{server}, Link: "static", FetchedAt: time.Now()}, nil
	}
}
