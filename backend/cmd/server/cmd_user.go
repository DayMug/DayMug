package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/term"

	"github.com/DayMug/DayMug/backend/internal/config"
	"github.com/DayMug/DayMug/backend/internal/service"
	"github.com/DayMug/DayMug/backend/internal/store"
)

// cmdUser dispatches the `user` subcommand: `daymug user <action> [...]`.
func cmdUser() {
	if len(os.Args) < 3 {
		printUserUsage()
		os.Exit(1)
	}
	action := os.Args[2]
	switch action {
	case "add":
		cmdUserAdd()
	case "passwd":
		cmdUserPasswd()
	case "help", "-h", "--help":
		printUserUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown user action: %s\n\n", action)
		printUserUsage()
		os.Exit(1)
	}
}

func printUserUsage() {
	fmt.Println(`Usage: daymug user <action> [--config PATH] [args]

Actions:
  add <username> --email EMAIL [--admin] [--work-dir PATH]
                       Create a new login-capable user. Prompts for the password
                       on stdin. <username> must equal the local-part of EMAIL
                       (everything before '@'). Promote the new user to admin
                       at the same time with --admin, or list the username under
                       admin.bootstrap_usernames in config.yaml so the next
                       restart promotes it.
  passwd <username>    Set or reset the password for a user (prompts on stdin).

Flags:
  --config PATH        Path to YAML config (overrides DAYMUG_CONFIG env;
                       defaults to <binary_dir>/config.yaml). The SQLite
                       path is hard-coded to <binary_dir>/data/database.db.`)
}

// openStoreFromConfig validates the YAML config (using the same resolution
// order as `serve`) and opens the SQLite database at the binary-relative
// fixed path. Returns the loaded *config.Config so callers that need to read
// users.default_home_root don't have to re-parse the YAML.
func openStoreFromConfig(configPath string) (*store.SQLiteStore, *config.Config) {
	cfg, err := config.Load(configPath)
	if err != nil {
		errf("load config: %v", err)
		os.Exit(1)
	}
	db, err := store.NewSQLiteStore(config.DefaultDBPath())
	if err != nil {
		errf("open database: %v", err)
		os.Exit(1)
	}
	if err := db.Init(); err != nil {
		errf("init database: %v", err)
		os.Exit(1)
	}
	return db, cfg
}

func errf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}

// readPasswordTwice prompts for a password on stdin without echo, asks for
// confirmation, and returns the verified value. Errors are non-recoverable
// (non-tty stdin, mismatched, empty, etc.) and exit the process.
func readPasswordTwice(prompt string) string {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		errf("stdin must be a terminal to read a password")
		os.Exit(1)
	}
	fmt.Fprint(os.Stderr, prompt)
	pw1, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		if errors.Is(err, io.EOF) {
			errf("aborted")
			os.Exit(1)
		}
		errf("read password: %v", err)
		os.Exit(1)
	}
	if strings.TrimSpace(string(pw1)) == "" {
		errf("password must not be empty")
		os.Exit(1)
	}

	fmt.Fprint(os.Stderr, "Confirm password: ")
	pw2, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		errf("read password: %v", err)
		os.Exit(1)
	}
	if !bytes.Equal(pw1, pw2) {
		errf("passwords do not match")
		os.Exit(1)
	}
	return string(pw1)
}

func cmdUserPasswd() {
	if err := runUserPasswd(); err != nil {
		errf("%v", err)
		os.Exit(1)
	}
}

func runUserPasswd() error {
	fs := flag.NewFlagSet("user passwd", flag.ContinueOnError)
	configPath := fs.String("config", "", "Path to YAML config (overrides DAYMUG_CONFIG env)")
	if err := fs.Parse(os.Args[3:]); err != nil {
		return fmt.Errorf("parse args: %w", err)
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: daymug user passwd [--config PATH] <username>")
	}
	username := fs.Arg(0)
	if username == "" {
		return fmt.Errorf("username must not be empty")
	}

	db, _ := openStoreFromConfig(*configPath)
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	user, err := db.GetUserByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("user %q not found — check the username", username)
		}
		return fmt.Errorf("look up user: %w", err)
	}

	plain := readPasswordTwice(fmt.Sprintf("New password for %s: ", username))
	hash, err := service.HashPassword(plain)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := db.SetUserPassword(ctx, user.ID, hash); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	fmt.Printf("password updated for %s\n", username)
	return nil
}

// addUserParams bundles the inputs to createHumanUser so the core validation
// + persistence logic can be unit-tested without faking flag parsing or
// terminal password prompts.
type addUserParams struct {
	Username string
	Email    string
	Password string
	Admin    bool
	WorkDir  string // empty => derive from cfg.Users.DefaultHomeRoot/<username>
}

func cmdUserAdd() {
	if err := runUserAdd(); err != nil {
		errf("%v", err)
		os.Exit(1)
	}
}

func runUserAdd() error {
	params, configPath, err := parseUserAddArgs(os.Args[3:])
	if err != nil {
		return err
	}

	db, cfg := openStoreFromConfig(configPath)
	defer func() { _ = db.Close() }()
	// Providers live in the database, not config.yaml; load them so the new
	// user can be bound to one.
	if err := service.LoadProviderSettings(context.Background(), db, cfg); err != nil {
		return err
	}

	params.Password = readPasswordTwice(fmt.Sprintf("New password for %s: ", params.Username))

	user, err := createHumanUser(context.Background(), db, cfg, params)
	if err != nil {
		return err
	}
	fmt.Printf("created user %s (id=%s, admin=%v, work_dir=%s)\n", user.Username, user.ID, user.IsAdmin, user.WorkDir)
	return nil
}

// parseUserAddArgs splits raw `daymug user add ...` args into the username
// (positional) and the flag values. Go's standard flag package stops parsing
// at the first non-flag argument, so `daymug user add admin --email x@y.com`
// would never see --email. We work around that by parsing repeatedly: each
// pass consumes leading flags up to the next positional, we move that
// positional aside, then parse what's left. Result: positional and flag
// arguments may appear in any order, matching what `--help` text suggests.
func parseUserAddArgs(raw []string) (addUserParams, string, error) {
	fs := flag.NewFlagSet("user add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "Path to YAML config (overrides DAYMUG_CONFIG env)")
	email := fs.String("email", "", "Email address (required; <username> must equal the local-part)")
	admin := fs.Bool("admin", false, "Mark the new user as admin")
	workDir := fs.String("work-dir", "", "Absolute work directory (default: users.default_home_root/<username>)")

	usage := "usage: daymug user add [--config PATH] <username> --email EMAIL [--admin] [--work-dir PATH]"

	rest := raw
	var positional []string
	for {
		if err := fs.Parse(rest); err != nil {
			return addUserParams{}, "", fmt.Errorf("parse args: %w", err)
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	if len(positional) < 1 {
		return addUserParams{}, "", fmt.Errorf("%s", usage)
	}
	if len(positional) > 1 {
		return addUserParams{}, "", fmt.Errorf("unexpected extra arguments %v\n%s", positional[1:], usage)
	}
	username := strings.TrimSpace(positional[0])
	if username == "" {
		return addUserParams{}, "", fmt.Errorf("username must not be empty")
	}

	return addUserParams{
		Username: username,
		Email:    strings.TrimSpace(*email),
		Admin:    *admin,
		WorkDir:  strings.TrimSpace(*workDir),
	}, *configPath, nil
}

// createHumanUser is the testable core of `daymug user add`. Mirrors the
// validation rules in handler.AdminHandler.CreateHuman so a CLI-created user
// is indistinguishable from a UI-created one — same username/email invariant,
// same default tools, same work_dir derivation. Returns the persisted row on
// success.
func createHumanUser(ctx context.Context, db store.Store, cfg *config.Config, p addUserParams) (store.User, error) {
	if p.Email == "" {
		return store.User{}, fmt.Errorf("--email is required")
	}
	expected := emailLocalPart(p.Email)
	if expected == "" {
		return store.User{}, fmt.Errorf("--email must contain a local-part before '@'")
	}
	if p.Username != expected {
		return store.User{}, fmt.Errorf("username %q must equal the local-part of --email %q (expected %q)", p.Username, p.Email, expected)
	}
	if err := service.ValidateUsername(p.Username); err != nil {
		return store.User{}, err
	}
	if p.Password == "" {
		return store.User{}, fmt.Errorf("password must not be empty")
	}

	if existing, err := db.GetUserByUsername(ctx, p.Username); err == nil && existing.ID != "" {
		return store.User{}, fmt.Errorf("username %q already exists", p.Username)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.User{}, fmt.Errorf("look up username: %w", err)
	}
	if existing, err := db.GetUserByEmail(ctx, p.Email); err == nil && existing.ID != "" {
		return store.User{}, fmt.Errorf("email %q already exists", p.Email)
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.User{}, fmt.Errorf("look up email: %w", err)
	}

	workDir := p.WorkDir
	if workDir == "" {
		if cfg == nil || cfg.Users.DefaultHomeRoot == "" {
			return store.User{}, fmt.Errorf("--work-dir is required (no users.default_home_root configured)")
		}
		workDir = filepath.Join(cfg.Users.DefaultHomeRoot, p.Username)
	}
	if !filepath.IsAbs(workDir) {
		return store.User{}, fmt.Errorf("--work-dir must be absolute, got %q", workDir)
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return store.User{}, fmt.Errorf("create work_dir %s: %w", workDir, err)
	}

	hash, err := service.HashPassword(p.Password)
	if err != nil {
		return store.User{}, fmt.Errorf("hash password: %w", err)
	}

	user := store.User{
		ID:           uuid.New().String(),
		Name:         p.Username,
		Username:     p.Username,
		PasswordHash: hash,
		Email:        p.Email,
		IsAdmin:      p.Admin,
		WorkDir:      workDir,
	}
	// Same rule as OIDC provisioning: the new user starts bound to the
	// "default" provider, or the first one configured.
	if prov, ok := cfg.InitialBindingProvider(); ok {
		user.ProviderBindings = map[string]string{prov.Type: prov.Name}
	}
	if err := db.CreateUser(ctx, user); err != nil {
		return store.User{}, fmt.Errorf("create user: %w", err)
	}
	if _, err := service.CreateDefaultAgent(ctx, db, user); err != nil {
		return store.User{}, fmt.Errorf("create default agent: %w", err)
	}
	return user, nil
}

// emailLocalPart returns everything before the first '@' in email, or "" if
// there's no '@' or the local-part itself is empty. Mirrors the helper of
// the same name in package handler — duplicated rather than imported to keep
// cmd/server free of a handler dependency.
func emailLocalPart(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return ""
}
