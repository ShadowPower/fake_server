package main

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"math/rand"
	"sort"
	"time"
)

// ==========================================
// KERNEL PANIC: 内核恐慌
// ==========================================

// --- 配置常量 ---
const (
	TargetFPS = 45 // 帧率提升到 45
	FrameTime = time.Second / TargetFPS

	// 像素密度配置 (Half-Block 模式: 1x2)
	PixelScaleX = 1
	PixelScaleY = 2

	// UI 布局常量
	TopHudHeight = 4 // 顶部信息栏高度

	// ANSI 颜色代码
	ColReset   = "\033[0m"
	ColBlack   = "\033[30m"
	ColRed     = "\033[31m"
	ColGreen   = "\033[32m"
	ColYellow  = "\033[33m"
	ColBlue    = "\033[34m"
	ColMagenta = "\033[35m"
	ColCyan    = "\033[36m"
	ColWhite   = "\033[37m"

	ColHiBlack   = "\033[90m"
	ColHiRed     = "\033[91m"
	ColHiGreen   = "\033[92m"
	ColHiYellow  = "\033[93m"
	ColHiBlue    = "\033[94m"
	ColHiMagenta = "\033[95m"
	ColHiCyan    = "\033[96m"
	ColHiWhite   = "\033[97m"

	// 按键映射
	KeyNone  = 0
	KeyUp    = 1001
	KeyDown  = 1002
	KeyLeft  = 1003
	KeyRight = 1004
	KeyEnter = 13
	KeyEsc   = 27
	KeySpace = 32

	CharPlaceholder = 0 // 宽字符占位符
)

// --- 基础数据结构 ---

type Vec2 struct {
	X, Y float64
}

// Entity 游戏实体
type Entity struct {
	ID        int // 唯一标识符
	Pos       Vec2
	Vel       Vec2
	Color     string
	HP        float64
	MaxHP     float64
	Radius    float64
	Type      int // 0:Player, 1:Enemy, 2:Bullet, 3:Particle, 4:Text
	SubType   int // 敌人类型/武器ID标识
	TargetID  int // 锁定的目标ID (用于追踪/激光)
	Pierce    int // 穿透次数
	Damage    float64
	Knockback float64
	Lifetime  float64
	MaxLife   float64
	FlashTime float64
	Text      string // 飘字内容
	Dead      bool
	Angle     float64 // 旋转角度
	ExtraData float64 // 通用额外数据(如回旋镖状态)
}

// WeaponDef 武器定义
type WeaponDef struct {
	ID          string
	Name        string
	Description string
	Type        int     // 0:投射物, 1:激光(锁定), 2:护盾(环绕), 3:区域, 4:地雷, 5:回旋, 6:后射, 7:追踪
	Cooldown    float64 // 基础冷却
	Damage      float64
	Speed       float64
	Count       int     // 投射物数量
	Spread      float64 // 散射角度
	Pierce      int     // 穿透数
	Color       string
	Knockback   float64
	Duration    float64 // 持续时间
}

// Upgrade 升级选项
type Upgrade struct {
	ID          string
	Name        string
	Description string
	Rarity      int // 0:普通, 1:稀有, 2:传说
	Apply       func(g *Game)
}

// Game 游戏主状态机
type Game struct {
	TermW, TermH   int
	PixelW, PixelH int
	State          int // 0:Menu, 1:Playing, 2:LevelUp, 3:GameOver, 4:Help, 5:Pause
	TimeAlive      time.Duration
	FrameCount     int64
	Quit           bool

	InputBuffer chan byte
	InputState  int
	EscTimer    float64

	MenuIdx int

	// 核心数据
	Player       *Entity
	XP           int
	Level        int
	NextLevelXP  int
	NextEntityID int // 全局实体ID计数器

	// 属性统计
	Stats struct {
		MoveSpeed   float64
		PickupRange float64
		FireRateMod float64 // 值越小越快
		DamageMod   float64
		MaxHPMod    float64
		ReflectDmg  float64
		BulletSpeed float64
		Luck        float64
		Regen       float64 // 新增：回血
		DamageRed   float64 // 新增：减伤
	}

	// 武器库
	Weapons      map[string]int
	WeaponTimers map[string]float64
	WeaponDefs   map[string]WeaponDef

	// 实体池
	Enemies   []*Entity
	Bullets   []*Entity
	Particles []*Entity
	Texts     []*Entity

	// 游戏循环控制
	SpawnTimer float64
	RegenTimer float64 // 回血计时器
	Difficulty float64
	Wave       int

	// 升级系统
	PendingUpgrades []Upgrade
	SelectorIdx     int

	// 渲染缓冲
	Canvas      *Canvas
	FrontBuffer [][]Cell
	BackBuffer  [][]Cell
}

// Cell 终端单元格
type Cell struct {
	Char  rune
	Color string
}

// Canvas 像素画布
type Canvas struct {
	Width, Height int
	Pixels        []bool
	Colors        []string
}

func NewCanvas(w, h int) *Canvas {
	return &Canvas{
		Width:  w,
		Height: h,
		Pixels: make([]bool, w*h),
		Colors: make([]string, w*h),
	}
}

func (c *Canvas) Clear() {
	for i := range c.Pixels {
		c.Pixels[i] = false
		c.Colors[i] = ""
	}
}

func (c *Canvas) SetPixel(x, y int, col string) {
	if x < 0 || x >= c.Width || y < 0 || y >= c.Height {
		return
	}
	idx := y*c.Width + x
	c.Pixels[idx] = true
	c.Colors[idx] = col
}

func (c *Canvas) DrawLine(x0, y0, x1, y1 int, col string) {
	dx := int(math.Abs(float64(x1 - x0)))
	dy := -int(math.Abs(float64(y1 - y0)))
	sx := 1
	if x0 >= x1 {
		sx = -1
	}
	sy := 1
	if y0 >= y1 {
		sy = -1
	}
	err := dx + dy
	for {
		c.SetPixel(x0, y0, col)
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func (c *Canvas) DrawCircle(xc, yc, r int, col string) {
	x := 0
	y := r
	d := 3 - 2*r
	c.drawCirclePixels(xc, yc, x, y, col)
	for y >= x {
		x++
		if d > 0 {
			y--
			d = d + 4*(x-y) + 10
		} else {
			d = d + 4*x + 6
		}
		c.drawCirclePixels(xc, yc, x, y, col)
	}
}

func (c *Canvas) drawCirclePixels(xc, yc, x, y int, col string) {
	c.SetPixel(xc+x, yc+y, col)
	c.SetPixel(xc-x, yc+y, col)
	c.SetPixel(xc+x, yc-y, col)
	c.SetPixel(xc-x, yc-y, col)
	c.SetPixel(xc+y, yc+x, col)
	c.SetPixel(xc-y, yc+x, col)
	c.SetPixel(xc+y, yc-x, col)
	c.SetPixel(xc-y, yc-x, col)
}

// --- 核心入口 ---

func RunKernelPanic(out io.Writer, in io.Reader, sizeFunc func() (int, int)) {
	rand.Seed(time.Now().UnixNano())

	w, h := sizeFunc()
	if w < 80 {
		w = 80
	}
	if h < 24 {
		h = 24
	}

	g := &Game{
		TermW: w, TermH: h,
		PixelW: w * PixelScaleX, PixelH: h * PixelScaleY,
		State:        0,
		InputBuffer:  make(chan byte, 128),
		FrontBuffer:  initBuffer(w, h),
		BackBuffer:   initBuffer(w, h),
		Canvas:       NewCanvas(w*PixelScaleX, h*PixelScaleY),
		WeaponDefs:   initWeaponDefs(),
		NextEntityID: 1,
	}
	g.ResetGame()

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

	out.Write([]byte("\033[?25l\033[2J"))
	defer out.Write([]byte("\033[?25h\033[0m\n\033[2J"))

	ticker := time.NewTicker(FrameTime)
	defer ticker.Stop()

	for !g.Quit {
		select {
		case <-ticker.C:
			nw, nh := sizeFunc()
			if nw != g.TermW || nh != g.TermH {
				g.Resize(nw, nh)
				out.Write([]byte("\033[2J"))
			}

			g.ProcessInput()
			g.Update()
			g.Render(out)
		}
	}
}

// --- 输入处理 ---

func (g *Game) ParseKey(b byte) int {
	if g.InputState == 0 {
		if b == 27 {
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
	} else if g.InputState == 1 {
		if b == '[' || b == 'O' {
			g.InputState = 2
			return KeyNone
		}
		g.InputState = 0
		return int(b)
	} else if g.InputState == 2 {
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
	}
	return KeyNone
}

func (g *Game) HandleKey(key int) {
	if key == 3 {
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
				g.State = 1
				g.ResetGame()
			} else if g.MenuIdx == 1 {
				g.State = 4
			} else {
				g.Quit = true
			}
		case 'q', KeyEsc:
			g.Quit = true
		}
	case 4: // Help
		if key == KeyEnter || key == KeySpace || key == KeyEsc || key == 'q' {
			g.State = 0
		}
	case 1: // Playing
		speed := g.Stats.MoveSpeed
		switch key {
		case KeyUp:
			g.Player.Vel = Vec2{0, -speed}
		case KeyDown:
			g.Player.Vel = Vec2{0, speed}
		case KeyLeft:
			g.Player.Vel = Vec2{-speed, 0}
		case KeyRight:
			g.Player.Vel = Vec2{speed, 0}
		case KeySpace:
			g.Player.Vel = Vec2{0, 0}
		case 'p', KeyEsc:
			g.State = 5
		}
	case 5: // Pause
		if key == 'p' || key == KeyEsc || key == KeyEnter {
			g.State = 1
		} else if key == 'q' {
			g.State = 0
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
				g.State = 1
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
	}

	g.TimeAlive += FrameTime
	g.FrameCount++

	// 难度基于时间线性增长 (每分钟 +1.0)
	// 取代原先的基于Spawn次数的增长，防止难度失控
	g.Difficulty = 1.0 + g.TimeAlive.Seconds()/60.0

	g.Player.Pos = Add(g.Player.Pos, g.Player.Vel)
	if g.Player.FlashTime > 0 {
		g.Player.FlashTime -= dt
	}

	// 自动回血逻辑
	if g.Stats.Regen > 0 && g.Player.HP < g.Player.MaxHP {
		g.RegenTimer += dt
		if g.RegenTimer >= 1.0 {
			g.RegenTimer = 0
			g.Player.HP += g.Stats.Regen
			if g.Player.HP > g.Player.MaxHP {
				g.Player.HP = g.Player.MaxHP
			}
		}
	}

	g.UpdateWeapons(dt)
	g.UpdateEnemies(dt)
	g.UpdateBullets(dt)
	g.UpdateParticles(dt)
	g.UpdateTexts(dt)

	if g.XP >= g.NextLevelXP {
		g.LevelUp()
	}
	if g.Player.HP <= 0 {
		g.State = 3
	}
}

func (g *Game) GetNextID() int {
	g.NextEntityID++
	return g.NextEntityID
}

func (g *Game) ResetGame() {
	g.TimeAlive = 0
	g.XP = 0
	g.Level = 1
	g.NextLevelXP = 15 // 降低初始升级经验，加快节奏
	g.Enemies = nil
	g.Bullets = nil
	g.Particles = nil
	g.Texts = nil
	g.Weapons = make(map[string]int)
	g.WeaponTimers = make(map[string]float64)
	g.Difficulty = 1.0
	g.SpawnTimer = 0
	g.Wave = 1
	g.NextEntityID = 1

	g.Stats.MoveSpeed = 1.2
	g.Stats.PickupRange = 25.0
	g.Stats.FireRateMod = 1.0
	g.Stats.DamageMod = 1.0
	g.Stats.MaxHPMod = 1.0
	g.Stats.ReflectDmg = 0.0
	g.Stats.BulletSpeed = 1.0
	g.Stats.Luck = 1.0
	g.Stats.Regen = 0.0
	g.Stats.DamageRed = 0.0

	g.AddWeapon("PING")

	g.Player = &Entity{
		ID:    g.GetNextID(),
		Pos:   Vec2{X: 0, Y: 0},
		Color: ColCyan,
		HP:    100, MaxHP: 100,
		Radius: 1.0,
		Type:   0,
	}
}

// 武器定义库 (12种)
func initWeaponDefs() map[string]WeaponDef {
	return map[string]WeaponDef{
		"PING":   {ID: "PING", Name: "ICMP脉冲", Description: "向最近敌人发射数据包", Type: 0, Cooldown: 0.6, Damage: 12, Speed: 4.0, Count: 1, Color: ColHiCyan},
		"DDOS":   {ID: "DDOS", Name: "DDOS洪流", Description: "快速发射低伤子弹", Type: 0, Cooldown: 0.15, Damage: 4, Speed: 5.5, Spread: 0.3, Count: 1, Color: ColWhite},
		"SSH":    {ID: "SSH", Name: "SSH隧道", Description: "建立穿透性激光连接", Type: 1, Cooldown: 2.0, Damage: 5, Duration: 0.8, Color: ColHiGreen},
		"FW":     {ID: "FW", Name: "防火墙", Description: "生成环绕自身的火球", Type: 2, Cooldown: 2.0, Damage: 18, Speed: 2.0, Count: 2, Color: ColHiYellow, Knockback: 4.0},
		"SQL":    {ID: "SQL", Name: "SQL注入", Description: "发射强力穿透代码", Type: 0, Cooldown: 1.2, Damage: 20, Speed: 3.5, Count: 1, Pierce: 4, Color: ColMagenta},
		"ZERO":   {ID: "ZERO", Name: "0-Day漏洞", Description: "引发大范围数据爆炸", Type: 3, Cooldown: 3.5, Damage: 60, Duration: 0.5, Color: ColWhite, Knockback: 12.0},
		"BRUTE":  {ID: "BRUTE", Name: "暴力破解", Description: "向四周发射散弹", Type: 0, Cooldown: 1.5, Damage: 9, Speed: 4.0, Count: 6, Spread: 0.8, Color: ColHiBlue},
		"VPN":    {ID: "VPN", Name: "VPN专线", Description: "发射自动追踪导弹", Type: 7, Cooldown: 1.0, Damage: 25, Speed: 3.0, Count: 1, Color: ColHiCyan, Knockback: 2.0},
		"TROJAN": {ID: "TROJAN", Name: "木马陷阱", Description: "放置高伤感应地雷", Type: 4, Cooldown: 2.5, Damage: 50, Duration: 15.0, Color: ColYellow},
		"LOG":    {ID: "LOG", Name: "日志回滚", Description: "向后发射高伤数据流", Type: 6, Cooldown: 0.8, Damage: 45, Speed: 4.0, Count: 1, Color: ColHiBlack},
		"RANSOM": {ID: "RANSOM", Name: "勒索软件", Description: "发射高击退法球", Type: 0, Cooldown: 1.8, Damage: 30, Speed: 2.5, Count: 1, Pierce: 99, Knockback: 15.0, Color: ColHiMagenta},
		"PHISH":  {ID: "PHISH", Name: "钓鱼邮件", Description: "发射回旋攻击", Type: 5, Cooldown: 1.5, Damage: 15, Speed: 5.0, Count: 1, Color: ColHiRed},
	}
}

func (g *Game) AddWeapon(id string) {
	if _, ok := g.Weapons[id]; ok {
		g.Weapons[id]++
	} else {
		g.Weapons[id] = 1
	}
}

func (g *Game) UpdateWeapons(dt float64) {
	// 查找最近敌人
	findTarget := func(rng float64) *Entity {
		var target *Entity
		minD := rng
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

	for id, lvl := range g.Weapons {
		def := g.WeaponDefs[id]
		g.WeaponTimers[id] += dt
		cd := def.Cooldown / g.Stats.FireRateMod
		if cd < 0.05 {
			cd = 0.05
		}

		if g.WeaponTimers[id] >= cd {
			used := false
			// 提升武器等级加成，解决后期伤害不足问题
			// 从 0.2 -> 0.35 (每级+35%伤害)
			dmg := def.Damage * g.Stats.DamageMod * (1.0 + float64(lvl-1)*0.35)

			switch def.Type {
			case 0: // 投射物 (普通)
				target := findTarget(350)
				if target != nil {
					used = true
					dir := Sub(target.Pos, g.Player.Pos).Normalize()
					cnt := def.Count + (lvl-1)/2
					for i := 0; i < cnt; i++ {
						spread := (rand.Float64() - 0.5) * def.Spread
						finalDir := Rotate(dir, spread)
						g.SpawnBullet(g.Player.Pos, finalDir, def.Speed*g.Stats.BulletSpeed, dmg, def.Color, def.Pierce, def.Knockback, 0, 0)
					}
				} else if id == "BRUTE" { // 暴力破解
					moveDir := g.Player.Vel
					if moveDir.X == 0 && moveDir.Y == 0 {
						moveDir = Vec2{1, 0}
					} else {
						moveDir = moveDir.Normalize()
					}
					used = true
					cnt := def.Count + lvl
					for i := 0; i < cnt; i++ {
						spread := (rand.Float64() - 0.5) * def.Spread
						finalDir := Rotate(moveDir, spread)
						g.SpawnBullet(g.Player.Pos, finalDir, def.Speed*g.Stats.BulletSpeed, dmg, def.Color, def.Pierce, def.Knockback, 0, 0)
					}
				}

			case 1: // 激光 (锁定)
				target := findTarget(300)
				if target != nil {
					used = true
					b := g.SpawnBullet(g.Player.Pos, Vec2{}, 0, dmg, def.Color, 999, 0, 1, 0)
					b.TargetID = target.ID
					b.Lifetime = def.Duration
				}

			case 2: // 护盾 (FW)
				count := 0
				for _, b := range g.Bullets {
					if !b.Dead && b.Type == 2 && b.SubType == 2 && b.Text == id {
						count++
					}
				}
				maxCount := def.Count + (lvl - 1)
				// 设置上限，防止视觉过于混乱
				if maxCount > 12 {
					maxCount = 12
				}
				if count < maxCount {
					used = true
					b := g.SpawnBullet(g.Player.Pos, Vec2{}, 0, dmg, def.Color, 999, def.Knockback, 2, 0)
					b.Text = id
					b.Angle = float64(count) * (math.Pi * 2 / float64(maxCount))
					b.Lifetime = 9999
				}

			case 3: // 区域 (ZERO)
				used = true
				b := g.SpawnBullet(g.Player.Pos, Vec2{}, 0, dmg, def.Color, 999, def.Knockback, 3, 0)
				b.Lifetime = def.Duration
				b.Radius = 60.0

			case 4: // 地雷 (TROJAN)
				used = true
				// 限制地雷数量，销毁最早的
				var mines []*Entity
				for _, b := range g.Bullets {
					if !b.Dead && b.Type == 2 && b.SubType == 4 {
						mines = append(mines, b)
					}
				}
				// 上限为 8
				if len(mines) >= 8 {
					// 销毁最早的
					minLife := 99999.0
					var oldest *Entity
					for _, m := range mines {
						if m.Lifetime < minLife {
							minLife = m.Lifetime
							oldest = m
						}
					}
					if oldest != nil {
						oldest.Dead = true
						g.SpawnParticles(oldest.Pos, oldest.Color, 2) // 销毁特效
					}
				}

				b := g.SpawnBullet(g.Player.Pos, Vec2{}, 0, dmg, def.Color, 1, def.Knockback, 4, 0)
				b.Lifetime = def.Duration
				b.Radius = 5.0

			case 5: // 回旋镖 (PHISH)
				target := findTarget(300)
				dir := Vec2{1, 0}
				if target != nil {
					dir = Sub(target.Pos, g.Player.Pos).Normalize()
				} else if g.Player.Vel.X != 0 || g.Player.Vel.Y != 0 {
					dir = g.Player.Vel.Normalize()
				}
				used = true
				g.SpawnBullet(g.Player.Pos, dir, def.Speed*g.Stats.BulletSpeed, dmg, def.Color, 99, def.Knockback, 5, 0)

			case 6: // 后射 (LOG)
				dir := g.Player.Vel
				if dir.X == 0 && dir.Y == 0 {
					dir = Vec2{-1, 0}
				} else {
					dir = Mul(dir.Normalize(), -1)
				}
				used = true
				g.SpawnBullet(g.Player.Pos, dir, def.Speed*g.Stats.BulletSpeed, dmg, def.Color, def.Pierce+lvl, def.Knockback, 0, 0)

			case 7: // 追踪 (VPN)
				target := findTarget(400)
				if target != nil {
					used = true
					startDir := Vec2{rand.Float64() - 0.5, rand.Float64() - 0.5}.Normalize()
					b := g.SpawnBullet(g.Player.Pos, startDir, def.Speed*g.Stats.BulletSpeed, dmg, def.Color, 1, def.Knockback, 7, 0)
					b.TargetID = target.ID
				}
			}

			if used {
				g.WeaponTimers[id] = 0
			}
		}
	}
}

func (g *Game) UpdateEnemies(dt float64) {
	cleanupDist := float64(g.PixelW)
	if cleanupDist < 500 {
		cleanupDist = 500
	}

	activeEnemies := g.Enemies[:0]
	for _, e := range g.Enemies {
		if !e.Dead && Dist(e.Pos, g.Player.Pos) < cleanupDist*1.5 {
			activeEnemies = append(activeEnemies, e)
		}
	}
	g.Enemies = activeEnemies

	// 生成频率基于难度，但有下限防止卡顿
	// 2.0 / 1.0 = 2.0s
	// 2.0 / 10.0 = 0.2s
	spawnRate := 2.0 / g.Difficulty
	if spawnRate < 0.1 { // 限制最小生成间隔为 0.1s
		spawnRate = 0.1
	}

	g.SpawnTimer += dt
	if g.SpawnTimer >= spawnRate {
		g.SpawnTimer = 0
		// 移除 difficulty 的自增，改用 TimeAlive 控制

		viewRadius := float64(g.PixelW/2) + 20.0
		if float64(g.PixelH/2) > viewRadius {
			viewRadius = float64(g.PixelH/2) + 20.0
		}
		spawnDist := viewRadius + 20.0 + rand.Float64()*50.0
		angle := rand.Float64() * math.Pi * 2
		pos := Add(g.Player.Pos, Vec2{math.Cos(angle) * spawnDist, math.Sin(angle) * spawnDist})

		hpMul := g.Difficulty
		var e *Entity

		r := rand.Float64()
		if r < 0.5 {
			e = &Entity{Pos: pos, Color: ColGreen, HP: 10 * hpMul, MaxHP: 10 * hpMul, Damage: 5, SubType: 0, Radius: 1.5}
		} else if r < 0.8 {
			e = &Entity{Pos: pos, Color: ColHiMagenta, HP: 6 * hpMul, MaxHP: 6 * hpMul, Damage: 8, SubType: 1, Radius: 1.0}
		} else if r < 0.95 {
			e = &Entity{Pos: pos, Color: ColBlue, HP: 25 * hpMul, MaxHP: 25 * hpMul, Damage: 12, SubType: 2, Radius: 2.0}
		} else {
			e = &Entity{Pos: pos, Color: ColHiRed, HP: 100 * hpMul, MaxHP: 100 * hpMul, Damage: 20, SubType: 3, Radius: 5.0}
		}
		e.Type = 1
		e.ID = g.GetNextID()
		g.Enemies = append(g.Enemies, e)
	}

	for _, e := range g.Enemies {
		if e.Dead {
			continue
		}
		if e.FlashTime > 0 {
			e.FlashTime -= dt
		}

		dir := Sub(g.Player.Pos, e.Pos).Normalize()
		speed := 0.0

		switch e.SubType {
		case 0:
			speed = 0.8
		case 1:
			speed = 1.5
		case 2:
			speed = 0.4
		case 3:
			speed = 0.5
		}

		for _, other := range g.Enemies {
			if e != other && !other.Dead {
				d := Dist(e.Pos, other.Pos)
				minD := e.Radius + other.Radius
				if d < minD {
					push := Sub(e.Pos, other.Pos).Normalize()
					e.Pos = Add(e.Pos, Mul(push, 0.5))
				}
			}
		}

		e.Pos = Add(e.Pos, Mul(dir, speed))

		if Dist(e.Pos, g.Player.Pos) < e.Radius+g.Player.Radius {
			g.HitPlayer(e.Damage)
			push := Sub(e.Pos, g.Player.Pos).Normalize()
			e.Pos = Add(e.Pos, Mul(push, 5.0))
			if g.Stats.ReflectDmg > 0 {
				g.DamageEnemy(e, g.Stats.ReflectDmg, true)
			}
		}
	}
}

func (g *Game) UpdateBullets(dt float64) {
	playerCenter := g.Player.Pos
	getEntityByID := func(id int) *Entity {
		for _, e := range g.Enemies {
			if e.ID == id && !e.Dead {
				return e
			}
		}
		return nil
	}

	for _, b := range g.Bullets {
		if b.Dead {
			continue
		}

		switch b.SubType {
		case 1: // 激光
			b.Lifetime -= dt
			if b.Lifetime <= 0 {
				b.Dead = true
				continue
			}
			b.Pos = playerCenter
			target := getEntityByID(b.TargetID)
			if target != nil {
				b.Vel = target.Pos
				g.DamageEnemy(target, b.Damage*dt*30, false)
			} else {
				b.Dead = true
			}

		case 2: // 护盾
			b.Angle += 2.0 * dt
			radius := 15.0
			offset := Vec2{math.Cos(b.Angle) * radius, math.Sin(b.Angle) * radius}
			b.Pos = Add(playerCenter, offset)
			for _, e := range g.Enemies {
				if !e.Dead && Dist(b.Pos, e.Pos) < b.Radius+e.Radius {
					g.DamageEnemy(e, b.Damage*dt*5.0, false)
					push := Sub(e.Pos, playerCenter).Normalize()
					e.Pos = Add(e.Pos, Mul(push, b.Knockback*0.1))
				}
			}

		case 3: // 区域
			b.Lifetime -= dt
			if b.Lifetime <= 0 {
				b.Dead = true
				continue
			}
			for _, e := range g.Enemies {
				if !e.Dead && Dist(b.Pos, e.Pos) < b.Radius+e.Radius {
					g.DamageEnemy(e, b.Damage*dt*5.0, false)
				}
			}

		case 4: // 地雷
			b.Lifetime -= dt
			if b.Lifetime <= 0 {
				b.Dead = true
				continue
			}
			for _, e := range g.Enemies {
				if !e.Dead && Dist(b.Pos, e.Pos) < b.Radius+e.Radius {
					b.Dead = true
					g.SpawnParticles(b.Pos, b.Color, 5)
					g.DamageEnemy(e, b.Damage, true)
					push := Sub(e.Pos, b.Pos).Normalize()
					e.Pos = Add(e.Pos, Mul(push, b.Knockback))
					break
				}
			}

		case 5: // 回旋镖
			b.Lifetime -= dt
			if b.Lifetime <= 0 {
				b.Dead = true
				continue
			}
			speed := math.Sqrt(b.Vel.X*b.Vel.X + b.Vel.Y*b.Vel.Y)
			if b.ExtraData == 0 {
				speed -= dt * 10.0
				if speed <= 0 {
					b.ExtraData = 1
					speed = 0
				}
				if speed > 0 {
					b.Vel = Mul(b.Vel.Normalize(), speed)
				}
			} else {
				dir := Sub(g.Player.Pos, b.Pos).Normalize()
				speed += dt * 15.0
				b.Vel = Mul(dir, speed)
				if Dist(b.Pos, g.Player.Pos) < 5.0 {
					b.Dead = true
				}
			}
			b.Pos = Add(b.Pos, b.Vel)

		case 7: // 追踪
			b.Lifetime -= dt
			if b.Lifetime <= 0 {
				b.Dead = true
				continue
			}
			target := getEntityByID(b.TargetID)
			if target != nil {
				wantedDir := Sub(target.Pos, b.Pos).Normalize()
				currentDir := b.Vel.Normalize()
				lerp := 0.15
				newDir := Add(Mul(currentDir, 1.0-lerp), Mul(wantedDir, lerp)).Normalize()
				speed := math.Sqrt(b.Vel.X*b.Vel.X + b.Vel.Y*b.Vel.Y)
				b.Vel = Mul(newDir, speed)
			}
			b.Pos = Add(b.Pos, b.Vel)
			if rand.Float64() < 0.3 {
				g.Particles = append(g.Particles, &Entity{
					Pos: b.Pos, Vel: Vec2{}, Color: b.Color, Type: 5, Lifetime: 0.3,
				})
			}

		default:
			b.Lifetime -= dt
			if b.Lifetime <= 0 {
				b.Dead = true
				continue
			}
			b.Pos = Add(b.Pos, b.Vel)
			if Dist(b.Pos, g.Player.Pos) > 600 {
				b.Dead = true
				continue
			}
		}

		if b.SubType != 1 && b.SubType != 2 && b.SubType != 3 && b.SubType != 4 {
			hit := false
			for _, e := range g.Enemies {
				if e.Dead {
					continue
				}
				if Dist(b.Pos, e.Pos) < e.Radius+2.0 {
					g.DamageEnemy(e, b.Damage, true)
					push := b.Vel.Normalize()
					if b.SubType == 5 {
						push = Sub(e.Pos, g.Player.Pos).Normalize()
					}
					e.Pos = Add(e.Pos, Mul(push, b.Knockback))

					if b.Pierce < 99 {
						b.Pierce--
						if b.Pierce <= 0 {
							b.Dead = true
							g.SpawnParticles(b.Pos, b.Color, 2)
						}
					}
					hit = true
					break
				}
			}
			if hit && b.Dead {
				continue
			}
		}
	}
}

func (g *Game) UpdateParticles(dt float64) {
	for _, p := range g.Particles {
		if p.Dead {
			continue
		}

		if p.Type == 3 {
			p.Lifetime += dt * 5
			dist := Dist(p.Pos, g.Player.Pos)
			if dist < g.Stats.PickupRange {
				dir := Sub(g.Player.Pos, p.Pos).Normalize()
				spd := (g.Stats.PickupRange - dist + 5.0) * 0.3
				p.Pos = Add(p.Pos, Mul(dir, spd))
				if dist < 3.0 {
					p.Dead = true
					g.GainXP(int(p.Damage))
				}
			}
		} else {
			p.Lifetime -= dt
			if p.Lifetime <= 0 {
				p.Dead = true
				continue
			}
			p.Pos = Add(p.Pos, p.Vel)
			p.Vel = Mul(p.Vel, 0.9)
		}
	}
	g.CleanupEntities()
}

func (g *Game) UpdateTexts(dt float64) {
	for _, t := range g.Texts {
		if t.Dead {
			continue
		}
		t.Lifetime -= dt
		if t.Lifetime <= 0 {
			t.Dead = true
		}
		t.Pos.Y -= 0.5
	}
}

// --- 辅助逻辑 ---

func (g *Game) SpawnBullet(pos, dir Vec2, spd, dmg float64, col string, pierce int, knockback float64, subType int, extra float64) *Entity {
	b := &Entity{
		Pos: pos, Vel: Mul(dir, spd), Color: col,
		Type: 2, SubType: subType, Damage: dmg, Lifetime: 3.0,
		Pierce: pierce, Knockback: knockback, Radius: 0.5,
		ExtraData: extra,
		ID:        g.GetNextID(),
	}
	g.Bullets = append(g.Bullets, b)
	return b
}

func (g *Game) SpawnParticles(pos Vec2, col string, count int) {
	for i := 0; i < count; i++ {
		ang := rand.Float64() * math.Pi * 2
		spd := rand.Float64() * 2.0
		vel := Vec2{math.Cos(ang) * spd, math.Sin(ang) * spd}
		g.Particles = append(g.Particles, &Entity{
			Pos: pos, Vel: vel, Color: col, Type: 5, Lifetime: 0.5 + rand.Float64()*0.5,
		})
	}
}

func (g *Game) DamageEnemy(e *Entity, dmg float64, showText bool) {
	e.HP -= dmg
	e.FlashTime = 0.15
	if showText && dmg > 1.0 {
		g.SpawnFloatText(fmt.Sprintf("%.0f", dmg), e.Pos, ColWhite)
	}

	if e.HP <= 0 {
		e.Dead = true
		g.SpawnParticles(e.Pos, e.Color, 8)
		xpVal := 1
		if e.SubType == 2 {
			xpVal = 5
		}
		if e.SubType == 3 {
			xpVal = 50
		}
		g.Particles = append(g.Particles, &Entity{
			Pos: e.Pos, Color: ColHiCyan, Type: 3, Damage: float64(xpVal),
			Lifetime: 0,
		})
	}
}

func (g *Game) HitPlayer(dmg float64) {
	if g.Player.FlashTime > 0 {
		return
	}
	// 减伤计算
	if g.Stats.DamageRed > 0 {
		dmg = dmg * (1.0 - g.Stats.DamageRed)
	}
	g.Player.HP -= dmg
	g.Player.FlashTime = 0.5
	g.SpawnFloatText(fmt.Sprintf("-%.0f", dmg), g.Player.Pos, ColRed)
}

func (g *Game) GainXP(amount int) {
	g.XP += amount
}

func (g *Game) SpawnFloatText(s string, pos Vec2, col string) {
	if len(g.Texts) > 20 {
		return
	}
	g.Texts = append(g.Texts, &Entity{
		Pos: pos, Text: s, Color: col, Lifetime: 0.8,
	})
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
	g.Texts = clean(g.Texts)
}

func (g *Game) LevelUp() {
	g.Level++
	g.XP -= g.NextLevelXP
	g.NextLevelXP = int(float64(g.NextLevelXP) * 1.15) // 降低递增系数
	g.State = 2

	pool := []Upgrade{
		{"SPD", "总线超频", "移动速度 +15%", 0, func(g *Game) { g.Stats.MoveSpeed *= 1.15 }},
		{"HP", "系统补丁", "生命上限 +20% 并恢复", 0, func(g *Game) {
			g.Stats.MaxHPMod *= 1.2
			g.Player.MaxHP = 100 * g.Stats.MaxHPMod
			g.Player.HP += 40
			if g.Player.HP > g.Player.MaxHP {
				g.Player.HP = g.Player.MaxHP
			}
		}},
		{"RANGE", "广域扫描", "拾取范围 +30%", 0, func(g *Game) { g.Stats.PickupRange *= 1.3 }},
		{"DMG", "高压核心", "全局伤害 +10%", 0, func(g *Game) { g.Stats.DamageMod *= 1.1 }},
		{"CD", "多线程处理", "攻击冷却 -10%", 1, func(g *Game) { g.Stats.FireRateMod *= 1.1 }},
		{"SPEED", "光纤传输", "子弹飞行速度 +20%", 0, func(g *Game) { g.Stats.BulletSpeed *= 1.2 }},
		// 新增通用升级
		{"REGEN", "系统重启", "每秒回复 2 HP", 1, func(g *Game) { g.Stats.Regen += 2.0 }},
		{"ARMOR", "内核加固", "受到伤害 -15%", 1, func(g *Game) {
			if g.Stats.DamageRed < 0.75 { // 上限 75%
				g.Stats.DamageRed += 0.15
			}
		}},
	}

	for id, def := range g.WeaponDefs {
		lvl := g.Weapons[id]
		rarity := 0
		if id == "SSH" || id == "ZERO" || id == "VPN" {
			rarity = 1
		}

		var nameDisplay string
		var descDisplay string

		if lvl == 0 {
			nameDisplay = "[新] " + def.Name
			descDisplay = def.Description
		} else {
			nameDisplay = def.Name
			descDisplay = fmt.Sprintf("升级至 Lv.%d (强化效果)", lvl+1)
		}

		pool = append(pool, Upgrade{
			ID: id, Name: nameDisplay, Description: descDisplay, Rarity: rarity,
			Apply: func(id string) func(g *Game) {
				return func(g *Game) { g.AddWeapon(id) }
			}(id),
		})
	}

	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if len(pool) > 6 {
		pool = pool[:6]
	}
	g.PendingUpgrades = pool
	g.SelectorIdx = 0
}

// --- 渲染系统 (Half-Block Canvas) ---

func toBg(c string) string {
	if len(c) < 5 {
		return ""
	}
	if c[2] == '3' {
		return c[:2] + "4" + c[3:]
	}
	if c[2] == '9' {
		return c[:2] + "10" + c[3:]
	}
	return ""
}

func (g *Game) Render(out io.Writer) {
	const ViewScale = 0.5

	hudPixelHeight := TopHudHeight * PixelScaleY
	for y := 0; y < hudPixelHeight; y++ {
		start := y * g.Canvas.Width
		end := start + g.Canvas.Width
		for i := start; i < end; i++ {
			g.Canvas.Pixels[i] = false
		}
	}

	g.Canvas.Clear()
	g.ClearBuffer(g.FrontBuffer)

	toScreen := func(v Vec2) (int, int, bool) {
		relX := v.X - g.Player.Pos.X
		relY := v.Y - g.Player.Pos.Y
		sx := int(relX*ViewScale) + g.PixelW/2
		sy := int(relY*ViewScale) + g.PixelH/2
		if sx >= -10 && sx < g.PixelW+10 && sy >= -10 && sy < g.PixelH+10 {
			return sx, sy, true
		}
		return 0, 0, false
	}

	if g.State == 1 || g.State == 2 || g.State == 3 || g.State == 5 {
		for _, p := range g.Particles {
			if px, py, ok := toScreen(p.Pos); ok {
				if p.Type == 3 {
					g.Canvas.SetPixel(px, py, p.Color)
					g.Canvas.SetPixel(px+1, py, p.Color)
					g.Canvas.SetPixel(px, py+1, p.Color)
					g.Canvas.SetPixel(px+1, py+1, p.Color)
				} else {
					g.Canvas.SetPixel(px, py, p.Color)
				}
			}
		}

		for _, e := range g.Enemies {
			if px, py, ok := toScreen(e.Pos); ok {
				col := e.Color
				if e.FlashTime > 0 {
					col = ColWhite
				}
				r := int(e.Radius * ViewScale)
				if r < 1 {
					r = 0
				}
				g.Canvas.DrawCircle(px, py, r, col)
			}
		}

		for _, b := range g.Bullets {
			sx, sy, ok1 := toScreen(b.Pos)
			ex, ey, _ := toScreen(b.Vel)

			if b.SubType == 1 {
				if ok1 {
					g.Canvas.DrawLine(sx, sy, ex, ey, b.Color)
				}
			} else if b.SubType == 2 {
				if ok1 {
					r := int(b.Radius * ViewScale)
					if r < 1 {
						r = 1
					}
					g.Canvas.DrawCircle(sx, sy, r, b.Color)
				}
			} else if b.SubType == 3 {
				if ok1 {
					r := int(b.Radius * ViewScale)
					g.Canvas.DrawCircle(sx, sy, r, b.Color)
				}
			} else {
				if ok1 {
					g.Canvas.SetPixel(sx, sy, b.Color)
				}
			}
		}

		pc := ColCyan
		if g.Player.FlashTime > 0 {
			pc = ColRed
		}
		cx, cy := g.PixelW/2, g.PixelH/2
		g.Canvas.SetPixel(cx, cy, ColWhite)
		g.Canvas.SetPixel(cx-1, cy, pc)
		g.Canvas.SetPixel(cx+1, cy, pc)
		g.Canvas.SetPixel(cx, cy-1, pc)
		g.Canvas.SetPixel(cx, cy+1, pc)
	}

	for y := 0; y < g.TermH; y++ {
		for x := 0; x < g.TermW; x++ {
			px := x
			pyTop := y * 2
			pyBot := y*2 + 1

			idxTop := pyTop*g.Canvas.Width + px
			topOn := g.Canvas.Pixels[idxTop]
			topCol := g.Canvas.Colors[idxTop]

			idxBot := pyBot*g.Canvas.Width + px
			botOn := g.Canvas.Pixels[idxBot]
			botCol := g.Canvas.Colors[idxBot]

			if topOn && botOn {
				if topCol == botCol {
					g.FrontBuffer[y][x] = Cell{Char: '█', Color: ColReset + topCol}
				} else {
					bg := toBg(botCol)
					g.FrontBuffer[y][x] = Cell{Char: '▀', Color: ColReset + bg + topCol}
				}
			} else if topOn {
				g.FrontBuffer[y][x] = Cell{Char: '▀', Color: ColReset + topCol}
			} else if botOn {
				g.FrontBuffer[y][x] = Cell{Char: '▄', Color: ColReset + botCol}
			}
		}
	}

	if g.State == 1 || g.State == 2 || g.State == 3 || g.State == 5 {
		for _, t := range g.Texts {
			sx, sy, ok := toScreen(t.Pos)
			if ok {
				tx, ty := sx/PixelScaleX, sy/PixelScaleY
				inTopHud := ty < TopHudHeight
				if !inTopHud {
					if tx >= 0 && tx < g.TermW && ty >= 0 && ty < g.TermH {
						g.DrawText(tx, ty, t.Text, t.Color)
					}
				}
			}
		}
		g.DrawHUD()
	}

	switch g.State {
	case 0:
		g.DrawMenu()
	case 2:
		g.DrawLevelUpUI()
	case 3:
		g.DrawGameOverUI()
	case 4:
		g.DrawTutorialUI()
	case 5:
		g.CenterText(g.TermH/2, "-- 游戏暂停 --", ColYellow)
	}

	var buf bytes.Buffer
	buf.WriteString(ColReset)

	for y := 0; y < g.TermH; y++ {
		for x := 0; x < g.TermW; x++ {
			f := g.FrontBuffer[y][x]
			b := g.BackBuffer[y][x]

			if f.Char == CharPlaceholder {
				g.BackBuffer[y][x] = f
				continue
			}

			if f != b {
				buf.WriteString(fmt.Sprintf("\033[%d;%dH", y+1, x+1))
				buf.WriteString(f.Color)
				if f.Char == 0 || f.Char == ' ' {
					buf.WriteString(" ")
				} else {
					buf.WriteString(string(f.Char))
				}

				g.BackBuffer[y][x] = f
				if isWide(f.Char) && x+1 < g.TermW {
					g.BackBuffer[y][x+1] = Cell{Char: CharPlaceholder}
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
	logoY := g.TermH/2 - 8
	if logoY < 1 {
		logoY = 1
	}
	for i, line := range logo {
		g.CenterText(logoY+i, line, ColRed)
	}
	g.CenterText(logoY+6, "VER 2.2 (BALANCED)", ColYellow)

	opts := []string{"[ 启动防御系统 ]", "[ 系统操作手册 ]", "[ 断 开 连 接 ]"}
	menuY := g.TermH/2 + 2
	for i, opt := range opts {
		col := ColHiBlack
		pre := "  "
		if i == g.MenuIdx {
			col = ColWhite
			pre = "> "
		}
		g.CenterText(menuY+i*2, pre+opt, col)
	}
	g.CenterText(g.TermH-2, "使用 [↑/↓] 选择, [回车] 确认", ColHiBlack)
}

func (g *Game) DrawTutorialUI() {
	g.DrawBox(4, 2, g.TermW-8, g.TermH-4, ColGreen)
	g.CenterText(4, "== 系统防御指南 ==", ColHiGreen)

	colL := ColHiWhite
	colR := ColHiBlack
	y := 6

	g.DrawText(8, y, "操作指令:", colL)
	y++
	g.DrawText(10, y, "[W/A/S/D] 移动核心", colR)
	y++
	g.DrawText(10, y, "[P] 暂停 / [Q] 退出", colR)
	y += 2

	g.DrawText(8, y, "威胁类型:", colL)
	y++
	g.DrawText(10, y, "脚本小子(绿): 数量多，移动慢", ColHiGreen)
	y++
	g.DrawText(10, y, "网络蠕虫(紫): 速度快，试图撞击", ColHiMagenta)
	y++
	g.DrawText(10, y, "僵尸网络(蓝): 难以被摧毁", ColHiBlue)
	y++
	g.DrawText(10, y, "APT组织(红): 巨型BOSS", ColHiRed)
	y += 2

	g.DrawText(8, y, "新式武器:", colL)
	y++
	g.DrawText(10, y, "SSH激光: 持续锁定切割", ColHiGreen)
	y++
	g.DrawText(10, y, "VPN导弹: 自动追踪目标", ColHiCyan)

	g.CenterText(g.TermH-4, "按 [任意键] 返回", ColWhite)
}

func (g *Game) DrawLevelUpUI() {
	// 动态调整高度以容纳6个选项
	w := 50
	h := 4 + len(g.PendingUpgrades)*3
	if h > g.TermH-2 {
		h = g.TermH - 2
	}

	bx, by := (g.TermW-w)/2, (g.TermH-h)/2
	if by < 0 {
		by = 0
	}

	for y := by; y < by+h; y++ {
		for x := bx; x < bx+w; x++ {
			g.FrontBuffer[y][x] = Cell{Char: ' ', Color: ColReset}
		}
	}
	g.DrawBox(bx, by, w, h, ColYellow)
	g.CenterText(by+1, ">> 系统升级可用 <<", ColHiYellow)

	for i, upg := range g.PendingUpgrades {
		// 统一颜色风格：未选中灰色，选中高亮
		nameCol := ColWhite
		descCol := ColHiBlack
		pre := "   "

		if i == g.SelectorIdx {
			nameCol = ColHiGreen
			descCol = ColWhite
			pre = " > "
		} else if upg.Rarity == 1 {
			// 稀有物品未选中时带点颜色
			nameCol = ColMagenta
		}

		y := by + 3 + i*3
		if y+2 >= by+h { // 防止越界
			break
		}

		g.DrawText(bx+2, y, pre+upg.Name, nameCol)
		g.DrawText(bx+6, y+1, upg.Description, descCol)
	}
}

func (g *Game) DrawGameOverUI() {
	w, h := 40, 10
	bx, by := (g.TermW-w)/2, (g.TermH-h)/2
	g.DrawBox(bx, by, w, h, ColRed)
	g.CenterText(by+2, "内核崩溃 (GAME OVER)", ColHiRed)
	g.CenterText(by+4, fmt.Sprintf("坚持时间: %ds", int(g.TimeAlive.Seconds())), ColWhite)
	g.CenterText(by+5, fmt.Sprintf("防御等级: Lv.%d", g.Level), ColWhite)
	g.CenterText(by+7, "按 [ENTER] 重启系统", ColHiBlack)
}

func (g *Game) DrawHUD() {
	barLen := 30
	hpRatio := g.Player.HP / g.Player.MaxHP
	if hpRatio < 0 {
		hpRatio = 0
	}
	if hpRatio > 1 {
		hpRatio = 1
	}
	filled := int(hpRatio * float64(barLen))

	startX := 6

	g.DrawText(2, 1, "HP:[", ColHiRed)
	for i := 0; i < barLen; i++ {
		char := '░'
		col := ColHiBlack
		if i < filled {
			char = '█'
			col = ColRed
		}
		if startX+i < g.TermW {
			g.FrontBuffer[1][startX+i] = Cell{Char: char, Color: col}
		}
	}
	g.DrawText(startX+barLen, 1, "]", ColHiRed)
	g.DrawText(startX+barLen+2, 1, fmt.Sprintf("%.0f/%.0f", g.Player.HP, g.Player.MaxHP), ColHiWhite)

	xpRatio := float64(g.XP) / float64(g.NextLevelXP)
	if xpRatio > 1 {
		xpRatio = 1
	}
	xpFilled := int(xpRatio * float64(barLen))

	g.DrawText(2, 2, "XP:[", ColHiCyan)
	for i := 0; i < barLen; i++ {
		char := '░'
		col := ColHiBlack
		if i < xpFilled {
			char = '█'
			col = ColCyan
		}
		if startX+i < g.TermW {
			g.FrontBuffer[2][startX+i] = Cell{Char: char, Color: col}
		}
	}
	g.DrawText(startX+barLen, 2, "]", ColHiCyan)
	g.DrawText(startX+barLen+2, 2, fmt.Sprintf("Lv.%d", g.Level), ColHiWhite)

	if g.TermW > 50 {
		title := "== 挂载 =="
		rx := g.TermW - len(title) - 2
		if rx < 0 {
			rx = 0
		}
		g.DrawText(rx, 1, title, ColHiWhite)

		y := 2
		var keys []string
		for k := range g.Weapons {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			if y > g.TermH-2 {
				break
			}
			lvl := g.Weapons[k]
			def := g.WeaponDefs[k]

			info := fmt.Sprintf("Lv.%d %s", lvl, def.Name)
			lineX := g.TermW - len(info) - 2
			if lineX < 0 {
				lineX = 0
			}

			g.DrawText(lineX, y, info, def.Color)
			y++
		}
	}

	tm := fmt.Sprintf("%02d:%02d", int(g.TimeAlive.Minutes()), int(g.TimeAlive.Seconds())%60)
	g.CenterText(g.TermH-1, tm, ColWhite)
}

// --- 基础绘图工具 ---

func (g *Game) DrawBox(x, y, w, h int, col string) {
	if x < 0 || y < 0 || x+w > g.TermW || y+h > g.TermH {
		// 简单越界保护
		return
	}

	// 强制重置颜色防止背景污染
	safeCol := ColReset + col

	for i := x; i < x+w; i++ {
		g.FrontBuffer[y][i] = Cell{Char: '─', Color: safeCol}
		g.FrontBuffer[y+h-1][i] = Cell{Char: '─', Color: safeCol}
	}
	for i := y; i < y+h; i++ {
		g.FrontBuffer[i][x] = Cell{Char: '│', Color: safeCol}
		g.FrontBuffer[i][x+w-1] = Cell{Char: '│', Color: safeCol}
	}
	g.FrontBuffer[y][x] = Cell{Char: '┌', Color: safeCol}
	g.FrontBuffer[y][x+w-1] = Cell{Char: '┐', Color: safeCol}
	g.FrontBuffer[y+h-1][x] = Cell{Char: '└', Color: safeCol}
	g.FrontBuffer[y+h-1][x+w-1] = Cell{Char: '┘', Color: safeCol}
}

func (g *Game) DrawText(x, y int, s string, col string) {
	if y < 0 || y >= g.TermH {
		return
	}

	// 强制重置颜色防止背景污染
	safeCol := ColReset + col

	currX := x
	for _, r := range s {
		if currX >= g.TermW {
			break
		}

		if currX >= 0 {
			g.FrontBuffer[y][currX] = Cell{Char: r, Color: safeCol}
		}

		if isWide(r) {
			currX++
			if currX < g.TermW && currX >= 0 {
				g.FrontBuffer[y][currX] = Cell{Char: CharPlaceholder}
			}
		}
		currX++
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
	x := (g.TermW - width) / 2
	g.DrawText(x, y, s, col)
}

func (g *Game) ClearBuffer(b [][]Cell) {
	for y := 0; y < g.TermH; y++ {
		for x := 0; x < g.TermW; x++ {
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
	if w < 40 {
		w = 40
	}
	if h < 10 {
		h = 10
	}
	g.TermW, g.TermH = w, h
	g.PixelW, g.PixelH = w*PixelScaleX, h*PixelScaleY
	g.FrontBuffer = initBuffer(w, h)
	g.BackBuffer = initBuffer(w, h)
	g.Canvas = NewCanvas(g.PixelW, g.PixelH)
}

// --- 数学工具 ---

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
func Rotate(v Vec2, angle float64) Vec2 {
	c, s := math.Cos(angle), math.Sin(angle)
	return Vec2{v.X*c - v.Y*s, v.X*s + v.Y*c}
}
func DistPointLine(p, a, b Vec2) float64 {
	l2 := math.Pow(a.X-b.X, 2) + math.Pow(a.Y-b.Y, 2)
	if l2 == 0 {
		return Dist(p, a)
	}
	t := ((p.X-a.X)*(b.X-a.X) + (p.Y-a.Y)*(b.Y-a.Y)) / l2
	if t < 0 {
		return Dist(p, a)
	}
	if t > 1 {
		return Dist(p, b)
	}
	proj := Vec2{a.X + t*(b.X-a.X), a.Y + t*(b.Y-a.Y)}
	return Dist(p, proj)
}

func isWide(r rune) bool {
	return r >= 0x2E80 || r == '◆'
}
