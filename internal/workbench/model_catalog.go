package workbench

import (
	"strings"
	"time"
)

type modelCapabilityRole struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	Capability  string `json:"capability"`
	TestSeconds int    `json:"testSeconds"`
	modelSnapshot
	ImageQuality string `json:"imageQuality,omitempty"`
}

type modelTemplateDefault struct {
	TemplateID string `json:"templateId"`
	Label      string `json:"label"`
	ModelID    string `json:"modelId"`
}

type modelTechnicalLimit struct {
	ID    string `json:"id"`
	Value int    `json:"value"`
	Unit  string `json:"unit"`
}

type modelCatalog struct {
	Roles            []modelCapabilityRole  `json:"roles"`
	TemplateDefaults []modelTemplateDefault `json:"templateDefaults"`
	Limits           []modelTechnicalLimit  `json:"limits"`
}

func describeModelCatalog(options []modelOption, workflows workflowModels, config HandlerConfig) modelCatalog {
	catalog := modelCatalog{
		Roles:            make([]modelCapabilityRole, 0, 7),
		TemplateDefaults: make([]modelTemplateDefault, 0),
		Limits: []modelTechnicalLimit{
			{"conversationTime", int(turnReplyTimeout / time.Second), "seconds"},
			{"conversationInput", maxTurnInputBytes, "bytes"},
			{"conversationOutput", maxTurnOutputBytes, "bytes"},
			{"workflowStepTime", int(workflowTaskTimeout / time.Second), "seconds"},
			{"reviewTime", int(workflowTaskTimeout / time.Second), "seconds"},
			{"reviewSource", maxReviewSourceBytes, "bytes"},
			{"automaticReviewTime", int(automaticReviewTimeout / time.Second), "seconds"},
			{"automaticReviewRounds", maxAutomaticReviewRounds, "count"},
			{"editTime", int(workflowTaskTimeout / time.Second), "seconds"},
			{"editSelection", maxSelectionBytes, "bytes"},
			{"visionTime", int(imageReviewTimeout / time.Second), "seconds"},
			{"visionImageBytes", maxMaterialImageBytes, "bytes"},
			{"visionImageEdge", maxMaterialImageEdge, "pixels"},
			{"visionImagePixels", maxMaterialImagePixels, "pixels"},
			{"imageTime", int(imageGenerationTimeout / time.Second), "seconds"},
			{"imageBytes", maxMaterialImageBytes, "bytes"},
		},
	}
	configured := make(map[string]modelOption, len(options))
	for _, option := range options {
		configured[option.ID] = option
	}
	for _, definition := range []struct {
		id, capability string
		inherits       bool
	}{
		{defaultModelID, "text", false},
		{alternateConversationModelID, "text", false},
		{alternateWorkflowModelID, "text", false},
		{"review", "text", strings.TrimSpace(config.ReviewModelName) == ""},
		{"edit", "text", strings.TrimSpace(config.EditModelName) == ""},
		{visionModelID, "vision", false},
		{imageGenerationModelID, "image", false},
	} {
		option := configured[definition.id]
		status := "unconfigured"
		if option.Model != "" {
			status = "configured"
			if definition.inherits {
				status = "inherited"
			}
		}
		catalog.Roles = append(catalog.Roles, modelCapabilityRole{
			ID: definition.id, Status: status, Capability: definition.capability,
			TestSeconds: int(modelTestTimeout(definition.id) / time.Second), modelSnapshot: option.modelSnapshot, ImageQuality: option.ImageQuality,
		})
	}
	for _, template := range projectTemplates {
		if projectWorkflowKey(template.ID) != "" {
			catalog.TemplateDefaults = append(catalog.TemplateDefaults, modelTemplateDefault{template.ID, template.Label, workflows.defaultID(template.ID)})
		}
	}
	return catalog
}
