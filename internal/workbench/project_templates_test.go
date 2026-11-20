package workbench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestProjectTemplateCatalogDescribesCurrentWorkflows(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(store, nil, HandlerConfig{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/v1/templates", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("template catalog returned %d: %s", response.Code, response.Body)
	}
	var catalog struct {
		Items []struct {
			ID, Label, Revision, Kind, Summary string
			Deliverables                       []string
			Steps                              []struct {
				Key, Title, Checkpoint                  string
				Optional, AcceptMarkdown, RequiresModel bool
			}
			Defaults struct {
				Checkpoints      *bool
				Language         string
				TargetPaperCount int
				Proposal         *proposalTargets
				MaxRounds        int
				TargetScore      int
			}
		}
	}
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, item := range catalog.Items {
		ids = append(ids, item.ID)
		if item.Label == "" || item.Summary == "" || item.Revision == "" || item.Steps == nil || len(item.Deliverables) == 0 {
			t.Fatalf("template has incomplete metadata: %+v", item)
		}
		if item.Kind == "workflow" {
			if item.Defaults.Checkpoints == nil || *item.Defaults.Checkpoints || len(item.Steps) != len(workflowStages[item.Revision]) {
				t.Fatalf("wrong workflow contract for %s: %+v", item.ID, item)
			}
			for i, stage := range workflowStages[item.Revision] {
				step := item.Steps[i]
				if step.Key != stage.key || step.Title != stage.title || step.Optional != stage.optional ||
					step.AcceptMarkdown != stage.acceptMarkdown || step.RequiresModel != (stage.prompt != "") {
					t.Fatalf("step %d of %s differs from the running workflow: %+v", i, item.ID, step)
				}
			}
		}
	}
	if want := []string{"freeform", "topic", "proposal", "literature_review", "paper", "experiment", "automatic_review"}; !slices.Equal(ids, want) {
		t.Fatalf("unexpected available templates: %v", ids)
	}
	items := catalog.Items
	if items[1].Steps[2].Checkpoint != "choose-topic" || items[2].Steps[1].Checkpoint != "confirm" ||
		items[2].Defaults.Proposal == nil || *items[2].Defaults.Proposal != (proposalTargets{"master", "academic", 5000, 30}) ||
		items[3].Defaults.TargetPaperCount != defaultReviewTarget || items[4].Defaults.Language != "zh" ||
		items[5].Revision != experimentWorkflow || !items[5].Steps[2].Optional || items[6].Kind != "review" ||
		items[6].Defaults.MaxRounds != defaultReviewRounds || items[6].Defaults.TargetScore != defaultReviewScore {
		t.Fatal("template defaults or checkpoints differ from the saved workflows")
	}
}
