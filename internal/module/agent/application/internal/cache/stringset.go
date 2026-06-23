package cache

import "sync"

// BoundedStringSet 是并发安全、有界、FIFO 淘汰的字符串集合:用于记录少量短期
// "标记"(如已被负反馈抑制的 runID),覆盖异步窗口期内的竞态判定。容量满时淘汰
// 最早加入的一条(环形覆盖),保证内存上界恒定。
//
// 它不是通用 LRU——命中查询不刷新位置,仅做"近期是否标记过"的存在性判定,这对
// 短窗口竞态(秒级)已足够,且实现极简、无额外分配。
type BoundedStringSet struct {
	mu       sync.Mutex
	set      map[string]struct{}
	ring     []string // 按加入顺序记录,用于 FIFO 淘汰
	head     int      // 下一个写入(淘汰)位置
	capacity int
}

// NewBoundedStringSet 构造容量为 capacity 的有界集合。capacity<=0 时回退为 1。
func NewBoundedStringSet(capacity int) *BoundedStringSet {
	if capacity <= 0 {
		capacity = 1
	}
	return &BoundedStringSet{
		set:      make(map[string]struct{}, capacity),
		ring:     make([]string, capacity),
		capacity: capacity,
	}
}

// Add 标记一个值。已存在则不重复占位;集合满时淘汰最早加入的值。空串忽略。
func (s *BoundedStringSet) Add(value string) {
	if s == nil || value == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.set[value]; exists {
		return
	}
	// 淘汰 head 槽上的旧值(若有),再写入新值,使 set 与 ring 始终一致。
	if old := s.ring[s.head]; old != "" {
		delete(s.set, old)
	}
	s.ring[s.head] = value
	s.set[value] = struct{}{}
	s.head = (s.head + 1) % s.capacity
}

// Contains 判定值是否在集合中。空串恒为 false。
func (s *BoundedStringSet) Contains(value string) bool {
	if s == nil || value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.set[value]
	return exists
}
