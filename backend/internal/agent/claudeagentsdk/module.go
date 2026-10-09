package claudeagentsdk

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// sdkModuleEnv pins the SDK entry point explicitly. Resolution below fills it
// in automatically when it can, so this is the escape hatch for layouts the
// search doesn't know about (a vendored copy, a pnpm store, a container image
// that installs the SDK somewhere exotic).
const sdkModuleEnv = "DAYMUG_CLAUDE_AGENT_SDK_MODULE"

const (
	sdkScope   = "@anthropic-ai"
	sdkPackage = "claude-agent-sdk"
	sdkEntry   = "sdk.mjs"
)

// sdkModule is a located installation of @anthropic-ai/claude-agent-sdk.
//
// Root — the node_modules directory the package sits in — matters as much as
// Entry: a jailed run only sees paths DayMug binds into the namespace, and a
// global install lives outside every default bind (it is under ~/.local or
// /usr/local/lib, and `npm` itself is just as invisible, so the bridge's own
// `npm root -g` fallback cannot rescue it either). Resolving the module here
// lets the caller bind it and hand the child an absolute path.
type sdkModule struct {
	Entry   string
	Root    string
	Version string
}

var (
	sdkModuleMu      sync.Mutex
	sdkModuleCached  *sdkModule
	npmGlobalRootMu  sync.Mutex
	npmGlobalRootRun bool
	npmGlobalRootDir string
)

// resolveSDKModule locates the SDK, caching the answer but re-resolving if the
// cached entry has since disappeared (an npm upgrade replaces the directory).
func resolveSDKModule() (sdkModule, bool) {
	sdkModuleMu.Lock()
	defer sdkModuleMu.Unlock()
	if sdkModuleCached != nil && fileExists(sdkModuleCached.Entry) {
		return *sdkModuleCached, true
	}
	sdkModuleCached = nil
	if override := strings.TrimSpace(os.Getenv(sdkModuleEnv)); override != "" {
		module := describeModule(override)
		sdkModuleCached = &module
		return module, true
	}
	for _, dir := range append(sdkSearchDirs(), npmGlobalRoot()) {
		if dir == "" {
			continue
		}
		entry := filepath.Join(dir, sdkScope, sdkPackage, sdkEntry)
		if !fileExists(entry) {
			continue
		}
		module := sdkModule{Entry: entry, Root: dir, Version: moduleVersion(filepath.Dir(entry))}
		sdkModuleCached = &module
		return module, true
	}
	return sdkModule{}, false
}

// sdkSearchDirs is the seam tests replace; it lists node_modules roots a global
// install plausibly lands in, cheapest first.
var sdkSearchDirs = defaultSDKSearchDirs

func defaultSDKSearchDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	for _, dir := range filepath.SplitList(os.Getenv("NODE_PATH")) {
		add(dir)
	}
	// npm's global root is <prefix>/lib/node_modules, and the prefix is the
	// grandparent of the node binary for every mainstream install (nvm, fnm,
	// system packages, a tarball unpacked into ~/.local).
	if node, err := exec.LookPath("node"); err == nil {
		if resolved, err := filepath.EvalSymlinks(node); err == nil {
			node = resolved
		}
		add(filepath.Join(filepath.Dir(filepath.Dir(node)), "lib", "node_modules"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".local", "lib", "node_modules"))
		add(filepath.Join(home, ".npm-global", "lib", "node_modules"))
		add(filepath.Join(home, "node_modules"))
	}
	add("/usr/local/lib/node_modules")
	add("/usr/lib/node_modules")
	add("/opt/homebrew/lib/node_modules")
	return dirs
}

// npmGlobalRoot asks npm where it installs globals — accurate for any prefix
// configuration, but it forks a Node process, so it runs once and only after
// the static candidates miss.
func npmGlobalRoot() string {
	npmGlobalRootMu.Lock()
	defer npmGlobalRootMu.Unlock()
	if npmGlobalRootRun {
		return npmGlobalRootDir
	}
	npmGlobalRootRun = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "npm", "root", "-g").Output()
	if err != nil {
		return ""
	}
	npmGlobalRootDir = strings.TrimSpace(string(out))
	return npmGlobalRootDir
}

// describeModule back-fills Root and Version for an operator-supplied entry
// path so an override is bound into the sandbox just like a discovered install.
func describeModule(entry string) sdkModule {
	module := sdkModule{Entry: entry, Root: filepath.Dir(entry)}
	for dir := filepath.Dir(entry); dir != "" && dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "node_modules" {
			module.Root = dir
			break
		}
	}
	module.Version = moduleVersion(filepath.Dir(entry))
	return module
}

func moduleVersion(packageDir string) string {
	data, err := os.ReadFile(filepath.Join(packageDir, "package.json"))
	if err != nil {
		return ""
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return ""
	}
	return manifest.Version
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// LocateSDK exposes the resolution above to the bootstrap pre-flight so the
// installer can tell an operator the SDK is missing *before* the first chat
// fails, instead of only writing it to the server log on turn one. Returns the
// entry point and package version when found.
func LocateSDK() (entry, version string, ok bool) {
	module, found := resolveSDKModule()
	return module.Entry, module.Version, found
}
