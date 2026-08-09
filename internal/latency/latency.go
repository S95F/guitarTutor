// Package latency implements the DSP for the round-trip latency
// calibration wizard (ROADMAP Phase 2). The wizard plays a known click
// train through the duplex output while recording the input; the hardware
// loop the user sets up (interface output cabled back into an input, or
// speaker to microphone) returns the train delayed by the full
// output-plus-input round trip. Estimate cross-correlates the captured
// signal against the click template to recover that delay in frames,
// which the app stores per device pair (internal/appconfig) and subtracts
// when aligning detected notes to the playback timeline per
// docs/DECISIONS.md D1.
//
// The package is pure DSP — slices in, numbers out. Audio I/O stays with
// the caller, so tests can run a synthetic loopback and the wizard can
// drive any audio.Backend.
package latency

import (
	"fmt"
	"math"
	"sort"
)

// Click synthesis parameters, matching the character of the engine's
// metronome click (internal/engine): a short sine burst under an
// exponential-decay envelope. The 1.5 kHz carrier sits well above guitar
// fundamentals and rings for ~9 cycles in 6 ms — enough structure for a
// sharp correlation peak, short enough that room reverb from a
// speaker-to-mic loop has died before the next click.
const (
	clickSeconds = 0.006  // burst length
	clickHz      = 1500.0 // carrier
	clickDecay   = 6.0    // decay exponent over the burst
	clickPeak    = 0.8    // normalized peak amplitude
)

// Estimation parameters.
const (
	// maxDelaySeconds bounds the positive-delay search per click. Half a
	// second is far beyond any real audio round trip; anything later
	// reads as "not this click".
	maxDelaySeconds = 0.5
	// minPeakCorr is the normalized-correlation floor for a per-click
	// peak to count as a detection. White noise against the ~300-sample
	// template tops out around 0.3 over a full search window; a real
	// click arrival scores 0.9+ even at poor SNR.
	minPeakCorr = 0.5
	// inlierFrames is how far a per-click delay estimate may sit from
	// the median and still count as agreeing with it.
	inlierFrames = 3
	// minConfidence is the confidence floor below which Estimate
	// returns an error instead of a usable offset.
	minConfidence = 0.5
)

// clickLen is the click burst length in frames at sampleRate.
func clickLen(sampleRate int) int {
	n := int(clickSeconds * float64(sampleRate))
	if n < 1 {
		n = 1
	}
	return n
}

// renderBurst prerenders one click: a clickHz sine under an
// exponential-decay envelope, normalized so the largest sample is exactly
// clickPeak.
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

// ClickTrain renders the calibration signal: n click bursts spaced exactly
// spacingFrames apart in a mono buffer of n*spacingFrames samples, so
// click i starts at frame i*spacingFrames and the last click is followed
// by its own slot of silence. Peak amplitude is ~0.8, loud enough to
// survive a speaker-to-mic loop without clipping an interface loopback.
//
// spacingFrames also bounds the delay Estimate can recover (see there);
// the wizard should use a spacing comfortably above the largest expected
// round trip — a full second is a good default. If spacingFrames is
// shorter than one burst (~6 ms), bursts are truncated to keep the
// spacing exact. Returns nil if any argument is not positive.
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

// Estimate recovers the loopback delay between played and captured.
// played is the signal sent to the output (a ClickTrain, or at least a
// buffer whose first clickLen frames hold one click, with clicks at
// i*spacingFrames for i < n); captured is the recording made while it
// played, in the same sample clock. offsetFrames is how many frames later
// the train arrived in captured than it was placed in played — always
// searched on the positive side only, up to min(500 ms, spacingFrames-1)
// per click. Delays at or beyond one spacing are indistinguishable from
// the next click arriving, so the wizard must choose spacingFrames above
// the largest delay it wants to detect. Estimate does refuse the usual
// signature of that mistake — every click matching at a tight cluster
// except the first, whose fully searched window held nothing — and
// returns an error naming the spacing rather than the aliased offset.
//
// Per click, the captured signal around the expected position is scanned
// with a normalized (Pearson) cross-correlation against the click
// template taken from played — invariant to capture gain and to any DC
// offset. The per-click delays are combined by median; confidence in
// [0, 1] is the median correlation peak scaled by the fraction of clicks
// that agree with the median within a few frames, so a tight cluster of
// strong peaks scores high and scattered or faint peaks score low.
//
// A non-nil error means the offset is not trustworthy (silence, noise,
// scattered estimates); the message tells the user what to check. The
// best-guess offset and the low confidence are still returned for
// display. Partial captures are fine: clicks that lie beyond the end of
// captured are simply excluded, and the remaining clicks carry the
// estimate.
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

	// The template is the first played click, made zero-mean. With a
	// zero-mean template the correlation numerator is independent of any
	// DC offset in the capture window, and the window variance in the
	// denominator removes it there too.
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

	// Prefix sums give every capture window's mean and energy in O(1),
	// so only the correlation numerator needs a per-lag pass.
	sum1 := make([]float64, len(captured)+1)
	sum2 := make([]float64, len(captured)+1)
	for i, s := range captured {
		v := float64(s)
		sum1[i+1] = sum1[i] + v
		sum2[i+1] = sum2[i] + v*v
	}

	maxDelay := int(maxDelaySeconds * float64(sampleRate))
	if maxDelay > spacingFrames-1 {
		// Past one spacing the next click's own arrival would correlate
		// perfectly and alias as a huge delay; cap the search below it.
		maxDelay = spacingFrames - 1
	}

	var (
		lags      []int
		peaks     []float64
		evaluated int
		// Click 0's fate feeds the aliasing check below: whether its
		// full search window fit in the capture, and where (if
		// anywhere) it was detected.
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
			continue // this click lies wholly beyond the capture
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

	// Aliasing guard. When the true delay meets or exceeds the click
	// spacing, every click's search window holds only the PREVIOUS
	// click's arrival — a tight cluster at delay-minus-spacing — while
	// click 0, with no predecessor, arrives nowhere in its window. That
	// used to cost just 1/n confidence and return a confidently wrong
	// offset. The signature is only trusted when click 0's full search
	// window fit in the capture (so its absence is evidence, not
	// truncation) and at least two later clicks agree unanimously.
	// Inside the guard click 0 contributed no inlier, so inliers counts
	// later clicks only.
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

// normCorr computes the Pearson correlation between the zero-mean
// template and the capture window starting at frame s. Because the
// template is zero-mean, subtracting the window mean from the window
// changes nothing in the numerator, so the dot product is taken directly;
// the window's mean enters only through its variance in the denominator.
// Windows with no variance (silence, pure DC) correlate as 0.
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

// medianInt returns the median of xs, sorting it in place. Even lengths
// round the average of the middle pair to the nearest integer.
func medianInt(xs []int) int {
	sort.Ints(xs)
	m := len(xs) / 2
	if len(xs)%2 == 1 {
		return xs[m]
	}
	return int(math.Round((float64(xs[m-1]) + float64(xs[m])) / 2))
}

// medianFloat returns the median of xs, sorting it in place. Even lengths
// average the middle pair.
func medianFloat(xs []float64) float64 {
	sort.Float64s(xs)
	m := len(xs) / 2
	if len(xs)%2 == 1 {
		return xs[m]
	}
	return (xs[m-1] + xs[m]) / 2
}
