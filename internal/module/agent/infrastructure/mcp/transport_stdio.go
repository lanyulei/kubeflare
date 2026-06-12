package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// stdioReadBufferSize 是 stdio 报文读取缓冲上限。MCP 单条报文(尤其 tools/list 的
// schema 与 tools/call 的结果)可能较大,放宽默认 bufio 上限避免 token 过长报错。
const stdioReadBufferSize = 1 << 20 // 1 MiB

// stdioTransport 在本地子进程的 stdin/stdout 上承载 JSON-RPC(MCP stdio 传输)。
// stdout 按行读报文,stdin 按行写报文,stderr 转结构化日志(便于排障 server 启动
// 失败)。close 时先关 stdin 让 server 优雅退出,再强制 kill 兜底,杜绝僵尸进程。
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Reader

	closeOnce sync.Once
	closeErr  error
}

// stdioConfig 描述如何启动一个 stdio MCP server 子进程。
type stdioConfig struct {
	command []string          // 可执行文件 + 参数,command[0] 为程序路径。
	env     map[string]string // 白名单注入的环境变量(凭证等),不继承宿主全环境。
	logger  *slog.Logger
	server  string // server 名,仅用于日志标识。
}

// newStdioConnect 返回一个建立 stdio 连接的 connectFunc。每次调用启动一个新的子进程
// 并完成管道接线;启动失败回收已分配资源。子进程环境仅含运行时必需的宿主白名单变量
// (PATH/HOME 等,见 buildEnv)加上配置显式声明的变量(凭证经 SecretsConfig 解密后
// 传入),不继承宿主全环境,在可运行性与凭证隔离之间取平衡。
func newStdioConnect(cfg stdioConfig) (connectFunc, error) {
	if len(cfg.command) == 0 || cfg.command[0] == "" {
		return nil, errors.New("mcp stdio server requires a command")
	}
	logger := cfg.logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context) (transport, error) {
		// 用不带超时的 background 派生命令 context:子进程需长生命周期运行,其存活
		// 由 Manager 的 close 显式控制,不能被建连时的 ctx 超时误杀。建连阶段的超时
		// 通过 initialize 调用的 ctx 体现(连接已建立、握手未完成即视为失败)。
		cmd := exec.Command(cfg.command[0], cfg.command[1:]...)
		cmd.Env = buildEnv(cfg.env)

		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("mcp stdio stdin pipe: %w", err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			_ = stdin.Close()
			return nil, fmt.Errorf("mcp stdio stdout pipe: %w", err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			_ = stdin.Close()
			return nil, fmt.Errorf("mcp stdio stderr pipe: %w", err)
		}
		if err := cmd.Start(); err != nil {
			_ = stdin.Close()
			return nil, fmt.Errorf("mcp stdio start %q: %w", cfg.command[0], err)
		}

		// stderr 转日志:server 启动 / 运行期的诊断信息(端口、鉴权失败等)由此可见。
		go drainStderr(stderr, logger, cfg.server)

		return &stdioTransport{
			cmd:    cmd,
			stdin:  stdin,
			reader: bufio.NewReaderSize(stdout, stdioReadBufferSize),
		}, nil
	}, nil
}

func (t *stdioTransport) send(payload []byte) error {
	// MCP stdio 以换行分隔报文;一次写出 payload + '\n',外层 session 已串行化写。
	if _, err := t.stdin.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}

func (t *stdioTransport) receive() (jsonRPCMessage, error) {
	return readMessage(t.reader)
}

// close 优雅关闭子进程:先关 stdin(多数 MCP server 据此退出),随后 kill 兜底并
// Wait 回收,避免僵尸进程。幂等。
func (t *stdioTransport) close() error {
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		if t.cmd.Process != nil {
			_ = t.cmd.Process.Kill()
		}
		// Wait 回收子进程资源;被 Kill 的进程返回非 nil error 属预期,不上报。
		_ = t.cmd.Wait()
	})
	return t.closeErr
}

// hostEnvPassthrough 是从宿主环境透传给子进程的最小变量白名单。stdio server 几乎
// 都需要 PATH 来定位运行时(npx→node、python 等),需要 HOME 定位用户级缓存 / 配置。
// 仅透传这几个无敏感语义的变量,既让 server 可正常运行,又不泄漏宿主的凭证类变量。
var hostEnvPassthrough = []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR"}

// buildEnv 构造子进程环境变量:宿主白名单变量(PATH/HOME 等运行时必需项)+ 配置
// 显式声明的变量(凭证等)。配置项覆盖同名宿主项。不继承宿主全环境,是 stdio 子进程
// 凭证隔离与可运行性的平衡点。
func buildEnv(env map[string]string) []string {
	merged := make(map[string]string, len(hostEnvPassthrough)+len(env))
	for _, key := range hostEnvPassthrough {
		if value, ok := os.LookupEnv(key); ok {
			merged[key] = value
		}
	}
	// 配置显式声明的变量优先级更高,可覆盖宿主同名项。
	for key, value := range env {
		merged[key] = value
	}
	out := make([]string, 0, len(merged))
	for key, value := range merged {
		out = append(out, key+"="+value)
	}
	return out
}

// drainStderr 持续把子进程 stderr 转为结构化日志,直至管道关闭。
//
// 必须保证 stderr 永远被消费:OS 管道缓冲(约 64KB)写满后,子进程任何 stderr 写入
// 都会永久阻塞。故这里用固定大小缓冲循环读取(而非 bufio.Scanner)——Scanner 遇到
// 超过其 buffer 上限的单行会直接停止扫描、goroutine 退出,从此 stderr 不再排空,
// 子进程随之写阻塞死锁。固定缓冲读取对任意长度输出都持续消费,杜绝该死锁。
func drainStderr(stderr io.Reader, logger *slog.Logger, server string) {
	reader := bufio.NewReaderSize(stderr, 32*1024)
	for {
		// ReadString 在命中换行或 EOF 时返回;超长行被切成多段分别记录,但始终消费,
		// 不会因单行过长而停止排空。
		line, err := reader.ReadString('\n')
		if trimmed := strings.TrimRight(line, "\r\n"); trimmed != "" {
			logger.Debug("mcp server stderr", "server", server, "line", trimmed)
		}
		if err != nil {
			return
		}
	}
}
