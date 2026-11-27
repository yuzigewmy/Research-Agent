package workbench

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMaterialPIIMaskText(t *testing.T) {
	input := "联系人 13812345678,13987654321 身份证 11010519900307123X 邮箱 alice.wang@example.edu.cn 银行卡 6222021234567890123 指标 0.9542"
	masked, count := maskPIIText(input)
	if count != 5 {
		t.Fatalf("expected 5 PII replacements, got %d (%s)", count, masked)
	}
	for _, secret := range []string{"13812345678", "13987654321", "11010519900307123X", "alice.wang@example.edu.cn", "6222021234567890123"} {
		if strings.Contains(masked, secret) {
			t.Fatalf("plaintext secret %q leaked in masked output: %s", secret, masked)
		}
	}
	for _, expected := range []string{"138****5678", "139****4321", "110105********123X", "a***@example.edu.cn", "6222***********0123", "0.9542"} {
		if !strings.Contains(masked, expected) {
			t.Fatalf("expected %q in masked output: %s", expected, masked)
		}
	}
}

func TestMaterialPIISearchDeleteAndTablePreview(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	ctx := context.Background()

	projA, _, err := store.createProject(ctx, createProjectInput{RequestID: "pii-a", Title: "资料脱敏项目A", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	projB, _, err := store.createProject(ctx, createProjectInput{RequestID: "pii-b", Title: "资料隔离项目B", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}

	// Build a CSV with >50 rows and sensitive columns + PII values.
	var csvBuilder strings.Builder
	csvBuilder.WriteString("sample_id,score,手机号,身份证号,email,bank_card,备注\n")
	for i := 1; i <= 55; i++ {
		fmt.Fprintf(&csvBuilder, "S%03d,%.4f,1381234%04d,11010519920512%04d,user%d@lab.org,622202123456%04d,联系人010-88886666\n",
			i, float64(i)*0.125, 1000+i, 1000+i, i, 2000+i)
	}
	csvV1, _, err := store.saveMaterial(ctx, projA.ProjectID, "participants_2026.csv", strings.NewReader(csvBuilder.String()))
	if err != nil {
		t.Fatal(err)
	}
	csvV2, _, err := store.saveMaterial(ctx, projA.ProjectID, "participants_2026.csv", strings.NewReader("sample_id,score\nS1,0.88\n"))
	if err != nil {
		t.Fatal(err)
	}
	noteA, _, err := store.saveMaterial(ctx, projA.ProjectID, "protocol.txt", strings.NewReader("study protocol"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.saveMaterial(ctx, projB.ProjectID, "participants_2026.csv", strings.NewReader("other")); err != nil {
		t.Fatal(err)
	}

	// 1. Search materials with whitespace-trimmed q and project isolation.
	searchURL := "http://127.0.0.1" + projectsPath + "/" + projA.ProjectID + "/materials?q=" + url.QueryEscape("  participants ")
	searchReq := httptest.NewRequest(http.MethodGet, searchURL, nil)
	searchRes := httptest.NewRecorder()
	handler.ServeHTTP(searchRes, searchReq)
	if searchRes.Code != http.StatusOK {
		t.Fatalf("search materials failed: %d %s", searchRes.Code, searchRes.Body)
	}
	var page struct {
		Items []Material `json:"items"`
	}
	if err := json.Unmarshal(searchRes.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected 2 matching materials in project A, got %+v", page.Items)
	}

	// 2. Table PII masked preview for csvV1.
	tableURL := "http://127.0.0.1" + projectsPath + "/" + projA.ProjectID + "/materials/" + csvV1.MaterialID + "/table-preview"
	tableReq := httptest.NewRequest(http.MethodGet, tableURL, nil)
	tableRes := httptest.NewRecorder()
	handler.ServeHTTP(tableRes, tableReq)
	if tableRes.Code != http.StatusOK {
		t.Fatalf("table-preview failed: %d %s", tableRes.Code, tableRes.Body)
	}
	var tablePreview MaterialTablePreview
	if err := json.Unmarshal(tableRes.Body.Bytes(), &tablePreview); err != nil {
		t.Fatal(err)
	}
	if tablePreview.TotalPreviewRows != 50 || !tablePreview.Truncated {
		t.Fatalf("expected 50 truncated rows, got rows=%d truncated=%v", tablePreview.TotalPreviewRows, tablePreview.Truncated)
	}
	if tablePreview.MaskedCellCount < 200 || len(tablePreview.MaskedColumns) < 4 {
		t.Fatalf("expected masked cells & columns, got count=%d cols=%+v", tablePreview.MaskedCellCount, tablePreview.MaskedColumns)
	}
	bodyText := tableRes.Body.String()
	for _, secret := range []string{"13812341001", "110105199205121001", "user1@lab.org", "6222021234562001"} {
		if strings.Contains(bodyText, secret) {
			t.Fatalf("plaintext PII %q leaked in table-preview response", secret)
		}
	}
	if tablePreview.Rows[0][1] != "0.1250" {
		t.Fatalf("numeric score column should remain unmasked, got %q", tablePreview.Rows[0][1])
	}

	// Non-table file returns 422 PREVIEW_UNAVAILABLE.
	nonTableReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+projectsPath+"/"+projA.ProjectID+"/materials/"+noteA.MaterialID+"/table-preview", nil)
	nonTableRes := httptest.NewRecorder()
	handler.ServeHTTP(nonTableRes, nonTableReq)
	if nonTableRes.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for non-table file, got %d %s", nonTableRes.Code, nonTableRes.Body)
	}

	// 3. Delete single material version (csvV2); csvV1 and project B remain untouched.
	// Cross-project delete must fail with 404.
	crossDel := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1"+projectsPath+"/"+projB.ProjectID+"/materials/"+csvV2.MaterialID, nil)
	crossRes := httptest.NewRecorder()
	handler.ServeHTTP(crossRes, crossDel)
	if crossRes.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-project material delete, got %d", crossRes.Code)
	}

	delReq := httptest.NewRequest(http.MethodDelete, "http://127.0.0.1"+projectsPath+"/"+projA.ProjectID+"/materials/"+csvV2.MaterialID, nil)
	delRes := httptest.NewRecorder()
	handler.ServeHTTP(delRes, delReq)
	if delRes.Code != http.StatusOK {
		t.Fatalf("delete material failed: %d %s", delRes.Code, delRes.Body)
	}

	remainingA, err := store.listMaterials(ctx, projA.ProjectID, "participants_2026.csv", 0, 10)
	if err != nil || len(remainingA) != 1 || remainingA[0].MaterialID != csvV1.MaterialID {
		t.Fatalf("expected only csvV1 to remain, got %+v (err=%v)", remainingA, err)
	}
	remainingB, err := store.listMaterials(ctx, projB.ProjectID, "", 0, 10)
	if err != nil || len(remainingB) != 1 {
		t.Fatalf("project B material should remain untouched: %+v", remainingB)
	}
}

func TestMaterialPIITurnReferenceMasking(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	proj, _, err := store.createProject(ctx, createProjectInput{RequestID: "ref-pii", Title: "对话引用脱敏", TemplateID: "freeform"})
	if err != nil {
		t.Fatal(err)
	}
	csvContent := "name,电话,身份证,邮箱,score\n张三,13911112222,310101199001019876,zhangsan@univ.edu.cn,92.5\n"
	mat, _, err := store.saveMaterial(ctx, proj.ProjectID, "cohort.csv", strings.NewReader(csvContent))
	if err != nil {
		t.Fatal(err)
	}

	prompt, err := store.turnInput(ctx, Turn{
		Text: "请汇总该表中的得分表现。",
		References: []TurnReference{{
			MaterialID: mat.MaterialID,
			Name:       mat.Name,
			SHA256:     mat.SHA256,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"13911112222", "310101199001019876", "zhangsan@univ.edu.cn"} {
		if strings.Contains(prompt, secret) {
			t.Fatalf("model context leaked plaintext PII %q: %s", secret, prompt)
		}
	}
	if !strings.Contains(prompt, "139****2222") || !strings.Contains(prompt, "92.5") {
		t.Fatalf("expected masked phone and preserved score in model context: %s", prompt)
	}
}
