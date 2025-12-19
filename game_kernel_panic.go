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
	TargetFPS = 30
	FrameTime = time.Second / TargetFPS

	// 像素密度配置 (Braille 模式: 2x4)
	PixelScaleX = 2
	PixelScaleY = 4

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
	ID        int
	Pos       Vec2 // 世界坐标 (无限)
	Vel       Vec2
	Color     string
	HP        float64
	MaxHP     float64
	Radius    float64
	Type      int // 0:Player, 1:Enemy, 2:Bullet, 3:Particle, 4:Text
	SubType   int // 敌人类型/武器类型
	Damage    float64
	Knockback float64 // 击退力度
	Lifetime  float64
	MaxLife   float64
	FlashTime float64 // 受伤闪白
	Text      string  // 飘字内容
	Dead      bool
	Angle     float64 // 旋转角度
}

// WeaponDef 武器定义
type WeaponDef struct {
	ID          string
	Name        string
	Description string
	Type        int // 0:投射物, 1:激光, 2:护盾(环绕), 3:区域
	Cooldown    float64
	Damage      float64
	Speed       float64
	Count       int     // 投射物数量
	Spread      float64 // 散射角度
	Pierce      int     // 穿透数 (99为无限)
	Color       string
	Knockback   float64 // 击退力
	Duration    float64 // 持续时间(激光/区域)
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
	TermW, TermH   int // 终端字符宽高
	PixelW, PixelH int // 画布像素宽高 (TermW*2, TermH*4)

	State      int // 0:Menu, 1:Playing, 2:LevelUp, 3:GameOver, 4:Help, 5:Pause
	TimeAlive  time.Duration
	FrameCount int64
	Quit       bool

	InputBuffer chan byte
	InputState  int
	EscTimer    float64

	MenuIdx int

	// 核心数据
	Player      *Entity
	XP          int
	Level       int
	NextLevelXP int

	// 属性统计
	Stats struct {
		MoveSpeed   float64
		PickupRange float64
		FireRateMod float64 // 攻速修正 (值越小越快)
		DamageMod   float64
		MaxHPMod    float64
		ReflectDmg  float64
		BulletSpeed float64
		Luck        float64
	}

	// 武器库 (武器ID -> 等级)
	Weapons      map[string]int
	WeaponTimers map[string]float64
	WeaponDefs   map[string]WeaponDef

	// 实体池
	Enemies   []*Entity
	Bullets   []*Entity
	Particles []*Entity
	Texts     []*Entity // 伤害数字

	// 游戏循环控制
	SpawnTimer float64
	Difficulty float64
	Wave       int

	// 升级系统
	PendingUpgrades []Upgrade
	SelectorIdx     int

	// 渲染缓冲
	Canvas      *Canvas // 像素画布
	FrontBuffer [][]Cell
	BackBuffer  [][]Cell
}

// Cell 终端单元格
type Cell struct {
	Char  rune
	Color string
}

// Canvas 像素画布 (用于次像素渲染)
type Canvas struct {
	Width, Height int
	Pixels        []bool   // 是否点亮
	Colors        []string // 每个像素点的颜色
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

// Bresenham 画线算法
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

// DrawCircle 画空心圆
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
		State:       0,
		InputBuffer: make(chan byte, 128),
		FrontBuffer: initBuffer(w, h),
		BackBuffer:  initBuffer(w, h),
		Canvas:      NewCanvas(w*PixelScaleX, h*PixelScaleY),
		WeaponDefs:  initWeaponDefs(),
	}
	g.ResetGame()

	// 输入监听
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

	// 初始清屏
	out.Write([]byte("\033[?25l\033[2J"))
	defer out.Write([]byte("\033[?25h\033[0m\n\033[2J"))

	ticker := time.NewTicker(FrameTime)
	defer ticker.Stop()

	for !g.Quit {
		select {
		case <-ticker.C:
			// 响应尺寸变化
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
				g.State = 1
				g.ResetGame()
			} else if g.MenuIdx == 1 {
				g.State = 4 // Help
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
			g.State = 5 // Pause
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

	// 1. 玩家物理 (无限地形，不限制边界，不施加摩擦力)
	g.Player.Pos = Add(g.Player.Pos, g.Player.Vel)

	if g.Player.FlashTime > 0 {
		g.Player.FlashTime -= dt
	}

	// 2. 逻辑更新
	g.UpdateWeapons(dt)
	g.UpdateEnemies(dt)
	g.UpdateBullets(dt)
	g.UpdateParticles(dt)
	g.UpdateTexts(dt)

	// 升级检查
	if g.XP >= g.NextLevelXP {
		g.LevelUp()
	}
	// 死亡
	if g.Player.HP <= 0 {
		g.State = 3
	}
}

func (g *Game) ResetGame() {
	g.TimeAlive = 0
	g.XP = 0
	g.Level = 1
	g.NextLevelXP = 20
	g.Enemies = nil
	g.Bullets = nil
	g.Particles = nil
	g.Texts = nil
	g.Weapons = make(map[string]int)
	g.WeaponTimers = make(map[string]float64)
	g.Difficulty = 1.0
	g.SpawnTimer = 0
	g.Wave = 1

	g.Stats.MoveSpeed = 1.2 // 基础速度
	g.Stats.PickupRange = 25.0
	g.Stats.FireRateMod = 1.0
	g.Stats.DamageMod = 1.0
	g.Stats.MaxHPMod = 1.0
	g.Stats.ReflectDmg = 0.0
	g.Stats.BulletSpeed = 1.0
	g.Stats.Luck = 1.0

	// 初始武器
	g.AddWeapon("PING")

	// 玩家出生在原点，缩小尺寸
	g.Player = &Entity{
		Pos:   Vec2{X: 0, Y: 0},
		Color: ColCyan,
		HP:    100, MaxHP: 100,
		Radius: 1.0, // 减小尺寸 (从 2.0 -> 1.0)
		Type:   0,
	}
}

// 武器定义库
func initWeaponDefs() map[string]WeaponDef {
	return map[string]WeaponDef{
		"PING":  {ID: "PING", Name: "ICMP脉冲", Description: "向最近的敌人发射数据包", Type: 0, Cooldown: 0.6, Damage: 12, Speed: 4.0, Count: 1, Color: ColHiCyan},
		"DDOS":  {ID: "DDOS", Name: "DDOS洪流", Description: "快速发射大量低伤子弹", Type: 0, Cooldown: 0.15, Damage: 4, Speed: 5.5, Spread: 0.3, Count: 1, Color: ColHiYellow},
		"SSH":   {ID: "SSH", Name: "SSH隧道", Description: "建立一条穿透性的激光连接", Type: 1, Cooldown: 1.8, Damage: 25, Duration: 0.2, Color: ColHiGreen, Pierce: 99},
		"FW":    {ID: "FW", Name: "防火墙", Description: "环绕自身的防御火球", Type: 2, Cooldown: 2.0, Damage: 8, Speed: 2.0, Count: 2, Color: ColHiRed, Knockback: 3.0},
		"SQL":   {ID: "SQL", Name: "SQL注入", Description: "发射能穿透敌人的恶意代码", Type: 0, Cooldown: 1.2, Damage: 20, Speed: 3.5, Count: 1, Pierce: 3, Color: ColHiMagenta},
		"ZERO":  {ID: "ZERO", Name: "0-Day漏洞", Description: "引发局部区域的数据爆炸", Type: 3, Cooldown: 3.0, Damage: 50, Duration: 0.5, Color: ColWhite, Knockback: 10.0},
		"BRUTE": {ID: "BRUTE", Name: "暴力破解", Description: "向四周发射散弹", Type: 0, Cooldown: 1.5, Damage: 10, Speed: 4.0, Count: 6, Spread: 6.28, Color: ColHiBlue},
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
		// 某些高级武器冷却缩减上限
		if cd < 0.05 {
			cd = 0.05
		}

		if g.WeaponTimers[id] >= cd {
			used := false

			dmg := def.Damage * g.Stats.DamageMod * (1.0 + float64(lvl-1)*0.2)

			switch def.Type {
			case 0: // 投射物
				target := findTarget(300) // 视距内
				if id == "BRUTE" {        // 360度特殊处理
					used = true
					cnt := def.Count + (lvl - 1)
					for i := 0; i < cnt; i++ {
						angle := (math.Pi * 2 / float64(cnt)) * float64(i)
						dir := Vec2{math.Cos(angle), math.Sin(angle)}
						g.SpawnBullet(g.Player.Pos, dir, def.Speed*g.Stats.BulletSpeed, dmg, def.Color, def.Pierce, def.Knockback)
					}
				} else if target != nil {
					used = true
					dir := Sub(target.Pos, g.Player.Pos).Normalize()
					cnt := def.Count
					if id == "PING" {
						cnt += (lvl - 1) / 2
					} // PING 每2级加一个子弹

					for i := 0; i < cnt; i++ {
						// 散射计算
						spread := (rand.Float64() - 0.5) * def.Spread
						finalDir := Rotate(dir, spread)
						g.SpawnBullet(g.Player.Pos, finalDir, def.Speed*g.Stats.BulletSpeed, dmg, def.Color, def.Pierce, def.Knockback)
					}
				}

			case 1: // 激光
				target := findTarget(250)
				if target != nil {
					used = true
					g.WeaponTimers[id] = 0 // 重置

					// 激光瞬间造成伤害并在画图时绘制
					dir := Sub(target.Pos, g.Player.Pos).Normalize()
					// 激光长度
					length := 200.0
					endPos := Add(g.Player.Pos, Mul(dir, length))

					// 射线检测
					for _, e := range g.Enemies {
						if e.Dead {
							continue
						}
						// 简单的点到线段距离
						if DistPointLine(e.Pos, g.Player.Pos, endPos) < e.Radius+2.0 {
							g.DamageEnemy(e, dmg, true)
						}
					}

					// 添加视觉特效实体
					g.Bullets = append(g.Bullets, &Entity{
						Pos: g.Player.Pos, Vel: endPos, // Vel 用作终点
						Type: 2, SubType: 1, // 激光
						Color: def.Color, Lifetime: def.Duration,
					})
				}

			case 2: // 护盾 (生成后由 UpdateBullets 维护位置)
				// 检查是否已经有足够的护盾球
				count := 0
				for _, b := range g.Bullets {
					if b.Type == 2 && b.SubType == 2 && b.Text == id {
						count++
					}
				}
				maxCount := def.Count + (lvl - 1)
				if count < maxCount {
					used = true
					g.Bullets = append(g.Bullets, &Entity{
						Type: 2, SubType: 2, Text: id,
						Damage: dmg, Color: def.Color, Radius: 2.0, // 减小护盾尺寸
						Knockback: def.Knockback,
						Angle:     float64(count) * (math.Pi * 2 / float64(maxCount)),
						Lifetime:  9999, // 永久存在直到逻辑移除
					})
				}
			}

			if used {
				g.WeaponTimers[id] = 0
			}
		}
	}
}

func (g *Game) UpdateEnemies(dt float64) {
	// 动态清理过远敌人 (无限地图内存优化)
	cleanupDist := float64(g.PixelW) // 屏幕宽度像素作为清理距离
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

	// 生成逻辑 (在屏幕外围生成)
	spawnRate := 1.5 / g.Difficulty
	if spawnRate < 0.1 {
		spawnRate = 0.1
	}

	g.SpawnTimer += dt
	if g.SpawnTimer >= spawnRate {
		g.SpawnTimer = 0
		g.Difficulty += 0.02

		// 在屏幕视野外随机生成
		viewRadius := float64(g.PixelW/2) + 20.0
		if float64(g.PixelH/2) > viewRadius {
			viewRadius = float64(g.PixelH/2) + 20.0
		}
		spawnDist := viewRadius + 20.0 + rand.Float64()*50.0

		angle := rand.Float64() * math.Pi * 2
		pos := Add(g.Player.Pos, Vec2{math.Cos(angle) * spawnDist, math.Sin(angle) * spawnDist})

		hpMul := g.Difficulty
		var e *Entity

		// 敌人半径缩小 40%
		r := rand.Float64()
		if r < 0.5 {
			e = &Entity{Pos: pos, Color: ColHiGreen, HP: 10 * hpMul, MaxHP: 10 * hpMul, Damage: 5, SubType: 0, Radius: 1.5} // 脚本小子 (普通)
		} else if r < 0.8 {
			e = &Entity{Pos: pos, Color: ColHiMagenta, HP: 6 * hpMul, MaxHP: 6 * hpMul, Damage: 8, SubType: 1, Radius: 1.0} // 蠕虫 (快速)
		} else if r < 0.95 {
			e = &Entity{Pos: pos, Color: ColHiBlue, HP: 25 * hpMul, MaxHP: 25 * hpMul, Damage: 12, SubType: 2, Radius: 2.0} // 僵尸网络 (肉盾)
		} else {
			e = &Entity{Pos: pos, Color: ColHiRed, HP: 50 * hpMul, MaxHP: 50 * hpMul, Damage: 15, SubType: 3, Radius: 3.0} // APT (Boss)
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

		// AI 移动
		dir := Sub(g.Player.Pos, e.Pos).Normalize()
		speed := 0.0

		switch e.SubType {
		case 0:
			speed = 0.8 // 普通追踪
		case 1:
			speed = 1.5 // 快速突进
		case 2:
			speed = 0.4 // 缓慢
		case 3:
			speed = 0.6 // Boss
		}

		// 斥力
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

		// 碰撞玩家
		if Dist(e.Pos, g.Player.Pos) < e.Radius+g.Player.Radius {
			g.HitPlayer(e.Damage)
			// 碰撞反弹
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

	for _, b := range g.Bullets {
		if b.Dead {
			continue
		}

		if b.Type == 2 { // Player Bullet
			if b.SubType == 1 { // 激光
				b.Lifetime -= dt
				if b.Lifetime <= 0 {
					b.Dead = true
				}
				continue
			} else if b.SubType == 2 { // 护盾
				b.Angle += 2.0 * dt
				radius := 15.0
				offset := Vec2{math.Cos(b.Angle) * radius, math.Sin(b.Angle) * radius}
				b.Pos = Add(playerCenter, offset)

				// 护盾碰撞逻辑
				for _, e := range g.Enemies {
					if !e.Dead && Dist(b.Pos, e.Pos) < b.Radius+e.Radius {
						// 护盾持续伤害与击退
						g.DamageEnemy(e, b.Damage*dt*5.0, false)
						push := Sub(e.Pos, playerCenter).Normalize()
						e.Pos = Add(e.Pos, Mul(push, b.Knockback*0.1))
					}
				}
				continue
			}

			// 普通投射物
			b.Lifetime -= dt
			if b.Lifetime <= 0 {
				b.Dead = true
				continue
			}
			b.Pos = Add(b.Pos, b.Vel)

			// 距离销毁 (防止子弹飞太远)
			if Dist(b.Pos, g.Player.Pos) > 600 {
				b.Dead = true
				continue
			}

			// 碰撞检测
			for _, e := range g.Enemies {
				if e.Dead {
					continue
				}
				if Dist(b.Pos, e.Pos) < e.Radius+2.0 {
					g.DamageEnemy(e, b.Damage, true)

					// 击退
					push := b.Vel.Normalize()
					e.Pos = Add(e.Pos, Mul(push, b.Knockback))

					// 穿透处理
					if b.SubType < 99 { // Pierce count stored in SubType
						b.SubType--
						if b.SubType <= 0 {
							b.Dead = true
							// 命中特效
							g.SpawnParticles(b.Pos, b.Color, 3)
						}
					}
					break // 一次移动只打中一个
				}
			}
		}
	}
}

func (g *Game) UpdateParticles(dt float64) {
	for _, p := range g.Particles {
		if p.Dead {
			continue
		}

		if p.Type == 3 { // XP Orb
			// 闪烁特效
			p.Lifetime += dt * 5

			dist := Dist(p.Pos, g.Player.Pos)
			if dist < g.Stats.PickupRange {
				dir := Sub(g.Player.Pos, p.Pos).Normalize()
				// 加速吸附
				spd := (g.Stats.PickupRange - dist + 5.0) * 0.3
				p.Pos = Add(p.Pos, Mul(dir, spd))
				if dist < 3.0 {
					p.Dead = true
					g.GainXP(int(p.Damage)) // XP amount stored in Damage
				}
			}
		} else { // Visual Particle
			p.Lifetime -= dt
			if p.Lifetime <= 0 {
				p.Dead = true
				continue
			}
			p.Pos = Add(p.Pos, p.Vel)
			p.Vel = Mul(p.Vel, 0.9) // 摩擦力
		}
	}

	// 清理
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
		t.Pos.Y -= 0.5 // 向上飘
	}
}

// --- 辅助逻辑 ---

func (g *Game) SpawnBullet(pos, dir Vec2, spd, dmg float64, col string, pierce int, knockback float64) {
	g.Bullets = append(g.Bullets, &Entity{
		Pos: pos, Vel: Mul(dir, spd), Color: col,
		Type: 2, SubType: pierce, Damage: dmg, Lifetime: 3.0,
		Knockback: knockback,
	})
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

		// 掉落经验
		xpVal := 1
		if e.SubType == 2 {
			xpVal = 5
		}
		if e.SubType == 3 {
			xpVal = 50
		}

		g.Particles = append(g.Particles, &Entity{
			Pos: e.Pos, Color: ColHiCyan, Type: 3, Damage: float64(xpVal), // XP value stored in Damage
			Lifetime: 0, // Used for animation offset
		})
	}
}

func (g *Game) HitPlayer(dmg float64) {
	if g.Player.FlashTime > 0 {
		return
	}
	g.Player.HP -= dmg
	g.Player.FlashTime = 0.5
	g.SpawnFloatText(fmt.Sprintf("-%.0f", dmg), g.Player.Pos, ColRed)

	// 屏幕震动效果 (视觉上不做，太复杂)
}

func (g *Game) GainXP(amount int) {
	g.XP += amount
}

func (g *Game) SpawnFloatText(s string, pos Vec2, col string) {
	// 限制飘字数量
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
	g.NextLevelXP = int(float64(g.NextLevelXP) * 1.2)
	g.State = 2

	// 随机抽取升级
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
	}

	// 添加武器升级
	for id, def := range g.WeaponDefs {
		lvl := g.Weapons[id]
		name := def.Name
		rarity := 0
		if id == "SSH" || id == "ZERO" {
			rarity = 1
		}

		desc := "获得新武器"
		if lvl > 0 {
			desc = fmt.Sprintf("升级到 Lv.%d (伤害/数量提升)", lvl+1)
		}

		pool = append(pool, Upgrade{
			ID: id, Name: name, Description: desc, Rarity: rarity,
			Apply: func(id string) func(g *Game) {
				return func(g *Game) { g.AddWeapon(id) }
			}(id),
		})
	}

	rand.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if len(pool) > 4 {
		pool = pool[:4]
	}
	g.PendingUpgrades = pool
	g.SelectorIdx = 0
}

// --- 渲染系统 (Braille Canvas) ---

func (g *Game) Render(out io.Writer) {
	// 0. 强制清除 UI 区域的 Canvas 像素 (防止 Braille 字符干扰 UI)
	// 策略：UI 不透明，底下的游戏内容不应该生成 Braille 点阵

	// A. 顶部 HUD 区域
	hudPixelHeight := TopHudHeight * PixelScaleY
	for y := 0; y < hudPixelHeight; y++ {
		start := y * g.Canvas.Width
		end := start + g.Canvas.Width
		for i := start; i < end; i++ {
			g.Canvas.Pixels[i] = false
		}
	}

	// 移除：不再清除侧边栏背景像素，允许 Braille 画面渲染在文字空隙中

	// 清空其余部分的 Canvas
	g.Canvas.Clear()

	g.ClearBuffer(g.FrontBuffer)

	// 计算摄像机位置 (玩家位于屏幕中心)
	// Camera TopLeft Pixel Coordinate
	camX := int(g.Player.Pos.X) - g.PixelW/2
	camY := int(g.Player.Pos.Y) - g.PixelH/2

	// 辅助函数：世界坐标转屏幕像素坐标
	toScreen := func(v Vec2) (int, int, bool) {
		sx := int(v.X) - camX
		sy := int(v.Y) - camY
		if sx >= -10 && sx < g.PixelW+10 && sy >= -10 && sy < g.PixelH+10 {
			return sx, sy, true
		}
		return 0, 0, false
	}

	// 1. 渲染游戏层到 Canvas
	if g.State == 1 || g.State == 2 || g.State == 3 || g.State == 5 {
		// 粒子
		for _, p := range g.Particles {
			if px, py, ok := toScreen(p.Pos); ok {
				if p.Type == 3 { // XP
					g.Canvas.SetPixel(px, py, p.Color)
					g.Canvas.SetPixel(px+1, py, p.Color)
					g.Canvas.SetPixel(px, py+1, p.Color)
					g.Canvas.SetPixel(px+1, py+1, p.Color)
				} else {
					g.Canvas.SetPixel(px, py, p.Color)
				}
			}
		}

		// 敌人
		for _, e := range g.Enemies {
			if px, py, ok := toScreen(e.Pos); ok {
				col := e.Color
				if e.FlashTime > 0 {
					col = ColWhite
				}
				g.Canvas.DrawCircle(px, py, int(e.Radius), col)
			}
		}

		// 子弹
		for _, b := range g.Bullets {
			sx, sy, ok1 := toScreen(b.Pos)
			ex, ey, _ := toScreen(b.Vel) // Vel is used as EndPos for Laser

			if b.SubType == 1 { // 激光
				// 只要有一端在屏幕内，或者穿过屏幕，就绘制
				if ok1 {
					g.Canvas.DrawLine(sx, sy, ex, ey, b.Color)
				}
			} else if b.SubType == 2 { // 护盾
				if ok1 {
					g.Canvas.DrawCircle(sx, sy, int(b.Radius), b.Color)
				}
			} else { // 普通
				if ok1 {
					g.Canvas.SetPixel(sx, sy, b.Color)
					// 只有当尺寸允许时才画大点，现在缩小了，只画1个点或2个点
					g.Canvas.SetPixel(sx+1, sy, b.Color)
					g.Canvas.SetPixel(sx, sy+1, b.Color)
					g.Canvas.SetPixel(sx+1, sy+1, b.Color)
				}
			}
		}

		// 玩家 (始终在屏幕中心)
		pc := ColCyan
		if g.Player.FlashTime > 0 {
			pc = ColRed
		}
		cx, cy := g.PixelW/2, g.PixelH/2
		// 简单的中心点
		g.Canvas.SetPixel(cx, cy, ColWhite)
		g.Canvas.SetPixel(cx-1, cy, pc)
		g.Canvas.SetPixel(cx+1, cy, pc)
		g.Canvas.SetPixel(cx, cy-1, pc)
		g.Canvas.SetPixel(cx, cy+1, pc)
	}

	// 2. 将 Canvas 转换到 字符 Buffer
	// Braille Unicode: 0x2800 + bitmask
	for y := 0; y < g.TermH; y++ {
		for x := 0; x < g.TermW; x++ {
			baseX, baseY := x*PixelScaleX, y*PixelScaleY
			var mask rune = 0
			var mainColor string = ""

			// Helper to check pixel and update state
			check := func(offsetX, offsetY int, bit rune) {
				idx := (baseY+offsetY)*g.Canvas.Width + (baseX + offsetX)
				if g.Canvas.Pixels[idx] {
					mask |= bit
					mainColor = g.Canvas.Colors[idx]
				}
			}

			check(0, 0, 1)   // Dot 1
			check(0, 1, 2)   // Dot 2
			check(0, 2, 4)   // Dot 3
			check(1, 0, 8)   // Dot 4
			check(1, 1, 16)  // Dot 5
			check(1, 2, 32)  // Dot 6
			check(0, 3, 64)  // Dot 7
			check(1, 3, 128) // Dot 8

			if mask != 0 {
				g.FrontBuffer[y][x] = Cell{Char: 0x2800 + mask, Color: mainColor}
			}
		}
	}

	// 3. 覆盖 UI 层 (标准字符)
	// 先画 World Text (飘字)，再画 Static UI (HUD)
	// 这样 Static UI 会覆盖飘字，避免 "== 挂 1 ==" 这种飘字破坏 UI 文字的情况

	if g.State == 1 || g.State == 2 || g.State == 3 || g.State == 5 {
		// 3.1 绘制飘字 (World Layer)
		for _, t := range g.Texts {
			// 映射回终端坐标
			sx, sy, ok := toScreen(t.Pos)
			if ok {
				tx, ty := sx/PixelScaleX, sy/PixelScaleY

				// 裁剪逻辑：如果飘字位于 顶部HUD 区域，则不绘制
				inTopHud := ty < TopHudHeight
				// 对于侧边栏，因为允许显示游戏画面，所以可以允许显示飘字，
				// 但如果飘字正好叠在文字上，会被下面的 DrawHUD 覆盖，符合预期
				// 避免飘字过于混乱，可以在 HUD 文字密集区域做额外检查，但目前“层级覆盖”已足够解决文字被破坏问题

				if !inTopHud {
					if tx >= 0 && tx < g.TermW && ty >= 0 && ty < g.TermH {
						g.DrawText(tx, ty, t.Text, t.Color)
					}
				}
			}
		}

		// 3.2 绘制 HUD (Static UI Layer)
		// 绘制 HUD 函数最后执行，确保其文字内容覆盖一切，包括飘字和点阵
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

	// 4. 差异输出
	var buf bytes.Buffer
	buf.WriteString(ColReset) // 每帧重置颜色防止污染

	for y := 0; y < g.TermH; y++ {
		for x := 0; x < g.TermW; x++ {
			f := g.FrontBuffer[y][x]
			b := g.BackBuffer[y][x]

			if f.Char == CharPlaceholder {
				g.BackBuffer[y][x] = f
				continue
			}

			if f != b {
				// 移动光标
				buf.WriteString(fmt.Sprintf("\033[%d;%dH", y+1, x+1))

				if f.Char == 0 || f.Char == ' ' {
					buf.WriteString(" ")
				} else {
					buf.WriteString(f.Color)
					buf.WriteString(string(f.Char))
				}

				g.BackBuffer[y][x] = f

				// 宽字符处理
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
	g.CenterText(logoY+6, "VER 2.0 (SSH EDITION)", ColYellow)

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
	g.DrawText(10, y, "APT组织(红): 极度危险，BOSS级", ColHiRed)
	y += 2

	g.DrawText(8, y, "战术建议:", colL)
	y++
	g.DrawText(10, y, "拾取掉落的 [◆] 升级防御等级", ColHiCyan)
	y++
	g.DrawText(10, y, "组合多种武器以应对不同威胁", colR)
	y++

	g.CenterText(g.TermH-4, "按 [任意键] 返回", ColWhite)
}

func (g *Game) DrawLevelUpUI() {
	w, h := 50, 16
	bx, by := (g.TermW-w)/2, (g.TermH-h)/2
	// 清空区域
	for y := by; y < by+h; y++ {
		for x := bx; x < bx+w; x++ {
			g.FrontBuffer[y][x] = Cell{Char: ' ', Color: ColReset}
		}
	}
	g.DrawBox(bx, by, w, h, ColYellow)
	g.CenterText(by+1, ">> 系统升级可用 <<", ColHiYellow)

	for i, upg := range g.PendingUpgrades {
		col := ColHiBlack
		pre := "   "
		if i == g.SelectorIdx {
			col = ColWhite
			pre = " > "
		}
		y := by + 4 + i*3

		nameColor := col
		if upg.Rarity == 1 {
			nameColor = ColHiMagenta
		}

		g.DrawText(bx+2, y, pre+upg.Name, nameColor)
		g.DrawText(bx+6, y+1, upg.Description, ColHiBlack)
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
	// 使用逐格填充的方式绘制血条，避免字符宽度问题
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
		// 边界检查
		if startX+i < g.TermW {
			g.FrontBuffer[1][startX+i] = Cell{Char: char, Color: col}
		}
	}
	g.DrawText(startX+barLen, 1, "]", ColHiRed)
	g.DrawText(startX+barLen+2, 1, fmt.Sprintf("%.0f/%.0f", g.Player.HP, g.Player.MaxHP), ColHiWhite)

	// XP Bar
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

	// 右侧武器信息
	// 仅当屏幕宽度足够时绘制右侧面板
	if g.TermW > 50 {
		title := "== 挂载 =="
		// 1. 标题提到更靠右上，尽量不遮挡中心
		rx := g.TermW - len(title) - 2
		if rx < 0 {
			rx = 0
		}
		g.DrawText(rx, 1, title, ColHiWhite)

		// 2. 武器列表从第 2 行开始
		y := 2
		var keys []string
		for k := range g.Weapons {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			if y > g.TermH-2 { // 留出底部空间
				break
			}
			lvl := g.Weapons[k]
			def := g.WeaponDefs[k]

			// 格式调整：Lv.1 PING
			info := fmt.Sprintf("Lv.%d %s", lvl, def.Name)

			// 计算位置：右对齐
			lineX := g.TermW - len(info) - 2
			if lineX < 0 {
				lineX = 0
			}

			g.DrawText(lineX, y, info, def.Color)
			y++
		}
	}

	// 底部时间
	tm := fmt.Sprintf("%02d:%02d", int(g.TimeAlive.Minutes()), int(g.TimeAlive.Seconds())%60)
	g.CenterText(g.TermH-1, tm, ColWhite)
}

// --- 基础绘图工具 ---

func (g *Game) DrawBox(x, y, w, h int, col string) {
	if x < 0 || y < 0 || x+w > g.TermW || y+h > g.TermH {
		// 如果部分越界，简单裁剪或不绘制
		// 为简单起见，这里做一个简单的裁剪绘制
		for i := x; i < x+w; i++ {
			if i >= 0 && i < g.TermW {
				if y >= 0 && y < g.TermH {
					g.FrontBuffer[y][i] = Cell{Char: '─', Color: col}
				}
				if y+h-1 >= 0 && y+h-1 < g.TermH {
					g.FrontBuffer[y+h-1][i] = Cell{Char: '─', Color: col}
				}
			}
		}
		for i := y; i < y+h; i++ {
			if i >= 0 && i < g.TermH {
				if x >= 0 && x < g.TermW {
					g.FrontBuffer[i][x] = Cell{Char: '│', Color: col}
				}
				if x+w-1 >= 0 && x+w-1 < g.TermW {
					g.FrontBuffer[i][x+w-1] = Cell{Char: '│', Color: col}
				}
			}
		}
		if x >= 0 && y >= 0 && x < g.TermW && y < g.TermH {
			g.FrontBuffer[y][x] = Cell{Char: '┌', Color: col}
		}
		if x+w-1 >= 0 && y >= 0 && x+w-1 < g.TermW && y < g.TermH {
			g.FrontBuffer[y][x+w-1] = Cell{Char: '┐', Color: col}
		}
		if x >= 0 && y+h-1 >= 0 && x < g.TermW && y+h-1 < g.TermH {
			g.FrontBuffer[y+h-1][x] = Cell{Char: '└', Color: col}
		}
		if x+w-1 >= 0 && y+h-1 >= 0 && x+w-1 < g.TermW && y+h-1 < g.TermH {
			g.FrontBuffer[y+h-1][x+w-1] = Cell{Char: '┘', Color: col}
		}
		return
	}

	for i := x; i < x+w; i++ {
		g.FrontBuffer[y][i] = Cell{Char: '─', Color: col}
		g.FrontBuffer[y+h-1][i] = Cell{Char: '─', Color: col}
	}
	for i := y; i < y+h; i++ {
		g.FrontBuffer[i][x] = Cell{Char: '│', Color: col}
		g.FrontBuffer[i][x+w-1] = Cell{Char: '│', Color: col}
	}
	g.FrontBuffer[y][x] = Cell{Char: '┌', Color: col}
	g.FrontBuffer[y][x+w-1] = Cell{Char: '┐', Color: col}
	g.FrontBuffer[y+h-1][x] = Cell{Char: '└', Color: col}
	g.FrontBuffer[y+h-1][x+w-1] = Cell{Char: '┘', Color: col}
}

func (g *Game) DrawText(x, y int, s string, col string) {
	if y < 0 || y >= g.TermH {
		return
	}

	currX := x
	for _, r := range s {
		if currX >= g.TermW {
			break
		}

		if currX >= 0 {
			g.FrontBuffer[y][currX] = Cell{Char: r, Color: col}
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
