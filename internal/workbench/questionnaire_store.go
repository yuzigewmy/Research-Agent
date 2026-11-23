package workbench

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
)

const questionnaireSchema = `
	CREATE TABLE questionnaires (
		project_id TEXT PRIMARY KEY NOT NULL REFERENCES projects(project_id),
		revision INTEGER NOT NULL,
		status TEXT NOT NULL CHECK(status IN ('DRAFT','PUBLISHED','CLOSED')),
		definition_json TEXT NOT NULL,
		public_id TEXT UNIQUE
	);
	CREATE TABLE questionnaire_responses (
		ordinal INTEGER PRIMARY KEY,
		project_id TEXT NOT NULL REFERENCES questionnaires(project_id),
		request_id TEXT NOT NULL,
		response_id TEXT NOT NULL UNIQUE,
		submitted_at TEXT NOT NULL,
		answers_json TEXT NOT NULL,
		UNIQUE(project_id, request_id)
	);
	CREATE INDEX questionnaire_responses_order ON questionnaire_responses(project_id, ordinal);
	PRAGMA user_version = 41;
`

const (
	questionnaireTemplate      = "interactive_survey"
	questionnaireDraft         = "DRAFT"
	questionnairePublished     = "PUBLISHED"
	questionnaireClosed        = "CLOSED"
	questionnaireResponseLimit = 1000
	// Keeps the exported answers and CSV metadata within the analysis file limit.
	questionnaireAnswerBytes = 7 << 10
	questionnaireCSVName     = "questionnaire-responses.csv"
)

var (
	errQuestionnaireState    = errors.New("questionnaire state conflict")
	errQuestionnaireRevision = errors.New("questionnaire revision conflict")
	errQuestionnaireClosed   = errors.New("questionnaire closed")
	errQuestionnaireFull     = errors.New("questionnaire response limit reached")
	errQuestionnaireAnswers  = errors.New("invalid questionnaire answers")
	questionnaireQuestionID  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,19}$`)
	questionnaireNumber      = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
)

type questionnaireQuestion struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Options  []string `json:"options,omitempty"`
}

type questionnaireDefinition struct {
	Title     string                  `json:"title"`
	Intro     string                  `json:"intro"`
	Questions []questionnaireQuestion `json:"questions"`
}

type questionnaireSnapshot struct {
	questionnaireDefinition
	Revision                 int    `json:"revision"`
	Status                   string `json:"status"`
	PublicID                 string `json:"publicId,omitempty"`
	ResponseCount            int    `json:"responseCount"`
	projectID, projectStatus string
}

type questionnaireReceipt struct {
	ResponseID  string `json:"responseId"`
	SubmittedAt string `json:"submittedAt"`
}

const selectQuestionnaire = `SELECT p.project_id, p.status, p.title,
	COALESCE(q.revision, 0), COALESCE(q.status, 'DRAFT'), COALESCE(q.public_id, ''),
	COALESCE(q.definition_json, ''), (SELECT COUNT(*) FROM questionnaire_responses a WHERE a.project_id = p.project_id)
	FROM projects p LEFT JOIN questionnaires q ON q.project_id = p.project_id WHERE p.template_id = ? AND `

func scanQuestionnaire(row *sql.Row) (questionnaireSnapshot, error) {
	var q questionnaireSnapshot
	var title, definition string
	if err := row.Scan(&q.projectID, &q.projectStatus, &title, &q.Revision, &q.Status, &q.PublicID, &definition, &q.ResponseCount); err != nil {
		return questionnaireSnapshot{}, err
	}
	if q.Revision == 0 {
		q.Title, q.Questions = title, []questionnaireQuestion{}
		return q, nil
	}
	err := json.Unmarshal([]byte(definition), &q.questionnaireDefinition)
	return q, err
}

func readQuestionnaire(ctx context.Context, db workspaceQuery, projectID string) (questionnaireSnapshot, error) {
	return scanQuestionnaire(db.QueryRowContext(ctx, selectQuestionnaire+"p.project_id = ?", questionnaireTemplate, projectID))
}

func readPublishedQuestionnaire(ctx context.Context, db workspaceQuery, publicID string) (questionnaireSnapshot, error) {
	return scanQuestionnaire(db.QueryRowContext(ctx, selectQuestionnaire+"q.public_id = ?", questionnaireTemplate, publicID))
}

func normalizeQuestionnaire(definition *questionnaireDefinition) error {
	definition.Title = strings.TrimSpace(definition.Title)
	definition.Intro = strings.TrimSpace(definition.Intro)
	if definition.Title == "" || len(definition.Title) > 240 || strings.ContainsFunc(definition.Title, unicode.IsControl) {
		return errors.New("问卷标题必填，最多 240 个 UTF-8 字节。")
	}
	if len(definition.Intro) > 2<<10 || strings.ContainsRune(definition.Intro, 0) {
		return errors.New("卷首语最多 2 KiB。")
	}
	if len(definition.Questions) < 1 || len(definition.Questions) > 20 {
		return errors.New("请添加 1 至 20 道题。")
	}
	ids := make(map[string]bool, len(definition.Questions))
	for index := range definition.Questions {
		question := &definition.Questions[index]
		question.Label = strings.TrimSpace(question.Label)
		if !questionnaireQuestionID.MatchString(question.ID) || ids[question.ID] || question.Label == "" ||
			len(question.Label) > 220 || strings.ContainsFunc(question.Label, unicode.IsControl) {
			return errors.New("请核对题号和题干，题号应唯一，题干最多 220 个 UTF-8 字节。")
		}
		ids[question.ID] = true
		switch question.Type {
		case "single":
			if len(question.Options) < 2 || len(question.Options) > 12 {
				return errors.New("每道单选题需要 2 至 12 个选项。")
			}
			options := make(map[string]bool, len(question.Options))
			for n, option := range question.Options {
				option = strings.TrimSpace(option)
				if option == "" || len(option) > 120 || options[option] || strings.ContainsFunc(option, unicode.IsControl) {
					return errors.New("单选选项须填写不同的文字，每项最多 120 个 UTF-8 字节。")
				}
				options[option], question.Options[n] = true, option
			}
		case "text":
			if len(question.Options) != 0 {
				return errors.New("文本题无需填写选项。")
			}
		default:
			return errors.New("请选择单选题或文本题。")
		}
	}
	return nil
}

func (s *Store) changeQuestionnaire(ctx context.Context, projectID string, revision int, definition *questionnaireDefinition, status string) (questionnaireSnapshot, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return questionnaireSnapshot{}, err
	}
	defer tx.Rollback()
	q, err := readQuestionnaire(ctx, tx, projectID)
	if err != nil {
		return questionnaireSnapshot{}, err
	}
	if q.Revision != revision {
		return questionnaireSnapshot{}, errQuestionnaireRevision
	}
	if q.projectStatus != projectReady {
		return questionnaireSnapshot{}, errQuestionnaireState
	}

	switch status {
	case questionnaireDraft:
		if q.Status != questionnaireDraft {
			return questionnaireSnapshot{}, errQuestionnaireState
		}
		q.questionnaireDefinition = *definition
	case questionnairePublished:
		if q.Status != questionnaireDraft || q.Revision == 0 {
			return questionnaireSnapshot{}, errQuestionnaireState
		}
		q.PublicID = rand.Text()
	case questionnaireClosed:
		if q.Status != questionnairePublished {
			return questionnaireSnapshot{}, errQuestionnaireState
		}
	}
	encoded, err := json.Marshal(q.questionnaireDefinition)
	if err != nil {
		return questionnaireSnapshot{}, err
	}
	q.Revision++
	q.Status = status
	if _, err := tx.ExecContext(ctx, `INSERT INTO questionnaires(project_id, revision, status, definition_json, public_id)
		VALUES (?, ?, ?, ?, NULLIF(?, '')) ON CONFLICT(project_id) DO UPDATE SET revision = excluded.revision,
		status = excluded.status, definition_json = excluded.definition_json, public_id = excluded.public_id`,
		projectID, q.Revision, q.Status, string(encoded), q.PublicID); err != nil {
		return questionnaireSnapshot{}, err
	}
	return q, tx.Commit()
}

func questionnaireAnswers(q questionnaireSnapshot, answers map[string]string) (string, error) {
	matched := 0
	for _, question := range q.Questions {
		answer, supplied := answers[question.ID]
		if supplied {
			matched++
			answer = strings.TrimSpace(answer)
			answers[question.ID] = answer
		}
		if answer == "" {
			if question.Required {
				return "", errQuestionnaireAnswers
			}
			delete(answers, question.ID)
			if supplied {
				matched--
			}
			continue
		}
		if len(answer) > 320 || strings.ContainsRune(answer, 0) ||
			(question.Type == "single" && !slices.Contains(question.Options, answer)) {
			return "", errQuestionnaireAnswers
		}
	}
	if matched != len(answers) {
		return "", errQuestionnaireAnswers
	}
	if len(answers) == 0 {
		return "{}", nil
	}
	body, err := json.Marshal(answers)
	if len(body) > questionnaireAnswerBytes {
		return "", errQuestionnaireAnswers
	}
	return string(body), err
}

func (s *Store) submitQuestionnaire(ctx context.Context, publicID, requestID string, answers map[string]string) (questionnaireReceipt, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return questionnaireReceipt{}, false, err
	}
	defer tx.Rollback()
	q, err := readPublishedQuestionnaire(ctx, tx, publicID)
	if err != nil {
		return questionnaireReceipt{}, false, err
	}
	var receipt questionnaireReceipt
	var recorded string
	err = tx.QueryRowContext(ctx, `SELECT response_id, submitted_at, answers_json FROM questionnaire_responses
		WHERE project_id = ? AND request_id = ?`, q.projectID, requestID).Scan(&receipt.ResponseID, &receipt.SubmittedAt, &recorded)
	if err == nil {
		body, invalid := questionnaireAnswers(q, answers)
		if invalid != nil || body != recorded {
			return questionnaireReceipt{}, false, errRequestConflict
		}
		return receipt, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return questionnaireReceipt{}, false, err
	}
	if q.Status != questionnairePublished || q.projectStatus != projectReady {
		return questionnaireReceipt{}, false, errQuestionnaireClosed
	}
	if q.ResponseCount >= questionnaireResponseLimit {
		return questionnaireReceipt{}, false, errQuestionnaireFull
	}
	body, err := questionnaireAnswers(q, answers)
	if err != nil {
		return questionnaireReceipt{}, false, err
	}
	receipt = questionnaireReceipt{ResponseID: rand.Text(), SubmittedAt: time.Now().UTC().Format(createdAtLayout)}
	if _, err := tx.ExecContext(ctx, `INSERT INTO questionnaire_responses(project_id, request_id, response_id, submitted_at, answers_json)
		VALUES (?, ?, ?, ?, ?)`, q.projectID, requestID, receipt.ResponseID, receipt.SubmittedAt, body); err != nil {
		return questionnaireReceipt{}, false, err
	}
	return receipt, true, tx.Commit()
}

func questionnaireCSVText(value string) string {
	if strings.HasPrefix(value, "\t") || strings.HasPrefix(value, "\r") || strings.HasPrefix(value, "\n") {
		return "'" + value
	}
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r == '\ufeff' })
	for _, first := range trimmed {
		if strings.ContainsRune("=+-@＝＋－＠", first) && !questionnaireNumber.MatchString(trimmed) {
			return "'" + value
		}
		break
	}
	return value
}

func (s *Store) questionnaireCSV(ctx context.Context, projectID string) ([]byte, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q, err := readQuestionnaire(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	if q.Status == questionnaireDraft {
		return nil, errQuestionnaireState
	}
	var body bytes.Buffer
	writer := csv.NewWriter(&body)
	header := []string{"response_id", "submitted_at", "source"}
	for _, question := range q.Questions {
		header = append(header, question.ID+" - "+question.Label)
	}
	if err := writer.Write(header); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT response_id, submitted_at, answers_json FROM questionnaire_responses
		WHERE project_id = ? ORDER BY ordinal`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, submittedAt, data string
		if err := rows.Scan(&id, &submittedAt, &data); err != nil {
			return nil, err
		}
		var answers map[string]string
		if err := json.Unmarshal([]byte(data), &answers); err != nil {
			return nil, err
		}
		record := []string{id, submittedAt, "web"}
		for _, question := range q.Questions {
			record = append(record, questionnaireCSVText(answers[question.ID]))
		}
		if err := writer.Write(record); err != nil {
			return nil, err
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	writer.Flush()
	return body.Bytes(), writer.Error()
}
