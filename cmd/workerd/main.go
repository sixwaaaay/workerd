package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/sixwaaaay/workerd/internal/client"
	"github.com/sixwaaaay/workerd/internal/config"
	"github.com/sixwaaaay/workerd/internal/daemon"
	"github.com/sixwaaaay/workerd/internal/logger"
	"github.com/sixwaaaay/workerd/internal/process"
	"github.com/spf13/cobra"
)

var (
	socketPath string
	configDir  string
)

func main() {
	// Default paths
	homeDir, _ := os.UserHomeDir()
	if homeDir == "" {
		homeDir = "/tmp"
	}
	defaultConfig := filepath.Join(homeDir, ".config", "workerd")
	defaultSocket := filepath.Join(homeDir, ".config", "workerd", "workerd.sock")

	if os.Geteuid() == 0 {
		defaultConfig = "/etc/workerd"
		defaultSocket = "/var/run/workerd.sock"
	}

	rootCmd := &cobra.Command{
		Use:   "workerd",
		Short: "A user-space process manager (like supervisor/docker-compose)",
		Long:  `workerd is a lightweight process manager that runs as a daemon and manages services defined in TOML config files.`,
	}

	rootCmd.PersistentFlags().StringVar(&socketPath, "socket", defaultSocket, "Unix socket path")
	rootCmd.PersistentFlags().StringVar(&configDir, "config", defaultConfig, "Config directory")

	rootCmd.AddCommand(daemonCmd())
	rootCmd.AddCommand(initCmd())
	rootCmd.AddCommand(addCmd())
	rootCmd.AddCommand(removeCmd())
	rootCmd.AddCommand(startCmd())
	rootCmd.AddCommand(stopCmd())
	rootCmd.AddCommand(restartCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(psCmd())
	rootCmd.AddCommand(logsCmd())
	rootCmd.AddCommand(reloadCmd())
	rootCmd.AddCommand(shutdownCmd())
	rootCmd.AddCommand(schemaCmd())
	rootCmd.AddCommand(versionCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func daemonCmd() *cobra.Command {
	var foreground bool

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the workerd daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check if daemon is already running
			if pid, err := readPIDFile(filepath.Join(configDir, "workerd.pid")); err == nil && pid > 0 {
				if proc, err := os.FindProcess(pid); err == nil {
					if err := proc.Signal(syscall.Signal(0)); err == nil {
						return fmt.Errorf("daemon is already running (PID %d)", pid)
					}
				}
			}
			if !foreground {
				return daemonize()
			}
			return runDaemon()
		},
	}
	cmd.Flags().BoolVarP(&foreground, "foreground", "f", false, "Run in foreground")
	return cmd
}

// readPIDFile reads a PID from a file.
func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return 0, err
	}
	return pid, nil
}

func daemonize() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find executable: %w", err)
	}

	daemonCmd := exec.Command(exe, "daemon",
		"--foreground",
		"--socket", socketPath,
		"--config", configDir,
	)

	daemonCmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	daemonCmd.Stdin = nil

	// Redirect stdout/stderr to log
	logFile := filepath.Join(configDir, "daemon.log")
	os.MkdirAll(configDir, 0755)
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("opening log file: %w", err)
	}
	daemonCmd.Stdout = f
	daemonCmd.Stderr = f

	if err := daemonCmd.Start(); err != nil {
		return fmt.Errorf("starting daemon: %w", err)
	}

	fmt.Printf("Daemon started with PID %d\n", daemonCmd.Process.Pid)
	fmt.Printf("Log: %s\n", logFile)
	return nil
}

func runDaemon() error {
	srv := daemon.NewServer(socketPath, configDir)
	return srv.Run()
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init <name>",
		Short: "Generate a template service config file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			serviceDir := filepath.Join(configDir, "services")
			os.MkdirAll(serviceDir, 0755)

			cfg := config.DefaultServiceConfig(name)
			cfg.Command = "/usr/bin/" + name

			toml := generateTOMLTemplate(cfg)
			path := filepath.Join(serviceDir, name+".toml")

			// Check if file exists
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("config file already exists: %s", path)
			}

			if err := os.WriteFile(path, []byte(toml), 0644); err != nil {
				return err
			}
			fmt.Printf("Created %s\n", path)
			return nil
		},
	}
}

func addCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <name|config-file>",
		Short: "Load a service into the daemon",
		Long: `Load a service configuration into the running daemon.

If given a service name (e.g. "myapp"), looks for <name>.toml in the
services directory. Use "init" first to create a template, edit it,
then "add" to load it.

If given a path to a .toml file, copies it into the services directory
and loads it immediately.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			serviceDir := filepath.Join(configDir, "services")
			os.MkdirAll(serviceDir, 0755)

			arg := args[0]
			var dstPath string

			// Determine if arg is a file path or a service name
			if strings.Contains(arg, string(filepath.Separator)) || strings.HasSuffix(arg, ".toml") {
				// It's a file path: copy to services dir and load
				srcPath, err := filepath.Abs(arg)
				if err != nil {
					return fmt.Errorf("resolving path: %w", err)
				}
				// Load config to get the service name
				cfg, err := config.LoadService(srcPath)
				if err != nil {
					return fmt.Errorf("loading config: %w", err)
				}
				dstPath = filepath.Join(serviceDir, cfg.Name+".toml")
				if srcPath != dstPath {
					data, err := os.ReadFile(srcPath)
					if err != nil {
						return fmt.Errorf("reading config: %w", err)
					}
					if err := os.WriteFile(dstPath, data, 0644); err != nil {
						return fmt.Errorf("copying config: %w", err)
					}
				}
			} else {
				// It's a service name: load from services dir
				dstPath = filepath.Join(serviceDir, arg+".toml")
				if _, err := os.Stat(dstPath); os.IsNotExist(err) {
					return fmt.Errorf("service %q not found — use 'workerd init %s' first to create a template", arg, arg)
				}
			}

			c := client.NewClient(socketPath)
			if err := c.Add(dstPath); err != nil {
				return err
			}
			fmt.Printf("Service loaded from %s\n", dstPath)
			return nil
		},
	}
}

func removeCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "remove <name>",
		Short:             "Remove a service",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: serviceNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client.NewClient(socketPath)
			if err := c.Remove(args[0]); err != nil {
				return err
			}
			fmt.Printf("Service %s removed\n", args[0])
			return nil
		},
	}
}

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "start <name>",
		Short:             "Start a service",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: serviceNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client.NewClient(socketPath)
			if err := c.Start(args[0]); err != nil {
				return err
			}
			fmt.Printf("Service %s started\n", args[0])
			return nil
		},
	}
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "stop <name>",
		Short:             "Stop a service",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: serviceNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client.NewClient(socketPath)
			if err := c.Stop(args[0]); err != nil {
				return err
			}
			fmt.Printf("Service %s stopped\n", args[0])
			return nil
		},
	}
}

func restartCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "restart <name>",
		Short:             "Restart a service",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: serviceNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client.NewClient(socketPath)
			if err := c.Restart(args[0]); err != nil {
				return err
			}
			fmt.Printf("Service %s restarted\n", args[0])
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "status [name]",
		Short:             "Show service status",
		ValidArgsFunction: serviceNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client.NewClient(socketPath)
			name := ""
			if len(args) > 0 {
				name = args[0]
			}
			services, err := c.Status(name)
			if err != nil {
				return err
			}
			printStatusTable(services)
			return nil
		},
	}
}

func psCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List all services",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client.NewClient(socketPath)
			services, err := c.List()
			if err != nil {
				return err
			}
			printStatusTable(services)
			return nil
		},
	}
}

func printStatusTable(services []*process.ServiceStatus) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tPID\tUPTIME\tRESTARTS\tDESCRIPTION")
	for _, s := range services {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\t%s\n",
			s.Name, s.State, s.PID, s.Uptime, s.RestartCount, s.Description)
	}
	w.Flush()
}

func logsCmd() *cobra.Command {
	var (
		follow bool
		lines  int
	)

	cmd := &cobra.Command{
		Use:               "logs <name>",
		Short:             "View service logs (shows both stdout and stderr by default)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: serviceNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client.NewClient(socketPath)

			if follow {
				// Subscribe to both streams
				done := make(chan struct{})
				go func() {
					c.LogsFollow(args[0], "stdout", lines, func(line logger.LogLine) {
						fmt.Printf("[%s] %s\n", line.Timestamp.Format("15:04:05"), line.Line)
					})
					close(done)
				}()
				c.LogsFollow(args[0], "stderr", lines, func(line logger.LogLine) {
					fmt.Printf("[stderr] [%s] %s\n", line.Timestamp.Format("15:04:05"), line.Line)
				})
				<-done
				return nil
			}

			// Non-follow: fetch both, merge by timestamp, display raw lines
			stdoutLines, _ := c.Logs(args[0], "stdout", lines)
			stderrLines, _ := c.Logs(args[0], "stderr", lines)
			if stdoutLines == nil && stderrLines == nil {
				return fmt.Errorf("no log data for %q", args[0])
			}

			allLines := append(stdoutLines, stderrLines...)
			sort.Slice(allLines, func(i, j int) bool {
				return allLines[i].Timestamp.Before(allLines[j].Timestamp)
			})

			for _, line := range allLines {
				// Raw line already has timestamp prefix from the collector
				if line.Stream == "stderr" {
					fmt.Print("[stderr] ")
				}
				fmt.Println(line.Line)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "Number of lines to show")
	return cmd
}

func reloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "Reload all service configurations",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := client.NewClient(socketPath)
			if err := c.Reload(); err != nil {
				return err
			}
			fmt.Println("Configs reloaded")
			return nil
		},
	}
}

func shutdownCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shutdown",
		Short: "Gracefully shut down the daemon",
		Long: `Shut down the daemon gracefully. All running services will be stopped before the daemon exits.
If the daemon is not reachable via socket, falls back to sending SIGTERM using the PID file.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Try API first
			c := client.NewClient(socketPath)
			if err := c.Shutdown(); err == nil {
				fmt.Println("Daemon is shutting down...")
				return nil
			}

			// Fallback: read PID file and send SIGTERM
			pidFile := filepath.Join(configDir, "workerd.pid")
			data, err := os.ReadFile(pidFile)
			if err != nil {
				return fmt.Errorf("daemon not reachable and no PID file found at %s", pidFile)
			}

			var pid int
			fmt.Sscanf(string(data), "%d", &pid)
			if pid <= 0 {
				return fmt.Errorf("invalid PID in %s", pidFile)
			}

			process, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("cannot find process %d: %w", pid, err)
			}

			if err := process.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("failed to send SIGTERM to PID %d: %w", pid, err)
			}

			fmt.Printf("Sent SIGTERM to daemon (PID %d)\n", pid)
			return nil
		},
	}
}

func schemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema",
		Short: "Output JSON Schema for service config",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Try from daemon first
			c := client.NewClient(socketPath)
			schema, err := c.Schema()
			if err == nil {
				fmt.Println(string(schema))
				return nil
			}
			// Fallback: generate locally
			schema, err = config.GenerateSchema()
			if err != nil {
				return err
			}
			fmt.Println(string(schema))
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("workerd v0.1.0")
		},
	}
}

// generateTOMLTemplate creates a TOML template string from a config.
func generateTOMLTemplate(cfg *config.ServiceConfig) string {
	var sb strings.Builder
	sb.WriteString("#:schema https://raw.githubusercontent.com/sixwaaaay/workerd/refs/heads/main/schemas/workerd.schema.json\n")
	sb.WriteString(fmt.Sprintf("# Service: %s\n", cfg.Name))
	sb.WriteString("# Edit this file to configure your service.\n\n")
	sb.WriteString(fmt.Sprintf("name = %q\n", cfg.Name))
	sb.WriteString(fmt.Sprintf("command = %q\n", cfg.Command))
	sb.WriteString("# description = \"\"\n")
	sb.WriteString("# args = []\n")
	sb.WriteString(fmt.Sprintf("# working_dir = %q\n", "/var/lib/"+cfg.Name))
	sb.WriteString("# enabled = true\n")
	sb.WriteString("\n")
	sb.WriteString("# [environment]\n")
	sb.WriteString("# APP_ENV = \"production\"\n")
	sb.WriteString("\n")
	sb.WriteString("# [restart]\n")
	sb.WriteString("# policy = \"on-failure\"\n")
	sb.WriteString("# max_retries = 3\n")
	sb.WriteString("# backoff = \"exponential\"\n")
	sb.WriteString("# backoff_initial = \"1s\"\n")
	sb.WriteString("# backoff_max = \"60s\"\n")
	sb.WriteString("\n")
	sb.WriteString("# [health_check]\n")
	sb.WriteString("# type = \"http\"\n")
	sb.WriteString("# http_url = \"http://localhost:8080/health\"\n")
	sb.WriteString("# interval = \"10s\"\n")
	sb.WriteString("# timeout = \"5s\"\n")
	sb.WriteString("# retries = 3\n")
	sb.WriteString("\n")
	sb.WriteString("# [stop]\n")
	sb.WriteString("# signal = \"SIGTERM\"\n")
	sb.WriteString("# timeout = \"10s\"\n")
	sb.WriteString("\n")
	sb.WriteString("# [log]\n")
	sb.WriteString("# max_size = \"0\"  # 0 or empty = no rotation\n")
	sb.WriteString("# max_files = 0    # 0 = unlimited\n")
	return sb.String()
}

// serviceNameCompletion provides shell completion for service names.
func serviceNameCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	c := client.NewClient(socketPath)
	services, err := c.List()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, s := range services {
		names = append(names, s.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
