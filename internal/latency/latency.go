package latency

import (
	"fmt"
	"math"
	"sort"
)

const (
	clickSeconds = 0.006
	clickHz      = 1500.0
	clickDecay   = 6.0
	clickPeak    = 0.8
)

const (
	maxDelaySeconds = 0.5

	minPeakCorr = 0.5

	inlierFrames = 3

	minConfidence = 0.5
)

func clickLen(sampleRate int) int {
	n := int(clickSeconds * float64(sampleRate))
	if n < 1 {
		n = 1
	}
	return n
}

func renderBurst(sampleRate int) []float32 {
	n := clickLen(sampleRate)
	buf := make([]float32, n)
	peak := 0.0
	for i := range buf {
		env := math.Exp(-clickDecay * float64(i) / float64(n))
		s := env * math.Sin(2*math.Pi*clickHz*float64(i)/float64(sampleRate))
		buf[i] = float32(s)
		if a := math.Abs(s); a > peak {
			peak = a
		}
	}
	if peak > 0 {
		g := float32(clickPeak / peak)
		for i := range buf {
			buf[i] *= g
		}
	}
	return buf
}

func ClickTrain(sampleRate, n, spacingFrames int) []float32 {
	if sampleRate <= 0 || n <= 0 || spacingFrames <= 0 {
		return nil
	}
	burst := renderBurst(sampleRate)
	out := make([]float32, n*spacingFrames)
	for i := 0; i < n; i++ {
		copy(out[i*spacingFrames:(i+1)*spacingFrames], burst)
	}
	return out
}

func Estimate(sampleRate int, played, captured []float32, spacingFrames, n int) (offsetFrames int, confidence float64, err error) {
	if sampleRate <= 0 {
		return 0, 0, fmt.Errorf("latency: sample rate must be positive, got %d", sampleRate)
	}
	if n <= 0 || spacingFrames <= 0 {
		return 0, 0, fmt.Errorf("latency: click count and spacing must be positive, got n=%d spacing=%d", n, spacingFrames)
	}
	tmplLen := clickLen(sampleRate)
	if len(played) < tmplLen {
		return 0, 0, fmt.Errorf("latency: played signal has %d frames, need at least one %d-frame click", len(played), tmplLen)
	}

	tmpl := make([]float64, tmplLen)
	var tmplMean float64
	for _, s := range played[:tmplLen] {
		tmplMean += float64(s)
	}
	tmplMean /= float64(tmplLen)
	var tmplEnergy float64
	for i, s := range played[:tmplLen] {
		tmpl[i] = float64(s) - tmplMean
		tmplEnergy += tmpl[i] * tmpl[i]
	}
	if tmplEnergy == 0 {
		return 0, 0, fmt.Errorf("latency: played signal is silent; pass the rendered click train")
	}

	sum1 := make([]float64, len(captured)+1)
	sum2 := make([]float64, len(captured)+1)
	for i, s := range captured {
		v := float64(s)
		sum1[i+1] = sum1[i] + v
		sum2[i+1] = sum2[i] + v*v
	}

	maxDelay := int(maxDelaySeconds * float64(sampleRate))
	if maxDelay > spacingFrames-1 {

		maxDelay = spacingFrames - 1
	}

	var (
		lags      []int
		peaks     []float64
		evaluated int

		click0Full     bool
		click0Detected bool
		click0Lag      int
	)
	for i := 0; i < n; i++ {
		base := i * spacingFrames
		limit := maxDelay
		if fit := len(captured) - tmplLen - base; fit < limit {
			limit = fit
		}
		if limit < 0 {
			continue
		}
		evaluated++
		if i == 0 {
			click0Full = limit == maxDelay
		}
		bestR, bestLag := math.Inf(-1), 0
		for d := 0; d <= limit; d++ {
			if r := normCorr(captured, base+d, tmpl, tmplEnergy, sum1, sum2); r > bestR {
				bestR, bestLag = r, d
			}
		}
		if bestR >= minPeakCorr {
			lags = append(lags, bestLag)
			peaks = append(peaks, bestR)
			if i == 0 {
				click0Detected, click0Lag = true, bestLag
			}
		}
	}
	if evaluated == 0 {
		return 0, 0, fmt.Errorf("latency: captured signal (%d frames) is too short to contain any of the %d expected clicks; record for at least the length of the click train", len(captured), n)
	}
	if len(lags) == 0 {
		return 0, 0, fmt.Errorf("latency: no click arrivals found in the captured signal: check that the loopback (cable, or speaker to mic) is connected, that the right capture device is selected, and that the input isn't muted")
	}

	offset := medianInt(lags)
	medianPeak := medianFloat(peaks)
	inliers := 0
	for _, l := range lags {
		if d := l - offset; -inlierFrames <= d && d <= inlierFrames {
			inliers++
		}
	}
	confidence = medianPeak * float64(inliers) / float64(evaluated)

	click0Inlier := click0Detected &&
		click0Lag-offset >= -inlierFrames && click0Lag-offset <= inlierFrames
	if click0Full && !click0Inlier && evaluated-1 >= 2 && inliers == evaluated-1 {
		return offset, confidence, fmt.Errorf("latency: the round-trip delay looks like it meets or exceeds the click spacing (%d frames, %.0f ms): every click after the first arrived at a consistent delay but the first never did, so each match is probably the previous click aliased one spacing late — increase the click spacing beyond the largest plausible delay, then run calibration again", spacingFrames, 1000*float64(spacingFrames)/float64(sampleRate))
	}

	if confidence < minConfidence {
		return offset, confidence, fmt.Errorf("latency: calibration is unreliable (confidence %.2f): clicks were detected only faintly or at inconsistent delays — check the loopback connection and the input level, then run calibration again", confidence)
	}
	return offset, confidence, nil
}

func normCorr(captured []float32, s int, tmpl []float64, tmplEnergy float64, sum1, sum2 []float64) float64 {
	l := len(tmpl)
	winSum := sum1[s+l] - sum1[s]
	winMean := winSum / float64(l)
	winVar := (sum2[s+l] - sum2[s]) - winMean*winSum
	if winVar <= 0 {
		return 0
	}
	var dot float64
	win := captured[s : s+l]
	for j, tv := range tmpl {
		dot += tv * float64(win[j])
	}
	return dot / math.Sqrt(tmplEnergy*winVar)
}

func medianInt(xs []int) int {
	sort.Ints(xs)
	m := len(xs) / 2
	if len(xs)%2 == 1 {
		return xs[m]
	}
	return int(math.Round((float64(xs[m-1]) + float64(xs[m])) / 2))
}

func medianFloat(xs []float64) float64 {
	sort.Float64s(xs)
	m := len(xs) / 2
	if len(xs)%2 == 1 {
		return xs[m]
	}
	return (xs[m-1] + xs[m]) / 2
}
