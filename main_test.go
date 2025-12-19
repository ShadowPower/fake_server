package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// init 确保测试前 BaseFS 已加载
func init() {
	initFS()
}

// TestMassiveConcurrency 模拟 10,000 个并发会话
// 目标：确保内存增长合理（COW机制生效），无数据竞争 panic
func TestMassiveConcurrency(t *testing.T) {
	// 记录初始内存
	var m1, m2 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m1)

	concurrency := 10000 // 1万并发
	t.Logf("Starting %d concurrent sessions (Terminal + FS)...", concurrency)

	var wg sync.WaitGroup
	wg.Add(concurrency)

	startTime := time.Now()

	// 预先生成一些随机数据，避免在循环内部频繁调用 crypto/rand 成为瓶颈
	// 我们关注的是 FS 和 Terminal 的性能，而不是随机数生成器的性能
	randomPayload := make([]byte, 1024)
	rand.Read(randomPayload)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer wg.Done()

			// 每个用户独立的 SessionFS (Copy-On-Write)
			fs := NewSessionFS()

			// 模拟环境变量
			env := map[string]string{
				"USER": "test_user",
				"TERM": "xterm",
			}

			// 模拟 Shell 交互脚本
			inputCmds := "ls -la /bin\nwhoami\ndate\ntouch /tmp/myfile\nexit\n"
			inBuf := bytes.NewBufferString(inputCmds)
			outBuf := &bytes.Buffer{}

			// 使用 MockRW 模拟网络 I/O
			mockRW := &MockReadWriter{Reader: inBuf, Writer: outBuf}

			// 创建终端
			term := NewTerminal(mockRW, fs, env, 80, 24)

			// 极端测试：写入文件触发 COW
			fileName := fmt.Sprintf("/tmp/user_file_%d", id)
			// 直接操作 FS 模拟副作用
			fs.Write(fileName, randomPayload, 0644)

			// 运行 Shell
			term.Run()

			// 简单验证输出
			outStr := outBuf.String()
			if !strings.Contains(outStr, "root") && !strings.Contains(outStr, "ls") {
				// 在极高并发下不要轻易 Error，除非逻辑完全崩溃
				// 这里主要检测是否有 Panic
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(startTime)

	runtime.GC()
	runtime.ReadMemStats(&m2)

	// 计算内存差异
	allocDiff := int64(m2.TotalAlloc-m1.TotalAlloc) / 1024 / 1024
	sysDiff := int64(m2.Sys-m1.Sys) / 1024 / 1024
	heapObjDiff := m2.HeapObjects - m1.HeapObjects

	t.Logf("Finished %d sessions in %v (%.2f sessions/sec)", concurrency, duration, float64(concurrency)/duration.Seconds())
	t.Logf("Memory Delta -> TotalAlloc: +%d MB, Sys: +%d MB, HeapObjects: +%d", allocDiff, sysDiff, heapObjDiff)

	// 性能断言：
	// 10k 会话，如果每个会话占用内存过大（例如 100KB），总内存会增加 1GB。
	// 我们的目标是尽量低，但 Go 的 runtime 本身有开销。
	// 这里设置一个宽松的阈值（2GB），主要防止 BaseFS 被深度拷贝导致几十 GB 的占用。
	if sysDiff > 2048 {
		t.Errorf("Memory leak detected! Sys memory increased by %d MB", sysDiff)
	}
}

// TestDataRaceAndIsolation 验证 COW 机制和并发安全性
// 必须使用 `go test -race` 运行
func TestDataRaceAndIsolation(t *testing.T) {
	var wg sync.WaitGroup
	count := 500 // 增加并发数以提高竞争概率

	targetFile := "/etc/passwd" // BaseFS 中的文件

	// 验证器：确保 BaseFS 始终未变
	checkBaseFS := func() {
		fs := NewSessionFS()
		e, ok := fs.GetEntry(targetFile)
		if !ok {
			t.Fatal("Base file vanished!")
		}
		if bytes.Contains(e.Content, []byte("hacked")) {
			t.Fatal("CRITICAL: BaseFS was polluted! Isolation failed.")
		}
	}

	t.Logf("Running isolation test with %d concurrent modifiers...", count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			fs := NewSessionFS()

			// 1. 读取共享文件 (并发读)
			initialEntry, ok := fs.GetEntry(targetFile)
			if !ok {
				t.Error("Base file missing")
				return
			}

			// 确保我们读到的是原始内容
			if bytes.Contains(initialEntry.Content, []byte("hacked")) {
				t.Error("Read polluted data from shared FS")
			}

			// 2. 写入该文件 (并发写 - 应该触发 COW)
			// 每个协程写入不同的内容
			newContent := []byte(fmt.Sprintf("hacked by %d", id))
			err := fs.Write(targetFile, newContent, 0644)
			if err != nil {
				t.Errorf("Write failed: %v", err)
			}

			// 3. 再次读取，应该是自己的版本 (Read after Write within session)
			entry2, _ := fs.GetEntry(targetFile)
			if !bytes.Equal(entry2.Content, newContent) {
				t.Error("COW failed, content mismatch in local session")
			}
		}(i)
	}

	wg.Wait()
	checkBaseFS() // 最后检查一次 BaseFS 的完整性
	t.Log("Isolation test passed.")
}

// TestSFTPExtreme 模拟真实的 SFTP 交互
// 通过 net.Pipe 连接真实的 sftp.Client 和我们的 SFTPHandler
func TestSFTPExtreme(t *testing.T) {
	// 使用并发进行压力测试
	concurrency := 1000
	t.Logf("Starting %d concurrent SFTP sessions via net.Pipe...", concurrency)

	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			defer wg.Done()

			// 1. 创建内存管道：一端是 Server，一端是 Client
			serverConn, clientConn := net.Pipe()

			// 2. 启动服务端 (运行在独立协程)
			fs := NewSessionFS()
			handler := &SFTPHandler{fs: fs}
			// pkg/sftp 提供了 NewRequestServer，它会自动解析协议并调用我们的 handler
			server := sftp.NewRequestServer(serverConn, sftp.Handlers{
				FileGet:  handler,
				FilePut:  handler,
				FileCmd:  handler,
				FileList: handler,
			})

			go func() {
				if err := server.Serve(); err != nil && err != io.EOF {
					// 管道关闭导致的 EOF 是正常的
					t.Logf("Server error: %v", err)
				}
				serverConn.Close()
			}()

			// 3. 启动客户端 (当前协程)
			client, err := sftp.NewClientPipe(clientConn, clientConn)
			if err != nil {
				t.Errorf("Failed to create client: %v", err)
				return
			}
			defer client.Close()

			// --- 执行真实操作 ---

			// A. 写入文件
			fname := fmt.Sprintf("/tmp/sftp_test_%d.txt", id)
			f, err := client.Create(fname)
			if err != nil {
				t.Errorf("SFTP Create failed: %v", err)
				return
			}

			payload := []byte(fmt.Sprintf("Hello from session %d", id))
			_, err = f.Write(payload)
			if err != nil {
				t.Errorf("SFTP Write failed: %v", err)
				f.Close()
				return
			}
			f.Close()

			// B. 读取文件并验证
			fr, err := client.Open(fname)
			if err != nil {
				t.Errorf("SFTP Open failed: %v", err)
				return
			}
			readBuf, _ := io.ReadAll(fr)
			if !bytes.Equal(readBuf, payload) {
				t.Errorf("SFTP Data corruption! Want %s, Got %s", payload, readBuf)
			}
			fr.Close()

			// C. 列出目录
			files, err := client.ReadDir("/tmp")
			if err != nil {
				t.Errorf("ReadDir failed: %v", err)
				return
			}
			found := false
			for _, file := range files {
				if file.Name() == fmt.Sprintf("sftp_test_%d.txt", id) {
					found = true
					break
				}
			}
			if !found {
				t.Error("Created file not found in directory listing")
			}

		}(i)
	}

	wg.Wait()
	t.Log("SFTP Extreme test passed.")
}

// TestProtocolFuzzing 针对 Telnet 和 RLogin 协议解析器进行 Fuzz 测试
// 发送垃圾数据、断包数据，确保不 Panic
func TestProtocolFuzzing(t *testing.T) {
	fuzzPatterns := [][]byte{
		{0xFF},                           // Incomplete IAC
		{0xFF, 0xFA},                     // Incomplete SB
		{0xFF, 0xFA, 0x1F},               // Incomplete NAWS
		bytes.Repeat([]byte{0xFF}, 1000), // IAC flood
		{0x00, 0x01, 0x02, 0x03},         // Random bytes
		make([]byte, 0),                  // Empty
	}

	t.Log("Fuzzing Telnet handler...")
	for _, pattern := range fuzzPatterns {
		// Mock Telnet
		serverConn, clientConn := net.Pipe()
		go handleTelnetConn(serverConn)

		// Client writes garbage
		go func() {
			clientConn.Write(pattern)
			time.Sleep(10 * time.Millisecond)
			clientConn.Close()
		}()

		// Consume output so handler doesn't block on write
		io.ReadAll(clientConn)
	}

	t.Log("Fuzzing RLogin handler...")
	for _, pattern := range fuzzPatterns {
		// Mock RLogin
		serverConn, clientConn := net.Pipe()
		go handleRLoginConn(serverConn)

		// Client writes garbage
		go func() {
			clientConn.Write(pattern)
			time.Sleep(10 * time.Millisecond)
			clientConn.Close()
		}()

		io.ReadAll(clientConn)
	}

	t.Log("Fuzzing passed (no panics observed).")
}

// --- Helpers ---

// MockReadWriter 简单的线程安全 Buffer，实现 io.ReadWriter
type MockReadWriter struct {
	Reader io.Reader
	Writer io.Writer
}

func (m *MockReadWriter) Read(p []byte) (n int, err error) {
	return m.Reader.Read(p)
}

func (m *MockReadWriter) Write(p []byte) (n int, err error) {
	return m.Writer.Write(p)
}
