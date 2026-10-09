package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// ProcessSpawner abstracts how a child process is started. Two implementations
// ship today for different protocol requirements:
//
//   - PipeSpawner gives structured transports such as Agent SDK and app-server
//     three independent streams. Their framing is incompatible with a PTY.
//   - PtySpawner allocates a pseudo-terminal and binds the child's
//     stdin / stdout / stderr to its slave side. It is the only layout used by
//     ordinary CLI adapters and interactive terminals.
//
// Spawners are stateless and safe for concurrent use. The chat path and the
// admin login path share the same interface.
type ProcessSpawner interface {
	// Spawn starts the child described by req. On success the caller owns
	// the returned RunningProcess and must eventually call Wait(). On
	// failure (binary missing, fork error, PTY allocation failed) the
	// process is not started and no cleanup is required.
	Spawn(ctx context.Context, req SpawnRequest) (RunningProcess, error)
}

// SpawnRequest describes a single process invocation. Both spawners read
// the same struct so the call site does not branch on mode.
type SpawnRequest struct {
	// Argv[0] is the binary; the remaining entries are passed verbatim.
	Argv []string
	// Env is the full child environment. Empty inherits the parent.
	Env []string
	// WorkDir is the child's working directory. Empty inherits the parent.
	WorkDir string
	// InitialStdin, if non-empty, is delivered to the child's stdin.
	// In one-shot mode (Interactive=false) it is wired in via cmd.Stdin
	// — a pipe even under PtySpawner — and stdin closes after delivery so
	// the child sees EOF, matching `claude -p`'s "read prompt until EOF"
	// contract. In Interactive mode it is asynchronously written to the
	// open stdin (pipe or PTY master) and stdin stays open for further
	// writes via Stdin().
	InitialStdin string
	// Interactive requests an open, writable stdin handle that lives for
	// the duration of the process. Used by the admin terminal to forward
	// keystrokes (including pasted OAuth codes) from the web UI. One-shot
	// callers (chat) leave this false.
	Interactive bool
	// StopGrace bounds how long a cancelled child is given to shut down
	// cleanly. When ctx is cancelled the spawner sends SIGTERM to the
	// child's whole process group; if the group hasn't exited within
	// StopGrace it is force-killed (SIGKILL) and Wait returns. Zero selects
	// defaultStopGrace.
	StopGrace time.Duration
	// InitialRows / InitialCols set the pseudo-terminal window size at
	// spawn time. Only honoured by PtySpawner; ignored by PipeSpawner. Zero
	// in either dimension falls back to the PTY default (80x24). Used by the
	// browser-terminal path so the child's TUI lays out for the client's
	// real viewport from the first frame instead of reflowing after the
	// first resize event arrives.
	InitialRows uint16
	InitialCols uint16
}

// RunningProcess is the live handle to a started child. Spawner-specific
// internals (pipes, PTY master) are hidden behind this interface so the
// chat and login paths see the same surface.
type RunningProcess interface {
	// Stdout returns the child's stdout stream. In PTY mode this carries
	// the merged stdout + stderr stream (PTYs only expose a single
	// bidirectional byte channel).
	Stdout() io.Reader
	// Stderr returns the child's stderr stream. nil in PTY mode (the
	// merged stream lives on Stdout).
	Stderr() io.Reader
	// Stdin returns a writer for sending bytes to the child's stdin.
	// Returns nil when the request set Interactive=false. Closing the
	// writer signals EOF to the child (relevant for pipe mode; PTY EOF
	// is handled by sending the configured eof character, currently
	// ^D / 0x04, before closing).
	Stdin() io.WriteCloser
	// Wait blocks until the child exits and reaps it. Returns the same
	// error semantics as exec.Cmd.Wait — nil on a zero exit code, a
	// *exec.ExitError otherwise. Must be called exactly once.
	Wait() error
	// Signal forwards an OS signal to the child.
	Signal(sig os.Signal) error
}

// ProcessInfo is optionally implemented by RunningProcess values that can
// expose their OS process identifiers. Stream watchdogs use this to inspect
// process-tree activity before declaring a silent CLI stuck.
type ProcessInfo interface {
	PID() int
	PGID() int
}

// Resizable is implemented by RunningProcess values whose underlying
// transport can change window size after start — i.e. PTY-backed children.
// The browser-terminal path type-asserts to this so it can forward xterm.js
// resize events to the child; pipe-backed processes simply don't implement
// it and resize is silently a no-op for them.
type Resizable interface {
	// Setsize updates the pseudo-terminal window size and raises SIGWINCH
	// on the child so its TUI relays out. Rows/cols of zero are rejected to
	// avoid handing the kernel a degenerate winsize.
	Setsize(rows, cols uint16) error
}

// NewSpawner returns the process layout used by ordinary CLI adapters. Runner
// mode is intentionally not configurable: structured transports explicitly
// downgrade this to PipeSpawner at their protocol boundary.
func NewSpawner() ProcessSpawner {
	return &PtySpawner{}
}

// defaultStopGrace is the SIGTERM→SIGKILL window applied to a cancelled child
// when SpawnRequest.StopGrace is zero. Long enough for claude to flush its
// session JSONL and run a quick Stop hook, short enough that a user who
// clicked "stop" isn't left waiting on a wedged process.
const defaultStopGrace = 5 * time.Second

func stopGrace(req SpawnRequest) time.Duration {
	if req.StopGrace > 0 {
		return req.StopGrace
	}
	return defaultStopGrace
}

// killSweepMargin keeps exec.Cmd's own WaitDelay deadline strictly behind the
// group SIGKILL sweep. The ordering is the whole point: WaitDelay kills only
// the leader, and the Wait() that unblocks right after runs reap(), which
// cancels the escalation. Fired together, the descendants would win that race
// and survive — the very leak the sweep exists to close.
const killSweepMargin = time.Second

// groupStopper escalates a cancelled child's process group from SIGTERM to
// SIGKILL. It supplies the half exec.Cmd cannot: cmd.WaitDelay only ever
// SIGKILLs cmd.Process, so a descendant that ignores SIGTERM — an MCP server,
// a bash tool subprocess — outlives the leader and is orphaned. Because Wait()
// returning also drops the group from liveChildren, the shutdown sweep can no
// longer reach it either, making the leak permanent. That is what a user who
// pressed Stop (or whose run tripped the stall watchdog) sees as "the web UI
// says idle but the agent is still running".
//
// reap() gates the escalation on the child not having been collected yet.
// Signalling -pgid after Wait() reaped the leader is a live hazard rather than
// a harmless no-op: the kernel may reuse the pid the moment the zombie is
// gone, so a late timer could SIGKILL an unrelated group. The timer callback
// and reap() take the same mutex, so the callback either runs while the group
// is provably still ours or observes reaped and does nothing.
type groupStopper struct {
	mu     sync.Mutex
	timer  *time.Timer
	reaped bool
}

// arm schedules the SIGKILL sweep of pgid one grace period out. Repeat calls
// (ctx cancel can only fire once, but Cancel is caller-supplied) keep the
// first deadline rather than pushing it back.
func (g *groupStopper) arm(pgid int, grace time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.reaped || g.timer != nil {
		return
	}
	g.timer = time.AfterFunc(grace, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if g.reaped {
			return
		}
		signalGroup(pgid, syscall.SIGKILL)
	})
}

// reap records that Wait() has collected the child. Callers must invoke it
// before unregisterChild so no escalation can outlive the pid's uniqueness.
func (g *groupStopper) reap() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reaped = true
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
}

// setGracefulStop replaces exec.CommandContext's default cancel behaviour — an
// abrupt SIGKILL of the leader process only — with a graceful escalation: on
// ctx cancel send SIGTERM to the child's whole process group, then give it
// `grace` to exit before SIGKILLing that same group.
//
// Signalling the group (negative pid) is the point: claude spawns its own
// descendants — MCP servers, tool subprocesses, bash — and a leader-only kill
// orphans them, leaking processes that keep holding the account's resources.
// The caller must arrange for the child to lead its own group (Setpgid for
// pipes; PTY mode inherits Setsid from pty.Start) so the negative-pid signal
// targets only the claude tree and can never escape to the server's own group.
//
// The returned groupStopper must be reaped by the caller's Wait().
func setGracefulStop(cmd *exec.Cmd, grace time.Duration) *groupStopper {
	stopper := &groupStopper{}
	cmd.WaitDelay = grace + killSweepMargin
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Setpgid / Setsid make the child its own group leader, so pid == pgid.
		pgid := cmd.Process.Pid
		// Arm before signalling: if delivering SIGTERM is itself what fails,
		// the sweep still has to be scheduled.
		stopper.arm(pgid, grace)
		// ESRCH means the group already exited between cancel and signal —
		// not an error worth surfacing.
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}
	return stopper
}

// liveChildren indexes the process group of every child that has been
// started and not yet reaped, so shutdown can force-terminate the whole
// fleet.
//
// 为什么需要一张全局表：每次 run 的 ctx 都刻意挂在 context.Background() 上
// （好让一次 drain 不打断正在流式输出的回复），所以关机超时的时候没有任何
// ctx 能取消它们，setGracefulStop 里那条 SIGTERM 也就永远不会触发。Linux 上
// systemd 的 KillMode=mixed 能兜住，但 macOS launchd 和 --skip-service 直跑
// 的部署会把 claude 子树孤儿化——孤儿继续写那份重启后马上要 --resume 的
// session JSONL，下一次启动读到的就是被两个进程交错写坏的记录。
//
// 表里存的是进程组 id 而不是 pid：子进程自己领一个组（Setpgid / Setsid），
// 组里还有它 fork 出来的 MCP server、bash 等后代，只杀 leader 一样会漏。
// 条目在 Wait() 收尸后才删除，因此登记期间 pid 不可能被系统复用——僵尸占着
// 号——向 -pgid 发信号不会误伤无关进程。
var liveChildren = struct {
	mu     sync.Mutex
	nextID int64
	groups map[int64]int // 登记 token → 子进程组 id
}{groups: map[int64]int{}}

// registerChild records a started child's process group and returns the
// token that unregisterChild needs. A pgid of 0 (process never started)
// is not recorded and yields token 0.
func registerChild(pgid int) int64 {
	if pgid <= 0 {
		return 0
	}
	liveChildren.mu.Lock()
	defer liveChildren.mu.Unlock()
	liveChildren.nextID++
	token := liveChildren.nextID
	liveChildren.groups[token] = pgid
	return token
}

func unregisterChild(token int64) {
	if token == 0 {
		return
	}
	liveChildren.mu.Lock()
	delete(liveChildren.groups, token)
	liveChildren.mu.Unlock()
}

func liveChildGroups() []int {
	liveChildren.mu.Lock()
	defer liveChildren.mu.Unlock()
	out := make([]int, 0, len(liveChildren.groups))
	for _, pgid := range liveChildren.groups {
		out = append(out, pgid)
	}
	return out
}

// terminateSweepPoll is how often TerminateAll re-checks whether the
// SIGTERMed groups have gone away. Small enough that a clean shutdown
// isn't padded by much, large enough not to spin.
const terminateSweepPoll = 50 * time.Millisecond

// TerminateAll sends SIGTERM to every live child's process group, gives them
// `grace` to exit, then SIGKILLs whatever is still registered. Returns the
// number of process groups that were signalled.
//
// This is the shutdown-path trigger for the machinery setGracefulStop already
// builds per-child: the server calls it only after the graceful job drain has
// timed out, so a reply that is still streaming inside the drain window is
// never cut short. Reaping stays with whoever called Spawn — killing the
// group makes its Wait return, which is exactly what unblocks the run
// goroutine so it can persist its result before the DB is closed.
func TerminateAll(grace time.Duration) int {
	pgids := liveChildGroups()
	if len(pgids) == 0 {
		return 0
	}
	for _, pgid := range pgids {
		signalGroup(pgid, syscall.SIGTERM)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if len(liveChildGroups()) == 0 {
			return len(pgids)
		}
		time.Sleep(terminateSweepPoll)
	}
	for _, pgid := range liveChildGroups() {
		signalGroup(pgid, syscall.SIGKILL)
	}
	return len(pgids)
}

// signalGroup signals a whole process group, refusing the two values that
// would turn a bookkeeping slip into "the server signals itself": pgid 0/1
// (kill(2) reads a non-positive group as "everything I may signal") and the
// server's own group.
func signalGroup(pgid int, sig syscall.Signal) {
	if pgid <= 1 || pgid == syscall.Getpgrp() {
		return
	}
	// 唯一的预期错误是 ESRCH（组已经退了），不值得上报。
	_ = syscall.Kill(-pgid, sig)
}

// ErrWorkDirUnavailable reports a SpawnRequest whose WorkDir cannot be entered
// — normally a conversation whose work_dir was deleted or renamed on disk.
var ErrWorkDirUnavailable = errors.New("working directory unavailable")

// checkWorkDir names the real culprit when the child's chdir(2) is doomed.
//
// os.startProcess only runs its "was it actually the Dir?" stat when
// ProcAttr.Sys is nil, and both spawners set SysProcAttr (Setpgid for pipes,
// Setsid via pty.Start). Without that stat a missing work_dir surfaces as a
// bare ENOENT from the child, which the parent wraps as
// &PathError{Op: "fork/exec", Path: argv[0]} — reporting a deleted directory
// as "fork/exec /usr/local/bin/node: no such file or directory" and sending
// whoever reads it hunting for a node install that was never broken.
//
// The returned error deliberately does not wrap the syscall errno: ENOENT is
// SpawnWithRetry's signal for the claude self-upgrade symlink race, and a
// deleted work_dir will not fix itself within three 200ms retries.
func checkWorkDir(dir string) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// The one case that must drop the errno: keeping it would put
			// ENOENT back in the chain and re-arm the retry this check exists
			// to skip. Every other stat failure (EACCES, ENOTDIR, …) is safe
			// to wrap and worth reporting verbatim.
			return fmt.Errorf("%w: %s no longer exists", ErrWorkDirUnavailable, dir)
		}
		return fmt.Errorf("%w: %s: %w", ErrWorkDirUnavailable, dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrWorkDirUnavailable, dir)
	}
	return nil
}

// PipeSpawner gives structured protocols independent stdin / stdout / stderr
// streams. It remains available for Agent SDK, app-server, and tests, but is
// not a selectable runner mode.
type PipeSpawner struct{}

// Spawn implements ProcessSpawner.
func (PipeSpawner) Spawn(ctx context.Context, req SpawnRequest) (RunningProcess, error) {
	if len(req.Argv) == 0 {
		return nil, fmt.Errorf("spawner: empty argv")
	}
	if err := checkWorkDir(req.WorkDir); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	if req.Env != nil {
		cmd.Env = req.Env
	}
	// Lead a new process group so a cancel can signal claude's whole tree,
	// then arrange the graceful SIGTERM→SIGKILL escalation.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stopper := setGracefulStop(cmd, stopGrace(req))

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe stderr: %w", err)
	}

	var stdinWriter io.WriteCloser
	if req.Interactive {
		// Interactive mode: keep stdin open for the lifetime of the child.
		// The caller closes it (or the spawner closes it on Wait) to signal
		// EOF.
		stdinWriter, err = cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("pipe stdin: %w", err)
		}
	} else if req.InitialStdin != "" {
		// One-shot mode: the prompt is read via stdin and the child sees
		// EOF the moment the strings.Reader is drained. Matches the
		// historical claude -p flow.
		cmd.Stdin = strings.NewReader(req.InitialStdin)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", req.Argv[0], err)
	}

	if req.Interactive && req.InitialStdin != "" {
		// Deliver the prologue asynchronously so a child that does not
		// read stdin immediately doesn't deadlock Spawn.
		go func() {
			_, _ = io.WriteString(stdinWriter, req.InitialStdin)
		}()
	}

	return &pipeProcess{
		cmd:     cmd,
		stdout:  stdout,
		stderr:  stderr,
		stdin:   stdinWriter,
		stopper: stopper,
		// Setpgid makes the child its own group leader, so pid == pgid.
		liveToken: registerChild(cmd.Process.Pid),
	}, nil
}

type pipeProcess struct {
	cmd       *exec.Cmd
	stdout    io.Reader
	stderr    io.Reader
	stdin     io.WriteCloser
	stopper   *groupStopper
	liveToken int64
	waitOnce  sync.Once
	waitErr   error
}

func (p *pipeProcess) Stdout() io.Reader     { return p.stdout }
func (p *pipeProcess) Stderr() io.Reader     { return p.stderr }
func (p *pipeProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *pipeProcess) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
func (p *pipeProcess) PGID() int {
	pid := p.PID()
	if pid == 0 {
		return 0
	}
	if pgid, err := syscall.Getpgid(pid); err == nil {
		return pgid
	}
	return pid
}
func (p *pipeProcess) Signal(s os.Signal) error {
	if p.cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	return p.cmd.Process.Signal(s)
}
func (p *pipeProcess) Wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		// 收尸之后才注销：在此之前 pid 号被僵尸占着不会被复用，
		// TerminateAll 对 -pgid 发信号才不会误伤别人。停掉升级定时器同理，
		// 而且必须排在注销之前——两者都是靠"还没收尸"来保证 pgid 仍属于我们。
		p.stopper.reap()
		unregisterChild(p.liveToken)
	})
	return p.waitErr
}

// PtySpawner allocates a pseudo-terminal via github.com/creack/pty and
// binds the child's three standard descriptors to its slave side. The
// returned RunningProcess exposes the PTY master as both Stdout (read)
// and Stdin (write); Stderr is nil because PTYs only carry a single
// merged byte stream.
//
// PTY mode is required for any subcommand that gates behaviour on
// isatty(stdin) — `claude login` is the headline case (it only prints
// the OAuth URL when it thinks it's talking to a real terminal). Ordinary CLI
// adapters always use this mode; headless runs set NO_COLOR / TERM=dumb so ANSI
// escapes do not pollute their NDJSON stream.
type PtySpawner struct{}

// Spawn implements ProcessSpawner.
func (PtySpawner) Spawn(ctx context.Context, req SpawnRequest) (RunningProcess, error) {
	if len(req.Argv) == 0 {
		return nil, fmt.Errorf("spawner: empty argv")
	}
	if err := checkWorkDir(req.WorkDir); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, req.Argv[0], req.Argv[1:]...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	if req.Env != nil {
		cmd.Env = req.Env
	}
	// pty.Start sets Setsid (new session ⇒ new process group led by the
	// child), so the negative-pid SIGTERM in setGracefulStop targets the
	// claude tree exactly as it does for pipes.
	stopper := setGracefulStop(cmd, stopGrace(req))

	// A one-shot prompt goes in over a pipe; only stdout/stderr ride the
	// PTY. Through the terminal it breaks in three ways: `codex exec`
	// refuses to read a prompt from a TTY stdin ("No prompt provided") and
	// exits 1, the line discipline echoes the prompt onto the front of the
	// first NDJSON line, and canonical mode silently truncates any input
	// line past 4095 bytes. pty.Start leaves a pre-set Stdin alone, but its
	// TIOCSCTTY targets child fd 0 by default, which is no longer the
	// terminal — point it at stdout instead.
	if !req.Interactive && req.InitialStdin != "" {
		cmd.Stdin = strings.NewReader(req.InitialStdin)
		cmd.SysProcAttr = &syscall.SysProcAttr{Ctty: 1}
	}

	var ptmx *os.File
	var err error
	if req.InitialRows > 0 && req.InitialCols > 0 {
		ptmx, err = pty.StartWithSize(cmd, &pty.Winsize{Rows: req.InitialRows, Cols: req.InitialCols})
	} else {
		ptmx, err = pty.Start(cmd)
	}
	if err != nil {
		return nil, fmt.Errorf("pty start %s: %w", req.Argv[0], err)
	}

	// pty.Start sets Setsid, so the child leads its own session and group:
	// pid == pgid, same as the pipe path.
	proc := &ptyProcess{cmd: cmd, ptmx: ptmx, interactive: req.Interactive, stopper: stopper, liveToken: registerChild(cmd.Process.Pid)}

	if req.Interactive && req.InitialStdin != "" {
		go func() {
			_, _ = ptmx.WriteString(req.InitialStdin)
		}()
	}

	return proc, nil
}

// ptyReader wraps the PTY master fd so its Read translates the EIO that
// Linux raises after the child closes the slave side into a clean EOF.
// Without this io.ReadAll on a PTY surfaces the EIO as an error, which
// would force every caller to special-case it.
type ptyReader struct {
	f *os.File
}

func (r *ptyReader) Read(p []byte) (int, error) {
	n, err := r.f.Read(p)
	if err == nil {
		return n, nil
	}
	// PathError → wrapped errno. SyscallError on some kernel paths.
	if errors.Is(err, syscall.EIO) {
		return n, io.EOF
	}
	return n, err
}

// ptyProcess wraps the PTY master and the child cmd. Stdout reads from
// the master; Stdin writes to it (in interactive mode); Stderr is nil
// because the PTY merges stderr into the same stream.
type ptyProcess struct {
	cmd         *exec.Cmd
	ptmx        *os.File
	interactive bool
	stopper     *groupStopper
	liveToken   int64
	waitOnce    sync.Once
	waitErr     error
}

func (p *ptyProcess) Stdout() io.Reader { return &ptyReader{f: p.ptmx} }
func (p *ptyProcess) Stderr() io.Reader { return nil }
func (p *ptyProcess) Stdin() io.WriteCloser {
	if !p.interactive {
		return nil
	}
	return p.ptmx
}
func (p *ptyProcess) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
func (p *ptyProcess) PGID() int {
	pid := p.PID()
	if pid == 0 {
		return 0
	}
	if pgid, err := syscall.Getpgid(pid); err == nil {
		return pgid
	}
	return pid
}
func (p *ptyProcess) Signal(s os.Signal) error {
	if p.cmd.Process == nil {
		return fmt.Errorf("process not started")
	}
	return p.cmd.Process.Signal(s)
}

// Setsize implements Resizable. pty.Setsize updates the master's winsize and
// the kernel raises SIGWINCH on the child automatically, so well-behaved TUIs
// (claude/codex included) relayout without any extra signal from us.
func (p *ptyProcess) Setsize(rows, cols uint16) error {
	if rows == 0 || cols == 0 {
		return fmt.Errorf("setsize: rows and cols must be non-zero (got %dx%d)", rows, cols)
	}
	return pty.Setsize(p.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}
func (p *ptyProcess) Wait() error {
	p.waitOnce.Do(func() {
		p.waitErr = p.cmd.Wait()
		// Same ordering rule as the pipe path: stop the SIGKILL escalation
		// and unregister only once the child is reaped.
		p.stopper.reap()
		unregisterChild(p.liveToken)
		// Closing the master after Wait signals EOF on any pending reader
		// and releases the kernel-side PTY pair. Done after Wait so a
		// late flush from the child still lands on the master before the
		// reader sees EOF.
		_ = p.ptmx.Close()
	})
	return p.waitErr
}
