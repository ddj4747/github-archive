package mirror

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitAuthenticationHeaderAndCredentialIsolation(t *testing.T) {
	const token = "ghp_local_auth_test_only"
	encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
		http.Error(w, "authentication rejected", http.StatusUnauthorized)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := runGit(ctx, "", token, nil, "ls-remote", server.URL)
	if err == nil {
		t.Fatal("expected authentication failure")
	}
	if authorization != "Basic "+encoded {
		t.Fatal("git did not send the expected credential header")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), encoded) {
		t.Fatal("credential was included in the error")
	}
	joined := strings.Join(gitEnv(token), "\n")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_1=credential.helper") || !strings.Contains(joined, "GIT_CONFIG_VALUE_1=\n") && !strings.HasSuffix(joined, "GIT_CONFIG_VALUE_1=") {
		t.Fatal("credential helper was not disabled")
	}
}

func TestGitWithoutTokenSendsNoAuthorization(t *testing.T) {
	var authorization string
	var requested bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		authorization = r.Header.Get("Authorization")
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = runGit(ctx, "", "", nil, "ls-remote", server.URL)
	if !requested || authorization != "" {
		t.Fatalf("unexpected Authorization header: %q", authorization)
	}
}
