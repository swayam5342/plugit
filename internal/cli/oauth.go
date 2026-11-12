package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/swayam5342/plugit/internal/config"
	"github.com/swayam5342/plugit/internal/httpx"
	"github.com/swayam5342/plugit/internal/oauth"
	"github.com/swayam5342/plugit/internal/secrets"
)

// oauthKeyringService is where plugit keeps OAuth refresh tokens.
const oauthKeyringService = "plugit"

func refreshTokenKey(api string) string { return "oauth-refresh-" + api }

// refreshTokenRef is the refresh token's keyring item, for messages.
func refreshTokenRef(api string) string {
	return "keyring:" + oauthKeyringService + "/" + refreshTokenKey(api)
}

// keyringStore keeps an API's refresh token in the OS keyring.
type keyringStore struct {
	get secrets.KeyringGetter
	set keyringSetter
	api string
}

func (k keyringStore) Load() (string, error) {
	if k.get == nil {
		return "", oauth.ErrNoRefreshToken
	}
	v, err := k.get.Get(oauthKeyringService, refreshTokenKey(k.api))
	switch {
	case errors.Is(err, secrets.ErrKeyringItemNotFound), err == nil && v == "":
		return "", fmt.Errorf("%w (plugit auth %s)", oauth.ErrNoRefreshToken, k.api)
	case err != nil:
		return "", fmt.Errorf("%w: reading %s", secrets.ErrKeyring, refreshTokenRef(k.api))
	}
	return v, nil
}

func (k keyringStore) Save(v string) error {
	if k.set == nil {
		return errors.New("no keyring to store the refresh token in")
	}
	if err := k.set.Set(oauthKeyringService, refreshTokenKey(k.api), v); err != nil {
		// Backend messages are not wrapped: they could quote the value.
		return fmt.Errorf("%w: could not store %s", secrets.ErrKeyring, refreshTokenRef(k.api))
	}
	return nil
}

// oauthConfig resolves an API's OAuth settings, including the client
// secret when it has one.
func oauthConfig(cfg *config.API, res *secrets.Resolver) (oauth.Config, error) {
	a := cfg.Auth
	oc := oauth.Config{Flow: oauth.Flow(a.Flow), TokenURL: a.TokenURL, DeviceURL: a.DeviceURL, ClientID: a.ClientID, Scopes: a.Scopes}
	if a.Secret != "" {
		ref, err := secrets.ParseSecretRef(a.Secret)
		if err != nil {
			return oc, fmt.Errorf("auth.secret: %w", err)
		}
		v, err := res.Resolve(ref)
		if err != nil {
			return oc, fmt.Errorf("auth.secret: %w", err)
		}
		oc.ClientSecret = v
	}
	return oc, nil
}

// oauthDoer reaches the authorization server through the hardened client,
// with the API's network settings (SSRF guard, same-origin redirects).
func oauthDoer(cfg *config.API) httpx.Doer {
	return httpx.NewClient(httpx.Options{
		AllowPrivate:     cfg.Network.AllowPrivate,
		Timeout:          cfg.Network.Timeout,
		MaxResponseBytes: 1 << 20,
	})
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return raw
}

// sleepCtx waits for d or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// authOAuth is plugit auth for type oauth2: store the client secret if it
// lives in the keyring, then verify it (client credentials), or sign in
// with the device flow and keep the refresh token in the keyring.
func authOAuth(ctx context.Context, cfg *config.API, d authDeps) error {
	res := d.resolver
	if res == nil {
		res = secrets.NewResolver()
	}
	if cfg.Auth.Secret != "" {
		if ref, err := secrets.ParseSecretRef(cfg.Auth.Secret); err == nil && ref.Scheme == secrets.Keyring {
			if err := storeSecret(d, cfg, ref, fmt.Sprintf("Client secret for %s (input hidden): ", cfg.Name)); err != nil {
				return err
			}
		}
	}
	oc, err := oauthConfig(cfg, res)
	if err != nil {
		return err
	}
	doer := oauthDoer(cfg)
	if d.doer != nil {
		doer = d.doer
	}

	if oc.Flow == oauth.ClientCredentials {
		if _, err := oauth.NewSource(oc, doer, nil, nil).Token(ctx); err != nil {
			return fmt.Errorf("checking the client credentials with %s: %w", hostOf(oc.TokenURL), err)
		}
		_, err := fmt.Fprintf(d.out, "Got an access token from %s: the client credentials work.\n", hostOf(oc.TokenURL))
		return err
	}

	dev, err := oauth.StartDevice(ctx, oc, doer)
	if err != nil {
		return fmt.Errorf("starting sign-in with %s: %w", hostOf(oc.DeviceURL), err)
	}
	where := dev.VerificationURI
	if _, err := fmt.Fprintf(d.out, "To sign in to %s, open:\n\n  %s\n\nand enter the code: %s\n", cfg.Name, where, dev.UserCode); err != nil {
		return err
	}
	if dev.VerificationURIComplete != "" && dev.VerificationURIComplete != where {
		_, _ = fmt.Fprintf(d.out, "(or open %s, which fills it in)\n", dev.VerificationURIComplete)
	}
	_, _ = fmt.Fprintf(d.out, "\nWaiting for you to approve (up to %s)...\n", dev.ExpiresIn.Round(time.Minute))
	sleep := d.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	tok, err := dev.Wait(ctx, oc, doer, sleep)
	if err != nil {
		return err
	}
	if tok.Refresh == "" {
		return errors.New("signed in, but the server issued no refresh token, so plugit couldn't keep access; check the client allows refresh tokens (some servers need an offline_access scope)")
	}
	if err := (keyringStore{set: d.keyring, api: cfg.Name}).Save(tok.Refresh); err != nil {
		return err
	}
	_, err = fmt.Fprintf(d.out, "Signed in. The refresh token is in %s; plugit serve uses it to get access tokens.\n", refreshTokenRef(cfg.Name))
	return err
}
