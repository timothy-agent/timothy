package api

import (
	"io"
	"net/http"

	"github.com/SumonMSelim/timothy/internal/platform/whisper"
)

// transcribeBodyLimit caps the uploaded audio clip: local speech-to-
// text is for one turn's dictation, not bulk file transcription.
const transcribeBodyLimit = 25 << 20 // 25MB

// registerTranscribe mounts the mic-input transcription route. nil
// leaves it unmounted (WHISPER_URL not set).
func (a *API) registerTranscribe(handle func(pattern string, h http.Handler), client *http.Client, whisperURL string) {
	if whisperURL == "" {
		return
	}
	handle("POST /v1/transcribe", a.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, transcribeBodyLimit)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			jsonError(w, http.StatusRequestEntityTooLarge, "bad_request", "audio body too large or unreadable")
			return
		}
		if len(raw) == 0 {
			jsonError(w, http.StatusBadRequest, "bad_request", "empty request body")
			return
		}
		text, err := whisper.Transcribe(r.Context(), client, whisperURL, raw)
		if err != nil {
			jsonError(w, http.StatusBadGateway, "transcribe_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"text": text})
	})))
}
