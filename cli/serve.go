package cli

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/katbyte/embyfin-mcp/lib/clog"
	"github.com/katbyte/embyfin-mcp/lib/version"
	"github.com/katbyte/embyfin-mcp/tools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

const (
	mcpPath           = "/mcp"
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the MCP server (stdio by default, HTTP with --listen)",
		Long: `Runs the MCP server for AI clients such as Claude Code.

Without --listen it speaks MCP over stdio: register it in .mcp.json with the EMBYFIN_*
environment variables set. With --listen (EMBYFIN_LISTEN, e.g. :8080) it serves the
Streamable HTTP transport at /mcp instead, for an always-on deployment such as the
docker-compose.yml in this repo. Set --auth-token (EMBYFIN_AUTH_TOKEN) to require
"Authorization: Bearer <token>" on that endpoint; without it anyone who can reach the
port can use every tool.`,
		Args:          cobra.NoArgs,
		PreRunE:       ValidateParams(connectionParams),
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true

			f := GetFlags()
			client, err := f.NewClient()
			if err != nil {
				return err
			}

			server := mcp.NewServer(&mcp.Implementation{
				Name:    "embyfin",
				Title:   "Emby/Jellyfin Library Curator",
				Version: version.Version,
			}, nil)

			tools.RegisterAll(server, client, tools.Options{EnableDelete: f.EnableDelete})

			if f.Listen == "" {
				return server.Run(cmd.Context(), &mcp.StdioTransport{})
			}

			return serveHTTP(cmd.Context(), server, f.Listen, f.AuthToken)
		},
	}
}

// serveHTTP serves the MCP server over Streamable HTTP at /mcp (plus GET /healthz for
// container health checks) until the context is cancelled or SIGINT/SIGTERM arrives,
// then drains in-flight requests.
func serveHTTP(ctx context.Context, server *mcp.Server, addr, authToken string) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.Handle(mcpPath, requireBearer(authToken, handler))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	if authToken == "" {
		clog.Log.Warnf("no auth token set (EMBYFIN_AUTH_TOKEN): anyone who can reach %s can use every tool", addr)
	}

	clog.Log.Infof("serving MCP over HTTP on %s%s", addr, mcpPath)

	errCh := make(chan error, 1)

	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}

		return fmt.Errorf("listening on %s: %w", addr, err)
	case <-ctx.Done():
	}

	clog.Log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}

	return nil
}

// requireBearer rejects requests without a matching "Authorization: Bearer <token>" header.
// An empty token disables the check.
func requireBearer(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}

	want := []byte("Bearer " + token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="embyfin-mcp"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}
