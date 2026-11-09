package httpapi

import (
	"context"
	"errors"
	"net/http"

	"idp.local/reporting/identity/signin"
	r "idp.local/reporting/reporting"
)

// Exchanger is what WithSignIn needs of the sign-in client (*signin.Client is the one).
type Exchanger interface {
	Public() signin.Public
	Exchange(ctx context.Context, code, verifier, nonce string) (signin.Token, error)
}

var _ Exchanger = (*signin.Client)(nil)

// errSignInTokenRefused is an access token the provider issued that this platform's verifier does not accept.
var errSignInTokenRefused = errors.New("SIGN_IN_TOKEN_REFUSED")

// WithSignIn mounts the browser sign-in of the console (S15-A, ADR 0019), the two routes that answer before anyone is signed in:
//
//   - GET /sign-in (which the core API answers {"enabled": false}) tells the console how to send the browser to the provider (signin.Public: the authorization endpoint, the client id, the
//     redirect URI, the scope, S256, whether the paste form stays). Nothing in it is secret.
//   - POST /sign-in/token takes {code, code_verifier, nonce} and exchanges them at the provider's token endpoint, server to server. The access
//     token it answers is handed back only if authenticate accepts it, as it will judge every request that carries it; nothing else of the
//     provider's answer (no ID token, no refresh token) leaves the server. A refusal is one of the fixed codes below, never the provider's text.
//
// Neither route is authenticated, and neither logs or answers a code, a verifier or a token other than the one answered.
func WithSignIn(fallback http.Handler, client Exchanger, authenticate Authenticate) (http.Handler, error) {
	if fallback == nil || client == nil || authenticate == nil {
		return nil, r.ErrInvalid
	}
	const base = "/api/reporting/v1"
	public := client.Public()
	mux := http.NewServeMux()
	mux.Handle("/", fallback)
	mux.HandleFunc("GET "+base+"/sign-in", func(w http.ResponseWriter, req *http.Request) {
		if len(req.URL.Query()) != 0 {
			problem(w, r.ErrInvalid, 0)
			return
		}
		_ = send(w, 200, public)
	})
	mux.HandleFunc("POST "+base+"/sign-in/token", func(w http.ResponseWriter, req *http.Request) {
		var input struct {
			Code     string `json:"code"`
			Verifier string `json:"code_verifier"`
			Nonce    string `json:"nonce"`
		}
		if e := decode(w, req, &input, bodySmall); e != nil {
			problem(w, e, 0)
			return
		}
		if len(req.URL.Query()) != 0 || !signin.ValidCode(input.Code) || !signin.ValidVerifier(input.Verifier) || !signin.ValidNonce(input.Nonce) {
			problem(w, r.ErrInvalid, 0)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), r.WorkTimeout)
		defer cancel()
		token, e := client.Exchange(ctx, input.Code, input.Verifier, input.Nonce)
		if e == nil {
			// The token is judged now as it will be on every request: a token this platform would refuse is not handed to the page.
			check, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/session", nil)
			if err != nil {
				e = signin.ErrUnavailable
			} else {
				check.Header.Set("Authorization", "Bearer "+token.AccessToken)
				if p, err := authenticate(check); err != nil || !p.Identity.Valid() {
					e = errSignInTokenRefused
				}
			}
		}
		if e != nil {
			signInProblem(w, e)
			return
		}
		_ = send(w, 200, token)
	})
	return secured(mux), nil
}

// signInProblem answers a refused exchange with its fixed code.
func signInProblem(w http.ResponseWriter, e error) {
	status, code := 502, signin.ErrUnavailable.Error()
	switch {
	case errors.Is(e, signin.ErrRefused):
		status, code = 400, signin.ErrRefused.Error()
	case errors.Is(e, signin.ErrReplayed):
		status, code = 409, signin.ErrReplayed.Error()
	case errors.Is(e, errSignInTokenRefused):
		status, code = 403, errSignInTokenRefused.Error()
	case errors.Is(e, signin.ErrBusy):
		status, code = 503, signin.ErrBusy.Error()
		w.Header().Set("Retry-After", "1")
	}
	_ = send(w, status, map[string]string{"error": code})
}
