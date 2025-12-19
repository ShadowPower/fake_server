package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// runCommand 执行单个命令
func (t *Terminal) runCommand(args []string, in io.Reader, out io.Writer) {
	if len(args) == 0 {
		return
	}
	cmd := args[0]

	// 环境变量展开
	for i := 1; i < len(args); i++ {
		if strings.HasPrefix(args[i], "$") {
			varName := args[i][1:]
			if val, ok := t.Env[varName]; ok {
				args[i] = val
			} else if varName == "?" {
				args[i] = strconv.Itoa(t.lastExitCode)
			} else if varName == "$" {
				args[i] = strconv.Itoa(t.pid)
			}
		}
	}

	switch cmd {
	case "ls", "ll":
		dir := t.FS.cwd
		opts := map[string]bool{"a": false, "l": cmd == "ll", "h": false, "t": false, "r": false, "R": false}
		paths := []string{}
		useColor := isTTY(out)

		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "-") {
				for _, char := range arg[1:] {
					opts[string(char)] = true
				}
			} else {
				paths = append(paths, t.FS.Abs(arg))
			}
		}
		if len(paths) == 0 {
			paths = append(paths, dir)
		}

		for _, p := range paths {
			files, err := t.FS.ListDir(p)
			if err != nil {
				fmt.Fprintf(out, "ls: 无法访问 '%s': %v\n", p, err)
				t.lastExitCode = 2
				continue
			}

			if opts["t"] {
				sort.Slice(files, func(i, j int) bool { return files[i].ModTime.After(files[j].ModTime) })
			}
			if opts["r"] {
				for i, j := 0, len(files)-1; i < j; i, j = i+1, j-1 {
					files[i], files[j] = files[j], files[i]
				}
			}

			if opts["l"] {
				// 模拟 ls -l 输出
				tw := tabwriter.NewWriter(out, 0, 0, 1, ' ', 0)
				total := 0
				for _, f := range files {
					if !opts["a"] && strings.HasPrefix(f.Name, ".") {
						continue
					}
					total += len(f.Content)/1024 + 4
				}
				fmt.Fprintf(out, "total %d\n", total)

				for _, f := range files {
					if !opts["a"] && strings.HasPrefix(f.Name, ".") {
						continue
					}
					modeStr := f.Mode.String()
					userName, groupName := "root", "root"
					if name, ok := getNameByID(Users, f.UID); ok {
						userName = name
					}
					if name, ok := getNameByID(Groups, f.GID); ok {
						groupName = name
					}
					sizeStr := strconv.Itoa(len(f.Content))
					if opts["h"] {
						sizeStr = formatSize(int64(len(f.Content)))
					}

					// 颜色处理
					nameColor := f.Name
					if useColor {
						if f.IsDir {
							nameColor = "\033[1;34m" + f.Name + "\033[0m"
						} else if f.Mode&0111 != 0 {
							nameColor = "\033[1;32m" + f.Name + "\033[0m"
						}
					}

					fmt.Fprintf(tw, "%s %d %s %s %5s %s %s\n",
						modeStr, f.Nlink, userName, groupName, sizeStr,
						f.ModTime.Format("Jan _2 15:04"), nameColor)
				}
				tw.Flush()
			} else {
				for _, f := range files {
					if !opts["a"] && strings.HasPrefix(f.Name, ".") {
						continue
					}
					nameColor := f.Name
					if useColor {
						if f.IsDir {
							nameColor = "\033[1;34m" + f.Name + "\033[0m"
						} else if f.Mode&0111 != 0 {
							nameColor = "\033[1;32m" + f.Name + "\033[0m"
						}
					}
					fmt.Fprintf(out, "%s  ", nameColor)
				}
				fmt.Fprint(out, "\n")
			}
		}

	case "cd":
		if len(args) > 1 {
			target := t.FS.Abs(args[1])
			if e, ok := t.FS.GetEntry(target); ok && e.IsDir {
				t.FS.cwd = target
			} else {
				fmt.Fprintf(out, "-bash: cd: %s: 没有那个文件或目录\n", args[1])
				t.lastExitCode = 1
			}
		} else {
			t.FS.cwd = "/root"
		}

	case "pwd":
		fmt.Fprintf(out, "%s\n", t.FS.cwd)

	case "cat":
		if len(args) > 1 {
			for _, f := range args[1:] {
				p := t.FS.Abs(f)
				// 处理特殊设备
				if p == "/dev/null" {
					continue
				}
				if p == "/dev/zero" {
					out.Write(make([]byte, 1024))
					continue
				}

				if e, ok := t.FS.GetEntry(p); ok {
					if e.IsDir {
						fmt.Fprintf(out, "cat: %s: 是一个目录\n", f)
						t.lastExitCode = 1
					} else {
						out.Write(e.Content)
						// 确保以换行结束，如果原文件没有
						if len(e.Content) > 0 && e.Content[len(e.Content)-1] != '\n' {
							out.Write([]byte("\n"))
						}
					}
				} else {
					fmt.Fprintf(out, "cat: %s: 没有那个文件或目录\n", f)
					t.lastExitCode = 1
				}
			}
		} else {
			// 使用 io.Copy 进行流式处理，避免死锁
			io.Copy(out, in)
		}

	case "echo":
		msg := strings.Join(args[1:], " ")
		fmt.Fprintf(out, "%s\n", msg)

	case "cp":
		if len(args) >= 3 {
			src := t.FS.Abs(args[1])
			dst := t.FS.Abs(args[2])
			e, ok := t.FS.GetEntry(src)
			if !ok {
				fmt.Fprintf(out, "cp: 无法获取 '%s' 的状态: 没有那个文件或目录\n", args[1])
				t.lastExitCode = 1
			} else if e.IsDir {
				fmt.Fprintf(out, "cp: -r 未指定; 省略目录 '%s'\n", args[1])
				t.lastExitCode = 1
			} else {
				if d, ok := t.FS.GetEntry(dst); ok && d.IsDir {
					dst = path.Join(dst, e.Name)
				}
				t.FS.Write(dst, e.Content, e.Mode)
			}
		}

	case "mv":
		if len(args) >= 3 {
			src := t.FS.Abs(args[1])
			dst := t.FS.Abs(args[2])
			if d, ok := t.FS.GetEntry(dst); ok && d.IsDir {
				dst = path.Join(dst, path.Base(src))
			}
			if err := t.FS.Rename(src, dst); err != nil {
				fmt.Fprintf(out, "mv: %v\n", err)
				t.lastExitCode = 1
			}
		}

	case "mkdir":
		for _, d := range args[1:] {
			if !strings.HasPrefix(d, "-") {
				t.FS.Mkdir(t.FS.Abs(d))
			}
		}

	case "rm", "rmdir":
		for _, f := range args[1:] {
			if !strings.HasPrefix(f, "-") {
				p := t.FS.Abs(f)
				if _, ok := t.FS.GetEntry(p); ok {
					t.FS.Remove(p)
				} else {
					fmt.Fprintf(out, "rm: 无法删除 '%s': 没有那个文件或目录\n", f)
					t.lastExitCode = 1
				}
			}
		}

	case "touch":
		for _, f := range args[1:] {
			p := t.FS.Abs(f)
			if _, ok := t.FS.GetEntry(p); !ok {
				t.FS.Write(p, []byte{}, 0644)
			}
		}

	case "grep":
		if len(args) < 2 {
			return // 忽略 stdin 如果没有 pattern
		}
		pattern := args[1]
		files := args[2:]
		// 极简实现，忽略 flags
		if strings.HasPrefix(pattern, "-") {
			if len(files) > 0 {
				pattern = files[0]
				files = files[1:]
			}
		}
		useColor := isTTY(out)

		doGrep := func(scanner *bufio.Scanner, fname string) {
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, pattern) {
					prefix := ""
					if len(files) > 1 {
						prefix = fname + ":"
						if useColor {
							prefix = "\033[35m" + fname + "\033[0m:"
						}
					}

					outputLine := line
					if useColor {
						// 高亮匹配
						outputLine = strings.ReplaceAll(line, pattern, "\033[1;31m"+pattern+"\033[0m")
					}
					fmt.Fprintf(out, "%s%s\n", prefix, outputLine)
				}
			}
		}

		if len(files) == 0 {
			// 使用 scanner 逐行读取，避免 io.ReadAll 导致的死锁
			scanner := bufio.NewScanner(in)
			doGrep(scanner, "(standard input)")
		} else {
			for _, f := range files {
				if e, ok := t.FS.GetEntry(t.FS.Abs(f)); ok && !e.IsDir {
					scanner := bufio.NewScanner(bytes.NewReader(e.Content))
					doGrep(scanner, f)
				} else {
					fmt.Fprintf(out, "grep: %s: 没有那个文件或目录\n", f)
				}
			}
		}

	case "ps":
		// 模拟真实 ps aux 输出
		fmt.Fprintln(out, "USER         PID %CPU %MEM    VSZ   RSS TTY      STAT START   TIME COMMAND")
		fmt.Fprintf(out, "root           1  0.0  0.1 168532 12856 ?        Ss   %s   0:02 /sbin/init\n", startTime.Format("15:04"))
		fmt.Fprintf(out, "root           2  0.0  0.0      0     0 ?        S    %s   0:00 [kthreadd]\n", startTime.Format("15:04"))
		fmt.Fprintf(out, "root         832  0.0  0.3  14520  6520 ?        Ss   %s   0:00 /usr/sbin/sshd -D\n", startTime.Format("15:04"))
		// 当前进程
		myPID := t.pid
		fmt.Fprintf(out, "root        %d  0.0  0.1  12340  4320 pts/0    Ss   %s   0:00 -bash\n", myPID, time.Now().Format("15:04"))
		fmt.Fprintf(out, "root        %d  0.0  0.0   9820  3210 pts/0    R+   %s   0:00 ps aux\n", myPID+10, time.Now().Format("15:04"))

	case "whoami":
		t.mu.Lock()
		fmt.Fprintln(out, t.Env["USER"])
		t.mu.Unlock()

	case "id":
		u := "root"
		t.mu.Lock()
		if v, ok := t.Env["USER"]; ok {
			u = v
		}
		t.mu.Unlock()
		uid, _ := Users[u]
		gid, _ := Groups[u]
		fmt.Fprintf(out, "uid=%d(%s) gid=%d(%s) groups=%d(%s)\n", uid, u, gid, u, gid, u)

	case "date":
		fmt.Fprintln(out, time.Now().Format(time.UnixDate))

	case "uptime":
		d := time.Since(startTime)
		fmt.Fprintf(out, " %s up %d min,  1 user,  load average: 0.00, 0.01, 0.05\n",
			time.Now().Format("15:04:05"), int(d.Minutes()))

	case "clear":
		out.Write([]byte("\033[H\033[2J"))

	case "exit", "logout":
		t.Running = false

	case "wget", "curl":
		if len(args) > 1 {
			url := args[len(args)-1]
			if cmd == "wget" {
				fmt.Fprintf(out, "--%s--  %s\n", time.Now().Format("2006-01-02 15:04:05"), url)
				fmt.Fprintf(out, "Resolving %s... 127.0.0.1\n", strings.Split(url, "/")[2])
				fmt.Fprintln(out, "Connecting to 127.0.0.1... connected.")
				fmt.Fprintln(out, "HTTP request sent, awaiting response... 404 Not Found")
				fmt.Fprintln(out, "2023-01-01 12:00:00 ERROR 404: Not Found.")
			} else {
				fmt.Fprintln(out, "curl: (6) Could not resolve host: "+url)
			}
			t.lastExitCode = 1
		} else {
			fmt.Fprintf(out, "%s: try 'help'\n", cmd)
			t.lastExitCode = 1
		}

	case "uname":
		if len(args) > 1 && args[1] == "-a" {
			fmt.Fprintln(out, "Linux ubuntu-server 5.15.0-generic #1 SMP Fri Jan 1 00:00:00 UTC 2022 x86_64 x86_64 x86_64 GNU/Linux")
		} else {
			fmt.Fprintln(out, "Linux")
		}

	case "free":
		fmt.Fprintln(out, "              total        used        free      shared  buff/cache   available")
		fmt.Fprintln(out, "Mem:       16303284     1024512     2543210        1234    12735562    15000000")
		fmt.Fprintln(out, "Swap:       2097148           0     2097148")

	case "df":
		fmt.Fprintln(out, "Filesystem      1K-blocks      Used Available Use% Mounted on")
		fmt.Fprintln(out, "/dev/sda2       102400000   5242880  97157120   6% /")
		fmt.Fprintln(out, "tmpfs             1630328         0   1630328   0% /run/user/0")

	case "chmod":
		if len(args) > 2 {
			if m, err := strconv.ParseInt(args[1], 8, 32); err == nil {
				for _, f := range args[2:] {
					t.FS.Chmod(t.FS.Abs(f), os.FileMode(m))
				}
			} else {
				fmt.Fprintf(out, "chmod: 无效模式: '%s'\n", args[1])
				t.lastExitCode = 1
			}
		}

	case "chown":
		// 忽略实际 chown 逻辑，仅做样子
		if len(args) < 3 {
			fmt.Fprintln(out, "chown: 缺少操作数")
			t.lastExitCode = 1
		}

	case "head", "tail":
		// 简易实现
		limit := 10
		files := []string{}
		for i := 1; i < len(args); i++ {
			if args[i] == "-n" && i+1 < len(args) {
				limit, _ = strconv.Atoi(args[i+1])
				i++
			} else {
				files = append(files, args[i])
			}
		}
		if len(files) == 0 {
			// 从 stdin 读取，必须流式处理以避免管道死锁
			if cmd == "head" {
				scanner := bufio.NewScanner(in)
				for i := 0; i < limit && scanner.Scan(); i++ {
					fmt.Fprintln(out, scanner.Text())
				}
			} else { // tail
				scanner := bufio.NewScanner(in)
				var lines []string
				for scanner.Scan() {
					lines = append(lines, scanner.Text())
				}
				start := 0
				if len(lines) > limit {
					start = len(lines) - limit
				}
				for i := start; i < len(lines); i++ {
					fmt.Fprintln(out, lines[i])
				}
			}
		} else {
			for _, f := range files {
				if len(files) > 1 {
					fmt.Fprintf(out, "==> %s <==\n", f)
				}
				if e, ok := t.FS.GetEntry(t.FS.Abs(f)); ok {
					printLines(out, string(e.Content), cmd == "head", limit)
				}
			}
		}

	case "wc":
		// 仅实现类似 wc -l 的行数统计
		handleWc := func(r io.Reader) int {
			count := 0
			scanner := bufio.NewScanner(r)
			for scanner.Scan() {
				count++
			}
			return count
		}

		if len(args) == 1 {
			// 从 stdin 读取，流式处理
			count := handleWc(in)
			fmt.Fprintf(out, "%d\n", count)
		} else {
			for _, f := range args[1:] {
				if e, ok := t.FS.GetEntry(t.FS.Abs(f)); ok {
					count := handleWc(bytes.NewReader(e.Content))
					fmt.Fprintf(out, "%d %s\n", count, f)
				}
			}
		}

	case "ping":
		if len(args) < 2 {
			fmt.Fprintln(out, "ping: usage error: Destination address required")
			t.lastExitCode = 1
			return
		}
		target := args[1]
		ip := "1.2.3.4" // Fake IP
		fmt.Fprintf(out, "PING %s (%s) 56(84) bytes of data.\n", target, ip)
		for i := 1; i <= 4; i++ {
			// 模拟延迟
			time.Sleep(100 * time.Millisecond) // 在自动化测试中不要睡太久
			fmt.Fprintf(out, "64 bytes from %s: icmp_seq=%d ttl=53 time=%.1f ms\n", ip, i, 20.0+rand.Float64()*10)
		}
		fmt.Fprintln(out, "")
		fmt.Fprintf(out, "--- %s ping statistics ---\n", target)
		fmt.Fprintf(out, "4 packets transmitted, 4 received, 0%% packet loss, time 3004ms\n")
		fmt.Fprintf(out, "rtt min/avg/max/mdev = 20.1/25.2/30.5/3.1 ms\n")

	case "sudo":
		if len(args) > 1 {
			// 简单的 sudo 模拟：如果是 root 直接执行，否则...也直接执行（蜜罐通常允许权限）
			// 实际上我们需要递归调用 runCommand
			newArgs := args[1:]
			t.runCommand(newArgs, in, out)
		}

	case "sleep":
		if len(args) > 1 {
			if d, err := strconv.Atoi(args[1]); err == nil {
				// 限制最大 sleep 时间，防止被 DoS
				if d > 5 {
					d = 5
				}
				time.Sleep(time.Duration(d) * time.Second)
			}
		}

	case "netstat", "ss":
		fmt.Fprintln(out, "Active Internet connections (w/o servers)")
		fmt.Fprintln(out, "Proto Recv-Q Send-Q Local Address           Foreign Address         State")
		fmt.Fprintf(out, "tcp        0     64 192.168.1.10:22         192.168.1.5:5678        ESTABLISHED\n")

	case "more", "less":
		// 简化为 cat
		if len(args) > 1 {
			t.runCommand(append([]string{"cat"}, args[1:]...), in, out)
		}

	case "history":
		for i, h := range t.History {
			fmt.Fprintf(out, "%5d  %s\n", i+1, h)
		}

	case "export":
		if len(args) == 1 {
			for k, v := range t.Env {
				fmt.Fprintf(out, "declare -x %s=\"%s\"\n", k, v)
			}
		} else {
			for _, kv := range args[1:] {
				parts := strings.SplitN(kv, "=", 2)
				if len(parts) == 2 {
					t.Env[parts[0]] = parts[1]
				}
			}
		}

	case "kernelpanic":
		pr, pw := io.Pipe()

		t.mu.Lock()
		t.RawModeWriter = pw
		t.mu.Unlock()

		// 恢复函数
		defer func() {
			t.mu.Lock()
			t.RawModeWriter = nil
			t.mu.Unlock()
			pw.Close()
		}()

		RunKernelPanic(out, pr, func() (int, int) {
			t.mu.Lock()
			defer t.mu.Unlock()
			return t.Width, t.Height
		})

	default:
		fmt.Fprintf(out, "%s: 未找到命令\n", cmd)
		t.lastExitCode = 127
	}
}

// isTTY 检查写入目标是否为模拟的终端
func isTTY(w io.Writer) bool {
	// 这是一个适用于我们环境的简单启发式方法：
	// 如果我们正在写入一个管道，那它就不是一个 TTY。
	// 如果我们正在写入 CRLFWriter (它包装了终端)，那它就是一个 TTY。
	// 这并非万无一失，但对我们的管道实现有效。

	// 管道的写入器是底层的 *io.PipeWriter
	if _, ok := w.(*io.PipeWriter); ok {
		return false
	}
	// 最终输出到终端的写入器是 *CRLFWriter
	if _, ok := w.(*CRLFWriter); ok {
		return true
	}
	// 备用方案，安全起见假设是 TTY (例如，对于单个命令或未知写入器)
	return true
}

// 辅助函数

func getNameByID(m map[string]int, id int) (string, bool) {
	for k, v := range m {
		if v == id {
			return k, true
		}
	}
	return "", false
}

func formatSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d", size)
	}
	units := []string{"", "K", "M", "G", "T", "P"}
	i := int(math.Floor(math.Log(float64(size)) / math.Log(1024)))
	s := float64(size) / math.Pow(1024, float64(i))
	if s < 10 && i > 0 {
		return fmt.Sprintf("%.1f%s", s, units[i])
	}
	return fmt.Sprintf("%.0f%s", s, units[i])
}

func printLines(out io.Writer, content string, isHead bool, limit int) {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	start, end := 0, len(lines)
	if isHead {
		if limit < end {
			end = limit
		}
	} else {
		if limit < end {
			start = end - limit
		}
	}

	for i := start; i < end; i++ {
		fmt.Fprintln(out, lines[i])
	}
}
