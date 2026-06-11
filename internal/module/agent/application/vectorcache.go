package application

import "sync"

// vectorCacheEntry 是泛型有界向量缓存中的一条:领域条目 + 其 embedding 向量
// (仅内存,空表示未向量化,降级关键词/最近序)。
type vectorCacheEntry[T any] struct {
	item   T
	vector []float32
	// key 是该条目的去重键(入缓存时由 keyOf 算一次并缓存,避免去重扫描时反复
	// 重算字符串)。keyOf 为 nil 或键为空时为 ""。
	key string
}

// boundedVectorCache 是并发安全、有界、按"新→旧"语义维护的内存向量缓存,统一
// 案例库与路由样例两处的缓存实现(消除重复)。设计要点:
//
//   - 环形缓冲:add 摊还 O(1)(不再每次重建整个切片),容量上限自动淘汰最旧;
//   - 去重:可选 keyOf,新条目与已存在的同键条目视为重复——移除旧的、头插新的
//     (语义为"用新案例替换同类旧案例",避免同类重复挤占缓存与污染 few-shot);
//   - updateVector:按 idOf 回填异步算出的向量(已淘汰则静默跳过);
//   - snapshot:返回"新→旧"序的浅拷贝,供只读检索。
//
// keyOf 为 nil 表示不去重;idOf 为 nil 表示禁用 updateVector(返回即不操作)。
type boundedVectorCache[T any] struct {
	mu       sync.RWMutex
	buf      []vectorCacheEntry[T] // 环形缓冲,容量固定为 capacity
	head     int                   // 最新元素的下标(buf[head] 为最新)
	size     int                   // 当前有效元素数
	capacity int
	keyOf    func(T) string // 去重键(nil 表示不去重)
	idOf     func(T) string // 回填向量用的稳定 ID(nil 表示禁用 updateVector)
}

func newBoundedVectorCache[T any](capacity int, keyOf func(T) string, idOf func(T) string) *boundedVectorCache[T] {
	if capacity <= 0 {
		capacity = 1
	}
	return &boundedVectorCache[T]{
		buf:      make([]vectorCacheEntry[T], capacity),
		capacity: capacity,
		keyOf:    keyOf,
		idOf:     idOf,
	}
}

// indexAt 把"新→旧"的逻辑位置 pos(0 为最新)映射到底层环形缓冲下标。
// 调用方须持有锁,且保证 0 <= pos < size。
func (c *boundedVectorCache[T]) indexAt(pos int) int {
	// head 为最新;向"旧"方向即下标递减(环绕)。
	idx := c.head - pos
	idx %= c.capacity
	if idx < 0 {
		idx += c.capacity
	}
	return idx
}

// add 头插一条(成为最新)。启用去重时,先移除已存在的同键旧条目,再头插,
// 实现"同类用新条目替换旧条目"。超出容量时自动淘汰最旧。摊还 O(1)(去重时
// 需一次 O(size) 扫描查重,但仅比对预存的 key 字符串,不重算 keyOf)。
func (c *boundedVectorCache[T]) add(item T, vector []float32) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	key := ""
	if c.keyOf != nil {
		key = c.keyOf(item)
		if key != "" {
			c.removeByKeyLocked(key)
		}
	}
	c.prependLocked(vectorCacheEntry[T]{item: item, vector: vector, key: key})
}

// prependLocked 把 entry 设为最新元素。调用方须持有写锁。
func (c *boundedVectorCache[T]) prependLocked(entry vectorCacheEntry[T]) {
	if c.size == 0 {
		c.head = 0
	} else {
		c.head = (c.head + 1) % c.capacity
	}
	c.buf[c.head] = entry
	if c.size < c.capacity {
		c.size++
	}
	// size == capacity 时,head 前移已自然覆盖了最旧元素(环形淘汰),无需额外处理。
}

// removeByKeyLocked 移除首个键匹配的条目(保持其余元素的新→旧相对序)。调用方
// 须持有写锁。命中至多一个(add 始终保证同键唯一)。比对预存的 entry.key,不重算
// keyOf。key 为空不应调用本函数(add 已保证)。
func (c *boundedVectorCache[T]) removeByKeyLocked(key string) {
	if key == "" || c.size == 0 {
		return
	}
	for pos := 0; pos < c.size; pos++ {
		idx := c.indexAt(pos)
		if c.buf[idx].key != key {
			continue
		}
		c.removeAtPosLocked(pos)
		return
	}
}

// removeAtPosLocked 移除逻辑位置 pos(0 为最新)的条目,通过把更旧侧的元素整体
// 向"新"方向移动一格补位,保持新→旧紧凑。调用方须持有写锁。
func (c *boundedVectorCache[T]) removeAtPosLocked(pos int) {
	// 把 [pos+1, size) 的元素各前移一个逻辑位(覆盖 pos)。
	for cur := pos; cur < c.size-1; cur++ {
		dst := c.indexAt(cur)
		src := c.indexAt(cur + 1)
		c.buf[dst] = c.buf[src]
	}
	// 清空原最旧槽(避免持有已逻辑删除元素的引用,利于 GC),并收缩 size。
	tailIdx := c.indexAt(c.size - 1)
	c.buf[tailIdx] = vectorCacheEntry[T]{}
	c.size--
}

// updateVector 按 idOf 回填指定条目的向量(异步向量化完成后调用)。条目已被
// 淘汰或未启用 idOf 时静默跳过。
func (c *boundedVectorCache[T]) updateVector(id string, vector []float32) {
	if c == nil || c.idOf == nil || id == "" || len(vector) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for pos := 0; pos < c.size; pos++ {
		idx := c.indexAt(pos)
		if c.idOf(c.buf[idx].item) == id {
			c.buf[idx].vector = vector
			return
		}
	}
}

// removeMatching 删除所有满足 pred 的条目(保持其余元素的新→旧相对序),返回
// 删除数。用于按业务条件下架缓存条目(如质量门控:删除某 run 的全部案例)。从旧
// 向新扫描并就地补位,单次写锁内完成。pred 为 nil 时为空操作。
func (c *boundedVectorCache[T]) removeMatching(pred func(T) bool) int {
	if c == nil || pred == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	removed := 0
	// 从最旧(size-1)向最新(0)扫描:removeAtPosLocked 仅前移更旧侧元素,逆序遍历
	// 时已访问过的较新位置不受补位影响,可安全原地删除。
	for pos := c.size - 1; pos >= 0; pos-- {
		if pred(c.buf[c.indexAt(pos)].item) {
			c.removeAtPosLocked(pos)
			removed++
		}
	}
	return removed
}

// snapshot 返回当前缓存"新→旧"序的浅拷贝(空时返回 nil),供只读检索。
func (c *boundedVectorCache[T]) snapshot() []vectorCacheEntry[T] {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.size == 0 {
		return nil
	}
	out := make([]vectorCacheEntry[T], c.size)
	for pos := 0; pos < c.size; pos++ {
		out[pos] = c.buf[c.indexAt(pos)]
	}
	return out
}

// replace 整组替换缓存内容(启动预热用),items 视为"新→旧"序,超出容量截断。
// 启用去重时同键仅保留最新一条(首次出现者)。
func (c *boundedVectorCache[T]) replace(entries []vectorCacheEntry[T]) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// 重置环形缓冲。
	c.buf = make([]vectorCacheEntry[T], c.capacity)
	c.head = 0
	c.size = 0

	var seen map[string]struct{}
	if c.keyOf != nil {
		seen = make(map[string]struct{}, len(entries))
	}
	// entries 为新→旧;按该序追加到"旧"方向,使最终 buf 的新→旧序与入参一致。
	for _, entry := range entries {
		if c.size >= c.capacity {
			break
		}
		if c.keyOf != nil {
			// 重算并缓存 key(入参 entry 可能未带 key,如预热构造的裸条目)。
			entry.key = c.keyOf(entry.item)
			if entry.key != "" {
				if _, dup := seen[entry.key]; dup {
					continue
				}
				seen[entry.key] = struct{}{}
			}
		}
		c.appendOldestLocked(entry)
	}
}

// appendOldestLocked 把 entry 追加为当前最旧元素(replace 按新→旧顺序灌入时用)。
// 调用方须持有写锁,且保证 size < capacity。
func (c *boundedVectorCache[T]) appendOldestLocked(entry vectorCacheEntry[T]) {
	if c.size == 0 {
		c.head = 0
		c.buf[0] = entry
		c.size = 1
		return
	}
	// 最旧元素的下一格(向旧方向)即新的最旧槽。
	idx := c.indexAt(c.size) // size 位置正是"比当前最旧再旧一格"
	c.buf[idx] = entry
	c.size++
}
