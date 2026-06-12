package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// transport 是 MCP 的底层双向报文通道,屏蔽 stdio(子进程)与 Streamable HTTP
// (远端)的差异。它只负责"发一条 JSON-RPC 报文 / 收一条 JSON-RPC 报文"与关闭,
// 请求-响应配对、超时、重连等语义由上层 session/Client 统一处理,避免散落在各
// transport 实现中。
type transport interface {
	// send 写出一条已编码的 JSON-RPC 报文(不含换行;stdio 实现自行追加分隔符)。
	send(payload []byte) error
	// receive 阻塞读取下一条 JSON-RPC 报文。连接关闭 / EOF 时返回错误,触发上层
	// 进入重连。
	receive() (jsonRPCMessage, error)
	// close 释放底层资源(关闭子进程 stdin/stdout 并回收进程,或关闭 HTTP 流)。
	close() error
}

// errSessionClosed 表示会话已关闭,所有在途与后续请求据此快速失败,不再等待。
var errSessionClosed = errors.New("mcp session closed")

// session 在一条 transport 之上实现 JSON-RPC 的请求-响应多路复用:单个后台读取
// goroutine 解析对端报文,按 id 派发到对应的等待者;写入侧串行化(互斥)避免并发
// 写交错。一个 session 对应"一次成功建立的连接",连接断开后由 Client 丢弃并重建,
// session 自身不做重连(职责单一)。
type session struct {
	transport transport
	nextID    atomic.Int64

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[int64]chan jsonRPCMessage
	closed  bool
	closeFn sync.Once

	// done 在读取 goroutine 退出后关闭,使在途请求能在连接断开时立即失败。
	done chan struct{}
	// readErr 记录读取 goroutine 的终止原因,供新请求快速失败时返回更具体的错误。
	readErr atomic.Pointer[error]
}

// newSession 基于已建立的 transport 启动一个会话并拉起后台读取循环。
func newSession(t transport) *session {
	s := &session{
		transport: t,
		pending:   make(map[int64]chan jsonRPCMessage),
		done:      make(chan struct{}),
	}
	go s.readLoop()
	return s
}

// readLoop 持续读取对端报文:响应按 id 派发给等待者;对端发起的请求 / 通知按"最小
// 可用"策略处理(对请求回 method not found,对通知忽略),保证不阻塞对端。任何读取
// 错误都终止会话并唤醒全部在途请求。
func (s *session) readLoop() {
	for {
		msg, err := s.transport.receive()
		if err != nil {
			s.terminate(err)
			return
		}
		// 响应:无 Method 且带 id,派发到等待者。
		if msg.Method == "" {
			s.dispatchResponse(msg)
			continue
		}
		// 对端请求(带 id):我方不实现 server→client 的能力调用,统一回 method not
		// found,使对端不必阻塞等待。通知(无 id)直接忽略。
		if len(msg.ID) > 0 {
			s.replyMethodNotFound(msg.ID, msg.Method)
		}
	}
}

func (s *session) dispatchResponse(msg jsonRPCMessage) {
	id, ok := parseRPCID(msg.ID)
	if !ok {
		return
	}
	s.mu.Lock()
	waiter, ok := s.pending[id]
	if ok {
		delete(s.pending, id)
	}
	s.mu.Unlock()
	if ok {
		// 缓冲为 1,发送不阻塞;读取 goroutine 不被单个慢等待者拖住。
		waiter <- msg
	}
}

func (s *session) replyMethodNotFound(id json.RawMessage, method string) {
	resp := outboundError{
		JSONRPC: jsonRPCVersion,
		ID:      id,
		Error: rpcError{
			Code:    codeMethodNotFound,
			Message: fmt.Sprintf("method %q not supported by client", method),
		},
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	_ = s.transport.send(payload)
	s.writeMu.Unlock()
}

// call 发起一次请求并等待响应,受 ctx 超时 / 取消约束。会话已关闭或在等待期间断开
// 时立即返回错误(不泄漏等待者)。
func (s *session) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := s.nextID.Add(1)
	waiter := make(chan jsonRPCMessage, 1)

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, s.terminationErr()
	}
	s.pending[id] = waiter
	s.mu.Unlock()

	req := outboundRequest{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: params}
	payload, err := json.Marshal(req)
	if err != nil {
		s.removePending(id)
		return nil, fmt.Errorf("marshal mcp request: %w", err)
	}

	s.writeMu.Lock()
	sendErr := s.transport.send(payload)
	s.writeMu.Unlock()
	if sendErr != nil {
		s.removePending(id)
		return nil, fmt.Errorf("send mcp request: %w", sendErr)
	}

	select {
	case <-ctx.Done():
		s.removePending(id)
		return nil, ctx.Err()
	case <-s.done:
		return nil, s.terminationErr()
	case msg := <-waiter:
		if msg.Error != nil {
			return nil, fmt.Errorf("mcp server error (%d): %s", msg.Error.Code, msg.Error.Message)
		}
		return msg.Result, nil
	}
}

// notify 发出一条不期待响应的通知。
func (s *session) notify(method string, params any) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return s.terminationErr()
	}
	note := outboundNotification{JSONRPC: jsonRPCVersion, Method: method, Params: params}
	payload, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("marshal mcp notification: %w", err)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.transport.send(payload)
}

func (s *session) removePending(id int64) {
	s.mu.Lock()
	delete(s.pending, id)
	s.mu.Unlock()
}

// terminate 关闭会话:标记关闭、唤醒全部在途等待者、关闭 transport,并记录终止原因。
// 幂等(close goroutine 退出与显式 Close 可能同时触发)。
//
// 唤醒在途等待者通过关闭 s.done 实现,而非关闭各 waiter channel:若关闭 waiter,
// call 的 select 会同时就绪 <-s.done 与 <-waiter,可能随机选中后者把零值 msg 误判为
// 成功空响应。统一只关 s.done,使 call 必然走 terminationErr 分支,语义确定。
func (s *session) terminate(cause error) {
	s.closeFn.Do(func() {
		if cause == nil {
			cause = errSessionClosed
		}
		s.readErr.Store(&cause)

		s.mu.Lock()
		s.closed = true
		s.pending = make(map[int64]chan jsonRPCMessage)
		s.mu.Unlock()

		close(s.done)
		_ = s.transport.close()
	})
}

// close 主动关闭会话(优雅停机 / server 移除时调用)。
func (s *session) close() {
	s.terminate(errSessionClosed)
}

func (s *session) terminationErr() error {
	if ptr := s.readErr.Load(); ptr != nil && *ptr != nil {
		return *ptr
	}
	return errSessionClosed
}

// parseRPCID 解析 JSON-RPC id(数字形式)。我方请求始终用数字 id,故只接受数字;
// 非数字(对端响应异常)返回 ok=false 被忽略。
func parseRPCID(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var id int64
	if err := json.Unmarshal(raw, &id); err != nil {
		return 0, false
	}
	return id, true
}

// readMessage 是 stdio / HTTP transport 复用的报文解码助手:从带缓冲的 reader 读取
// 一条以换行分隔的 JSON-RPC 报文。返回 io.EOF 表示连接正常关闭。
//
// 防 OOM:单条报文硬上限 maxMessageBytes。外部 / 半可信的 MCP server 若持续输出而
// 不发换行(或返回超大报文),ReadBytes 会无界累积内存拖垮进程。这里读到上限仍无
// 换行即报错终止会话(由上层重连),把内存占用钳死在上限内。
func readMessage(reader *bufio.Reader) (jsonRPCMessage, error) {
	line, err := readLineLimited(reader, maxMessageBytes)
	if len(line) == 0 {
		// 无数据:连接关闭(io.EOF)或超限空读,原样上报由上层终止会话。
		if err == nil {
			err = errMessageClosed
		}
		return jsonRPCMessage{}, err
	}
	if errors.Is(err, errMessageTooLarge) {
		// 报文超限:即便已读到部分数据也必须丢弃并终止会话(防 OOM)。
		return jsonRPCMessage{}, err
	}
	// 其余情况(err==nil 正常成行,或 io.EOF 但末条无换行)均尝试解析已读数据:
	// 容忍流末尾未换行的最后一条报文,与改造前行为一致。
	var msg jsonRPCMessage
	if unmarshalErr := json.Unmarshal(line, &msg); unmarshalErr != nil {
		// 行内非合法 JSON:多见于 server 误把日志写到 stdout。返回错误终止会话由上层
		// 重连,优于静默丢弃导致响应永远等不到。
		return jsonRPCMessage{}, fmt.Errorf("decode mcp message: %w", unmarshalErr)
	}
	return msg, nil
}

// errMessageTooLarge 表示单条报文超过 maxMessageBytes,触发会话终止与重连。
var errMessageTooLarge = errors.New("mcp message exceeds size limit")

// errMessageClosed 表示连接已无更多数据(读到 0 字节且无底层错误)。
var errMessageClosed = errors.New("mcp connection closed")

// readLineLimited 读取一行(以 '\n' 结尾),累计字节超过 limit 即返回
// errMessageTooLarge。它替代裸 ReadBytes('\n') 以提供内存上界。返回的 line 不含
// 超限保护之外的额外拷贝开销(分段读取,命中换行即止)。
func readLineLimited(reader *bufio.Reader, limit int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		// ReadSlice 在命中分隔符或缓冲满时返回;buf 累计跨多次 ReadSlice 的分段。
		if len(buf)+len(chunk) > limit {
			return buf, errMessageTooLarge
		}
		buf = append(buf, chunk...)
		if err == nil {
			return buf, nil
		}
		if err == bufio.ErrBufferFull {
			// 缓冲满但未到换行:继续读后续分段(已受 limit 约束)。
			continue
		}
		// io.EOF 或其它错误:返回已读分段与错误,由 readMessage 据 len(line) 判定。
		return buf, err
	}
}
