package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"path"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// CRLFWriter 包装 io.Writer，将所有 \n 转换为 \r\n，解决阶梯效应
type CRLFWriter struct {
	w io.Writer
}

func (cw *CRLFWriter) Write(p []byte) (n int, err error) {
	// 逐字节处理以确保稳健性，虽然比 ReplaceAll 慢，但对模拟终端来说性能足够
	// 且能处理跨次写入的边界情况（虽然这里简化为每次 Write 独立处理）
	var buf bytes.Buffer
	for _, b := range p {
		if b == '\n' {
			buf.Write([]byte("\r\n"))
		} else {
			buf.WriteByte(b)
		}
	}
	// 注意：返回值 n 应该代表输入 p 被消费的字节数，而不是 buf 写入的字节数
	// 否则上层 io.Copy 等可能会认为写入错误
	_, err = cw.w.Write(buf.Bytes())
	return len(p), err
}

// Terminal 抽象 Shell 逻辑
type Terminal struct {
	mu sync.Mutex

	// I/O
	RW io.ReadWriter // 原始读写接口

	// State
	FS           *SessionFS
	Env          map[string]string
	History      []string
	Width        int
	Height       int
	Running      bool
	pid          int
	lastExitCode int

	// Line Editing State
	buffer []rune
	cursor int // buffer 中的光标位置

	// RawModeWriter 用于支持交互式全屏应用
	// 当不为 nil 时，所有的输入都会直接写入此 Writer，而不是进入行编辑器
	RawModeWriter io.Writer

	// Input Channel (用于解耦读取和执行)
	// 这是为了防止在执行阻塞命令（如游戏循环）时，主线程被阻塞导致无法读取网络输入
	keyChan chan rune
}

func NewTerminal(rw io.ReadWriter, fs *SessionFS, env map[string]string, w, h int) *Terminal {
	return &Terminal{
		RW:      rw,
		FS:      fs,
		Env:     env,
		Width:   w,
		Height:  h,
		Running: true,
		buffer:  make([]rune, 0, 1024),
		pid:     1000 + rand.Intn(20000),
		keyChan: make(chan rune, 128), // 带缓冲的通道，防止按键丢失
	}
}

func (t *Terminal) Resize(w, h int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Width = w
	t.Height = h
}

// Print 输出字符串，自动处理换行
func (t *Terminal) Print(s string) {
	// 使用 CRLFWriter 逻辑
	cw := &CRLFWriter{w: t.RW}
	cw.Write([]byte(s))
}

func (t *Terminal) Prompt() {
	t.mu.Lock()
	user := t.Env["USER"]
	if user == "" {
		user = "root"
	}
	t.mu.Unlock()

	hostname := "ubuntu-server"
	dir := t.FS.cwd

	// 处理路径缩写
	if dir == "/root" {
		dir = "~"
	} else if strings.HasPrefix(dir, "/root/") {
		dir = "~" + dir[5:]
	} else if strings.HasPrefix(dir, "/home/"+user) {
		dir = "~" + dir[len("/home/"+user):]
	}

	// 提示符颜色：绿用户@红主机:蓝目录
	promptSign := "$"
	if user == "root" {
		promptSign = "#"
	}

	fmt.Fprintf(t.RW, "\r\033[1;32m%s@%s\033[0m:\033[1;34m%s\033[0m%s ", user, hostname, dir, promptSign)

	// 重绘当前 buffer
	t.RW.Write([]byte(string(t.buffer)))

	// 移动光标回正确位置
	if len(t.buffer) > t.cursor {
		fmt.Fprintf(t.RW, "\033[%dD", len(t.buffer)-t.cursor)
	}
}

func (t *Terminal) ClearLine() {
	t.RW.Write([]byte("\r\033[K"))
}

// inputLoop 独立运行的输入读取循环，防止 exec 阻塞导致无法读取输入
// 这是一个关键的并发修复，解决了游戏模式下无法响应按键的问题
func (t *Terminal) inputLoop() {
	reader := bufio.NewReader(t.RW)
	for t.Running {
		r, _, err := reader.ReadRune()
		if err != nil {
			close(t.keyChan)
			return // 通常是 io.EOF
		}

		t.mu.Lock()
		rawWriter := t.RawModeWriter
		t.mu.Unlock()

		if rawWriter != nil {
			// 如果处于 Raw Mode，直接转发输入给应用程序
			// 注意：这里简单地将 rune 转回 string，对于多字节字符也是安全的
			if _, err := rawWriter.Write([]byte(string(r))); err != nil {
				// 写入失败通常意味着管道关闭，游戏结束，忽略错误
			}
		} else {
			// 否则发送给 Shell 主循环进行行编辑处理
			t.keyChan <- r
		}
	}
}

func (t *Terminal) Run() {
	t.Print("Welcome to Ubuntu 22.04 LTS (GNU/Linux 5.15.0-generic x86_64)\n")
	t.Print(" * Documentation:  https://help.ubuntu.com\n")
	t.Print(" * Management:     https://landscape.canonical.com\n")
	t.Print(" * Support:        https://ubuntu.com/advantage\n\n")
	t.Prompt()

	// 启动独立的输入读取协程
	go t.inputLoop()

	histIdx := -1

	// 用于解析 ANSI 转义序列的简单状态机
	// 0: None, 1: ESC, 2: [
	escState := 0

	// 主循环现在从 channel 读取按键，而不是直接从 reader 读取
	for r := range t.keyChan {
		// --- ANSI 转义序列解析 (方向键) ---
		if escState == 0 {
			if r == 27 {
				escState = 1
				continue
			}
		} else if escState == 1 {
			if r == '[' {
				escState = 2
				continue
			}
			escState = 0 // Reset if not [
			// Fallthrough to handle as normal char (simplified)
		} else if escState == 2 {
			escState = 0
			switch r {
			case 'A': // Up
				if len(t.History) > 0 {
					if histIdx == -1 {
						histIdx = len(t.History)
					}
					if histIdx > 0 {
						histIdx--
						t.buffer = []rune(t.History[histIdx])
						t.cursor = len(t.buffer)
						t.ClearLine()
						t.Prompt()
					}
				}
				continue
			case 'B': // Down
				if histIdx < len(t.History) {
					histIdx++
					if histIdx >= len(t.History) {
						histIdx = len(t.History)
						t.buffer = []rune{}
					} else {
						t.buffer = []rune(t.History[histIdx])
					}
					t.cursor = len(t.buffer)
					t.ClearLine()
					t.Prompt()
				}
				continue
			case 'C': // Right
				if t.cursor < len(t.buffer) {
					t.cursor++
					t.RW.Write([]byte("\033[C"))
				}
				continue
			case 'D': // Left
				if t.cursor > 0 {
					t.cursor--
					t.RW.Write([]byte("\033[D"))
				}
				continue
			}
		}

		// --- 标准输入处理 ---
		switch r {
		case 3: // Ctrl+C
			t.buffer = t.buffer[:0]
			t.cursor = 0
			t.Print("^C\n")
			t.Prompt()

		case 4: // Ctrl+D
			if len(t.buffer) == 0 {
				return
			}

		case 13, 10: // Enter
			t.Print("\n")
			cmd := string(t.buffer)
			if len(strings.TrimSpace(cmd)) > 0 {
				if len(t.History) == 0 || t.History[len(t.History)-1] != cmd {
					t.History = append(t.History, cmd)
					if len(t.History) > 100 {
						t.History = t.History[1:]
					}
				}
				histIdx = len(t.History)
				t.execPipeline(cmd) // 这里可能会阻塞，但 inputLoop 依然在工作
				if !t.Running {
					return
				}
			}
			t.buffer = t.buffer[:0]
			t.cursor = 0
			t.Prompt()

		case 127, 8: // Backspace
			if t.cursor > 0 {
				t.buffer = append(t.buffer[:t.cursor-1], t.buffer[t.cursor:]...)
				t.cursor--
				t.ClearLine()
				t.Prompt()
			}

		case 9: // TAB
			t.handleAutoComplete()

		default:
			if unicode.IsPrint(r) {
				t.buffer = append(t.buffer[:t.cursor], append([]rune{r}, t.buffer[t.cursor:]...)...)
				t.cursor++
				if t.cursor == len(t.buffer) {
					// 将 rune 作为 UTF-8 字符串写回终端
					t.RW.Write([]byte(string(r)))
				} else {
					t.ClearLine()
					t.Prompt()
				}
			}
		}
	}
}

func (t *Terminal) handleAutoComplete() {
	line := string(t.buffer[:t.cursor])
	parts := strings.Fields(line)

	var lastWord string
	if len(parts) > 0 && !strings.HasSuffix(line, " ") {
		lastWord = parts[len(parts)-1]
	} else {
		lastWord = ""
	}

	isCmd := (len(parts) == 0) || (len(parts) == 1 && !strings.HasSuffix(line, " "))
	var candidates []string

	if isCmd {
		for _, binDir := range []string{"/bin", "/usr/bin"} {
			files, _ := t.FS.ListDir(binDir)
			for _, f := range files {
				if strings.HasPrefix(f.Name, lastWord) {
					candidates = append(candidates, f.Name)
				}
			}
		}
	} else {
		dir, filePrefix := path.Split(lastWord)
		absDir := t.FS.Abs(dir)
		files, _ := t.FS.ListDir(absDir)
		for _, f := range files {
			if strings.HasPrefix(f.Name, filePrefix) {
				name := f.Name
				if f.IsDir {
					name += "/"
				}
				candidates = append(candidates, path.Join(dir, name))
			}
		}
	}

	// 去重并排序
	unique := make(map[string]bool)
	var clean []string
	for _, c := range candidates {
		if !unique[c] {
			unique[c] = true
			clean = append(clean, c)
		}
	}
	sort.Strings(clean)
	candidates = clean

	if len(candidates) == 1 {
		completion := candidates[0][len(lastWord):]
		t.buffer = append(t.buffer, []rune(completion)...)
		t.cursor += len(completion)
		if !strings.HasSuffix(candidates[0], "/") {
			t.buffer = append(t.buffer, ' ')
			t.cursor++
		}
		t.ClearLine()
		t.Prompt()
	} else if len(candidates) > 1 {
		t.Print("\n")
		// 简单列出
		for _, c := range candidates {
			fmt.Fprintf(t.RW, "%s  ", c)
		}
		t.Print("\n")
		t.Prompt()
	}
}

func (t *Terminal) execPipeline(cmdline string) {
	t.lastExitCode = 0

	// 重构：更健壮的重定向和管道处理
	// 1. 首先确定最终的输出目的地
	finalOut := &CRLFWriter{w: t.RW} // 默认输出到终端
	var fileWriteBuffer *bytes.Buffer
	var fileWritePath string

	parts := parseArgs(cmdline)
	args := parts
	for i := 0; i < len(parts); i++ {
		if (parts[i] == ">" || parts[i] == ">>") && i+1 < len(parts) {
			fileWritePath = t.FS.Abs(parts[i+1])
			fileWriteBuffer = &bytes.Buffer{}
			// 从传递给命令的参数中移除重定向标记
			args = parts[:i]
			cmdline = strings.Join(args, " ") // 重建不带重定向的命令行
			break
		}
	}

	// 2. 运行管道，将输出定向到正确的目标
	var effectiveOut io.Writer = finalOut
	if fileWriteBuffer != nil {
		effectiveOut = fileWriteBuffer
	}

	pipeline := strings.Split(cmdline, "|")
	t.runPipelineWithOutput(pipeline, effectiveOut)

	// 3. 如果输出被重定向，则将缓冲区内容写入文件
	if fileWriteBuffer != nil {
		// 注意：'>>' (追加) 在此被简化为覆盖，对于蜜罐来说是可接受的
		t.FS.Write(fileWritePath, fileWriteBuffer.Bytes(), 0644)
	}
}

func (t *Terminal) runPipelineWithOutput(pipeline []string, finalOut io.Writer) {
	var commands [][]string
	for _, cmdStr := range pipeline {
		args := parseArgs(cmdStr)
		// 预先过滤掉空命令
		if len(args) > 0 {
			commands = append(commands, args)
		}
	}

	// 如果所有命令都为空（例如仅输入了 "||"），直接返回
	if len(commands) == 0 {
		return
	}

	if len(commands) == 1 {
		// 单个命令直接执行，不需要建立管道
		t.runCommand(commands[0], &bytes.Buffer{}, finalOut)
		return
	}

	var wg sync.WaitGroup
	var in io.Reader = &bytes.Buffer{}

	for i, args := range commands {
		wg.Add(1)

		var out io.Writer
		var pipeR *io.PipeReader
		var pipeW *io.PipeWriter // 默认为 nil

		if i < len(commands)-1 {
			pipeR, pipeW = io.Pipe()
			out = pipeW
		} else {
			out = finalOut
			// pipeW 在这里保持为 nil
		}

		// fix: 处理 Typed Nil Interface 问题
		var pipeCloser io.Closer
		if pipeW != nil {
			pipeCloser = pipeW
		}

		go func(args []string, stdin io.Reader, stdout io.Writer, outCloser io.Closer) {
			defer wg.Done()

			// 执行完后关闭输出端，通知下游 EOF
			if outCloser != nil {
				defer outCloser.Close()
			}

			// 如果输入端是管道（PipeReader），执行完后必须关闭它
			if r, ok := stdin.(io.Closer); ok {
				defer r.Close()
			}

			t.runCommand(args, stdin, stdout)
		}(args, in, out, pipeCloser)

		in = pipeR
	}
	wg.Wait()
}

func parseArgs(cmdline string) []string {
	var args []string
	var buf strings.Builder
	inQuote := false
	var quoteChar rune

	for _, r := range cmdline {
		switch r {
		case '"', '\'':
			if !inQuote {
				inQuote = true
				quoteChar = r
			} else if r == quoteChar {
				inQuote = false
			} else {
				buf.WriteRune(r)
			}
		case ' ', '\t':
			if !inQuote {
				if buf.Len() > 0 {
					args = append(args, buf.String())
					buf.Reset()
				}
			} else {
				buf.WriteRune(r)
			}
		default:
			buf.WriteRune(r)
		}
	}
	if buf.Len() > 0 {
		args = append(args, buf.String())
	}
	return args
}

// Exec 对外接口
func (t *Terminal) Exec(cmdline string) {
	t.execPipeline(cmdline)
}
