package httpapi

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/nexusriot/omegagrid-agent-go/internal/memory"
)

type memoryAddRequest struct {
	Text      string         `json:"text"`
	Meta      map[string]any `json:"meta"`
	Namespace string         `json:"namespace"`
	// TTLSeconds bounds the memory's lifetime; 0 = MEMORY_DEFAULT_TTL_DAYS,
	// negative = never expires.
	TTLSeconds float64 `json:"ttl_seconds"`
}

func (d *Deps) handleMemoryAdd(w http.ResponseWriter, r *http.Request) {
	var req memoryAddRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Text == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	res, err := d.Memory.AddMemoryOpts(req.Text, req.Meta, memory.AddOptions{
		Namespace: req.Namespace,
		TTL:       time.Duration(req.TTLSeconds * float64(time.Second)),
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"memory_id": res.MemoryID,
		"skipped":   res.Skipped,
		"reason":    res.Reason,
	})
}

type memorySearchRequest struct {
	Query string `json:"query"`
	K     int    `json:"k"`
	// Namespaces restricts the search; omitted means every namespace.
	Namespaces []string `json:"namespaces"`
}

func (d *Deps) handleMemorySearch(w http.ResponseWriter, r *http.Request) {
	var req memorySearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	if req.K == 0 {
		req.K = 5
	}
	var res *memory.SearchResult
	var err error
	if req.Namespaces != nil {
		res, err = d.Memory.SearchMemoryIn(req.Query, req.K, req.Namespaces)
	} else {
		res, err = d.Memory.SearchMemory(req.Query, req.K)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hits": res.Hits})
}

// handleMemoryList returns a newest-first page of stored memories.
// Query params: limit (default 100, 0 = all), offset (default 0), namespace
// (present = only that namespace; empty value = the default namespace),
// include_expired.
func (d *Deps) handleMemoryList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := queryInt(q.Get("limit"), 100)
	offset := queryInt(q.Get("offset"), 0)
	var f memory.ListFilter
	if q.Has("namespace") {
		ns := q.Get("namespace")
		f.Namespace = &ns
	}
	f.IncludeExpired = q.Get("include_expired") == "true" || q.Get("include_expired") == "1"

	hits, total, err := d.Memory.ListMemoriesFilter(f, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"hits":   hits,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// handleMemoryGet returns a single stored memory by ID.
func (d *Deps) handleMemoryGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	hit, err := d.Memory.GetMemory(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if hit == nil {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hit": hit})
}

// handleMemoryDelete removes a stored memory by ID.
func (d *Deps) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existed, err := d.Memory.DeleteMemory(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !existed {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": id})
}

type memoryUpdateRequest struct {
	Text *string        `json:"text"`
	Meta map[string]any `json:"meta"`
	// TTLSeconds, when present, replaces the expiry: >0 sets it that far
	// from now, <=0 removes it.
	TTLSeconds *float64 `json:"ttl_seconds"`
}

// handleMemoryUpdate edits a memory in place (PUT /api/memory/{id}).
func (d *Deps) handleMemoryUpdate(w http.ResponseWriter, r *http.Request) {
	var req memoryUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	meta := req.Meta
	if req.TTLSeconds != nil {
		if meta == nil {
			meta = map[string]any{}
		}
		if *req.TTLSeconds > 0 {
			exp := time.Now().Add(time.Duration(*req.TTLSeconds * float64(time.Second)))
			meta["expires_at"] = strconv.FormatFloat(float64(exp.UnixMilli())/1000, 'f', 3, 64)
		} else {
			meta["expires_at"] = nil
		}
	}
	hit, err := d.Memory.UpdateMemory(chi.URLParam(r, "id"), req.Text, meta)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if hit == nil {
		writeError(w, http.StatusNotFound, "memory not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "hit": hit})
}

// handleMemoryNamespaces lists namespaces with their live memory counts.
func (d *Deps) handleMemoryNamespaces(w http.ResponseWriter, _ *http.Request) {
	counts, err := d.Memory.Namespaces()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type row struct {
		Namespace string `json:"namespace"`
		Count     int    `json:"count"`
	}
	out := make([]row, 0, len(counts))
	for ns, n := range counts {
		out = append(out, row{ns, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Namespace < out[j].Namespace })
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "namespaces": out})
}

// handleMemoryExport streams every memory as JSON Lines. ?embeddings=1 adds
// the vectors, which only an importer using the same embeddings model can
// reuse; without them the importer re-embeds.
func (d *Deps) handleMemoryExport(w http.ResponseWriter, r *http.Request) {
	withEmb := r.URL.Query().Get("embeddings") == "1" || r.URL.Query().Get("embeddings") == "true"
	recs, err := d.Memory.ExportMemories(withEmb)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="memories.jsonl"`)
	enc := json.NewEncoder(w)
	for _, rec := range recs {
		if err := enc.Encode(rec); err != nil {
			return
		}
	}
}

// maxImportBytes caps an import body. The global 4 MiB request cap is lifted
// for this route only: an export with embeddings is ~10 KB per memory.
const maxImportBytes = 64 << 20

// handleMemoryImport adds the records of a JSON Lines export (or a JSON
// array of records), deduplicating like any other add.
func (d *Deps) handleMemoryImport(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, maxImportBytes)
	recs, err := decodeImport(body)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rep := d.Memory.ImportMemories(recs)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "report": rep})
}

func decodeImport(r io.Reader) ([]memory.ExportRecord, error) {
	br := bufio.NewReader(r)
	first, err := br.Peek(1)
	for err == nil && (first[0] == ' ' || first[0] == '\n' || first[0] == '\r' || first[0] == '\t') {
		_, _ = br.ReadByte()
		first, err = br.Peek(1)
	}
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(br)
	if first[0] == '[' {
		var recs []memory.ExportRecord
		if err := dec.Decode(&recs); err != nil {
			return nil, fmt.Errorf("decode records: %w", err)
		}
		return recs, nil
	}
	var recs []memory.ExportRecord
	for {
		var rec memory.ExportRecord
		if err := dec.Decode(&rec); err == io.EOF {
			return recs, nil
		} else if err != nil {
			return nil, fmt.Errorf("record %d: %w", len(recs)+1, err)
		}
		recs = append(recs, rec)
	}
}
