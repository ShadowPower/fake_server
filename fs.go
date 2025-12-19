package main

import (
	"errors"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// ==========================================
// 高性能会话级文件系统 (COW - Copy On Write)
// ==========================================

const (
	MaxFileSize = 5 << 20 // 限制单个文件最大 5MB
)

// FileEntry 定义文件元数据
type FileEntry struct {
	Name    string
	IsDir   bool
	Content []byte
	Mode    os.FileMode
	ModTime time.Time
	UID     int
	GID     int
	Nlink   int
	// 新增：文件级互斥锁，用于支持高并发写入
	mu sync.RWMutex
}

// SessionFS 会话文件系统，基于 COW 技术
type SessionFS struct {
	overlay map[string]*FileEntry // 会话层修改，nil 表示已删除
	mu      sync.RWMutex
	cwd     string
}

func NewSessionFS() *SessionFS {
	return &SessionFS{
		overlay: make(map[string]*FileEntry),
		cwd:     "/root",
	}
}

// Abs 将相对路径转换为绝对路径，并处理 . 和 ..
func (fs *SessionFS) Abs(p string) string {
	if p == "" {
		return fs.cwd
	}
	if p == "~" {
		return "/root"
	}
	if strings.HasPrefix(p, "~/") {
		return path.Join("/root", p[2:])
	}
	if !strings.HasPrefix(p, "/") {
		p = path.Join(fs.cwd, p)
	}
	return path.Clean(p)
}

// GetEntry 获取文件元数据。实现 COW 逻辑：先查 Overlay，再查 BaseFS
func (fs *SessionFS) GetEntry(p string) (*FileEntry, bool) {
	p = path.Clean(p)

	// 1. 检查会话层 (加读锁)
	fs.mu.RLock()
	e, ok := fs.overlay[p]
	fs.mu.RUnlock()

	if ok {
		// 如果在 overlay 中存在但为 nil，说明被删除了
		if e == nil {
			return nil, false
		}
		return e, true
	}

	// 2. 检查基础层 (只读，无锁)
	if e, ok := BaseFS[p]; ok {
		return e, true
	}
	return nil, false
}

// ListDir 列出目录内容，合并 BaseFS 和 Overlay
func (fs *SessionFS) ListDir(dirPath string) ([]*FileEntry, error) {
	dirPath = path.Clean(dirPath)
	entry, ok := fs.GetEntry(dirPath)
	if !ok || !entry.IsDir {
		return nil, os.ErrNotExist
	}

	items := make(map[string]*FileEntry)

	// 1. 加载 BaseFS 中的子项 (已优化：使用缓存)
	if children, ok := BaseFSDirCache[dirPath]; ok {
		for _, e := range children {
			items[e.Name] = e
		}
	}

	// 2. 加载 Overlay 中的子项，合并/应用变更
	fs.mu.RLock()
	for p, e := range fs.overlay {
		if path.Dir(p) == dirPath && p != dirPath {
			if e == nil {
				// 条目被删除
				delete(items, path.Base(p))
			} else {
				// 条目被添加或修改
				items[e.Name] = e
			}
		}
	}
	fs.mu.RUnlock()

	// 3. 转换为切片并排序
	res := make([]*FileEntry, 0, len(items))
	for _, i := range items {
		res = append(res, i)
	}
	sort.Slice(res, func(i, j int) bool {
		return strings.ToLower(res[i].Name) < strings.ToLower(res[j].Name)
	})
	return res, nil
}

func (fs *SessionFS) Write(p string, data []byte, mode os.FileMode) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if len(data) > MaxFileSize {
		return errors.New("超出磁盘限额")
	}
	p = path.Clean(p)

	if existing, ok := fs.overlay[p]; ok && existing != nil {
		existing.mu.Lock()
		existing.Content = data
		existing.ModTime = time.Now()
		if mode != 0 {
			existing.Mode = mode
		}
		existing.mu.Unlock()
	} else {
		// 从 BaseFS 继承属性或创建新属性
		base, ok := BaseFS[p]
		uid, gid := 0, 0
		if ok {
			uid, gid = base.UID, base.GID
		}
		if mode == 0 {
			if ok {
				mode = base.Mode
			} else {
				mode = 0644
			}
		}
		fs.overlay[p] = &FileEntry{
			Name:    path.Base(p),
			IsDir:   false,
			Content: data,
			Mode:    mode,
			ModTime: time.Now(),
			UID:     uid,
			GID:     gid,
			Nlink:   1,
		}
	}
	return nil
}

func (fs *SessionFS) Mkdir(p string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p = path.Clean(p)
	fs.overlay[p] = &FileEntry{
		Name:    path.Base(p),
		IsDir:   true,
		Mode:    0755 | os.ModeDir,
		ModTime: time.Now(),
		UID:     0, GID: 0, Nlink: 2,
	}
	return nil
}

func (fs *SessionFS) Remove(p string) error {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	p = path.Clean(p)
	fs.overlay[p] = nil
	return nil
}

func (fs *SessionFS) Rename(oldP, newP string) error {
	e, ok := fs.GetEntry(oldP)
	if !ok {
		return os.ErrNotExist
	}

	// 注意：这里我们做了一个浅拷贝，这在重命名时通常是可以的，
	// 但如果之后修改 newEntry.Content，因为是切片引用，可能会影响旧的（如果旧的还存在）。
	// 在本系统中，旧的被标记为 nil (删除)，所以没问题。
	newEntry := *e
	newEntry.Name = path.Base(newP)
	newEntry.ModTime = time.Now()
	// 重置锁状态 (复制 sync.Mutex 是不安全的，必须重置)
	newEntry.mu = sync.RWMutex{}

	fs.mu.Lock()
	fs.overlay[newP] = &newEntry
	fs.overlay[oldP] = nil
	fs.mu.Unlock()
	return nil
}

func (fs *SessionFS) Chmod(p string, mode os.FileMode) error {
	e, ok := fs.GetEntry(p)
	if !ok {
		return os.ErrNotExist
	}

	// 如果是 BaseFS 的文件，需要先 COW 到 Overlay
	fs.mu.Lock()
	defer fs.mu.Unlock()

	// 再次检查 Overlay，防止并发竞态
	if existing, ok := fs.overlay[p]; ok && existing != nil {
		existing.mu.Lock()
		existing.Mode = (existing.Mode &^ 0777) | (mode & 0777)
		existing.ModTime = time.Now()
		existing.mu.Unlock()
	} else {
		// 从 BaseFS 复制
		newEntry := *e
		newEntry.Mode = (newEntry.Mode &^ 0777) | (mode & 0777)
		newEntry.ModTime = time.Now()
		// 重置锁
		newEntry.mu = sync.RWMutex{}
		fs.overlay[p] = &newEntry
	}
	return nil
}

func (fs *SessionFS) Chown(p string, uid, gid int) error {
	e, ok := fs.GetEntry(p)
	if !ok {
		return os.ErrNotExist
	}

	fs.mu.Lock()
	defer fs.mu.Unlock()

	if existing, ok := fs.overlay[p]; ok && existing != nil {
		existing.mu.Lock()
		if uid != -1 {
			existing.UID = uid
		}
		if gid != -1 {
			existing.GID = gid
		}
		existing.ModTime = time.Now()
		existing.mu.Unlock()
	} else {
		newEntry := *e
		if uid != -1 {
			newEntry.UID = uid
		}
		if gid != -1 {
			newEntry.GID = gid
		}
		newEntry.ModTime = time.Now()
		newEntry.mu = sync.RWMutex{}
		fs.overlay[p] = &newEntry
	}
	return nil
}
