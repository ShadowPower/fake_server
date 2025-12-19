package main

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/rand"
	"time"
)

// ==========================================
// KERNEL PANIC: 内核恐慌
// ==========================================

// --- 配置常量 ---
const (
	TargetFPS = 15 // 目标帧率 (SSH环境限制)
	FrameTime = time.Second / TargetFPS

	// ANSI 颜色代码
	ColReset  = "\033[0m"
	ColWhite  = "\033[1;37m"
	ColGray   = "\033[0;37m"
	ColRed    = "\033[1;31m"
	ColGreen  = "\033[1;32m"
	ColYellow = "\033[1;33m"
	ColBlue   = "\033[1;34m"
	ColPurple = "\033[1;35m"
	ColCyan   = "\033[1;36m"

	// 按键映射
	KeyNone  = 0
	KeyUp    = 1001
	KeyDown  = 1002
	KeyLeft  = 1003
	KeyRight = 1004
	KeyEnter = 13
	KeyEsc   = 27
	KeySpace = 32

	// 游戏符号
	SymPlayer = '@'
	SymBullet = '•'
	SymXP     = '✦' // 经验值符号
	SymHeart  = '♥'
	SymEnemy1 = 'x' // 僵尸进程
	SymEnemy2 = 'T' // 特洛伊
	SymEnemy3 = '#' // Rootkit

	CharPlaceholder = 0 // 宽字符占位符 (防止渲染重叠)
)

// --- 数据结构 ---

// Vec2 二维向量
type Vec2 struct {
	X, Y float64
}

// Entity 游戏实体 (玩家、敌人、子弹、粒子)
type Entity struct {
	ID        int
	Pos       Vec2
	Vel       Vec2
	Char      rune
	Color     string
	HP        float64
	MaxHP     float64
	Type      int // 0: Player, 1: Enemy, 2: Bullet, 3: Particle
	EnemyType int
	Damage    float64
	Lifetime  float64
	FlashTime float64 // 受伤闪烁计时
	Dead      bool
}

// Upgrade 升级选项定义
type Upgrade struct {
	ID          string
	Name        string
	Description string
	Rarity      int // 0: 普通, 1: 稀有
	Apply       func(g *Game)
}

// Game 游戏主状态机
type Game struct {
	Width, Height int
	State         int // 0:Menu, 1:Playing, 2:LevelUp, 3:GameOver, 4:Help
	TimeAlive     time.Duration
	FrameCount    int64
	Quit          bool

	// 输入处理
	InputBuffer chan byte
	InputState  int // ANSI 序列解析状态
	EscTimer    float64

	// 菜单状态
	MenuIdx int

	// 玩家数据
	Player      *Entity
	XP          int
	Level       int
	NextLevelXP int
	Stats       struct {
		MoveSpeed   float64
		PickupRange float64
		FireRateMod float64
		DamageMod   float64
		MaxHPMod    float64
		ReflectDmg  float64 // 反伤
		MultiShot   int     // 分裂箭数量
	}

	// 战斗系统
	Weapons      map[string]int
	WeaponTimers map[string]float64

	// 实体池
	Enemies   []*Entity
	Bullets   []*Entity
	Particles []*Entity

	// 游戏循环控制
	SpawnTimer float64
	Difficulty float64

	// 升级系统
	PendingUpgrades []Upgrade
	SelectorIdx     int

	// 双缓冲渲染
	FrontBuffer [][]Cell
	BackBuffer  [][]Cell
}

// Cell 屏幕单元格
type Cell struct {
	Char  rune
	Color string
}

// --- 核心入口 ---

// RunKernelPanic 启动游戏主循环
func RunKernelPanic(out io.Writer, in io.Reader, sizeFunc func() (int, int)) {
	rand.Seed(time.Now().UnixNano())

	w, h := sizeFunc()
	// 设置最小分辨率保护
	if w < 60 {
		w = 80
	}
	if h < 24 {
		h = 24
	}

	g := &Game{
		Width: w, Height: h,
		State:       0,
		InputBuffer: make(chan byte, 128),
		Weapons:     make(map[string]int),
		FrontBuffer: initBuffer(w, h),
		BackBuffer:  initBuffer(w, h),
	}
	g.ResetGame()

	// 独立的输入读取协程
	go func() {
		buf := make([]byte, 1)
		for {
			n, err := in.Read(buf)
			if err != nil || n == 0 {
				close(g.InputBuffer)
				return
			}
			g.InputBuffer <- buf[0]
		}
	}()

	// 初始化屏幕
	out.Write([]byte("\033[?25l\033[2J"))                // 隐藏光标，清屏
	defer out.Write([]byte("\033[?25h\033[0m\n\033[2J")) // 恢复光标

	ticker := time.NewTicker(FrameTime)
	defer ticker.Stop()

	// 游戏循环
	for !g.Quit {
		select {
		case <-ticker.C:
			// 响应终端尺寸变化
			nw, nh := sizeFunc()
			if nw != g.Width || nh != g.Height {
				g.Resize(nw, nh)
				out.Write([]byte("\033[2J"))
			}

			g.ProcessInput()
			g.Update()
			g.Render(out)
		}
	}
}

// --- 输入处理 (增加鲁棒性) ---

// ParseKey 解析原始字节流，识别 ANSI 转义序列
func (g *Game) ParseKey(b byte) int {
	if g.InputState == 0 {
		if b == 27 { // ESC
			g.InputState = 1
			return KeyNone
		}
		if b == 13 || b == 10 {
			return KeyEnter
		}
		switch b {
		case 32:
			return KeySpace
		case 'w', 'W':
			return KeyUp
		case 's', 'S':
			return KeyDown
		case 'a', 'A':
			return KeyLeft
		case 'd', 'D':
			return KeyRight
		case 'q', 'Q':
			return 'q'
		case 'p', 'P':
			return 'p'
		}
		return int(b)
	}

	if g.InputState == 1 {
		if b == '[' || b == 'O' { // Handle both CSI and SS3
			g.InputState = 2
			return KeyNone
		}
		g.InputState = 0
		return int(b)
	}

	if g.InputState == 2 {
		g.InputState = 0
		switch b {
		case 'A':
			return KeyUp
		case 'B':
			return KeyDown
		case 'C':
			return KeyRight
		case 'D':
			return KeyLeft
		}
		return int(b)
	}

	return KeyNone
}

// HandleKey 处理游戏逻辑按键
func (g *Game) HandleKey(key int) {
	if key == 3 { // Ctrl+C
		g.Quit = true
		return
	}

	switch g.State {
	case 0: // Menu
		switch key {
		case KeyUp:
			g.MenuIdx--
			if g.MenuIdx < 0 {
				g.MenuIdx = 2
			}
		case KeyDown:
			g.MenuIdx++
			if g.MenuIdx > 2 {
				g.MenuIdx = 0
			}
		case KeyEnter, KeySpace:
			if g.MenuIdx == 0 {
				g.State = 1 // Start Game
				g.ResetGame()
			} else if g.MenuIdx == 1 {
				g.State = 4 // Help
			} else {
				g.Quit = true // Exit
			}
		case 'q', KeyEsc:
			g.Quit = true
		}

	case 4: // Help
		if key == KeyEnter || key == KeySpace || key == KeyEsc || key == 'q' {
			g.State = 0
		}

	case 1: // Playing
		switch key {
		case KeyUp:
			g.Player.Vel = Vec2{0, -g.Stats.MoveSpeed}
		case KeyDown:
			g.Player.Vel = Vec2{0, g.Stats.MoveSpeed}
		case KeyLeft:
			g.Player.Vel = Vec2{-g.Stats.MoveSpeed * 1.5, 0}
		case KeyRight:
			g.Player.Vel = Vec2{g.Stats.MoveSpeed * 1.5, 0}
		case KeySpace:
			g.Player.Vel = Vec2{0, 0} // 刹车
		case 'p', KeyEsc:
			g.State = 0 // 暂停回菜单
		}

	case 2: // LevelUp
		switch key {
		case KeyUp, KeyLeft:
			g.SelectorIdx--
			if g.SelectorIdx < 0 {
				g.SelectorIdx = len(g.PendingUpgrades) - 1
			}
		case KeyDown, KeyRight:
			g.SelectorIdx++
			if g.SelectorIdx >= len(g.PendingUpgrades) {
				g.SelectorIdx = 0
			}
		case KeyEnter, KeySpace:
			if len(g.PendingUpgrades) > 0 {
				g.PendingUpgrades[g.SelectorIdx].Apply(g)
				g.State = 1 // Resume
			}
		}

	case 3: // GameOver
		if key == KeyEnter || key == KeySpace || key == 'q' {
			g.State = 0
		}
	}
}

func (g *Game) ProcessInput() {
Loop:
	for {
		select {
		case b, ok := <-g.InputBuffer:
			if !ok {
				g.Quit = true
				return
			}
			key := g.ParseKey(b)
			if key != KeyNone {
				g.HandleKey(key)
			}
		default:
			break Loop
		}
	}
}

// --- 游戏逻辑 ---

func (g *Game) Update() {
	dt := float64(FrameTime.Seconds())

	// ESC 键去抖/超时判断 (处理单独按下 ESC 的情况)
	if g.InputState > 0 {
		g.EscTimer += dt
		if g.EscTimer > 0.1 {
			g.InputState = 0
			g.EscTimer = 0
			g.HandleKey(KeyEsc)
		}
	} else {
		g.EscTimer = 0
	}

	if g.State != 1 {
		return
	} // 仅在游戏进行时更新

	g.TimeAlive += FrameTime
	g.FrameCount++

	// 玩家移动
	g.Player.Pos = Add(g.Player.Pos, g.Player.Vel)
	if g.Player.FlashTime > 0 {
		g.Player.FlashTime -= dt
		if g.Player.FlashTime < 0 {
			g.Player.FlashTime = 0
		}
	}

	// 核心子系统更新
	g.UpdateWeapons(dt)
	g.UpdateEnemies(dt)
	g.UpdateBullets(dt)
	g.UpdateParticles(dt)

	// 升级检查
	if g.XP >= g.NextLevelXP {
		g.LevelUp()
	}

	// 死亡检查
	if g.Player.HP <= 0 {
		g.State = 3
	}
}

func (g *Game) ResetGame() {
	g.TimeAlive = 0
	g.XP = 0
	g.Level = 1
	g.NextLevelXP = 15
	g.Enemies = nil
	g.Bullets = nil
	g.Particles = nil
	g.Weapons = make(map[string]int)
	g.WeaponTimers = make(map[string]float64)
	g.Difficulty = 1.0
	g.SpawnTimer = 0

	// 默认属性
	g.Stats.MoveSpeed = 1.0
	g.Stats.PickupRange = 6.0
	g.Stats.FireRateMod = 1.0
	g.Stats.DamageMod = 1.0
	g.Stats.MaxHPMod = 1.0
	g.Stats.ReflectDmg = 0.0
	g.Stats.MultiShot = 0

	g.Weapons["PING"] = 1

	g.Player = &Entity{
		Pos:   Vec2{X: 0, Y: 0},
		Char:  SymPlayer,
		Color: ColCyan,
		HP:    100,
		MaxHP: 100,
		Type:  0,
	}
}

// UpdateEnemies 敌人生成与AI
func (g *Game) UpdateEnemies(dt float64) {
	g.SpawnTimer += dt
	spawnRate := 1.8 / g.Difficulty
	if g.SpawnTimer >= spawnRate {
		g.SpawnTimer = 0
		g.Difficulty += 0.05

		// 随机位置生成
		angle := rand.Float64() * math.Pi * 2
		dist := 35.0 + rand.Float64()*10.0
		pos := Add(g.Player.Pos, Vec2{math.Cos(angle) * dist * 1.5, math.Sin(angle) * dist})

		r := rand.Float64()
		var e *Entity
		if r < 0.6 {
			// 僵尸进程
			e = &Entity{Pos: pos, Char: SymEnemy1, Color: ColGreen, HP: 15 * g.Difficulty, Damage: 5, EnemyType: 0, MaxHP: 15 * g.Difficulty}
		} else if r < 0.85 {
			// 特洛伊
			e = &Entity{Pos: pos, Char: SymEnemy2, Color: ColPurple, HP: 10 * g.Difficulty, Damage: 8, EnemyType: 1, MaxHP: 10 * g.Difficulty}
		} else {
			// Rootkit
			e = &Entity{Pos: pos, Char: SymEnemy3, Color: ColRed, HP: 40 * g.Difficulty, Damage: 10, EnemyType: 2, MaxHP: 40 * g.Difficulty}
		}
		e.Type = 1
		g.Enemies = append(g.Enemies, e)
	}

	for _, e := range g.Enemies {
		if e.Dead {
			continue
		}
		if e.FlashTime > 0 {
			e.FlashTime -= dt
		}

		// 简单的追踪 AI
		dir := Sub(g.Player.Pos, e.Pos).Normalize()
		speed := 0.0

		switch e.EnemyType {
		case 0:
			speed = 0.35 // 普通
		case 1:
			if int(g.FrameCount/5)%3 != 0 {
				speed = 0.7
			} // 突进
		case 2:
			speed = 0.15 // 坦克
			// Rootkit 发射子弹
			if int(g.FrameCount)%30 == 0 && rand.Float64() < 0.1 {
				g.SpawnBullet(e.Pos, dir, 0.6, 5*g.Difficulty, 4)
			}
		}

		// 敌人之间的斥力
		for _, other := range g.Enemies {
			if e != other && !other.Dead {
				d := Dist(e.Pos, other.Pos)
				if d < 1.5 {
					push := Sub(e.Pos, other.Pos).Normalize()
					e.Pos = Add(e.Pos, Mul(push, 0.05))
				}
			}
		}

		e.Pos = Add(e.Pos, Mul(dir, speed))

		// 碰撞玩家
		if Dist(e.Pos, g.Player.Pos) < 1.5 {
			g.HitPlayer(e.Damage)
			e.Pos = Sub(e.Pos, Mul(dir, 4.0)) // 击退敌人

			// 触发反伤
			if g.Stats.ReflectDmg > 0 {
				e.HP -= g.Stats.ReflectDmg
				e.FlashTime = 0.2
				g.SpawnText(fmt.Sprintf("-%.0f", g.Stats.ReflectDmg), e.Pos, ColYellow)
				if e.HP <= 0 {
					e.Dead = true
				}
			}
		}
	}
}

// UpdateBullets 子弹移动与碰撞
func (g *Game) UpdateBullets(dt float64) {
	for _, b := range g.Bullets {
		if b.Dead {
			continue
		}
		b.Lifetime -= dt
		if b.Lifetime <= 0 {
			b.Dead = true
			continue
		}

		b.Pos = Add(b.Pos, b.Vel)

		// 敌方子弹 (Type 4)
		if b.Type == 4 {
			if Dist(b.Pos, g.Player.Pos) < 1.0 {
				g.HitPlayer(b.Damage)
				b.Dead = true
			}
			continue
		}

		// 玩家子弹
		for _, e := range g.Enemies {
			if e.Dead {
				continue
			}
			if Dist(b.Pos, e.Pos) < 1.5 {
				e.HP -= b.Damage
				e.FlashTime = 0.1

				push := b.Vel.Normalize()
				e.Pos = Add(e.Pos, Mul(push, 0.4))

				if e.HP <= 0 {
					e.Dead = true
					// 掉落经验
					val := 1
					if e.EnemyType == 2 {
						val = 5
					}
					for i := 0; i < val; i++ {
						off := Vec2{(rand.Float64() - 0.5) * 2, (rand.Float64() - 0.5) * 2}
						g.Particles = append(g.Particles, &Entity{
							Pos: Add(e.Pos, off), Char: SymXP, Color: ColCyan, Type: 3, Lifetime: 30.0,
						})
					}
					g.SpawnText("KILL", e.Pos, ColRed)
				} else {
					g.SpawnText(fmt.Sprintf("%.0f", b.Damage), e.Pos, ColWhite)
				}

				if b.EnemyType != 99 {
					b.Dead = true
				} // 99 为穿透
				break
			}
		}
	}
}

// UpdateWeapons 武器冷却与发射
func (g *Game) UpdateWeapons(dt float64) {
	for name, lvl := range g.Weapons {
		g.WeaponTimers[name] += dt

		if name == "PING" {
			cd := 0.7 / g.Stats.FireRateMod
			if g.WeaponTimers[name] >= cd {
				g.WeaponTimers[name] = 0
				target := g.FindNearestEnemy()
				if target != nil {
					dir := Sub(target.Pos, g.Player.Pos).Normalize()
					dmg := (10 + float64(lvl)*5) * g.Stats.DamageMod
					// 主炮
					g.SpawnBullet(g.Player.Pos, dir, 2.0, dmg, 0)
					// 分裂箭
					if g.Stats.MultiShot > 0 {
						for i := 1; i <= g.Stats.MultiShot; i++ {
							ang := float64(i) * 0.2
							g.SpawnBullet(g.Player.Pos, Rotate(dir, ang), 2.0, dmg*0.6, 0)
							g.SpawnBullet(g.Player.Pos, Rotate(dir, -ang), 2.0, dmg*0.6, 0)
						}
					}
				}
			}
		} else if name == "GREP" {
			cd := 2.0 / g.Stats.FireRateMod
			if g.WeaponTimers[name] >= cd {
				g.WeaponTimers[name] = 0
				target := g.FindNearestEnemy()
				if target != nil {
					dir := Sub(target.Pos, g.Player.Pos).Normalize()
					dmg := (8 + float64(lvl)*3) * g.Stats.DamageMod
					for i := 0; i < 3; i++ {
						start := Add(g.Player.Pos, Mul(dir, float64(i)*0.8))
						g.SpawnBullet(start, dir, 3.0, dmg, 99)
					}
				}
			}
		} else if name == "SUDO" {
			if g.WeaponTimers[name] >= 0.05 {
				g.WeaponTimers[name] = 0
				count := 2 + lvl
				baseAng := float64(g.FrameCount) * 0.2
				dmg := (5 + float64(lvl)*2) * g.Stats.DamageMod
				for i := 0; i < count; i++ {
					ang := baseAng + (math.Pi*2/float64(count))*float64(i)
					offset := Vec2{math.Cos(ang) * 6, math.Sin(ang) * 4}
					pos := Add(g.Player.Pos, offset)
					g.Bullets = append(g.Bullets, &Entity{
						Pos: pos, Char: 'S', Color: ColYellow, Type: 2, Damage: dmg, Lifetime: 0.1, EnemyType: 99,
					})
				}
			}
		}
	}
}

// UpdateParticles 粒子更新 (经验拾取)
func (g *Game) UpdateParticles(dt float64) {
	for _, p := range g.Particles {
		if p.Dead {
			continue
		}
		p.Lifetime -= dt
		if p.Lifetime <= 0 {
			p.Dead = true
			continue
		}

		if p.Char == SymXP {
			dist := Dist(p.Pos, g.Player.Pos)
			if dist < g.Stats.PickupRange {
				dir := Sub(g.Player.Pos, p.Pos).Normalize()
				spd := (g.Stats.PickupRange - dist + 1) * 0.8
				p.Pos = Add(p.Pos, Mul(dir, spd))
				if dist < 1.0 {
					p.Dead = true
					g.XP++
				}
			}
		} else if p.Char == 0 {
			p.Pos.Y -= 0.1 // 文字上浮
		}
	}
	g.CleanupEntities()
}

func (g *Game) HitPlayer(dmg float64) {
	if g.Player.FlashTime > 0 {
		return
	}
	g.Player.HP -= dmg
	g.Player.FlashTime = 0.5
	g.SpawnText("警告", g.Player.Pos, ColRed)
}

func (g *Game) LevelUp() {
	g.Level++
	g.XP -= g.NextLevelXP
	g.NextLevelXP = int(float64(g.NextLevelXP) * 1.3)
	g.State = 2

	// 生成升级选项池
	pool := []Upgrade{
		{"PING", "PING 指令 +1", "发射频率大幅增加", 0, func(g *Game) { g.Weapons["PING"]++ }},
		{"GREP", "GREP 射线", "获得可穿透的激光武器", 1, func(g *Game) { g.Weapons["GREP"]++ }},
		{"SUDO", "SUDO 护盾", "获得环绕自身的防御层", 1, func(g *Game) { g.Weapons["SUDO"]++ }},
		{"SPD", "总线超频", "移动速度 +20%", 0, func(g *Game) { g.Stats.MoveSpeed *= 1.2 }},
		{"HP", "内核补丁", "生命上限 +20% 并回血", 0, func(g *Game) {
			g.Stats.MaxHPMod *= 1.2
			g.Player.MaxHP = 100 * g.Stats.MaxHPMod
			g.Player.HP += 30
			if g.Player.HP > g.Player.MaxHP {
				g.Player.HP = g.Player.MaxHP
			}
		}},
		{"RANGE", "缓存命中", "经验拾取范围 +50%", 0, func(g *Game) { g.Stats.PickupRange *= 1.5 }},
		{"DMG", "高压电源", "所有武器伤害 +15%", 0, func(g *Game) { g.Stats.DamageMod *= 1.15 }},
		{"FW", "防火墙", "受到伤害时反弹 5 点伤害", 1, func(g *Game) { g.Stats.ReflectDmg += 5 }},
		{"LB", "负载均衡", "PING 指令增加额外分裂子弹", 1, func(g *Game) { g.Stats.MultiShot++ }},
	}
	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	g.PendingUpgrades = pool[:3]
	g.SelectorIdx = 0
}

// --- 渲染系统 ---

func (g *Game) Render(out io.Writer) {
	g.ClearBuffer(g.FrontBuffer)
	cx, cy := g.Width/2, g.Height/2

	// 世界坐标转屏幕坐标
	toScreen := func(v Vec2) (int, int, bool) {
		sx := int(v.X-g.Player.Pos.X) + cx
		sy := int((v.Y-g.Player.Pos.Y)*0.6) + cy
		if sx >= 0 && sx < g.Width && sy >= 0 && sy < g.Height {
			return sx, sy, true
		}
		return 0, 0, false
	}

	// 渲染游戏场景
	if g.State == 1 || g.State == 2 || g.State == 3 {
		// 粒子
		for _, p := range g.Particles {
			if p.Char != 0 {
				if sx, sy, ok := toScreen(p.Pos); ok {
					g.DrawCell(sx, sy, p.Char, p.Color)
				}
			}
		}
		// 敌人
		for _, e := range g.Enemies {
			if sx, sy, ok := toScreen(e.Pos); ok {
				col := e.Color
				if e.FlashTime > 0 {
					col = ColWhite
				}
				g.DrawCell(sx, sy, e.Char, col)
			}
		}
		// 子弹
		for _, b := range g.Bullets {
			if sx, sy, ok := toScreen(b.Pos); ok {
				g.DrawCell(sx, sy, b.Char, b.Color)
			}
		}

		// 玩家
		col := g.Player.Color
		if g.Player.FlashTime > 0 {
			col = ColRed
		}
		g.DrawCell(cx, cy, g.Player.Char, col)

		g.DrawHUD()
	}

	// 渲染 UI 层 (覆盖在场景之上)
	switch g.State {
	case 0:
		g.DrawMenu()
	case 2:
		g.DrawLevelUp()
	case 3:
		g.DrawGameOver()
	case 4:
		g.DrawHelp()
	}

	// 差异输出到终端 (双缓冲)
	var buf bytes.Buffer
	for y := 0; y < g.Height; y++ {
		for x := 0; x < g.Width; x++ {
			f := g.FrontBuffer[y][x]
			b := g.BackBuffer[y][x]

			// 占位符跳过
			if f.Char == CharPlaceholder {
				g.BackBuffer[y][x] = f
				continue
			}

			if f != b {
				// 移动光标并输出
				buf.WriteString(fmt.Sprintf("\033[%d;%dH%s%c", y+1, x+1, f.Color, f.Char))
				g.BackBuffer[y][x] = f

				// 处理宽字符占位
				if isWide(f.Char) && x+1 < g.Width {
					g.BackBuffer[y][x+1] = Cell{Char: CharPlaceholder, Color: f.Color}
				}
			}
		}
	}
	if buf.Len() > 0 {
		out.Write(buf.Bytes())
	}
}

// --- UI 组件 ---

func (g *Game) DrawMenu() {
	logo := []string{
		` _  __                    _   ____             _      `,
		`| |/ /___ _ __ _ __   ___| | |  _ \ __ _ _ __ (_) ___ `,
		`| ' // _ \ '__| '_ \ / _ \ | | |_) / _' | '_ \| |/ __|`,
		`| . \  __/ |  | | | |  __/ | |  __/ (_| | | | | | (__ `,
		`|_|\_\___|_|  |_| |_|\___|_| |_|   \__,_|_| |_|_|\___|`,
	}
	logoY := g.Height/2 - 8
	for i, line := range logo {
		g.CenterText(logoY+i, line, ColRed)
	}
	g.CenterText(logoY+6, "VER 2.0 (SSH EDITION)", ColYellow)

	opts := []string{"[ 启动防御系统 ]", "[ 系统操作手册 ]", "[ 断 开 连 接 ]"}
	menuY := g.Height/2 + 2
	for i, opt := range opts {
		col := ColGray
		pre := "  "
		if i == g.MenuIdx {
			col = ColWhite
			pre = "> "
		}
		g.CenterText(menuY+i*2, pre+opt, col)
	}

	g.CenterText(g.Height-2, "使用 [↑/↓] 选择, [回车] 确认", ColGray)
}

func (g *Game) DrawHelp() {
	g.DrawBox(4, 3, g.Width-8, g.Height-6, ColGreen)
	g.CenterText(5, "== 操作手册 ==", ColGreen)
	lines := []string{
		" ",
		"移动控制:",
		"  [W/A/S/D] 或 [方向键] 移动",
		"  [空格] 紧急刹车 (防滑)",
		" ",
		"战斗系统:",
		"  自动攻击最近的目标",
		"  拾取 [✦] 升级你的内核",
		"  避免接触红色高危进程",
		" ",
		"目标: 尽可能长时间存活",
	}
	y := 7
	for _, l := range lines {
		g.DrawText(g.Width/2-15, y, l, ColWhite)
		y++
	}
	g.CenterText(g.Height-5, "按 [回车] 返回", ColWhite)
}

func (g *Game) DrawLevelUp() {
	w, h := 50, 14
	bx, by := (g.Width-w)/2, (g.Height-h)/2
	// 清除背景
	for y := by; y < by+h; y++ {
		for x := bx; x < bx+w; x++ {
			g.DrawCell(x, y, ' ', ColReset)
		}
	}
	g.DrawBox(bx, by, w, h, ColYellow)
	g.CenterText(by+1, ">> 系统升级 <<", ColYellow)

	for i, upg := range g.PendingUpgrades {
		col := ColGray
		pre := "  "
		if i == g.SelectorIdx {
			col = ColWhite
			pre = "> "
		}
		y := by + 4 + i*3
		g.DrawText(bx+4, y, pre+upg.Name, col)
		g.DrawText(bx+6, y+1, upg.Description, ColGray)
	}
}

func (g *Game) DrawGameOver() {
	w, h := 40, 10
	bx, by := (g.Width-w)/2, (g.Height-h)/2
	g.DrawBox(bx, by, w, h, ColRed)
	g.CenterText(by+2, "内核崩溃 (GAME OVER)", ColRed)
	g.CenterText(by+4, fmt.Sprintf("存活时间: %ds", int(g.TimeAlive.Seconds())), ColWhite)
	g.CenterText(by+5, fmt.Sprintf("系统等级: %d", g.Level), ColWhite)
	g.CenterText(by+7, "按 [回车] 返回主菜单", ColGray)
}

// DrawHUD 绘制详细的 HUD，包括经验条和血条
func (g *Game) DrawHUD() {
	// 1. 绘制血条 (左上角)
	hpPct := g.Player.HP / g.Player.MaxHP
	if hpPct < 0 {
		hpPct = 0
	}
	hpWidth := 15
	hpFill := int(hpPct * float64(hpWidth))

	g.DrawText(2, 1, "HP:", ColRed)
	for i := 0; i < hpWidth; i++ {
		char := '░'
		col := ColRed
		if i < hpFill {
			char = '█'
		}
		g.DrawCell(6+i, 1, char, col)
	}
	g.DrawText(6+hpWidth+2, 1, fmt.Sprintf("%.0f/%.0f", g.Player.HP, g.Player.MaxHP), ColWhite)

	// 2. 绘制等级和时间 (右上角)
	lvlStr := fmt.Sprintf("LV.%d  %02d:%02d", g.Level, int(g.TimeAlive.Minutes()), int(g.TimeAlive.Seconds())%60)
	g.DrawText(g.Width-len(lvlStr)-2, 1, lvlStr, ColYellow)

	// 3. 绘制经验条 (底部)
	xpPct := float64(g.XP) / float64(g.NextLevelXP)
	if xpPct > 1 {
		xpPct = 1
	}
	xpWidth := g.Width - 20
	xpFill := int(xpPct * float64(xpWidth))

	g.DrawText(2, g.Height-2, "XP:", ColCyan)
	for i := 0; i < xpWidth; i++ {
		char := '░'
		col := ColBlue
		if i < xpFill {
			char = '█'
			col = ColCyan
		}
		g.DrawCell(6+i, g.Height-2, char, col)
	}
	xpStr := fmt.Sprintf("%d/%d", g.XP, g.NextLevelXP)
	g.DrawText(g.Width/2-len(xpStr)/2, g.Height-2, xpStr, ColWhite)

	// 4. 底部提示
	hint := "WASD:移动 SPACE:刹车 P:菜单"
	g.DrawText(g.Width/2-len(hint)/2, g.Height-1, hint, ColGray)
}

// --- 基础绘图 ---

func isWide(r rune) bool {
	// 简单的宽字符判断，涵盖了常用的中文、符号
	return r >= 0x2E80 || r == '★' || r == '✦' || r == '♥' || r == '█' || r == '░'
}

func (g *Game) DrawBox(x, y, w, h int, col string) {
	g.DrawCell(x, y, '┌', col)
	g.DrawCell(x+w-1, y, '┐', col)
	g.DrawCell(x, y+h-1, '└', col)
	g.DrawCell(x+w-1, y+h-1, '┘', col)
	for i := 1; i < w-1; i++ {
		g.DrawCell(x+i, y, '─', col)
		g.DrawCell(x+i, y+h-1, '─', col)
	}
	for i := 1; i < h-1; i++ {
		g.DrawCell(x, y+i, '│', col)
		g.DrawCell(x+w-1, y+i, '│', col)
	}
}

func (g *Game) DrawText(x, y int, s string, col string) {
	currX := x
	for _, r := range s {
		if currX >= g.Width {
			break
		}
		g.DrawCell(currX, y, r, col)
		if isWide(r) {
			currX += 2
		} else {
			currX++
		}
	}
}

func (g *Game) CenterText(y int, s string, col string) {
	width := 0
	for _, r := range s {
		if isWide(r) {
			width += 2
		} else {
			width++
		}
	}
	x := (g.Width - width) / 2
	g.DrawText(x, y, s, col)
}

func (g *Game) DrawCell(x, y int, c rune, col string) {
	if x >= 0 && x < g.Width && y >= 0 && y < g.Height {
		g.FrontBuffer[y][x] = Cell{Char: c, Color: col}
		// 如果是宽字符，需要给右边位置设置占位符，防止渲染重叠
		if isWide(c) && x+1 < g.Width {
			g.FrontBuffer[y][x+1] = Cell{Char: CharPlaceholder, Color: col}
		}
	}
}

func (g *Game) ClearBuffer(b [][]Cell) {
	for y := 0; y < g.Height; y++ {
		for x := 0; x < g.Width; x++ {
			b[y][x] = Cell{Char: ' ', Color: ColReset}
		}
	}
}

func initBuffer(w, h int) [][]Cell {
	b := make([][]Cell, h)
	for i := range b {
		b[i] = make([]Cell, w)
	}
	return b
}

func (g *Game) Resize(w, h int) {
	g.Width = w
	g.Height = h
	g.FrontBuffer = initBuffer(w, h)
	g.BackBuffer = initBuffer(w, h)
}

// --- 辅助逻辑 ---

func (g *Game) FindNearestEnemy() *Entity {
	var target *Entity
	minD := 9999.0
	for _, e := range g.Enemies {
		if e.Dead {
			continue
		}
		d := Dist(g.Player.Pos, e.Pos)
		if d < minD {
			minD = d
			target = e
		}
	}
	return target
}

func (g *Game) SpawnBullet(pos, dir Vec2, spd, dmg float64, etype int) {
	g.Bullets = append(g.Bullets, &Entity{
		Pos: pos, Vel: Mul(dir, spd), Char: SymBullet, Color: ColYellow,
		Type: 2, Damage: dmg, Lifetime: 2.0, EnemyType: etype,
	})
}

func (g *Game) SpawnText(s string, pos Vec2, col string) {
	// 暂不实现完整的文字粒子，因为字符网格下文字粒子效果不好
}

func (g *Game) CleanupEntities() {
	clean := func(list []*Entity) []*Entity {
		n := 0
		for _, e := range list {
			if !e.Dead {
				list[n] = e
				n++
			}
		}
		return list[:n]
	}
	g.Enemies = clean(g.Enemies)
	g.Bullets = clean(g.Bullets)
	g.Particles = clean(g.Particles)
}

// 向量旋转
func Rotate(v Vec2, angle float64) Vec2 {
	cos := math.Cos(angle)
	sin := math.Sin(angle)
	return Vec2{
		X: v.X*cos - v.Y*sin,
		Y: v.X*sin + v.Y*cos,
	}
}

func Add(a, b Vec2) Vec2         { return Vec2{a.X + b.X, a.Y + b.Y} }
func Sub(a, b Vec2) Vec2         { return Vec2{a.X - b.X, a.Y - b.Y} }
func Mul(a Vec2, s float64) Vec2 { return Vec2{a.X * s, a.Y * s} }
func Dist(a, b Vec2) float64     { return math.Sqrt(math.Pow(a.X-b.X, 2) + math.Pow(a.Y-b.Y, 2)) }
func (v Vec2) Normalize() Vec2 {
	l := math.Sqrt(v.X*v.X + v.Y*v.Y)
	if l == 0 {
		return Vec2{0, 0}
	}
	return Vec2{v.X / l, v.Y / l}
}
