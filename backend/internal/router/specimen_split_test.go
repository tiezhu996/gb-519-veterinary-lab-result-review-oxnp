package router_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/blueship581/veterinary-lab-result-review/backend/internal/config"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/database"
	"github.com/blueship581/veterinary-lab-result-review/backend/internal/router"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type envelope struct {
	Data    json.RawMessage `json:"data"`
	Error   string          `json:"error"`
	Message string          `json:"message"`
}

func newSplitTestServer(t *testing.T) (*gin.Engine, *gorm.DB, map[string]string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := filepath.Join(t.TempDir(), "split-integration.db")
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.MigrateAndSeedForTests(db); err != nil {
		t.Fatalf("migrate and seed: %v", err)
	}
	cfg := config.Config{
		AppName: "veterinary-lab-result-review", Environment: "test",
		JWTSecret: "integration-secret", TokenTTL: time.Hour, RequestLimit: 100000,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	engine := router.New(cfg, db, nil, logger)
	tokens := map[string]string{}
	for _, username := range []string{"admin", "reviewer", "operator", "viewer"} {
		body := `{"username":"` + username + `","password":"Admin123!"}`
		rec := serve(t, engine, http.MethodPost, "/api/auth/login", "", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("login %s: %s", username, rec.Body.String())
		}
		var resp struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode login: %v", err)
		}
		tokens[username] = resp.Data.Token
	}
	return engine, db, tokens
}

func serve(t *testing.T, engine *gin.Engine, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	return serveWithRequestID(t, engine, method, path, token, t.Name(), body)
}

func serveWithRequestID(t *testing.T, engine *gin.Engine, method, path, token, requestID, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Buffer
	if body != "" {
		reader = bytes.NewBufferString(body)
	} else {
		reader = bytes.NewBuffer(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-Request-ID", requestID)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", rec.Body.String(), err)
	}
	var data map[string]any
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("decode data: %v", err)
		}
	}
	return data
}

func TestSpecimenSplitHTTPEndToEnd(t *testing.T) {
	engine, db, tokens := newSplitTestServer(t)
	op, viewer, admin := tokens["operator"], tokens["viewer"], tokens["admin"]

	// 1. Seed data carries available amount.
	rec := serve(t, engine, http.MethodGet, "/api/specimens?pageSize=100", op, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list specimens: %s", rec.Body.String())
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var motherID float64
	for _, item := range list.Data {
		if item["code"] == "S-001" {
			motherID = item["id"].(float64)
			if item["availableAmount"].(float64) != 60 {
				t.Fatalf("seed available amount mismatch: %v", item["availableAmount"])
			}
		}
	}
	if motherID == 0 {
		t.Fatal("S-001 not seeded")
	}
	id := int(motherID)

	// 2. Viewer cannot split (403).
	denied := serve(t, engine, http.MethodPost, path(id, "/split"), viewer,
		`{"expectedVersion":1,"reason":"viewer may not split","portions":[{"amount":1},{"amount":1}]}`)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("viewer split should be 403, got %d", denied.Code)
	}

	// 3. Operator splits into 3 portions; mother debited, children traceable.
	ok := serveWithRequestID(t, engine, http.MethodPost, path(id, "/split"), op, "it-split-batch",
		`{"expectedVersion":1,"reason":"PCR 与培养分样","portions":[{"amount":10},{"amount":20},{"amount":10}]}`)
	if ok.Code != http.StatusOK {
		t.Fatalf("split should succeed: %s", ok.Body.String())
	}
	mother := decodeData(t, ok)
	if mother["status"] != "received" || mother["version"].(float64) != 2 || mother["availableAmount"].(float64) != 20 {
		t.Fatalf("unexpected mother after split: status=%v version=%v amount=%v", mother["status"], mother["version"], mother["availableAmount"])
	}
	children, _ := mother["children"].([]any)
	wantCodes := []string{"S-001-01", "S-001-02", "S-001-03"}
	wantAmounts := []float64{10, 20, 10}
	if len(children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(children))
	}
	childIDs := make([]int, 3)
	for i, raw := range children {
		child := raw.(map[string]any)
		if child["code"] != wantCodes[i] || child["amount"].(float64) != wantAmounts[i] {
			t.Fatalf("child %d mismatch: %#v", i, child)
		}
		if child["status"] != "received" || child["parentId"].(float64) != motherID ||
			child["sequence"].(float64) != float64(i+1) || child["availableUnit"] != "mL" {
			t.Fatalf("child %d lineage mismatch: %#v", i, child)
		}
		childIDs[i] = int(child["id"].(float64))
	}

	// 4. Insufficient amount rejects the whole batch (422), mother unchanged.
	over := serve(t, engine, http.MethodPost, path(id, "/split"), op,
		`{"expectedVersion":2,"reason":"超出可用量被拒","portions":[{"amount":15},{"amount":10}]}`)
	if over.Code != http.StatusUnprocessableEntity {
		t.Fatalf("insufficient amount should be 422, got %d", over.Code)
	}
	reread := serve(t, engine, http.MethodGet, path(id, ""), op, "")
	mother2 := decodeData(t, reread)
	if mother2["availableAmount"].(float64) != 20 || mother2["version"].(float64) != 2 {
		t.Fatalf("mother changed on rejected batch: %#v", mother2)
	}
	if len(mother2["children"].([]any)) != 3 {
		t.Fatal("rejected batch created children")
	}

	// 5. Duplicate submission with the stale version is a 409 conflict.
	dup := serve(t, engine, http.MethodPost, path(id, "/split"), op,
		`{"expectedVersion":1,"reason":"PCR 与培养分样","portions":[{"amount":10},{"amount":20},{"amount":10}]}`)
	if dup.Code != http.StatusConflict {
		t.Fatalf("duplicate split should be 409, got %d (%s)", dup.Code, dup.Body.String())
	}

	// 6. A hold specimen cannot be split.
	var holdID int
	for _, item := range list.Data {
		if item["code"] == "S-003" {
			holdID = int(item["id"].(float64))
		}
	}
	badState := serve(t, engine, http.MethodPost, path(holdID, "/split"), op,
		`{"expectedVersion":1,"reason":"hold 状态分装被拒","portions":[{"amount":5},{"amount":5}]}`)
	if badState.Code != http.StatusUnprocessableEntity {
		t.Fatalf("hold split should be 422, got %d", badState.Code)
	}

	// 7. Portion counts outside 2-5 are bad requests.
	one := serve(t, engine, http.MethodPost, path(id, "/split"), op, `{"expectedVersion":2,"reason":"单份非法 x","portions":[{"amount":1}]}`)
	if one.Code != http.StatusBadRequest {
		t.Fatalf("single portion should be 400, got %d", one.Code)
	}
	six := serve(t, engine, http.MethodPost, path(id, "/split"), op,
		`{"expectedVersion":2,"reason":"六份非法 x","portions":[{"amount":1},{"amount":1},{"amount":1},{"amount":1},{"amount":1},{"amount":1}]}`)
	if six.Code != http.StatusBadRequest {
		t.Fatalf("six portions should be 400, got %d", six.Code)
	}

	// 8. Mother cannot be disposed while children are open; reason is readable.
	hold := serve(t, engine, http.MethodPost, path(id, "/transition"), op,
		`{"status":"hold","expectedVersion":2,"reason":"母样进入暂存隔离"}`)
	if hold.Code != http.StatusOK {
		t.Fatalf("mother hold: %s", hold.Body.String())
	}
	blockedDispose := serve(t, engine, http.MethodPost, path(id, "/transition"), op,
		`{"status":"disposed","expectedVersion":3,"reason":"提前处置被阻断"}`)
	if blockedDispose.Code != http.StatusUnprocessableEntity {
		t.Fatalf("dispose with open children should be 422, got %d", blockedDispose.Code)
	}
	withReason := serve(t, engine, http.MethodGet, path(id, ""), op, "")
	mother3 := decodeData(t, withReason)
	reason, _ := mother3["blockedReason"].(string)
	if reason == "" {
		t.Fatal("blockedReason must be exposed for a mother with open children")
	}

	// 9. Dispose every child (received -> hold -> disposed), then the mother.
	for _, childID := range childIDs {
		if r := serve(t, engine, http.MethodPost, path(childID, "/transition"), op,
			`{"status":"hold","expectedVersion":1,"reason":"子样暂存隔离"}`); r.Code != http.StatusOK {
			t.Fatalf("child hold: %s", r.Body.String())
		}
		if r := serve(t, engine, http.MethodPost, path(childID, "/transition"), op,
			`{"status":"disposed","expectedVersion":2,"reason":"子样完成处置"}`); r.Code != http.StatusOK {
			t.Fatalf("child dispose: %s", r.Body.String())
		}
	}
	motherDisposed := serve(t, engine, http.MethodPost, path(id, "/transition"), op,
		`{"status":"disposed","expectedVersion":3,"reason":"子样均已处置"}`)
	if motherDisposed.Code != http.StatusOK || decodeData(t, motherDisposed)["status"] != "disposed" {
		t.Fatalf("mother should dispose after children: %s", motherDisposed.Body.String())
	}

	// 10. Testing mother keeps testing state; codes remain continuous and lineage
	// is readable after reload.
	var testingID int
	for _, item := range list.Data {
		if item["code"] == "S-002" {
			testingID = int(item["id"].(float64))
		}
	}
	testingSplit := serve(t, engine, http.MethodPost, path(testingID, "/split"), op,
		`{"expectedVersion":1,"reason":"检测中继续分装 x","portions":[{"amount":5},{"amount":5}]}`)
	if testingSplit.Code != http.StatusOK {
		t.Fatalf("testing split: %s", testingSplit.Body.String())
	}
	testingMother := decodeData(t, testingSplit)
	if testingMother["status"] != "testing" || testingMother["availableAmount"].(float64) != 35 {
		t.Fatalf("testing mother mismatch: %#v", testingMother)
	}
	reloaded := serve(t, engine, http.MethodGet, "/api/specimens?pageSize=100", admin, "")
	var list2 struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(reloaded.Body.Bytes(), &list2); err != nil {
		t.Fatal(err)
	}
	codes := map[string]map[string]any{}
	for _, item := range list2.Data {
		codes[item["code"].(string)] = item
	}
	if codes["S-001-02"] == nil || codes["S-001-02"]["parentId"].(float64) != motherID ||
		codes["S-001-02"]["relatedCode"] != "S-001" || codes["S-001-02"]["splitSeq"].(float64) != 2 ||
		codes["S-001-02"]["availableAmount"].(float64) != 20 {
		t.Fatalf("child lineage not readable after reload: %#v", codes["S-001-02"])
	}
	if codes["S-002-01"] == nil || codes["S-002-02"] == nil {
		t.Fatal("continuous testing-batch codes missing")
	}
	if codes["S-001"]["status"] != "disposed" {
		t.Fatal("mother disposal did not persist")
	}

	// 11. Audit history records the split batch with actor and request ID.
	auditRec := serve(t, engine, http.MethodGet, "/api/audits/Specimen/"+strconv.Itoa(id)+"?limit=50", admin, "")
	if auditRec.Code != http.StatusOK {
		t.Fatalf("audit history: %s", auditRec.Body.String())
	}
	var audits struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(auditRec.Body.Bytes(), &audits); err != nil {
		t.Fatal(err)
	}
	var motherSplitAudits int
	for _, entry := range audits.Data {
		if entry["requestId"] == "it-split-batch" && entry["action"] == "split" {
			motherSplitAudits++
			if entry["actor"] != "operator" {
				t.Fatalf("split audit missing operator actor: %#v", entry)
			}
		}
	}
	if motherSplitAudits != 1 {
		t.Fatalf("expected one mother split audit, got %d", motherSplitAudits)
	}

	var splitAudits, childAudits int64
	if err := db.Table("audit_logs").Where("request_id = ? AND action = ?", "it-split-batch", "split").Count(&splitAudits).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("audit_logs").Where("request_id = ? AND action = ?", "it-split-batch", "split-child").Count(&childAudits).Error; err != nil {
		t.Fatal(err)
	}
	if splitAudits != 1 || childAudits != 3 {
		t.Fatalf("expected 1 split + 3 split-child audits, got %d and %d", splitAudits, childAudits)
	}

	// The append-only provenance table ties children back to the mother.
	var provenanceCount int64
	if err := db.Table("specimen_splits").Where("parent_id = ? AND request_id = ?", id, "it-split-batch").Count(&provenanceCount).Error; err != nil {
		t.Fatal(err)
	}
	if provenanceCount != 3 {
		t.Fatalf("expected 3 provenance rows, got %d", provenanceCount)
	}
}

func path(id int, suffix string) string {
	if suffix == "" {
		return "/api/specimens/" + strconv.Itoa(id)
	}
	return "/api/specimens/" + strconv.Itoa(id) + suffix
}
