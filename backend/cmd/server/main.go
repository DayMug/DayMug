package main

import (
	"fmt"
	"os"

	// 嵌入 IANA 时区库。默认时区是 Asia/Shanghai，而 time.LoadLocation 只会去
	// 系统 zoneinfo 里找：distroless / scratch / alpine 这类容器没有 tzdata 包，
	// 查找失败会让 config 校验在启动时直接 fatal，也会让 cron 调度把合法任务
	// 标成 invalid_schedule 永久禁用（一次破坏性 DB 写入）。用约 450KB 的二进制
	// 体积换掉这类只在部署环境才暴露的故障，是刻意的取舍。
	_ "time/tzdata"
)

// Version is set at build time via -ldflags "-X main.Version=..."
var Version = "dev"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		return
	}
	cmd := os.Args[1]

	switch cmd {
	case "serve":
		cmdServe()
	case "stop":
		cmdStop()
	case "init":
		cmdInit()
	case "bootstrap":
		cmdBootstrap()
	case "doctor":
		cmdDoctor()
	case "uninstall":
		cmdUninstall()
	case "upgrade":
		cmdUpgrade()
	case "upgrade-watchdog":
		cmdUpgradeWatchdog()
	case "check-config":
		cmdCheckConfig()
	case "user":
		cmdUser()
	case "version", "--version", "-v":
		fmt.Printf("daymug %s\n", Version)
	case "status":
		cmdStatus()
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Usage: daymug <command>

Commands:
  bootstrap [--force-config]   Pre-flight + install + register system service.
            [--skip-service]   Reports Agent SDK and Codex app-server
            [--no-start]       prerequisites, lays out ~/.daymug/, then
                               registers and starts the user-level service
                               (systemd --user on Linux, LaunchAgent on
                               macOS); a running service is restarted on
                               the new binary. --no-start installs the
                               Linux unit without enabling or starting it.
                               Idempotent.
  doctor [--config p]          Check runtimes, config, service state, linger
                               (Linux) and whether the server answers on its
                               health endpoint. Exits non-zero when DayMug
                               cannot run; each failure prints a fix.
  uninstall [--purge] [--yes]  Stop and remove the user-level service. Keeps
                               ~/.daymug (config, database, user homes)
                               unless --purge, which asks before deleting the
                               directory this binary lives in (--yes skips
                               the question).
  init [--force]               Generate config.yaml + data/ + users/ in the
                               current directory (alternative to 'bootstrap'
                               for non-standard layouts).
  serve                        Start the HTTP server (default subcommand).
                               Stops on Ctrl-C (SIGINT). A bare SIGTERM is
                               ignored unless systemd is stopping the unit,
                               so a stray kill from an agent's shell cannot
                               take the server down; set
                               DAYMUG_HONOR_SIGTERM=1 for supervisors that
                               only send SIGTERM.
  stop [--timeout 90s]         Gracefully stop the server started from this
                               binary (via data/daymug.pid).
  check-config [--config p]    Load and validate config.yaml without starting
                               anything; exits non-zero on the first error.
                               The self-upgrade runs it with the incoming
                               release before swapping binaries.
  upgrade [--version vX.Y.Z]   Apply a release from the hard-coded release
          [--rollback]         manifest. Without --version the
                               latest release is used. --version pins the
                               upgrade to a specific tag (also supports
                               downgrades). --rollback restores the
                               previous binary instead and cannot be
                               combined with --version.
  user add <username>          Create a new login-capable user (prompts for
    --email EMAIL              the password). Use --admin to grant admin rights
    [--admin]                  immediately, or list the username under
    [--work-dir PATH]          admin.bootstrap_usernames in config.yaml so the
                               next restart promotes it. The first admin can
                               also be created on the web setup page.
                               Accounts are bound in the admin panel.
  user passwd <username>       Set or reset a user's password (interactive).
  version                      Print version information.
  status                       Show the user service status (systemd --user / launchd).

Environment variables:
  DAYMUG_CONFIG    Override the YAML config path. When unset, the binary
                     auto-resolves config.yaml next to itself (e.g.
                     ~/.daymug/config.yaml after 'daymug bootstrap').
  DAYMUG_ADDR      Listen address override (default: from config or :8080).

Filesystem layout (after 'daymug bootstrap'):
  ~/.daymug/daymug           the binary
  ~/.daymug/config.yaml       YAML config, auto-resolved
  ~/.daymug/data/database.db  SQLite database, auto-resolved
  ~/.daymug/logs/daymug.log   server log, mirrored from stderr
  ~/.daymug/users/            default home root for new human users`)
}
