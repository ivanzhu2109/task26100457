package ledger

import "math"

// treapKey 为二叉搜索树的排序键，按 (a, b) 字典序比较。
// a 通常为时间戳（UTC 秒），b 为内部序号，用于区分同一时刻的多个事件。
type treapKey struct {
	a int64
	b int64
}

func keyLess(x, y treapKey) bool {
	if x.a != y.a {
		return x.a < y.a
	}
	return x.b < y.b
}

// prio 由键确定性派生优先级，避免依赖全局随机源。
func keyPrio(key treapKey) uint64 {
	z := uint64(key.a)*0x9E3779B97F4A7C15 + uint64(key.b)*0xC2B2AE3D27D4EB4F + 0x165667B19E3779F9
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// sumTreap 是按 treapKey 排序、维护子树值求和的 Treap。
// 用于各类“截至某时刻的累计值”前缀查询。
type sumTreap struct {
	root *sumNode
}

type sumNode struct {
	key   treapKey
	val   int64
	sum   int64
	prio  uint64
	left  *sumNode
	right *sumNode
}

func sumOf(n *sumNode) int64 {
	if n == nil {
		return 0
	}
	return n.sum
}

func (n *sumNode) pull() {
	n.sum = sumOf(n.left) + n.val + sumOf(n.right)
}

func (t *sumTreap) insert(key treapKey, val int64) {
	t.root = sumInsert(t.root, &sumNode{key: key, val: val, sum: val, prio: keyPrio(key)})
}

func sumInsert(root, n *sumNode) *sumNode {
	if root == nil {
		return n
	}
	if keyLess(n.key, root.key) {
		root.left = sumInsert(root.left, n)
		if root.left.prio < root.prio {
			return sumRotateRight(root)
		}
	} else {
		root.right = sumInsert(root.right, n)
		if root.right.prio < root.prio {
			return sumRotateLeft(root)
		}
	}
	root.pull()
	return root
}

func sumRotateRight(n *sumNode) *sumNode {
	l := n.left
	n.left = l.right
	l.right = n
	n.pull()
	l.pull()
	return l
}

func sumRotateLeft(n *sumNode) *sumNode {
	r := n.right
	n.right = r.left
	r.left = n
	n.pull()
	r.pull()
	return r
}

func (t *sumTreap) erase(key treapKey) {
	t.root = sumErase(t.root, key)
}

func sumErase(root *sumNode, key treapKey) *sumNode {
	if root == nil {
		return nil
	}
	if keyLess(key, root.key) {
		root.left = sumErase(root.left, key)
	} else if keyLess(root.key, key) {
		root.right = sumErase(root.right, key)
	} else {
		if root.left == nil {
			return root.right
		}
		if root.right == nil {
			return root.left
		}
		if root.left.prio < root.right.prio {
			root = sumRotateRight(root)
			root.right = sumErase(root.right, key)
		} else {
			root = sumRotateLeft(root)
			root.left = sumErase(root.left, key)
		}
		root.pull()
		return root
	}
	root.pull()
	return root
}

// prefixSum 返回所有 key <= 给定键的节点的值之和。
func (t *sumTreap) prefixSum(key treapKey) int64 {
	var acc int64
	n := t.root
	for n != nil {
		if keyLess(key, n.key) {
			n = n.left
		} else {
			acc += sumOf(n.left) + n.val
			n = n.right
		}
	}
	return acc
}

// timeAtPrefix 返回值的前缀和首次达到 target（>=）的节点的 key.a。
// 仅当所有节点值非负时语义正确；不存在时返回 false。
func (t *sumTreap) timeAtPrefix(target int64) (int64, bool) {
	n := t.root
	var acc int64 // 当前节点之前所有节点的值之和
	for n != nil {
		leftSum := sumOf(n.left)
		switch {
		case acc+leftSum >= target:
			n = n.left
		case acc+leftSum+n.val >= target:
			return n.key.a, true
		default:
			acc += leftSum + n.val
			n = n.right
		}
	}
	return 0, false
}

// maxTreap 是按 treapKey 排序、维护子树最大值的 Treap。
// 用于“起始时刻 <= t 的区间中最大的结束时刻”这类前缀最大值查询。
type maxTreap struct {
	root *maxNode
}

type maxNode struct {
	key   treapKey
	val   int64
	max   int64
	prio  uint64
	left  *maxNode
	right *maxNode
}

func maxOf(n *maxNode) int64 {
	if n == nil {
		return math.MinInt64
	}
	return n.max
}

func (n *maxNode) pull() {
	m := n.val
	if maxOf(n.left) > m {
		m = maxOf(n.left)
	}
	if maxOf(n.right) > m {
		m = maxOf(n.right)
	}
	n.max = m
}

func (t *maxTreap) insert(key treapKey, val int64) {
	t.root = maxInsert(t.root, &maxNode{key: key, val: val, max: val, prio: keyPrio(key)})
}

func maxInsert(root, n *maxNode) *maxNode {
	if root == nil {
		return n
	}
	if keyLess(n.key, root.key) {
		root.left = maxInsert(root.left, n)
		if root.left.prio < root.prio {
			return maxRotateRight(root)
		}
	} else {
		root.right = maxInsert(root.right, n)
		if root.right.prio < root.prio {
			return maxRotateLeft(root)
		}
	}
	root.pull()
	return root
}

func maxRotateRight(n *maxNode) *maxNode {
	l := n.left
	n.left = l.right
	l.right = n
	n.pull()
	l.pull()
	return l
}

func maxRotateLeft(n *maxNode) *maxNode {
	r := n.right
	n.right = r.left
	r.left = n
	n.pull()
	r.pull()
	return r
}

func (t *maxTreap) erase(key treapKey) {
	t.root = maxErase(t.root, key)
}

func maxErase(root *maxNode, key treapKey) *maxNode {
	if root == nil {
		return nil
	}
	if keyLess(key, root.key) {
		root.left = maxErase(root.left, key)
	} else if keyLess(root.key, key) {
		root.right = maxErase(root.right, key)
	} else {
		if root.left == nil {
			return root.right
		}
		if root.right == nil {
			return root.left
		}
		if root.left.prio < root.right.prio {
			root = maxRotateRight(root)
			root.right = maxErase(root.right, key)
		} else {
			root = maxRotateLeft(root)
			root.left = maxErase(root.left, key)
		}
		root.pull()
		return root
	}
	root.pull()
	return root
}

// prefixMax 返回所有 key <= 给定键的节点的值的最大值；空时返回 math.MinInt64。
func (t *maxTreap) prefixMax(key treapKey) int64 {
	best := int64(math.MinInt64)
	n := t.root
	for n != nil {
		if keyLess(key, n.key) {
			n = n.left
		} else {
			if maxOf(n.left) > best {
				best = maxOf(n.left)
			}
			if n.val > best {
				best = n.val
			}
			n = n.right
		}
	}
	return best
}
