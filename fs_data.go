package main

import (
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// ==========================================
// 基础文件系统数据初始化
// ==========================================

var (
	startTime      = time.Now()
	BaseFS         map[string]*FileEntry
	BaseFSDirCache map[string][]*FileEntry // 性能优化：预先索引的目录内容
	Users          map[string]int          // 用户名 -> UID
	Groups         map[string]int          // 组名 -> GID
	// 新增：全局共享的会话文件系统，确保数据在不同连接间持久化
	GlobalSessionFS *SessionFS
)

func initFS() {
	BaseFS = make(map[string]*FileEntry)
	BaseFSDirCache = make(map[string][]*FileEntry) // 初始化缓存
	Users = make(map[string]int)
	Groups = make(map[string]int)
	t := time.Now()

	// 辅助函数：添加文件
	add := func(filePath, content string, mode os.FileMode, uid, gid int) {
		BaseFS[filePath] = &FileEntry{
			Name:    path.Base(filePath),
			IsDir:   false,
			Content: []byte(content),
			Mode:    mode,
			ModTime: t,
			UID:     uid, GID: gid, Nlink: 1,
		}
	}

	// 1. 初始化目录结构
	dirs := []string{
		"/", "/bin", "/boot", "/dev", "/etc", "/home", "/lib", "/lib64",
		"/media", "/mnt", "/opt", "/proc", "/root", "/run", "/sbin",
		"/srv", "/sys", "/tmp", "/usr", "/var", "/usr/bin", "/usr/sbin",
		"/usr/local", "/usr/local/bin", "/var/log", "/home/user",
		"/etc/ssh", "/etc/systemd", "/etc/network",
		"/proc/sys", "/proc/sys/kernel", "/proc/net",
		"/sys/class", "/sys/class/net", "/sys/class/net/eth0",
		"/var/www", "/var/www/html",
	}
	for _, d := range dirs {
		BaseFS[d] = &FileEntry{
			Name:    path.Base(d),
			IsDir:   true,
			Mode:    0755 | os.ModeDir,
			ModTime: t,
			UID:     0, GID: 0, Nlink: 2,
		}
	}

	// 2. 初始化用户和组
	passwdContent := "root:x:0:0:root:/root:/bin/bash\n" +
		"daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\n" +
		"bin:x:2:2:bin:/bin:/usr/sbin/nologin\n" +
		"sys:x:3:3:sys:/dev:/usr/sbin/nologin\n" +
		"sync:x:4:65534:sync:/bin:/bin/sync\n" +
		"games:x:5:60:games:/usr/games:/usr/sbin/nologin\n" +
		"man:x:6:12:man:/var/cache/man:/usr/sbin/nologin\n" +
		"lp:x:7:7:lp:/var/spool/lpd:/usr/sbin/nologin\n" +
		"mail:x:8:8:mail:/var/mail:/usr/sbin/nologin\n" +
		"news:x:9:9:news:/var/spool/news:/usr/sbin/nologin\n" +
		"www-data:x:33:33:www-data:/var/www:/usr/sbin/nologin\n" +
		"sshd:x:108:65534::/run/sshd:/usr/sbin/nologin\n" +
		"user:x:1000:1000:user:/home/user:/bin/bash\n"
	groupContent := "root:x:0:\n" +
		"daemon:x:1:\n" +
		"bin:x:2:\n" +
		"sys:x:3:\n" +
		"adm:x:4:syslog\n" +
		"tty:x:5:\n" +
		"disk:x:6:\n" +
		"lp:x:7:\n" +
		"mail:x:8:\n" +
		"news:x:9:\n" +
		"www-data:x:33:\n" +
		"sshd:x:108:\n" +
		"user:x:1000:\n"

	parseUsersGroups := func(passwd, group string) {
		for _, line := range strings.Split(passwd, "\n") {
			parts := strings.Split(line, ":")
			if len(parts) > 3 {
				name := parts[0]
				uid, _ := strconv.Atoi(parts[2])
				Users[name] = uid
			}
		}
		for _, line := range strings.Split(group, "\n") {
			parts := strings.Split(line, ":")
			if len(parts) > 2 {
				name := parts[0]
				gid, _ := strconv.Atoi(parts[2])
				Groups[name] = gid
			}
		}
	}
	parseUsersGroups(passwdContent, groupContent)

	add("/etc/passwd", passwdContent, 0644, 0, 0)
	add("/etc/group", groupContent, 0644, 0, 0)
	add("/etc/hostname", "ubuntu-server", 0644, 0, 0)
	add("/etc/os-release", "PRETTY_NAME=\"Ubuntu 22.04.1 LTS\"\nNAME=\"Ubuntu\"\nVERSION_ID=\"22.04\"\nVERSION=\"22.04.1 LTS (Jammy Jellyfish)\"\nID=ubuntu\n", 0644, 0, 0)
	add("/etc/issue", "Ubuntu 22.04.1 LTS \\n \\l\n", 0644, 0, 0)
	add("/etc/shadow", "root:*:18890:0:99999:7:::\nuser:$6$...:18890:0:99999:7:::\n", 0640, 0, 42)
	add("/root/.bashrc", "export PS1='\\[\\033[01;32m\\]\\u@\\h\\[\\033[00m\\]:\\[\\033[01;34m\\]\\w\\[\\033[00m\\]\\$ '\nalias ll='ls -alF'\n", 0644, 0, 0)
	add("/etc/hosts", "127.0.0.1 localhost\n127.0.1.1 ubuntu-server\n", 0644, 0, 0)
	add("/etc/resolv.conf", "nameserver 1.1.1.1\nnameserver 8.8.8.8\n", 0644, 0, 0)
	add("/etc/fstab", "/dev/sda2 / ext4 defaults 0 0\n", 0644, 0, 0)

	// 3. 模拟 /proc 和 /sys
	add("/proc/version", "Linux version 5.15.0-generic (buildd@lcy02-amd64-001) (gcc version 11.2.0 (Ubuntu 11.2.0-19ubuntu1))", 0444, 0, 0)
	add("/proc/cpuinfo", "processor\t: 0\nvendor_id\t: GenuineIntel\ncpu family\t: 6\nmodel\t\t: 165\nmodel name\t: Intel(R) Core(TM) i7-10700 CPU @ 2.90GHz\n\nprocessor\t: 1\nvendor_id\t: GenuineIntel\ncpu family\t: 6\nmodel\t\t: 165\nmodel name\t: Intel(R) Core(TM) i7-10700 CPU @ 2.90GHz\n", 0444, 0, 0)
	add("/proc/meminfo", "MemTotal:       16303284 kB\nMemFree:         2543210 kB\nMemAvailable:   10234123 kB\nBuffers:          223412 kB\nCached:          8123456 kB\nSwapTotal:       2097148 kB\nSwapFree:        2097148 kB\n", 0444, 0, 0)
	add("/proc/uptime", "3600.00 7100.00", 0444, 0, 0)
	add("/proc/loadavg", "0.01 0.05 0.05 1/256 12345", 0444, 0, 0)
	add("/sys/class/net/eth0/address", "00:11:22:33:44:55\n", 0444, 0, 0)

	// 4. 模拟特殊设备
	add("/dev/null", "", 0666, 0, 0)
	add("/dev/zero", "", 0666, 0, 0)
	add("/dev/random", "gibberish...", 0666, 0, 0)
	add("/dev/urandom", "more gibberish...", 0666, 0, 0)

	// 5. 模拟二进制文件 (仅占位，实际逻辑在 commands.go)
	cmds := []string{
		"ls", "cd", "pwd", "cat", "echo", "touch", "mkdir", "rm", "mv", "cp",
		"grep", "ps", "top", "kill", "id", "whoami", "w", "last", "history",
		"date", "uptime", "free", "df", "uname", "stty", "env", "clear", "exit",
		"vi", "vim", "wget", "curl", "ssh", "chmod", "chown", "which", "find",
		"head", "tail", "wc", "export", "mount", "stat", "who", "sudo",
		"ping", "netstat", "ss", "sleep", "ln", "rmdir", "more", "less",
		"kernelpanic",
	}
	binContent := "\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x02\x00\x3e\x00\x01\x00\x00\x00"
	for _, c := range cmds {
		add("/bin/"+c, binContent, 0755, 0, 0)
		add("/usr/bin/"+c, binContent, 0755, 0, 0)
	}

	// 性能优化：在 BaseFS 完全构建后，填充目录缓存
	for p, e := range BaseFS {
		dir := path.Dir(p)
		if p != dir { // 不将目录自身添加到其父目录的列表中
			BaseFSDirCache[dir] = append(BaseFSDirCache[dir], e)
		}
	}

	// 初始化全局共享的 SessionFS
	GlobalSessionFS = NewSessionFS()
}
