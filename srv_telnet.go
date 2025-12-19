package main

import (
	"bufio"
	"bytes"
	"io"
	"log"
	"net"
	"strings"
	"time"
)

const (
	TelnetBindAddr = "0.0.0.0:2300"
	// Telnet Commands
	cmdSE   = 240 // End of subnegotiation parameters
	cmdNOP  = 241 // No operation
	cmdGA   = 249 // Go ahead
	cmdSB   = 250 // Subnegotiation
	cmdWILL = 251 // Will option
	cmdWONT = 252 // Won't option
	cmdDO   = 253 // Do option
	cmdDONT = 254 // Don't option
	cmdIAC  = 255 // Interpret as command

	// Telnet Options
	optEcho   = 1  // Echo
	optSGA    = 3  // Suppress Go Ahead
	optTTYPE  = 24 // Terminal Type
	optNAWS   = 31 // Negotiate About Window Size
	optNewEnv = 39 // New Environment Option

	// Subnegotiation Commands
	subIS   = 0
	subSEND = 1

	// New Environment Suboptions
	envVAR     = 0
	envVALUE   = 1
	envUSERVAR = 3
)

func runTelnetServer() {
	ln, err := net.Listen("tcp", TelnetBindAddr)
	if err != nil {
		log.Printf("[Telnet] Failed to listen: %v", err)
		return
	}
	log.Printf("[Telnet] Server listening on %s", TelnetBindAddr)

	for {
		c, err := ln.Accept()
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		go handleTelnetConn(c)
	}
}

type TelnetStream struct {
	conn              net.Conn
	reader            *bufio.Reader
	term              *Terminal
	env               map[string]string
	negotiatedOptions map[byte]bool
	initialWidth      int // 修复：用于缓存在 Terminal 创建前的窗口宽度
	initialHeight     int // 修复：用于缓存在 Terminal 创建前的窗口高度
	skipLF            bool
}

func (ts *TelnetStream) handleOption(cmd, opt byte) error {
	if ts.negotiatedOptions[opt] {
		return nil
	}

	switch cmd {
	case cmdWILL:
		switch opt {
		case optTTYPE, optNAWS, optNewEnv:
			if _, err := ts.conn.Write([]byte{cmdIAC, cmdDO, opt}); err != nil {
				return err
			}
			ts.negotiatedOptions[opt] = true
			if opt == optTTYPE {
				return ts.sendTTYPErequest()
			}
			if opt == optNewEnv {
				return ts.sendNEWENVrequest()
			}
		default:
			if _, err := ts.conn.Write([]byte{cmdIAC, cmdDONT, opt}); err != nil {
				return err
			}
		}
	case cmdDO:
		switch opt {
		case optSGA, optEcho:
			if _, err := ts.conn.Write([]byte{cmdIAC, cmdWILL, opt}); err != nil {
				return err
			}
			ts.negotiatedOptions[opt] = true
		default:
			if _, err := ts.conn.Write([]byte{cmdIAC, cmdWONT, opt}); err != nil {
				return err
			}
		}
	case cmdWONT, cmdDONT:
	}
	return nil
}

func (ts *TelnetStream) handleSubnegotiation(data []byte) {
	if len(data) == 0 {
		return
	}
	opt := data[0]
	payload := data[1:]

	switch opt {
	case optNAWS:
		if len(payload) >= 4 {
			w := int(payload[0])<<8 | int(payload[1])
			h := int(payload[2])<<8 | int(payload[3])
			// 修复：区分处理登录前和登录后的尺寸更新
			if ts.term != nil {
				// 会话已开始，实时更新
				ts.term.Resize(w, h)
			} else {
				// 会话未开始，缓存尺寸
				ts.initialWidth = w
				ts.initialHeight = h
			}
		}
	case optTTYPE:
		if len(payload) >= 2 && payload[0] == subIS {
			termType := string(payload[1:])
			if ts.env != nil {
				ts.env["TERM"] = termType
			}
		}
	case optNewEnv:
		if len(payload) >= 1 && payload[0] == subIS {
			vars := payload[1:]
			for len(vars) > 0 {
				var key, val string
				if vars[0] == envVAR || vars[0] == envUSERVAR {
					vars = vars[1:]
					endKey := bytes.IndexByte(vars, envVALUE)
					if endKey == -1 {
						break
					}
					key = string(vars[:endKey])
					vars = vars[endKey+1:]

					endVal := -1
					for i, b := range vars {
						if b == envVAR || b == envUSERVAR {
							endVal = i
							break
						}
					}

					if endVal == -1 {
						val = string(vars)
						vars = nil
					} else {
						val = string(vars[:endVal])
						vars = vars[endVal:]
					}

					if ts.env != nil && key != "" {
						ts.env[key] = val
					}
				} else {
					break
				}
			}
		}
	}
}

func (ts *TelnetStream) sendTTYPErequest() error {
	_, err := ts.conn.Write([]byte{cmdIAC, cmdSB, optTTYPE, subSEND, cmdIAC, cmdSE})
	return err
}

func (ts *TelnetStream) sendNEWENVrequest() error {
	buf := []byte{cmdIAC, cmdSB, optNewEnv, subSEND}
	buf = append(buf, envVAR)
	buf = append(buf, []byte("USER")...)
	buf = append(buf, envVAR)
	buf = append(buf, []byte("TERM")...)
	buf = append(buf, cmdIAC, cmdSE)
	_, err := ts.conn.Write(buf)
	return err
}

func (ts *TelnetStream) Read(p []byte) (n int, err error) {
	for {
		b, err := ts.reader.ReadByte()
		if err != nil {
			return 0, err
		}

		if ts.skipLF {
			ts.skipLF = false
			if b == '\n' {
				continue
			}
		}
		ts.skipLF = false

		if b != cmdIAC {
			if b == '\r' {
				ts.skipLF = true
				p[0] = '\n'
			} else {
				p[0] = b
			}
			return 1, nil
		}

		cmd, err := ts.reader.ReadByte()
		if err != nil {
			return 0, err
		}

		if cmd == cmdIAC {
			p[0] = cmdIAC
			return 1, nil
		}

		switch cmd {
		case cmdWILL, cmdWONT, cmdDO, cmdDONT:
			opt, err := ts.reader.ReadByte()
			if err != nil {
				return 0, err
			}
			if err := ts.handleOption(cmd, opt); err != nil {
				return 0, err
			}
		case cmdSB:
			var subBuf []byte
			for {
				sb, err := ts.reader.ReadByte()
				if err != nil {
					return 0, err
				}
				if sb == cmdIAC {
					next, err := ts.reader.ReadByte()
					if err != nil {
						return 0, err
					}
					if next == cmdSE {
						break
					}
					subBuf = append(subBuf, sb, next)
				} else {
					subBuf = append(subBuf, sb)
				}
			}
			ts.handleSubnegotiation(subBuf)
		case cmdNOP, cmdGA:
		}
	}
}

func (ts *TelnetStream) Write(p []byte) (n int, err error) {
	var buf bytes.Buffer
	for _, b := range p {
		if b == cmdIAC {
			buf.WriteByte(cmdIAC)
		}
		buf.WriteByte(b)
	}
	return ts.conn.Write(buf.Bytes())
}

func handleTelnetConn(c net.Conn) {
	defer c.Close()

	env := map[string]string{
		"TERM":  "vt100",
		"SHELL": "/bin/bash",
	}

	ts := &TelnetStream{
		conn:              c,
		reader:            bufio.NewReader(c),
		env:               env,
		negotiatedOptions: make(map[byte]bool),
		initialWidth:      80, // 设置默认值
		initialHeight:     24, // 设置默认值
	}

	ts.Write([]byte("\r\nUbuntu 22.04 LTS\r\nubuntu-server login: "))

	user := readLine(ts)
	if user == "" {
		return
	}
	env["USER"] = strings.TrimSpace(user)
	if env["USER"] == "root" {
		env["HOME"] = "/root"
	} else {
		env["HOME"] = "/home/" + env["USER"]
	}

	ts.Write([]byte("Password: "))
	readLine(ts)
	ts.Write([]byte("\r\n"))

	fs := GlobalSessionFS

	// 使用协商后缓存的尺寸创建 Terminal
	term := NewTerminal(ts, fs, env, ts.initialWidth, ts.initialHeight)
	ts.term = term
	term.Run()
}

func readLine(r io.Reader) string {
	var buf []byte
	b := make([]byte, 1)
	for {
		n, err := r.Read(b)
		if err != nil || n == 0 {
			break
		}
		ch := b[0]
		if ch == '\n' || ch == '\r' {
			break
		}
		buf = append(buf, ch)
	}
	return string(bytes.TrimSpace(buf))
}
