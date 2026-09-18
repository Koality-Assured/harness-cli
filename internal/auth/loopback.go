package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

const (
	DefaultLoopbackHost = "127.0.0.1"
	DefaultLoopbackPort = 8085
	MaxPortRetries      = 10
)

// LoopbackResult contains the outcome of an OAuth redirect capture.
type LoopbackResult struct {
	Code             string
	State            string
	Error            string
	ErrorDescription string
}

// StartLoopbackListener binds a local HTTP server and captures the OAuth callback redirect.
func StartLoopbackListener(expectedState string) (string, <-chan LoopbackResult, func(), error) {
	resultCh := make(chan LoopbackResult, 1)

	var listener net.Listener
	var actualPort int
	var err error

	for offset := 0; offset <= MaxPortRetries; offset++ {
		targetPort := DefaultLoopbackPort + offset
		addr := fmt.Sprintf("%s:%d", DefaultLoopbackHost, targetPort)
		listener, err = net.Listen("tcp", addr)
		if err == nil {
			actualPort = targetPort
			break
		}
	}

	if listener == nil {
		return "", nil, nil, fmt.Errorf("failed to bind loopback listener across ports %d-%d: %w", DefaultLoopbackPort, DefaultLoopbackPort+MaxPortRetries, err)
	}

	redirectURI := fmt.Sprintf("http://%s:%d/callback", DefaultLoopbackHost, actualPort)

	mux := http.NewServeMux()
	server := &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := q.Get("code")
		state := q.Get("state")
		errParam := q.Get("error")
		errDesc := q.Get("error_description")

		// Antagonistic CSRF Defense: Validate state against expected state
		if expectedState != "" && state != expectedState {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Authentication Failed - State Mismatch</title></head>
<body style="font-family: sans-serif; text-align: center; padding-top: 50px;">
  <h2 style="color: #d9534f;">Authentication Failed: Security Violation</h2>
  <p>The state parameter did not match the expected session state.</p>
  <p>To protect against CSRF attacks, this authentication request has been rejected.</p>
</body>
</html>`))
			resultCh <- LoopbackResult{
				Error:            "state_mismatch",
				ErrorDescription: fmt.Sprintf("State mismatch: expected '%s', got '%s'", expectedState, state),
			}
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		if errParam != "" {
			_, _ = w.Write([]byte(fmt.Sprintf(`<!DOCTYPE html>
<html>
<head><title>Authentication Failed</title></head>
<body style="font-family: sans-serif; text-align: center; padding-top: 50px;">
  <h2 style="color: #d9534f;">Authentication Failed</h2>
  <p>Error: %s</p>
  <p>%s</p>
  <p>You can close this tab and return to the terminal.</p>
</body>
</html>`, errParam, errDesc)))
			resultCh <- LoopbackResult{
				Error:            errParam,
				ErrorDescription: errDesc,
			}
			return
		}

		_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Authentication Successful</title></head>
<body style="font-family: sans-serif; text-align: center; padding-top: 50px;">
  <h2 style="color: #28a745;">Authentication Successful!</h2>
  <p>Your credentials have been securely stored in the Harness Keyring.</p>
  <p>You may now close this browser tab and return to the terminal.</p>
</body>
</html>`))

		resultCh <- LoopbackResult{
			Code:  code,
			State: state,
		}
	})

	go func() {
		_ = server.Serve(listener)
	}()

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		_ = listener.Close()
	}

	return redirectURI, resultCh, cleanup, nil
}
