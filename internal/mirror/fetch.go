package mirror

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Fetch updates heads and tags without pruning or enabling mirror mode.
func Fetch(ctx context.Context, mirrorPath, remote, token string) error {
	if err := ValidateGitHubRemote(remote); err != nil {
		return err
	}
	return fetchRemote(ctx, mirrorPath, remote, token)
}

func fetchRemote(ctx context.Context, mirrorPath, remote, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(mirrorPath), 0755); err != nil {
		return err
	}
	_, err := os.Stat(mirrorPath)
	if os.IsNotExist(err) {
		if err := runGit(ctx, "", token, nil, "init", "--bare", mirrorPath); err != nil {
			return err
		}
		for _, entry := range [][2]string{{"gc.auto", "0"}, {"gc.autopacklimit", "0"}, {"fetch.prune", "false"}} {
			if err := runGit(ctx, mirrorPath, token, nil, "config", "--local", entry[0], entry[1]); err != nil {
				return err
			}
		}
		if err := runGit(ctx, mirrorPath, token, nil, "remote", "add", "origin", remote); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	// The configured URL is passed directly, so stale origin configuration
	// cannot redirect a poll. The refspecs never prune the local history.
	fetch := func() error {
		return runGit(ctx, mirrorPath, token, nil, "fetch", "--no-prune", remote, "+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*")
	}
	err = fetch()
	if err == nil || ctx.Err() != nil {
		return err
	}
	// Do not repair on a network or authentication failure. Independently
	// confirm a local configuration parse failure before touching the file.
	configPath := filepath.Join(mirrorPath, "config")
	check := runGit(ctx, "", token, io.Discard, "config", "--file", configPath, "--list")
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if check == nil || !strings.Contains(check.Error(), "bad config line") {
		return err
	}
	if repairErr := repairConfig(ctx, mirrorPath, remote, token); repairErr != nil {
		return fmt.Errorf("corrupt mirror config could not be safely repaired: %w", repairErr)
	}
	return fetch()
}

// tailWriter bounds error output even if git emits an arbitrarily large stream.
type tailWriter struct{ b []byte }

func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	const limit = 4096
	if len(p) >= limit {
		w.b = append(w.b[:0], p[len(p)-limit:]...)
		return n, nil
	}
	if len(w.b)+len(p) > limit {
		w.b = append(w.b[:0], w.b[len(w.b)+len(p)-limit:]...)
	}
	w.b = append(w.b, p...)
	return n, nil
}

func gitEnv(token string) []string {
	env := make([]string, 0, len(os.Environ())+7)
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		upper := strings.ToUpper(key)
		if strings.HasPrefix(upper, "GIT_CONFIG_") || strings.HasPrefix(upper, "GIT_TRACE") || upper == "GIT_CURL_VERBOSE" || upper == "GIT_TERMINAL_PROMPT" || upper == "GIT_HTTP_LOW_SPEED_LIMIT" || upper == "GIT_HTTP_LOW_SPEED_TIME" {
			continue
		}
		env = append(env, e)
	}
	// GitHub's Git-over-HTTPS endpoint expects the token as a Basic-auth
	// password. Keep the header in the child environment, never the command
	// line or repository configuration. An empty helper prevents Git from
	// consulting or erasing unrelated saved credentials on authentication failure.
	header := ""
	if token != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		header = "Authorization: Basic " + encoded
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_HTTP_LOW_SPEED_LIMIT=1", "GIT_HTTP_LOW_SPEED_TIME=120", "GIT_CONFIG_COUNT=5", "GIT_CONFIG_KEY_0=http.extraheader", "GIT_CONFIG_VALUE_0="+header, "GIT_CONFIG_KEY_1=credential.helper", "GIT_CONFIG_VALUE_1=", "GIT_CONFIG_KEY_2=gc.auto", "GIT_CONFIG_VALUE_2=0", "GIT_CONFIG_KEY_3=gc.autopacklimit", "GIT_CONFIG_VALUE_3=0", "GIT_CONFIG_KEY_4=fetch.prune", "GIT_CONFIG_VALUE_4=false")
}

func runGit(ctx context.Context, dir, token string, stdout io.Writer, args ...string) error {
	return runGitInput(ctx, dir, token, nil, stdout, args...)
}

func runGitInput(ctx context.Context, dir, token string, stdin io.Reader, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(token)
	cmd.Stdin = stdin
	tail := new(tailWriter)
	if stdout == nil {
		cmd.Stdout = tail
	} else {
		cmd.Stdout = stdout
	}
	cmd.Stderr = tail
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		detail := string(tail.b)
		if token != "" {
			detail = strings.ReplaceAll(detail, token, "[redacted]")
			detail = strings.ReplaceAll(detail, base64.StdEncoding.EncodeToString([]byte("x-access-token:"+token)), "[redacted]")
		}
		return fmt.Errorf("git %s failed: %w: %s", args[0], err, strings.TrimSpace(detail))
	}
	return nil
}
