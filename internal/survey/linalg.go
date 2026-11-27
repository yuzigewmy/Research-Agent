package survey

import (
	"math"
	"sort"
)

// householderQR computes an in-place thin QR factorization of an n-by-p matrix
// (n >= p) stored in row-major order. Returns upper-triangular R (p-by-p) and
// the first p entries of Q^T*y, plus ok=false if rank-deficient.
func householderQR(x [][]float64, y []float64) (r [][]float64, qty []float64, ok bool) {
	n := len(x)
	if n == 0 {
		return nil, nil, false
	}
	p := len(x[0])
	if n < p || p == 0 {
		return nil, nil, false
	}
	a := make([][]float64, n)
	for i := range n {
		a[i] = make([]float64, p)
		copy(a[i], x[i])
	}
	b := make([]float64, n)
	copy(b, y)

	for k := range p {
		var normSq float64
		for i := k; i < n; i++ {
			normSq += a[i][k] * a[i][k]
		}
		if normSq == 0 || math.IsNaN(normSq) || math.IsInf(normSq, 0) {
			return nil, nil, false
		}
		norm := math.Sqrt(normSq)
		sign := 1.0
		if a[k][k] < 0 {
			sign = -1.0
		}
		u0 := a[k][k] + sign*norm
		v := make([]float64, n-k)
		v[0] = 1
		vNormSq := 1.0
		for i := k + 1; i < n; i++ {
			v[i-k] = a[i][k] / u0
			vNormSq += v[i-k] * v[i-k]
		}
		beta := 2.0 / vNormSq

		a[k][k] = -sign * norm
		for i := k + 1; i < n; i++ {
			a[i][k] = 0
		}
		for j := k + 1; j < p; j++ {
			var dot float64
			for i := k; i < n; i++ {
				dot += v[i-k] * a[i][j]
			}
			scale := beta * dot
			for i := k; i < n; i++ {
				a[i][j] -= scale * v[i-k]
			}
		}
		var dotB float64
		for i := k; i < n; i++ {
			dotB += v[i-k] * b[i]
		}
		scaleB := beta * dotB
		for i := k; i < n; i++ {
			b[i] -= scaleB * v[i-k]
		}
	}

	r = make([][]float64, p)
	var maxDiag float64
	for i := range p {
		r[i] = make([]float64, p)
		copy(r[i], a[i][:p])
		if abs := math.Abs(r[i][i]); abs > maxDiag {
			maxDiag = abs
		}
	}
	if maxDiag == 0 || math.IsNaN(maxDiag) || math.IsInf(maxDiag, 0) {
		return nil, nil, false
	}
	tol := 1e-10 * maxDiag
	for i := range p {
		if math.Abs(r[i][i]) <= tol || math.IsNaN(r[i][i]) || math.IsInf(r[i][i], 0) {
			return nil, nil, false
		}
	}
	return r, b[:p], true
}

// solveUpperTriangular solves R * x = b where R is p-by-p upper triangular.
func solveUpperTriangular(r [][]float64, b []float64) ([]float64, bool) {
	p := len(r)
	x := make([]float64, p)
	for i := p - 1; i >= 0; i-- {
		sum := b[i]
		for j := i + 1; j < p; j++ {
			sum -= r[i][j] * x[j]
		}
		if r[i][i] == 0 {
			return nil, false
		}
		val := sum / r[i][i]
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return nil, false
		}
		x[i] = val
	}
	return x, true
}

// invertUpperTriangular computes R^{-1} for an upper-triangular p-by-p matrix R.
func invertUpperTriangular(r [][]float64) ([][]float64, bool) {
	p := len(r)
	inv := make([][]float64, p)
	for i := range p {
		inv[i] = make([]float64, p)
	}
	for col := range p {
		e := make([]float64, p)
		e[col] = 1
		sol, ok := solveUpperTriangular(r, e)
		if !ok {
			return nil, false
		}
		for row := range p {
			inv[row][col] = sol[row]
		}
	}
	return inv, true
}

// choleskyInvertAndLogDet decomposes symmetric positive definite matrix A = L * L^T,
// returning A^{-1} and ln(det(A)). Returns ok=false if A is not positive definite.
func choleskyInvertAndLogDet(a [][]float64) (inv [][]float64, logDet float64, ok bool) {
	p := len(a)
	if p == 0 {
		return nil, 0, false
	}
	l := make([][]float64, p)
	for i := range p {
		l[i] = make([]float64, p)
		for j := 0; j <= i; j++ {
			sum := a[i][j]
			for k := 0; k < j; k++ {
				sum -= l[i][k] * l[j][k]
			}
			if i == j {
				if sum <= 1e-12 || math.IsNaN(sum) || math.IsInf(sum, 0) {
					return nil, 0, false
				}
				l[i][j] = math.Sqrt(sum)
				logDet += 2 * math.Log(l[i][j])
			} else {
				l[i][j] = sum / l[j][j]
			}
		}
	}

	// Invert lower-triangular L.
	lInv := make([][]float64, p)
	for i := range p {
		lInv[i] = make([]float64, p)
		lInv[i][i] = 1.0 / l[i][i]
		for j := 0; j < i; j++ {
			var sum float64
			for k := j; k < i; k++ {
				sum += l[i][k] * lInv[k][j]
			}
			lInv[i][j] = -sum / l[i][i]
		}
	}

	// A^{-1} = L^{-T} * L^{-1}.
	inv = make([][]float64, p)
	for i := range p {
		inv[i] = make([]float64, p)
	}
	for i := range p {
		for j := i; j < p; j++ {
			var sum float64
			for k := j; k < p; k++ {
				sum += lInv[k][i] * lInv[k][j]
			}
			if math.IsNaN(sum) || math.IsInf(sum, 0) {
				return nil, 0, false
			}
			inv[i][j] = sum
			inv[j][i] = sum
		}
	}
	return inv, logDet, true
}

// jacobiEigen computes eigenvalues (sorted descending) and corresponding unit
// eigenvectors (columns of vecs, i.e., vecs[row][col]) of a real symmetric matrix.
func jacobiEigen(matrix [][]float64) (values []float64, vecs [][]float64) {
	p := len(matrix)
	a := make([][]float64, p)
	v := make([][]float64, p)
	for i := range p {
		a[i] = make([]float64, p)
		copy(a[i], matrix[i])
		v[i] = make([]float64, p)
		v[i][i] = 1
	}

	maxIter := max(100, 100*p*p)
	for range maxIter {
		var maxOff float64
		row, col := 0, 1
		for i := range p {
			for j := i + 1; j < p; j++ {
				if abs := math.Abs(a[i][j]); abs > maxOff {
					maxOff = abs
					row, col = i, j
				}
			}
		}
		if maxOff < 1e-13 {
			break
		}
		app, aqq, apq := a[row][row], a[col][col], a[row][col]
		tau := (aqq - app) / (2 * apq)
		var t float64
		if tau >= 0 {
			t = 1 / (tau + math.Hypot(1, tau))
		} else {
			t = -1 / (-tau + math.Hypot(1, tau))
		}
		c := 1 / math.Hypot(1, t)
		s := t * c

		a[row][row] = app - t*apq
		a[col][col] = aqq + t*apq
		a[row][col] = 0
		a[col][row] = 0
		for k := range p {
			if k == row || k == col {
				continue
			}
			akp, akq := a[k][row], a[k][col]
			a[k][row] = c*akp - s*akq
			a[row][k] = a[k][row]
			a[k][col] = s*akp + c*akq
			a[col][k] = a[k][col]
		}
		for k := range p {
			vkp, vkq := v[k][row], v[k][col]
			v[k][row] = c*vkp - s*vkq
			v[k][col] = s*vkp + c*vkq
		}
	}

	order := make([]int, p)
	for i := range p {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		return a[order[i]][order[i]] > a[order[j]][order[j]]
	})

	values = make([]float64, p)
	vecs = make([][]float64, p)
	for i := range p {
		vecs[i] = make([]float64, p)
	}
	for col, idx := range order {
		values[col] = a[idx][idx]
		// Canonicalize sign so the entry with largest magnitude is positive.
		var maxAbs float64
		sign := 1.0
		for row := range p {
			val := v[row][idx]
			if abs := math.Abs(val); abs > maxAbs {
				maxAbs = abs
				if val < 0 {
					sign = -1.0
				} else {
					sign = 1.0
				}
			}
		}
		for row := range p {
			vecs[row][col] = sign * v[row][idx]
		}
	}
	return values, vecs
}

// pearsonCorrelationMatrix computes the p-by-p Pearson correlation matrix from
// n complete observations (rows of length p). Returns ok=false if any column
// has zero variance or non-finite values.
func pearsonCorrelationMatrix(rows [][]float64, p int) (corr [][]float64, means []float64, stds []float64, ok bool) {
	n := len(rows)
	if n < 2 || p == 0 {
		return nil, nil, nil, false
	}
	means = make([]float64, p)
	for _, row := range rows {
		for j := range p {
			means[j] += row[j]
		}
	}
	for j := range p {
		means[j] /= float64(n)
	}

	stds = make([]float64, p)
	for _, row := range rows {
		for j := range p {
			d := row[j] - means[j]
			stds[j] += d * d
		}
	}
	for j := range p {
		if stds[j] <= 0 || math.IsNaN(stds[j]) || math.IsInf(stds[j], 0) {
			return nil, nil, nil, false
		}
		stds[j] = math.Sqrt(stds[j] / float64(n-1))
	}

	corr = make([][]float64, p)
	for i := range p {
		corr[i] = make([]float64, p)
		corr[i][i] = 1
	}
	denom := float64(n - 1)
	for i := range p {
		for j := i + 1; j < p; j++ {
			var sum float64
			for _, row := range rows {
				sum += (row[i] - means[i]) * (row[j] - means[j])
			}
			r := sum / (denom * stds[i] * stds[j])
			if math.IsNaN(r) || math.IsInf(r, 0) {
				return nil, nil, nil, false
			}
			r = max(-1.0, min(1.0, r))
			corr[i][j] = r
			corr[j][i] = r
		}
	}
	return corr, means, stds, true
}

// chiSquareUpperTail returns P(Chi2_df >= x) = Q(df/2, x/2) using regularized
// upper incomplete gamma series/continued fraction expansion.
func chiSquareUpperTail(x float64, df int) float64 {
	if df <= 0 || x <= 0 || math.IsNaN(x) {
		return 1
	}
	if math.IsInf(x, 1) {
		return 0
	}
	a := float64(df) / 2.0
	z := x / 2.0
	if z < a+1.0 {
		return max(0, min(1, 1-gammaPSeries(a, z)))
	}
	return max(0, min(1, gammaQContinuedFraction(a, z)))
}

func gammaPSeries(a, z float64) float64 {
	lgamma, _ := math.Lgamma(a)
	ap := a
	term := 1.0 / a
	sum := term
	for range 1000 {
		ap += 1.0
		term *= z / ap
		sum += term
		if math.Abs(term) <= math.Abs(sum)*1e-14 {
			break
		}
	}
	return sum * math.Exp(-z+a*math.Log(z)-lgamma)
}

func gammaQContinuedFraction(a, z float64) float64 {
	const tiny = 1e-30
	lgamma, _ := math.Lgamma(a)
	b := z + 1.0 - a
	c := 1.0 / tiny
	d := 1.0 / b
	h := d
	for i := 1; i <= 1000; i++ {
		an := -float64(i) * (float64(i) - a)
		b += 2.0
		d = an*d + b
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = b + an/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1.0 / d
		del := d * c
		h *= del
		if math.Abs(del-1.0) <= 1e-14 {
			break
		}
	}
	return math.Exp(-z+a*math.Log(z)-lgamma) * h
}
