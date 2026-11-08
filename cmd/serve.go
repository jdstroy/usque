package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Diniboy1123/usque/api"
	"github.com/Diniboy1123/usque/config"
	"github.com/Diniboy1123/usque/internal"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run multiple L4 listeners in one process and share the same MASQUE connection",
	Long:  "Starts one shared MASQUE L4 proxy and then serves multiple SOCKS5 and HTTP listeners from the same process. All listeners share the same tunnel client and reconnect logic.",
	Run: func(cmd *cobra.Command, args []string) {
		if !config.ConfigLoaded {
			cmd.Println("Config not loaded. Please register first.")
			return
		}

		httpAddrs, err := cmd.Flags().GetStringArray("http")
		if err != nil {
			cmd.Printf("Failed to get HTTP listener list: %v\n", err)
			return
		}
		socksAddrs, err := cmd.Flags().GetStringArray("socks")
		if err != nil {
			cmd.Printf("Failed to get SOCKS listener list: %v\n", err)
			return
		}
		if len(httpAddrs) == 0 && len(socksAddrs) == 0 {
			cmd.Println("No listeners configured. Use --http and/or --socks with one or more addresses.")
			return
		}

		opts, cfg, err := buildL4ProxyConfig(cmd, "serve")
		if err != nil {
			cmd.Println(err)
			return
		}

		proxy, err := api.NewL4Proxy(cfg)
		if err != nil {
			cmd.Printf("Failed to create shared L4 proxy: %v\n", err)
			return
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		var started int
		for _, addr := range httpAddrs {
			addr := addr
			started++
			go func() {
				if err := serveL4HTTPListener(addr, opts.username, opts.password, proxy); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
					log.Printf("HTTP listener %s exited: %v", addr, err)
				}
			}()
		}
		for _, addr := range socksAddrs {
			addr := addr
			started++
			go func() {
				if err := serveL4SOCKSListener(addr, opts.username, opts.password, proxy); err != nil && !errors.Is(err, net.ErrClosed) {
					log.Printf("SOCKS listener %s exited: %v", addr, err)
				}
			}()
		}
		if started == 0 {
			cmd.Println("No listeners started.")
			return
		}

		log.Printf("serve mode active with %d shared listeners; one MASQUE client is used for all of them", started)
		<-ctx.Done()
		log.Println("Received shutdown signal; stopping listeners")
	},
}

func serveL4HTTPListener(addr, username, password string, proxy *api.L4Proxy) error {
	var authHeader string
	if username != "" && password != "" {
		authHeader = "Basic " + internal.LoginToBase64(username, password)
	}

	server := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authHeader != "" && r.Header.Get("Proxy-Authorization") != authHeader {
				w.Header().Set("Proxy-Authenticate", `Basic realm="Proxy"`)
				http.Error(w, "Proxy authentication required", http.StatusProxyAuthRequired)
				return
			}
			if r.Method == http.MethodConnect {
				handleL4HTTPConnect(w, r, proxy)
				return
			}
			handleL4HTTPForward(w, r, proxy)
		}),
	}
	log.Printf("L4 HTTP proxy listening on %s", addr)
	return server.ListenAndServe()
}

func serveL4SOCKSListener(addr, username, password string, proxy *api.L4Proxy) error {
	server, err := internal.NewSOCKS5Server(internal.SOCKS5Config{
		Addr:     addr,
		Username: username,
		Password: password,
		DialTCP: func(ctx context.Context, network, address string) (net.Conn, error) {
			return proxy.DialContext(ctx, address)
		},
		TCPOnly: true,
		Logger:  log.Default(),
	})
	if err != nil {
		return err
	}
	log.Printf("L4 SOCKS proxy listening on %s", addr)
	return server.Start()
}

func init() {
	addL4ProxyFlags(serveCmd, "8000", "HTTP")
	serveCmd.Flags().StringArray("http", nil, "HTTP proxy listener address(es); repeatable")
	serveCmd.Flags().StringArray("socks", nil, "SOCKS5 listener address(es); repeatable")
	rootCmd.AddCommand(serveCmd)
}

func authorityWithPortServe(authority, defaultPort string) (string, error) {
	if authority == "" {
		return "", fmt.Errorf("empty authority")
	}
	if strings.HasPrefix(authority, "[") || strings.Count(authority, ":") == 1 {
		host, port, err := net.SplitHostPort(authority)
		if err == nil {
			return net.JoinHostPort(host, port), nil
		}
	}
	host := authority
	if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") {
		host = strings.TrimPrefix(strings.TrimSuffix(authority, "]"), "[")
	}
	return net.JoinHostPort(host, defaultPort), nil
}
