package credit

// evtNode 是事件 treap 的节点。key 为时刻（UTC 秒），seq 为全局唯一序号，
// 二者共同构成排序键。val/sum 的 4 个分量含义由使用方约定。
type evtNode struct {
	key         int64
	seq         int64
	prio        uint64
	left, right *evtNode
	val, sum    [4]int64
}

// evtTreap 是按 (key, seq) 排序的随机平衡树，维护子树和，支持前缀和查询。
type evtTreap struct {
	root *evtNode
	rng  uint64
}

func newEvtTreap(seed uint64) *evtTreap { return &evtTreap{rng: seed} }

// nextPrio 用 splitmix64 生成随机优先级。
func (t *evtTreap) nextPrio() uint64 {
	t.rng += 0x9e3779b97f4a7c15
	z := t.rng
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func lessEvt(aKey, aSeq, bKey, bSeq int64) bool {
	return aKey < bKey || (aKey == bKey && aSeq < bSeq)
}

func (n *evtNode) pull() {
	n.sum = n.val
	if n.left != nil {
		for i := range n.sum {
			n.sum[i] += n.left.sum[i]
		}
	}
	if n.right != nil {
		for i := range n.sum {
			n.sum[i] += n.right.sum[i]
		}
	}
}

func rotateRight(n *evtNode) *evtNode {
	l := n.left
	n.left = l.right
	l.right = n
	n.pull()
	l.pull()
	return l
}

func rotateLeft(n *evtNode) *evtNode {
	r := n.right
	n.right = r.left
	r.left = n
	n.pull()
	r.pull()
	return r
}

func insertNode(n, x *evtNode) *evtNode {
	if n == nil {
		return x
	}
	if lessEvt(x.key, x.seq, n.key, n.seq) {
		n.left = insertNode(n.left, x)
		if n.left.prio > n.prio {
			return rotateRight(n)
		}
	} else {
		n.right = insertNode(n.right, x)
		if n.right.prio > n.prio {
			return rotateLeft(n)
		}
	}
	n.pull()
	return n
}

func eraseNode(n *evtNode, key, seq int64) *evtNode {
	if n == nil {
		return nil
	}
	if n.key == key && n.seq == seq {
		switch {
		case n.left == nil:
			return n.right
		case n.right == nil:
			return n.left
		case n.left.prio > n.right.prio:
			n = rotateRight(n)
			n.right = eraseNode(n.right, key, seq)
		default:
			n = rotateLeft(n)
			n.left = eraseNode(n.left, key, seq)
		}
	} else if lessEvt(key, seq, n.key, n.seq) {
		n.left = eraseNode(n.left, key, seq)
	} else {
		n.right = eraseNode(n.right, key, seq)
	}
	if n != nil {
		n.pull()
	}
	return n
}

// add 在 (key, seq) 处插入增量 delta。
func (t *evtTreap) add(key, seq int64, delta [4]int64) {
	x := &evtNode{key: key, seq: seq, prio: t.nextPrio(), val: delta, sum: delta}
	t.root = insertNode(t.root, x)
}

// remove 删除 (key, seq) 处的事件。
func (t *evtTreap) remove(key, seq int64) {
	t.root = eraseNode(t.root, key, seq)
}

// prefix 返回 key <= at 的所有事件的各分量和。
func (t *evtTreap) prefix(at int64) [4]int64 {
	var acc [4]int64
	n := t.root
	for n != nil {
		if n.key <= at {
			if n.left != nil {
				for i := range acc {
					acc[i] += n.left.sum[i]
				}
			}
			for i := range acc {
				acc[i] += n.val[i]
			}
			n = n.right
		} else {
			n = n.left
		}
	}
	return acc
}

// firstKeyGE 返回使分量 comp 的前缀和首次 >= threshold 的最小 key；
// 若不存在则返回 ok=false。要求各节点 val[comp] 非负且 threshold > 0。
func (t *evtTreap) firstKeyGE(comp int, threshold int64) (key int64, ok bool) {
	var acc int64 // 已跳过（严格小于当前子树）的键的分量和
	n := t.root
	for n != nil {
		var leftSum int64
		if n.left != nil {
			leftSum = n.left.sum[comp]
		}
		switch {
		case acc+leftSum >= threshold:
			n = n.left
		case acc+leftSum+n.val[comp] >= threshold:
			return n.key, true
		default:
			acc += leftSum + n.val[comp]
			n = n.right
		}
	}
	return 0, false
}
