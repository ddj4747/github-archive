package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"time"

	"githubarchive/internal/config"
	"githubarchive/internal/github"
	"githubarchive/internal/poll"
	"githubarchive/internal/store"
)

var runLoop = poll.Run
var reloadInterval = time.Second
var heartbeatInterval = 5 * time.Second

type runtimeState struct {
	config config.Config
	token  string
	db     *store.Store
	client *github.Client
}

func prepare(cfg config.Config, token string) (*runtimeState, error) {
	db, err := store.Open(cfg.DataRoot)
	if err != nil {
		return nil, err
	}
	client, err := github.New(token)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &runtimeState{config: cfg, token: token, db: db, client: client}, nil
}

func (s *runtimeState) start(ctx context.Context) (context.CancelFunc, <-chan error) {
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- runLoop(loopCtx, s.config.DataRoot, s.config.Repositories, s.config.Users, s.token, s.db, s.client)
	}()
	return cancel, done
}

func Run(ctx context.Context, configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	token := os.Getenv(cfg.TokenEnv)
	if strings.TrimSpace(token) == "" {
		token = ""
	}
	state, err := prepare(cfg, token)
	if err != nil {
		return err
	}
	if err := state.db.MarkHeartbeat(time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, "worker heartbeat could not be recorded")
	}
	cancel, done := state.start(ctx)
	ticker := time.NewTicker(reloadInterval)
	heartbeat := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	defer heartbeat.Stop()
	defer func() { cancel(); <-done; _ = state.db.StopHeartbeat(); state.db.Close() }()
	invalidReported := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			// The deferred cleanup must not wait a second time on this channel.
			done = closedResult()
			if err != nil {
				message := err.Error()
				if state.token != "" {
					message = strings.ReplaceAll(message, state.token, "[redacted]")
				}
				return fmt.Errorf("%s", message)
			}
			return nil
		case <-heartbeat.C:
			if err := state.db.MarkHeartbeat(time.Now()); err != nil {
				fmt.Fprintln(os.Stderr, "worker heartbeat could not be recorded")
			}
		case <-ticker.C:
			nextConfig, err := config.Load(configPath)
			if err != nil {
				if !invalidReported {
					fmt.Fprintln(os.Stderr, "configuration reload failed; continuing with previous configuration")
					invalidReported = true
				}
				continue
			}
			nextToken := os.Getenv(nextConfig.TokenEnv)
			if strings.TrimSpace(nextToken) == "" {
				nextToken = ""
			}
			if reflect.DeepEqual(state.config, nextConfig) && state.token == nextToken {
				invalidReported = false
				continue
			}
			next, err := prepare(nextConfig, nextToken)
			if err != nil {
				if !invalidReported {
					fmt.Fprintln(os.Stderr, "configuration reload failed; continuing with previous configuration")
					invalidReported = true
				}
				continue
			}
			cancel()
			<-done
			_ = state.db.StopHeartbeat()
			state.db.Close()
			state = next
			if err := state.db.MarkHeartbeat(time.Now()); err != nil {
				fmt.Fprintln(os.Stderr, "worker heartbeat could not be recorded")
			}
			cancel, done = state.start(ctx)
			invalidReported = false
		}
	}
}

func closedResult() <-chan error { c := make(chan error); close(c); return c }

func main() {
	path := flag.String("config", "config.yaml", "configuration file")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := Run(ctx, *path); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
