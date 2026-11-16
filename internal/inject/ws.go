// Package inject 通过本地 CDP 把 wbmux 面板注入到 WorkBuddy 客户端的渲染进程。
//
// # 为什么自己写 WebSocket
//
// 需求只有一句话：往 Runtime.evaluate 发一段 JSON，读回一段 JSON。
// 引入 gorilla/websocket 这类依赖，换来的是 go.mod 里多一条永远不会被
// 用满的第三方代码——而本项目的卖点是"单一可执行文件、零运行时依赖"，
// 编译期依赖也尽量保持干净。所以这里只实现 RFC 6455 里握手、掩码帧、
// 分片、ping/pong 这几个必需的子集，一百多行，行为完全可控。
//
// # 为什么不改客户端任何文件
//
// 全程只连客户端自己开出的调试端口（--remote-debugging-port），在渲染层
// 动态注入 DOM。刷新窗口即还原，卸载 wbmux 后不留任何痕迹。
package inject

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// wsConn 是一条已完成握手的 WebSocket 连接。
//
// 只支持 CDP 需要的那一档：文本帧、客户端掩码、无扩展协商。
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
}

// dialWS 完成 WebSocket 握手。
func dialWS(raw string, timeout time.Duration) (*wsConn, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("调试地址无法解析: %w", err)
	}
	if u.Scheme != "ws" {
		return nil, fmt.Errorf("不支持的调试地址协议: %s", u.Scheme)
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}

	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		return nil, fmt.Errorf("连接调试端口失败: %w", err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))

	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, err
	}
	req := "GET " + path + " HTTP/1.1\r\n" +
		"Host: " + u.Host + "\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(keyBytes) + "\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		_ = conn.Close()
		return nil, err
	}

	br := bufio.NewReaderSize(conn, 64<<10)
	status, err := br.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("读取握手响应失败: %w", err)
	}
	if !strings.Contains(status, "101") {
		_ = conn.Close()
		return nil, fmt.Errorf("调试端口拒绝升级: %s", strings.TrimSpace(status))
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("读取握手响应头失败: %w", err)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	_ = conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, br: br}, nil
}

// writeFrame 发送一个客户端帧（必须掩码）。
func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()

	head := make([]byte, 0, 14)
	head = append(head, 0x80|opcode) // FIN=1
	n := len(payload)
	switch {
	case n <= 125:
		head = append(head, 0x80|byte(n))
	case n <= 0xFFFF:
		head = append(head, 0x80|126, byte(n>>8), byte(n))
	default:
		head = append(head, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		head = append(head, ext[:]...)
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	head = append(head, mask...)

	buf := make([]byte, 0, len(head)+n)
	buf = append(buf, head...)
	for i, b := range payload {
		buf = append(buf, b^mask[i%4])
	}
	_, err := c.conn.Write(buf)
	return err
}

// readFrame 读取一帧。返回 fin 表示是否为消息的最后一帧。
func (c *wsConn) readFrame(timeout time.Duration) (fin bool, opcode byte, payload []byte, err error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(timeout))
	var h [2]byte
	if _, err = io.ReadFull(c.br, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin = h[0]&0x80 != 0
	opcode = h[0] & 0x0F
	masked := h[1]&0x80 != 0
	n := int64(h[1] & 0x7F)
	switch n {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		n = int64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(c.br, ext[:]); err != nil {
			return false, 0, nil, err
		}
		n = int64(binary.BigEndian.Uint64(ext[:]))
	}
	if n < 0 || n > 32<<20 {
		return false, 0, nil, fmt.Errorf("调试帧体积异常: %d", n)
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(c.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, opcode, payload, nil
}

// readMessage 读一条完整消息，自动处理 ping/pong 与分片。
func (c *wsConn) readMessage(timeout time.Duration) ([]byte, error) {
	var out []byte
	for {
		fin, opcode, payload, err := c.readFrame(timeout)
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x9: // ping → 回 pong
			_ = c.writeFrame(0xA, payload)
			continue
		case 0xA: // pong
			continue
		case 0x8: // close
			return nil, io.EOF
		}
		out = append(out, payload...)
		if fin {
			return out, nil
		}
	}
}

// close 关闭连接。
func (c *wsConn) close() {
	_ = c.conn.SetWriteDeadline(time.Now().Add(300 * time.Millisecond))
	_ = c.writeFrame(0x8, nil)
	_ = c.conn.Close()
}
