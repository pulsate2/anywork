package git

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// git 需要凭据时执行 GIT_ASKPASS <prompt>(prompt 作为 argv[1]),从它的 stdout
// 读一行作为答案。我们把 GIT_ASKPASS 指向本二进制自身(自重生),当环境里出现
// LR_ASKPASS_MODE=1 时,本进程不启动服务,而是作为"凭据代理"跑 askpass 逻辑:
// 连回主进程的临时监听端口,发 token+prompt,等主进程经 WS 从浏览器要到的答案。

// 环境变量键。
const (
	envAskPassMode  = "LR_ASKPASS_MODE"
	envAskPassAddr  = "LR_ASKPASS_ADDR"  // 主进程临时监听地址,如 127.0.0.1:51234
	envAskPassToken = "LR_ASKPASS_TOKEN" // 一次 git 调用的随机 token
)

// AskPassModeReported 供 main 判断是否进入代理模式。
func InAskPassMode() bool { return os.Getenv(envAskPassMode) == "1" }

// RunAskPassProxy 以 git 的 askpass 身份运行:交互一行经主进程拿到的答案。
// 主进程 main() 在启动服务器前调用;不会返回,结束后 os.Exit。
func RunAskPassProxy() {
	prompt := ""
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}
	addr := os.Getenv(envAskPassAddr)
	token := os.Getenv(envAskPassToken)

	// 连主进程;超时防止 git 没配好/主进程已退出时我们的子进程挂死。
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		os.Exit(1)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))

	// 协议:一行 token,一行 prompt;随后读一行答案。
	if _, err := fmt.Fprintf(conn, "%s\n%s\n", token, prompt); err != nil {
		os.Exit(1)
	}
	rd := bufio.NewReader(conn)
	ans, err := rd.ReadString('\n')
	if err != nil && err != io.EOF {
		os.Exit(1)
	}
	os.Stdout.WriteString(ans)
	os.Exit(0)
}

// credServer 为一次 run() 创造的临时认证服务器:单连接,serve 一次就被 .Close。
type credServer struct {
	ln     net.Listener
	broker *CredentialBroker
	token  string
}

// newCredServer 创建并启动一次性回环监听;返回 env 需要注入的 GIT_ASKPASS 相关变量。
func (b *CredentialBroker) newCredServer() (*credServer, []string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, err
	}
	token := randomToken()
	exe, err := os.Executable()
	if err != nil {
		ln.Close()
		return nil, nil, err
	}
	s := &credServer{ln: ln, broker: b, token: token}
	env := []string{
		"GIT_ASKPASS=" + exe,
		"GIT_TERMINAL_PROMPT=0",
		envAskPassMode + "=1",
		envAskPassAddr + "=" + ln.Addr().String(),
		envAskPassToken + "=" + token,
	}
	return s, env, nil
}

// closeEnvToken 供 run() 在命令结束/出错后调用,让 askpass 至少能区分"命令已结束"。
func (s *credServer) close() {
	if s == nil {
		return
	}
	s.ln.Close()
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// guessField 从 git 的 prompt 文本判断这次要的是用户名还是密码。
func guessField(prompt string) string {
	lower := strings.ToLower(prompt)
	switch {
	case strings.Contains(lower, "password"):
		return "password"
	case strings.Contains(lower, "username"):
		return "username"
	default:
		return "username" // 默认:先问用户名
	}
}

var errBadCredToken = errors.New("bad askpass token")

// serveOnce 接受一次 askpass 子进程连接,完成一次 token+prompt→答案的交换。
// 用后即弃,连接关闭即返回。
func (s *credServer) serveOnce(timeout time.Duration) error {
	s.ln.(*net.TCPListener).SetDeadline(time.Now().Add(timeout))
	conn, err := s.ln.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	rd := bufio.NewReader(conn)
	token, err := rd.ReadString('\n')
	if err != nil {
		return err
	}
	prompt, err := rd.ReadString('\n')
	if err != nil {
		return err
	}
	token = trimNL(token)
	prompt = trimNL(prompt)
	if token != s.token {
		return errBadCredToken
	}
	ans, err := s.broker.await(token, prompt, guessField(prompt), timeout)
	if err != nil {
		return err
	}
	_, err = conn.Write([]byte(ans + "\n"))
	return err
}

func trimNL(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '\r' {
		s = s[:len(s)-1]
	}
	return s
}