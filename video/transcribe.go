package video

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// Transcribe turns a WAV from Fetch into text with whisper.cpp, and returns
// the name of the model it used.
//
// The transcript is raw speech-to-text: misheard words are expected ("ground
// sugar" for "ground turkey"), and the model that structures the recipe is
// told so.
func Transcribe(ctx context.Context, t Tools, wavPath string) (text, model string, err error) {
	whisper, err := t.Find(ToolWhisper)
	if err != nil {
		return "", "", err
	}
	modelPath, model, err := t.ModelPath()
	if err != nil {
		return "", "", err
	}

	// -otxt writes the transcript to <outBase>.txt rather than stdout, which
	// also carries whisper's progress lines on some builds.
	outBase := strings.TrimSuffix(wavPath, ".wav") + ".transcript"
	if _, err := run(ctx, whisper, transcribeArgs(modelPath, model, wavPath, outBase)...); err != nil {
		return "", model, fmt.Errorf("video: transcribe: %w", err)
	}
	b, err := os.ReadFile(outBase + ".txt")
	if err != nil {
		return "", model, fmt.Errorf("video: transcribe: %w", err)
	}
	return strings.Join(strings.Fields(string(b)), " "), model, nil
}

func transcribeArgs(modelPath, model, wavPath, outBase string) []string {
	lang := "auto"
	if EnglishOnly(model) {
		lang = "en"
	}
	return []string{
		"-m", modelPath,
		"-f", wavPath,
		"-l", lang,
		"-t", strconv.Itoa(threads()),
		"-nt", "-np",
		"-otxt", "-of", outBase,
	}
}

// threads leaves a core free so the rest of the app (and the machine)
// stays responsive while a transcription runs.
func threads() int {
	n := runtime.NumCPU() - 1
	if n < 1 {
		return 1
	}
	if n > 8 {
		return 8 // whisper.cpp stops getting faster past ~8 threads
	}
	return n
}
