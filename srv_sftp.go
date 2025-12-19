package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"
)

// SFTPHandler bridges the sftp packet with our in-memory SessionFS
type SFTPHandler struct {
	fs *SessionFS
}

// Fileread implements sftp.FileReader
func (h *SFTPHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	// Try to get file from our virtual FS
	e, ok := h.fs.GetEntry(r.Filepath)
	if !ok {
		return nil, os.ErrNotExist
	}

	// 读取时加读锁，防止并发写入导致切片读取越界
	e.mu.RLock()
	content := e.Content
	e.mu.RUnlock()

	// Wrap the byte content in a ReaderAt
	return bytes.NewReader(content), nil
}

// Filewrite implements sftp.FileWriter
func (h *SFTPHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	// 创建写入器
	return &SFTPWriter{fs: h.fs, path: r.Filepath}, nil
}

// Filecmd implements sftp.FileCmder (Mkdir, Rmdir, Rename, Chmod, etc.)
func (h *SFTPHandler) Filecmd(r *sftp.Request) error {
	switch r.Method {
	case "Setstat":
		// Handle Chmod
		mode := r.Attributes().FileMode()
		if mode != 0 {
			return h.fs.Chmod(r.Filepath, mode)
		}
		return nil
	case "Rename":
		return h.fs.Rename(r.Filepath, r.Target)
	case "Rmdir", "Remove":
		return h.fs.Remove(r.Filepath)
	case "Mkdir":
		return h.fs.Mkdir(r.Filepath)
	}
	return sftp.ErrSSHFxOpUnsupported
}

// Filelist implements sftp.FileLister
func (h *SFTPHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		entries, err := h.fs.ListDir(r.Filepath)
		if err != nil {
			return nil, err
		}
		list := make(lister, len(entries))
		for i, e := range entries {
			list[i] = &fileInfo{e}
		}
		return list, nil
	case "Stat", "Lstat":
		e, ok := h.fs.GetEntry(r.Filepath)
		if !ok {
			return nil, os.ErrNotExist
		}
		return lister{&fileInfo{e}}, nil
	}
	return nil, sftp.ErrSSHFxOpUnsupported
}

// --- Helper Types ---

// SFTPWriter 优化版：避免持有全局锁，解决大文件卡顿和Panic问题
type SFTPWriter struct {
	fs   *SessionFS
	path string
	// 不再在 Writer 内部维护 buf，直接操作 FileEntry
}

func (w *SFTPWriter) WriteAt(p []byte, off int64) (int, error) {
	// 1. 获取或创建 Overlay Entry (短暂持有全局锁)
	// 这一步确保文件存在于 Overlay 层，如果是 BaseFS 的文件，执行 COW 复制
	w.fs.mu.Lock()
	entry, exists := w.fs.overlay[w.path]
	if !exists || entry == nil {
		// 需要从 BaseFS 复制或新建
		var baseContent []byte
		var mode os.FileMode = 0644
		uid, gid := 0, 0

		// 检查 BaseFS
		if baseEntry, ok := BaseFS[w.path]; ok {
			baseContent = baseEntry.Content
			mode = baseEntry.Mode
			uid, gid = baseEntry.UID, baseEntry.GID
		}

		// 创建新 Entry (Deep Copy content)
		// 预留空间：如果是在文件末尾追加，可以预分配多一点
		initCap := len(baseContent)
		if int(off)+len(p) > initCap {
			initCap = int(off) + len(p)
		}

		newContent := make([]byte, len(baseContent), initCap)
		copy(newContent, baseContent)

		entry = &FileEntry{
			Name:    path.Base(w.path),
			Content: newContent,
			Mode:    mode,
			ModTime: time.Now(),
			UID:     uid,
			GID:     gid,
		}
		w.fs.overlay[w.path] = entry
	}
	w.fs.mu.Unlock() // 立即释放全局锁，允许其他用户操作其他文件

	// 2. 写入数据 (持有文件级锁)
	entry.mu.Lock()
	defer entry.mu.Unlock()

	end := int(off) + len(p)
	if end > MaxFileSize {
		return 0, errors.New("quota exceeded")
	}

	// 扩容检查
	if end > cap(entry.Content) {
		newCap := end
		// 简单的倍增策略，防止频繁分配
		if newCap < cap(entry.Content)*2 {
			newCap = cap(entry.Content) * 2
		}
		if newCap > MaxFileSize {
			newCap = MaxFileSize
		}

		// [FIX] 彻底修复 Panic: makeslice: cap out of range
		// make 的第二个参数是 len，第三个是 cap。必须保证 newCap >= len
		newSlice := make([]byte, len(entry.Content), newCap)
		copy(newSlice, entry.Content)
		entry.Content = newSlice
	}

	// 扩展长度：如果写入位置超过了当前长度（例如文件空洞或追加）
	if end > len(entry.Content) {
		entry.Content = entry.Content[:end]
	}

	// 复制数据
	copy(entry.Content[off:], p)
	entry.ModTime = time.Now()

	return len(p), nil
}

// Adapter for os.FileInfo to satisfy sftp.ListerAt
type fileInfo struct{ e *FileEntry }

func (f *fileInfo) Name() string { return f.e.Name }
func (f *fileInfo) Size() int64 {
	// 为了数据一致性，获取大小时加读锁
	f.e.mu.RLock()
	defer f.e.mu.RUnlock()
	return int64(len(f.e.Content))
}
func (f *fileInfo) Mode() os.FileMode  { return f.e.Mode }
func (f *fileInfo) ModTime() time.Time { return f.e.ModTime }
func (f *fileInfo) IsDir() bool        { return f.e.IsDir }
func (f *fileInfo) Sys() interface{}   { return nil }

type lister []os.FileInfo

func (l lister) ListAt(f []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(f, l[offset:])
	return n, nil
}
