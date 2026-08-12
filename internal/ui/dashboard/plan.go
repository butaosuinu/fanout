package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/butaosuinu/fanout/internal/core/backend"
)

// planCaptureLines は plan 抽出のために遡る通常スクリーン履歴の行数。peek の
// 「直近の出力」と違い plan ブロック全体が必要なので、固定で深めに取る
// (クエリ可変にはしない — 上限なしの履歴 capture を外から指定させない)。
const planCaptureLines = 2000

const (
	planOpenTag  = "<proposed_plan>"
	planCloseTag = "</proposed_plan>"
)

// planResponse is the GET /api/plan wire contract the SPA consumes. Found
// false (with empty Plan) is a successful 200: the pane is a plan-mode pane
// but no complete plan block is currently in the capturable output (not yet
// proposed, or scrolled out of the alternate screen).
type planResponse struct {
	PaneID     string `json:"paneId"`
	CapturedAt string `json:"capturedAt"` // RFC3339 UTC
	Found      bool   `json:"found"`
	Plan       string `json:"plan"`
}

// handlePlan serves GET /api/plan?pane=%N: a one-shot read of the last
// complete <proposed_plan> block in one recorded Codex Plan Mode pane.
// Validation, headers, HEAD handling, and the error contract are shared with
// /api/peek (requireLivePane / beginPaneCapture / peekError); the only
// plan-specific gate is PlanMode plus Agent == "codex" — capture stays scoped
// to Codex panes fanout launched in plan mode. The capture is read-only
// (tmux capture-pane), so the dashboard stays mutation-free.
func (s *Server) handlePlan(w http.ResponseWriter, r *http.Request) {
	paneID := r.URL.Query().Get("pane")
	pv, ok := s.requireLivePane(w, paneID)
	if !ok {
		return
	}
	if backend.NormalizeName(pv.Backend) != backend.Tmux {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s is not a tmux plan-mode pane", paneID))
		return
	}
	if !pv.PlanMode || pv.Agent != "codex" {
		peekError(w, http.StatusNotFound, fmt.Sprintf("pane %s is not a codex plan-mode pane", paneID))
		return
	}
	if !s.beginPaneCapture(w, r, pv) {
		return
	}
	out, err := s.capturePlan(paneID, planCaptureLines)
	if err != nil {
		peekError(w, http.StatusBadGateway, "tmux capture-pane: "+err.Error())
		return
	}
	plan, found := extractLastPlan(out)
	// A failed response write means the client went away; nothing to do here.
	_ = json.NewEncoder(w).Encode(planResponse{
		PaneID:     paneID,
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		Found:      found,
		Plan:       plan,
	})
}

// extractLastPlan は capture 出力中の最後の「中身のある完全な」
// <proposed_plan>...</proposed_plan> ブロックの中身を返す(前後空白は trim)。
// 後方走査のテキスト検索: 最後の閉じタグ → その手前の最後の開きタグの組を
// 候補とし、中身が空・"..." (briefing の指示文
// 「wrapped in <proposed_plan>...</proposed_plan>」が transcript にエコー
// された行)のブロックはスキップしてさらに前を探す。閉じタグ未到達
// (開きタグのみ = 生成中)はブロック不成立で false。capture 出力は構造を
// 持たないので、plan 本文のコードフェンス内に書かれたタグも本物と区別しない
// (既知の割り切り)。
func extractLastPlan(out string) (string, bool) {
	rest := out
	for {
		end := strings.LastIndex(rest, planCloseTag)
		if end < 0 {
			return "", false
		}
		start := strings.LastIndex(rest[:end], planOpenTag)
		if start < 0 {
			return "", false
		}
		body := strings.TrimSpace(rest[start+len(planOpenTag) : end])
		if body != "" && body != "..." && body != "…" {
			return body, true
		}
		rest = rest[:start] // 空/指示文エコーのブロックより前を探す
	}
}
