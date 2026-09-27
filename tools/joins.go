package tools

// joins is a union-find over a list's positions: which members have been
// joined, directly or through others. The duplicate audits build their
// groups with it, one pair at a time, so a pair kept apart stays apart
// however the members around it are joined.
type joins []int

func newJoins(n int) joins {
	j := make(joins, n)
	for i := range j {
		j[i] = i
	}

	return j
}

// find is the member standing for i's group.
func (j joins) find(i int) int {
	for j[i] != i {
		j[i] = j[j[i]]
		i = j[i]
	}

	return i
}

// join puts a's and b's groups together, and says which member now stands
// for both.
func (j joins) join(a, b int) int {
	ra, rb := j.find(a), j.find(b)
	if ra != rb {
		j[rb] = ra
	}

	return ra
}

// groups are the members of each group of two or more, in list order, the
// groups in the order their first members come.
func (j joins) groups() [][]int {
	byRoot := map[int][]int{}
	var roots []int
	for i := range j {
		r := j.find(i)
		if byRoot[r] == nil {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], i)
	}
	var out [][]int
	for _, r := range roots {
		if len(byRoot[r]) > 1 {
			out = append(out, byRoot[r])
		}
	}

	return out
}
