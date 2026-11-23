// Package executil prepares subprocesses for Termux's execution environment.
package executil

import (
	"debug/elf"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/aymanbagabas/go-pty"
)

// Prepare must run before StdinPipe/StdoutPipe/StderrPipe: a failed exec.Cmd.Start
// closes those pipes, so retrying Start on the same command cannot repair them.
func Prepare(cmd *exec.Cmd) error {
	if runtime.GOOS != "android" {
		return nil
	}
	p, err := prepare(cmd.Path, cmd.Args, cmd.Dir, cmd.Env, systemLinkerAvailable())
	if err != nil {
		return err
	}
	cmd.Path, cmd.Args, cmd.Env = p.path, p.args, p.env
	return nil
}

func PreparePTY(cmd *pty.Cmd) error {
	if runtime.GOOS != "android" {
		return nil
	}
	p, err := prepare(cmd.Path, cmd.Args, cmd.Dir, cmd.Env, systemLinkerAvailable())
	if err != nil {
		return err
	}
	cmd.Path, cmd.Args, cmd.Env = p.path, p.args, p.env
	return nil
}

func Run(cmd *exec.Cmd) error {
	if err := Prepare(cmd); err != nil {
		return err
	}
	return cmd.Run()
}

func Output(cmd *exec.Cmd) ([]byte, error) {
	if err := Prepare(cmd); err != nil {
		return nil, err
	}
	return cmd.Output()
}

var systemLinkerAvailable = sync.OnceValue(func() bool {
	// Direct linker execution was added in Android 10 (API 29). Older Android
	// can execute app files normally, but still needs Linux shebang remapping.
	cmd := exec.Command("/system/bin/getprop", "ro.build.version.sdk")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "LD_PRELOAD=") && !strings.HasPrefix(kv, "LD_LIBRARY_PATH=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	b, err := cmd.Output()
	if err != nil {
		return false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return err == nil && n >= 29
})

type launch struct {
	path string
	args []string
	env  []string
}

func prepare(path string, args []string, dir string, env []string, useLinker bool) (launch, error) {
	original := launch{path, args, env}
	if env == nil {
		env = os.Environ()
	}
	prefix := envValue(env, "PREFIX")
	if prefix == "" {
		prefix = envValue(env, "TERMUX__PREFIX")
	}
	if !filepath.IsAbs(prefix) || filepath.Clean(prefix) == "/" {
		return original, nil
	}
	resolved, err := executablePath(path, dir, env, prefix)
	if err != nil {
		return original, err
	}
	if !inTermuxData(resolved, prefix) {
		return original, nil
	}
	if len(args) == 0 {
		args = []string{path}
	}
	return prepareFile(resolved, args[1:], dir, env, prefix, useLinker, 0)
}

func prepareFile(path string, args []string, dir string, env []string, prefix string, useLinker bool, depth int) (launch, error) {
	if depth > 4 {
		return launch{}, fmt.Errorf("Termux 脚本解释器嵌套过深: %s", path)
	}
	if !inTermuxData(path, prefix) {
		return launch{path, append([]string{path}, args...), env}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return launch{}, err
	}
	header, err := io.ReadAll(io.LimitReader(file, 4096))
	file.Close()
	if err != nil {
		return launch{}, err
	}
	if strings.HasPrefix(string(header), "#!") {
		line := strings.TrimSpace(strings.SplitN(string(header[2:]), "\n", 2)[0])
		interpreter, option := line, ""
		if i := strings.IndexAny(line, " \t"); i >= 0 {
			interpreter, option = line[:i], strings.TrimSpace(line[i:])
		}
		if interpreter == "" {
			return launch{}, fmt.Errorf("Termux 脚本缺少解释器: %s", path)
		}
		// npm's codex/claude/pi normally use #!/usr/bin/env node. Resolve
		// node ourselves instead of asking env to exec it and hitting EACCES again.
		if filepath.Base(interpreter) == "env" && option != "" &&
			!strings.HasPrefix(option, "-") && !strings.ContainsAny(option, " \t") {
			interpreter, option = option, ""
		} else if strings.HasPrefix(interpreter, "/usr/bin/") ||
			strings.HasPrefix(interpreter, "/usr/local/bin/") || strings.HasPrefix(interpreter, "/bin/") {
			interpreter = filepath.Join(prefix, "bin", filepath.Base(interpreter))
		}
		interpreter, err = executablePath(interpreter, dir, env, prefix)
		if err != nil {
			return launch{}, fmt.Errorf("Termux 脚本解释器不可用 (%s): %w", path, err)
		}
		nextArgs := []string{}
		if option != "" {
			// Like the kernel, preserve the optional shebang argument as one
			// argument (including env -S). Never interpolate a shell command.
			nextArgs = append(nextArgs, option)
		}
		nextArgs = append(nextArgs, path)
		nextArgs = append(nextArgs, args...)
		return prepareFile(interpreter, nextArgs, dir, env, prefix, useLinker, depth+1)
	}
	p := launch{path, append([]string{path}, args...), env}
	if !useLinker {
		return p, nil
	}
	f, err := elf.Open(path)
	if err != nil {
		return launch{}, fmt.Errorf("Termux 可执行文件不是 ELF 或带 shebang 的脚本: %s", path)
	}
	defer f.Close()
	if f.Type != elf.ET_DYN {
		return launch{}, fmt.Errorf("Android 系统链接器不支持静态/非 PIE 程序: %s", path)
	}
	for _, prog := range f.Progs {
		if prog.Type != elf.PT_INTERP || prog.Filesz > 4096 {
			continue
		}
		b, err := io.ReadAll(prog.Open())
		if err != nil {
			return launch{}, err
		}
		linker := strings.TrimRight(string(b), "\x00")
		if (f.Class == elf.ELFCLASS64 && linker == "/system/bin/linker64") ||
			(f.Class == elf.ELFCLASS32 && linker == "/system/bin/linker") {
			p.path = linker
			p.args = append([]string{linker, path}, args...)
			p.env = setEnv(env, "TERMUX_EXEC__PROC_SELF_EXE", path)
			return p, nil
		}
	}
	return launch{}, fmt.Errorf("不是可供 Android 系统链接器加载的动态程序,请安装 Termux/Android 版本: %s", path)
}

func executablePath(path, dir string, env []string, prefix string) (string, error) {
	if !strings.ContainsRune(path, filepath.Separator) {
		search := filepath.SplitList(envValue(env, "PATH"))
		search = append(search, filepath.Join(prefix, "bin"))
		for _, base := range search {
			if !filepath.IsAbs(base) {
				continue // Do not implicitly execute from the working directory.
			}
			candidate, err := executablePath(filepath.Join(base, path), dir, env, prefix)
			if err == nil {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("未找到 Termux 命令 %s: %w", path, exec.ErrNotFound)
	}
	if !filepath.IsAbs(path) && dir != "" {
		path = filepath.Join(dir, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("不是具有执行权限的文件: %s", path)
	}
	return path, nil
}

// Executable accounts for Android system-linker launches, for which
// os.Executable (/proc/self/exe) otherwise reports the linker itself.
func Executable() (string, error) {
	if runtime.GOOS == "android" {
		for _, path := range []string{os.Getenv("TERMUX_EXEC__PROC_SELF_EXE"), os.Args[0]} {
			if !filepath.IsAbs(path) || path == "/system/bin/linker64" || path == "/system/bin/linker" {
				continue
			}
			if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
				return path, nil
			}
		}
	}
	return os.Executable()
}

func inTermuxData(path, prefix string) bool {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	realPrefix, err := filepath.EvalSymlinks(prefix)
	if err != nil || realPrefix == "/" {
		return false
	}
	root := realPrefix
	// $PREFIX is <app-data>/files/usr; native CLI installers also use $HOME.
	if filepath.Base(realPrefix) == "usr" && filepath.Base(filepath.Dir(realPrefix)) == "files" {
		root = filepath.Dir(realPrefix)
	}
	return strings.HasPrefix(realPath, root+string(filepath.Separator))
}

func envValue(env []string, key string) string {
	value := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			value = v
		}
	}
	return value
}

func setEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return append(out, key+"="+value)
}
