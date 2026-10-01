// Contextual-bandit routing (proposal 4.2.3).
//
// When selfish routing crosses its price-of-anarchy threshold (or a task is high stakes from the
// start), Neural Hive switches to LinUCB (Li, Chu, Langford & Schapire, 2010). Each agent keeps
// the ridge-regression statistics A_a and b_a; the router picks the agent maximising
// theta_a . x + alpha * sqrt(x . A_a^-1 . x), then updates only the chosen agent from the
// observed reward. The bonus term is what makes the router keep exploring agents it has not
// tried much, and the model adapts on its own when a previously reliable agent starts slipping.
package routing

import "math"

// LinUCB is a disjoint-linear contextual bandit over agents.
type LinUCB struct {
	Alpha float64
	Dim   int
	A     map[string][][]float64
	b     map[string][]float64
	Order []string
}

// NewLinUCB creates a bandit with alpha exploration weight and a d-dimensional context.
func NewLinUCB(dim int, alpha float64) *LinUCB {
	return &LinUCB{Alpha: alpha, Dim: dim, A: map[string][][]float64{}, b: map[string][]float64{}}
}

func (l *LinUCB) ensure(arm string) {
	if _, ok := l.A[arm]; ok {
		return
	}
	a := make([][]float64, l.Dim)
	for i := range a {
		a[i] = make([]float64, l.Dim)
		a[i][i] = 1.0
	}
	l.A[arm] = a
	l.b[arm] = make([]float64, l.Dim)
	l.Order = append(l.Order, arm)
}

// Score returns the UCB value of an arm for context x.
func (l *LinUCB) Score(arm string, x []float64) float64 {
	l.ensure(arm)
	inv, err := matInverse(l.A[arm])
	if err != nil {
		return math.Inf(-1)
	}
	theta := matVec(inv, l.b[arm])
	mean := dot(theta, x)
	ax := matVec(inv, x)
	bonus := math.Sqrt(math.Max(0, dot(x, ax)))
	return mean + l.Alpha*bonus
}

// Select chooses the arm with the highest UCB value. Ties break on the first arm seen.
func (l *LinUCB) Select(x []float64, arms []string) string {
	best := ""
	bestScore := math.Inf(-1)
	for _, a := range arms {
		s := l.Score(a, x)
		if s > bestScore {
			bestScore = s
			best = a
		}
	}
	return best
}

// Update applies the LinUCB update for the chosen arm: A += x x^T ; b += r x.
func (l *LinUCB) Update(arm string, x []float64, reward float64) {
	l.ensure(arm)
	a := l.A[arm]
	for i := 0; i < l.Dim; i++ {
		for j := 0; j < l.Dim; j++ {
			a[i][j] += x[i] * x[j]
		}
		l.b[arm][i] += reward * x[i]
	}
}

// Arms returns the arms seen so far in first-seen order.
func (l *LinUCB) Arms() []string { return l.Order }

func dot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func matVec(m [][]float64, v []float64) []float64 {
	out := make([]float64, len(m))
	for i := range m {
		out[i] = dot(m[i], v)
	}
	return out
}

// matInverse inverts a small square matrix by Gauss-Jordan elimination with partial pivoting.
func matInverse(in [][]float64) ([][]float64, error) {
	n := len(in)
	a := make([][]float64, n)
	for i := range a {
		a[i] = make([]float64, 2*n)
		for j := 0; j < n; j++ {
			a[i][j] = in[i][j]
		}
		a[i][n+i] = 1.0
	}
	for col := 0; col < n; col++ {
		piv := col
		for r := col + 1; r < n; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[piv][col]) {
				piv = r
			}
		}
		if math.Abs(a[piv][col]) < 1e-12 {
			return nil, errSingular
		}
		a[col], a[piv] = a[piv], a[col]
		p := a[col][col]
		for j := 0; j < 2*n; j++ {
			a[col][j] /= p
		}
		for r := 0; r < n; r++ {
			if r == col {
				continue
			}
			f := a[r][col]
			if f == 0 {
				continue
			}
			for j := 0; j < 2*n; j++ {
				a[r][j] -= f * a[col][j]
			}
		}
	}
	out := make([][]float64, n)
	for i := range out {
		out[i] = make([]float64, n)
		copy(out[i], a[i][n:])
	}
	return out, nil
}

type singularError struct{}

func (singularError) Error() string { return "singular matrix" }

var errSingular = singularError{}
