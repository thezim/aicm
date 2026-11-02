package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/thezim/aicm/internal/config"
	"github.com/thezim/aicm/internal/daemon"
	"github.com/thezim/aicm/internal/display"
	"github.com/thezim/aicm/internal/model"
	"github.com/thezim/aicm/internal/platform"
	"github.com/thezim/aicm/internal/tui"
	"github.com/thezim/aicm/internal/version"
)

// Run executes the CLI and returns a process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "daemon":
			return runDaemonCommand(args[1:], stdout, stderr)
		case "connections":
			return runConnections(args[1:], stdout, stderr)
		case "close":
			return runClose(args[1:], stdout, stderr)
		case "version":
			fmt.Fprintln(stdout, version.Version)
			return 0
		case "help":
			printHelp(stdout)
			return 0
		default:
			fmt.Fprintf(stderr, "aicm: unknown command %q\n\n", args[0])
			printHelp(stderr)
			return 2
		}
	}

	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			printHelp(stdout)
			return 0
		}
	}
	fs := flag.NewFlagSet("aicm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "config file path (default ~/.config/aicm/config.yml)")
	profile := fs.String("profile", "", "AWS shared config profile (optional)")
	region := fs.String("region", "", "AWS region (optional)")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = func() { printHelp(fs.Output()) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "aicm: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version.Version)
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), platform.Signals()...)
	defer stop()
	summary, err := tui.Run(ctx, tui.Options{
		ConfigPath: *configPath,
		Profile:    *profile,
		Region:     *region,
		Version:    version.Version,
	})
	if summary != "" {
		fmt.Fprintln(stdout, summary)
	}
	if err != nil {
		fmt.Fprintf(stderr, "aicm: %v\n", err)
		return 1
	}
	return 0
}

func runDaemonCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: aicm daemon run|status|stop")
		return 2
	}
	switch args[0] {
	case "run":
		fs := flag.NewFlagSet("daemon run", flag.ContinueOnError)
		fs.SetOutput(stderr)
		configPath := fs.String("config", "", "config file path")
		if err := fs.Parse(args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		return runDaemon(*configPath, stderr)
	case "status":
		return daemonStatus(stdout, stderr)
	case "stop":
		return daemonStop(stdout, stderr)
	default:
		fmt.Fprintf(stderr, "aicm daemon: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runDaemon(configPath string, stderr io.Writer) int {
	setupDaemonLog()
	opts := daemon.Options{Version: version.Version}
	path := strings.TrimSpace(configPath)
	var cfg *config.Config
	var err error
	if path == "" {
		path, err = config.DefaultPath()
	}
	if err == nil {
		if loaded, lerr := config.Load(path); lerr == nil {
			cfg = loaded
		} else {
			log.Printf("daemon: config load failed, using defaults: %v", lerr)
		}
	}
	if cfg != nil {
		opts.ConnectTimeout = cfg.ConnectTimeout()
		opts.IdleExit = cfg.IdleExit()
	}
	ctx, stop := signal.NotifyContext(context.Background(), platform.Signals()...)
	defer stop()
	log.Printf("daemon: starting version=%s config=%s", version.Version, path)
	err = daemon.Run(ctx, opts)
	if errors.Is(err, daemon.ErrDaemonRunning) {
		fmt.Fprintln(stderr, "aicm daemon: already running")
		return 0
	}
	if err != nil {
		log.Printf("daemon: exited with error: %v", err)
		fmt.Fprintf(stderr, "aicm daemon: %v\n", err)
		return 1
	}
	log.Printf("daemon: stopped")
	return 0
}

func setupDaemonLog() {
	path, err := platform.LogPath()
	if err != nil {
		return
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > 5*1024*1024 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
}

func daemonStatus(stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client, err := daemon.TryConnect(ctx)
	if err != nil {
		fmt.Fprintln(stdout, "daemon: not running")
		return 1
	}
	defer client.Close()
	var info model.DaemonInfo
	if err := client.Call(ctx, "daemon_info", nil, &info); err != nil {
		fmt.Fprintf(stderr, "aicm daemon status: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "daemon: running\nversion: %s\nprotocol: %d\nstarted: %s\nconnections: %d\nclients: %d\nidle_exit_after: %s\n",
		info.Version, info.Protocol, info.StartedAt.Local().Format(time.RFC3339), info.Connections, info.Clients, info.IdleExitAfter)
	return 0
}

func daemonStop(stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := daemon.TryConnect(ctx)
	if err != nil {
		fmt.Fprintln(stdout, "daemon: not running")
		return 0
	}
	defer client.Close()
	if err := client.Call(ctx, "daemon_stop", nil, nil); err != nil {
		fmt.Fprintf(stderr, "aicm daemon stop: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "daemon: stopping")
	return 0
}

func runConnections(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("connections", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := daemon.EnsureRunning(ctx, "")
	if err != nil {
		fmt.Fprintf(stderr, "aicm connections: %v\n", err)
		return 1
	}
	defer client.Close()
	var conns []model.Connection
	if err := client.Call(ctx, "list_connections", nil, &conns); err != nil {
		fmt.Fprintf(stderr, "aicm connections: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(conns); err != nil {
			fmt.Fprintf(stderr, "aicm connections: %v\n", err)
			return 1
		}
		return 0
	}
	if len(conns) == 0 {
		fmt.Fprintln(stdout, "No active connections.")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tACCOUNT\tPORT\tSTATE\tAGE")
	for _, c := range conns {
		name := display.Sanitize(c.InstanceName)
		if name == "" {
			name = c.InstanceID
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\n", c.ID, name, c.AccountID, c.LocalPort, stateLabel(c), age(c.StartedAt))
	}
	_ = tw.Flush()
	return 0
}

func runClose(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("close", flag.ContinueOnError)
	fs.SetOutput(stderr)
	all := fs.Bool("all", false, "close all connections")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if !*all && fs.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: aicm close <connection-id> | aicm close --all")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := daemon.EnsureRunning(ctx, "")
	if err != nil {
		fmt.Fprintf(stderr, "aicm close: %v\n", err)
		return 1
	}
	defer client.Close()
	params := daemon.CloseParams{All: *all}
	if !*all {
		params.ID = fs.Arg(0)
	}
	if err := client.Call(ctx, "close_connection", params, nil); err != nil {
		fmt.Fprintf(stderr, "aicm close: %v\n", err)
		return 1
	}
	if *all {
		fmt.Fprintln(stdout, "Closing all connections.")
	} else {
		fmt.Fprintf(stdout, "Closing connection %s.\n", params.ID)
	}
	return 0
}

func printHelp(w io.Writer) {
	fmt.Fprint(w, `aicm — AWS Instance Connection Manager

List, start, and stop EC2 instances, open SSM console sessions, and open RDP
sessions through AWS SSM.

Usage:
  aicm [--config PATH] [--profile NAME] [--region NAME]
  aicm daemon run [--config PATH]
  aicm daemon status
  aicm daemon stop
  aicm connections [--json]
  aicm close <connection-id> | --all
  aicm version

Flags:
  --config PATH    config file (default ~/.config/aicm/config.yml)
  --profile NAME   AWS profile fallback when the standard chain is unresolved
  --region NAME    AWS region fallback when the standard chain is unresolved
  --version        print version and exit

Default TUI keys:
  ↑/↓ j/k move   i info   c console   r RDP (Windows)   s start   S stop
  R refresh   tab connections   o open RDP   x close   / filter   e edit config
  ? help   q quit

Tunnels keep running after the TUI exits.
`)
}

func stateLabel(c model.Connection) string {
	s := string(c.State)
	if c.RDPError != "" && c.State == model.StateReady {
		s = "ready!"
	}
	return s
}

func age(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}
