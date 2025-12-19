package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"io"
	"log"
	"net"
	"os"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

const (
	SSHBindAddr = "0.0.0.0:2200"
	HostKeyFile = "ssh_host_ed25519_key"
)

func runSSHServer() {
	// SSH Config
	config := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.1",
		NoClientAuth:  false,
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			// 蜜罐模式：接受所有密码
			return nil, nil
		},
	}
	config.AddHostKey(loadHostKey())

	// Listener
	ln, err := net.Listen("tcp", SSHBindAddr)
	if err != nil {
		log.Printf("[SSH] Failed to listen: %v", err)
		return
	}
	log.Printf("[SSH] Server listening on %s", SSHBindAddr)

	for {
		c, err := ln.Accept()
		if err != nil {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		go handleSSHConn(c, config)
	}
}

func loadHostKey() ssh.Signer {
	b, err := os.ReadFile(HostKeyFile)
	if err == nil {
		k, err := ssh.ParsePrivateKey(b)
		if err == nil {
			return k
		}
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	bytes, _ := x509.MarshalPKCS8PrivateKey(priv)
	os.WriteFile(HostKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: bytes}), 0600)
	s, _ := ssh.NewSignerFromKey(priv)
	return s
}

func handleSSHConn(c net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}

	// 丢弃全局请求，但保持连接活跃
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "unknown channel")
			continue
		}
		ch, req, err := newCh.Accept()
		if err != nil {
			continue
		}

		fs := GlobalSessionFS

		// 定义环境变量
		env := map[string]string{
			"TERM":  "xterm-256color",
			"PATH":  "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"USER":  "root",
			"HOME":  "/root",
			"SHELL": "/bin/bash",
			"LANG":  "en_US.UTF-8",
		}

		go func(in <-chan *ssh.Request, channel ssh.Channel) {
			cols, rows := 80, 24
			var activeTerm *Terminal

			for r := range in {
				switch r.Type {
				case "pty-req":
					if len(r.Payload) > 4 {
						l := binary.BigEndian.Uint32(r.Payload[0:4]) // TERM len
						if len(r.Payload) >= 4+int(l) {
							env["TERM"] = string(r.Payload[4 : 4+l])
							// Payload continues with width/height...
							rest := r.Payload[4+l:]
							if len(rest) >= 8 {
								cols = int(binary.BigEndian.Uint32(rest[0:4]))
								rows = int(binary.BigEndian.Uint32(rest[4:8]))
							}
						}
					}
					r.Reply(true, nil)
					if activeTerm != nil {
						activeTerm.Resize(cols, rows)
					}

				case "window-change":
					if len(r.Payload) >= 8 {
						cols = int(binary.BigEndian.Uint32(r.Payload[0:4]))
						rows = int(binary.BigEndian.Uint32(r.Payload[4:8]))
						if activeTerm != nil {
							activeTerm.Resize(cols, rows)
						}
					}

				case "env":
					if len(r.Payload) > 4 {
						l := binary.BigEndian.Uint32(r.Payload[0:4])
						if len(r.Payload) >= 8+int(l) {
							k := string(r.Payload[4 : 4+l])
							v := string(r.Payload[8+l:])
							env[k] = v
						}
					}
					r.Reply(true, nil)

				case "shell":
					r.Reply(true, nil)
					// 启动交互式 Shell
					term := NewTerminal(channel, fs, env, cols, rows)
					activeTerm = term
					go func() {
						term.Run()
						channel.Close()
					}()

				case "exec":
					if len(r.Payload) > 4 {
						l := binary.BigEndian.Uint32(r.Payload[0:4])
						cmd := string(r.Payload[4 : 4+l])
						r.Reply(true, nil)

						// 执行单次命令
						term := NewTerminal(channel, fs, env, cols, rows)
						// exec 不需要 Run() 的循环，直接 Exec
						term.Exec(cmd)
						// 发送退出状态
						channel.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
						channel.Close()
						return
					}
					r.Reply(false, nil)

				case "subsystem":
					if string(r.Payload[4:]) == "sftp" {
						r.Reply(true, nil)
						h := &SFTPHandler{fs: fs}
						srv := sftp.NewRequestServer(channel, sftp.Handlers{
							FileGet: h, FilePut: h, FileCmd: h, FileList: h,
						})
						if err := srv.Serve(); err == io.EOF {
							srv.Close()
						}
						return
					}
					r.Reply(false, nil)
				case "keepalive@openssh.com":
					r.Reply(true, nil)
					continue
				default:
					r.Reply(false, nil)
				}
			}
		}(req, ch)
	}
}
