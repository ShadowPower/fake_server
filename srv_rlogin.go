package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"log"
	"net"
	"strings"
	"time"
)

const (
	RLoginBindAddr = "0.0.0.0:5130"
)

func runRLoginServer() {
	ln, err := net.Listen("tcp", RLoginBindAddr)
	if err != nil {
		log.Printf("[RLogin] Failed to listen: %v", err)
		return
	}
	log.Printf("[RLogin] Server listening on %s", RLoginBindAddr)

	for {
		c, err := ln.Accept()
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		go handleRLoginConn(c)
	}
}

type RLoginStream struct {
	conn          net.Conn
	reader        *bufio.Reader
	term          *Terminal
	state         int
	winSizeBuf    []byte
	initialWidth  int // 修复：用于缓存在 Terminal 创建前的窗口宽度
	initialHeight int // 修复：用于缓存在 Terminal 创建前的窗口高度
}

func (rs *RLoginStream) Read(p []byte) (n int, err error) {
	for {
		b, err := rs.reader.ReadByte()
		if err != nil {
			return 0, err
		}

		switch rs.state {
		case 0:
			if b == 0xFF {
				rs.state = 1
			} else {
				p[0] = b
				return 1, nil
			}
		case 1:
			if b == 0xFF {
				rs.state = 2
			} else {
				rs.state = 0
			}
		case 2:
			if b == 's' {
				rs.state = 3
			} else {
				rs.state = 0
			}
		case 3:
			if b == 's' {
				rs.state = 4
				rs.winSizeBuf = make([]byte, 0, 8)
			} else {
				rs.state = 0
			}
		case 4:
			rs.winSizeBuf = append(rs.winSizeBuf, b)
			if len(rs.winSizeBuf) == 8 {
				rows := binary.BigEndian.Uint16(rs.winSizeBuf[0:2])
				cols := binary.BigEndian.Uint16(rs.winSizeBuf[2:4])
				// 修复：区分处理登录前和登录后的尺寸更新
				if rs.term != nil {
					// 会话已开始，实时更新
					rs.term.Resize(int(cols), int(rows))
				} else {
					// 会话未开始，缓存尺寸
					rs.initialWidth = int(cols)
					rs.initialHeight = int(rows)
				}
				rs.state = 0
			}
		}
	}
}

func (rs *RLoginStream) Write(p []byte) (n int, err error) {
	return rs.conn.Write(p)
}

func handleRLoginConn(c net.Conn) {
	defer c.Close()
	reader := bufio.NewReader(c)

	var clientUser, serverUser, termInfo []byte
	var err error
	_, err = reader.ReadBytes(0)
	if err != nil {
		return
	}
	clientUser, err = reader.ReadBytes(0)
	if err != nil {
		return
	}
	serverUser, err = reader.ReadBytes(0)
	if err != nil {
		return
	}
	termInfo, err = reader.ReadBytes(0)
	if err != nil {
		return
	}

	c.Write([]byte{0})

	termType := "vt100"
	termStr := string(bytes.TrimRight(termInfo, "\x00"))
	if parts := strings.Split(termStr, "/"); len(parts) > 0 {
		termType = parts[0]
	}

	rs := &RLoginStream{
		conn:          c,
		reader:        reader,
		initialWidth:  80, // 设置默认值
		initialHeight: 24, // 设置默认值
	}

	c.Write([]byte("Password: "))
	readLine(rs)
	c.Write([]byte("\r\n"))

	fs := GlobalSessionFS

	env := map[string]string{
		"TERM":  termType,
		"USER":  string(bytes.TrimRight(clientUser, "\x00")),
		"SHELL": "/bin/bash",
	}
	if string(bytes.TrimRight(serverUser, "\x00")) == "root" {
		env["USER"] = "root"
	}

	// 修复：使用协商后缓存的尺寸创建 Terminal
	term := NewTerminal(rs, fs, env, rs.initialWidth, rs.initialHeight)
	rs.term = term
	term.Run()
}
