package literature

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Work contains deposited Crossref metadata. Missing optional fields are
// omitted from JSON. URL is the DOI resolver URL formed from the DOI.
type Work struct {
	DOI      string   `json:"doi"`
	Title    string   `json:"title,omitempty"`
	Journal  string   `json:"journal,omitempty"`
	Year     int      `json:"year,omitempty"`
	Language string   `json:"language,omitempty"`
	Authors  []string `json:"authors,omitempty"`
	Abstract string   `json:"abstract,omitempty"`
	URL      string   `json:"url"`
}

const (
	maxDOIBytes      = 1024
	maxAuthors       = 30
	maxAuthorRunes   = 160
	maxTitleRunes    = 500
	maxAbstractRunes = 4000
	maxLanguageRunes = 35
)

var (
	doiPrefix = regexp.MustCompile(`^10\.[0-9]{4,}(\.[0-9]+)*$`)
	blockEnd  = regexp.MustCompile(`(?i)</([a-z][a-z0-9_.-]*:)?(p|title|sec|div|li|list-item)\s*>|<([a-z][a-z0-9_.-]*:)?br\s*/?>`)
	markup    = regexp.MustCompile(`(?s)<!--.*?-->|</?[a-zA-Z][a-zA-Z0-9_.:-]*(\s+[^<>]*)?/?>`)
)

// NormalizeDOI returns a lower-case DOI from a raw identifier, a doi: label,
// or a URL at a recognized DOI resolver. It validates the identifier locally.
func NormalizeDOI(input string) (string, error) {
	input = strings.TrimSpace(input)
	if len(input) > 4*maxDOIBytes || !utf8.ValidString(input) {
		return "", ErrInvalidDOI
	}
	if len(input) >= 4 && strings.EqualFold(input[:4], "doi:") {
		input = strings.TrimSpace(input[4:])
	}
	if strings.Contains(input, "://") && !strings.HasPrefix(input, "10.") {
		parsed, err := url.Parse(input)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") ||
			(!strings.EqualFold(parsed.Host, "doi.org") && !strings.EqualFold(parsed.Host, "dx.doi.org")) ||
			parsed.User != nil || strings.ContainsAny(input, "?#") {
			return "", ErrInvalidDOI
		}
		input = strings.TrimPrefix(parsed.Path, "/")
	}
	prefix, suffix, found := strings.Cut(input, "/")
	if !found || !doiPrefix.MatchString(prefix) || len(input) > maxDOIBytes || !utf8.ValidString(input) {
		return "", ErrInvalidDOI
	}
	for _, segment := range strings.Split(suffix, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalidDOI
		}
	}
	for _, char := range suffix {
		if char == '\\' || !unicode.IsGraphic(char) || unicode.IsSpace(char) {
			return "", ErrInvalidDOI
		}
	}
	return strings.ToLower(input), nil
}

type crossrefDate struct {
	Parts [][]int `json:"date-parts"`
}

type crossrefWork struct {
	DOI             string       `json:"DOI"`
	Title           []string     `json:"title"`
	Journal         []string     `json:"container-title"`
	Language        string       `json:"language"`
	Abstract        string       `json:"abstract"`
	Published       crossrefDate `json:"published"`
	Issued          crossrefDate `json:"issued"`
	PublishedPrint  crossrefDate `json:"published-print"`
	PublishedOnline crossrefDate `json:"published-online"`
	Posted          crossrefDate `json:"posted"`
	Authors         []struct {
		Given  string `json:"given"`
		Family string `json:"family"`
		Name   string `json:"name"`
	} `json:"author"`
}

func (record crossrefWork) work(doi string) Work {
	resolver := url.URL{Scheme: "https", Host: "doi.org", Path: "/" + doi}
	work := Work{
		DOI:      doi,
		Title:    firstText(record.Title),
		Journal:  firstText(record.Journal),
		Language: plainText(record.Language, maxLanguageRunes),
		Abstract: plainText(record.Abstract, maxAbstractRunes),
		URL:      resolver.String(),
	}
	for _, date := range []crossrefDate{record.Published, record.Issued, record.PublishedPrint, record.PublishedOnline, record.Posted} {
		if len(date.Parts) > 0 && len(date.Parts[0]) > 0 && date.Parts[0][0] > 0 && date.Parts[0][0] <= 9999 {
			work.Year = date.Parts[0][0]
			break
		}
	}
	for _, author := range record.Authors {
		name := plainText(strings.TrimSpace(author.Given)+" "+strings.TrimSpace(author.Family), maxAuthorRunes)
		if name == "" {
			name = plainText(author.Name, maxAuthorRunes)
		}
		if name != "" {
			work.Authors = append(work.Authors, name)
			if len(work.Authors) == maxAuthors {
				break
			}
		}
	}
	return work
}

func firstText(values []string) string {
	for _, value := range values {
		if text := plainText(value, maxTitleRunes); text != "" {
			return text
		}
	}
	return ""
}

// Crossref abstracts may include JATS tags. Preserve paragraph separation and
// keep the text for display without interpreting it as markup.
func plainText(text string, maxRunes int) string {
	if strings.Contains(text, "<") {
		text = blockEnd.ReplaceAllString(text, " ")
		text = markup.ReplaceAllString(text, "")
	}
	text = strings.Join(strings.Fields(html.UnescapeString(text)), " ")
	for offset := range text {
		if maxRunes == 0 {
			return strings.TrimSpace(text[:offset])
		}
		maxRunes--
	}
	return text
}
