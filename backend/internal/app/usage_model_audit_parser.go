package app

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const usageModelAuditParserVersion = 1

// Bounds apply to individual external log values, not the complete log file.
// Exceeding them fails explicitly; no truncated data is reported as a full audit.
const auditValueLimit = 8 << 20
const auditAttemptLimit = 1024

type usageModelAuditAttempt struct {
	Index           int      `json:"index"`
	RequestModel    *string  `json:"request_model"`
	ResponseModel   *string  `json:"response_model"`
	SignatureModel  *string  `json:"signature_model"`
	Models          []string `json:"models"`
	SignatureStatus string   `json:"signature_status"`
	Status          string   `json:"status"`
	Conflict        bool     `json:"conflict"`
}

type usageModelAuditResult struct {
	RecordID           int                      `json:"record_id"`
	CheckedAt          time.Time                `json:"checked_at"`
	CheckedBy          int                      `json:"checked_by"`
	ParserVersion      int                      `json:"parser_version"`
	SourceStatus       string                   `json:"source_status"`
	QueueResponseModel *string                  `json:"queue_response_model"`
	LogResponseModel   *string                  `json:"log_response_model"`
	SignatureModel     *string                  `json:"signature_model"`
	Status             string                   `json:"status"`
	Association        string                   `json:"association"`
	Attempts           []usageModelAuditAttempt `json:"attempts"`
	LogSHA256          string                   `json:"log_sha256"`
}

var auditSectionPattern = regexp.MustCompile(`^=== API (REQUEST|RESPONSE)(?: ([1-9][0-9]*))? ===$`)
var auditSignaturePattern = regexp.MustCompile(`^[A-Za-z0-9+/_-]+={0,2}$`)

type auditAttemptState struct {
	usageModelAuditAttempt
	responseSeen bool
}

type auditBody struct {
	attempt         *auditAttemptState
	request         bool
	started         bool
	sse             bool
	data            strings.Builder
	terminal        bool
	malformed       bool
	seen            bool
	signatures      map[int]string
	signatureModels []string
	signatureBytes  int
	modelBytes      int
	modelSet        map[string]bool
}

// parseUsageModelAudit only reads upstream sections. It retains conflicts and
// never associates a retry merely because it is first or last in the log.
func parseUsageModelAudit(log io.Reader, _ UsageRecord) (usageModelAuditResult, error) {
	result := usageModelAuditResult{Status: "unsupported", Association: "none", Attempts: []usageModelAuditAttempt{}}
	attempts := map[int]*auditAttemptState{}
	var body *auditBody
	websocket := false
	downstream := false
	retainedBytes := 0
	finish := func() error {
		if body == nil {
			return nil
		}
		if err := body.finish(); err != nil {
			return err
		}
		if body.request {
			if body.attempt.RequestModel != nil {
				retainedBytes += len(*body.attempt.RequestModel)
			}
		} else {
			for _, model := range body.attempt.Models {
				retainedBytes += len(model)
			}
			if body.attempt.SignatureModel != nil {
				retainedBytes += len(*body.attempt.SignatureModel)
			}
		}
		if retainedBytes > auditValueLimit {
			return errors.New("upstream audit result exceeds parser limit (8 MiB)")
		}
		return nil
	}
	scanner := bufio.NewScanner(log)
	scanner.Buffer(make([]byte, 4096), auditValueLimit)
	for scanner.Scan() {
		if downstream {
			continue
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.HasPrefix(line, "=== ") && strings.HasSuffix(line, " ===") {
			if err := finish(); err != nil {
				return result, err
			}
			body = nil
			// Everything after the downstream response marker belongs to the
			// client-facing transcript, including any marker-like body text.
			if line == "=== RESPONSE ===" {
				downstream = true
				continue
			}
			if strings.Contains(line, "WEBSOCKET") {
				websocket = true
			}
			match := auditSectionPattern.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			index := 1
			if match[2] != "" {
				var err error
				index, err = strconv.Atoi(match[2])
				if err != nil {
					return result, errors.New("invalid upstream attempt index")
				}
			}
			a := attempts[index]
			if a == nil {
				if len(attempts) >= auditAttemptLimit {
					return result, errors.New("upstream log exceeds parser attempt limit")
				}
				a = &auditAttemptState{usageModelAuditAttempt: usageModelAuditAttempt{Index: index, Models: []string{}, Status: "incomplete", SignatureStatus: "missing"}}
				attempts[index] = a
			}
			request := match[1] == "REQUEST"
			if !request && a.responseSeen {
				return result, errors.New("duplicate upstream response section")
			}
			if !request {
				a.responseSeen = true
			}
			body = &auditBody{attempt: a, request: request, signatures: map[int]string{}, modelSet: map[string]bool{}}
			continue
		}
		if body == nil {
			continue
		}
		if !body.started {
			if !body.request && strings.HasPrefix(line, "Status: ") {
				status, err := strconv.Atoi(strings.TrimPrefix(line, "Status: "))
				if err != nil || status >= 400 {
					body.malformed = true
				}
			}
			if line == "Body:" {
				body.started = true
			}
			continue
		}
		if err := body.line(line); err != nil {
			return result, err
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("read upstream log (maximum line size 8 MiB): %w", err)
	}
	if err := finish(); err != nil {
		return result, err
	}
	indices := make([]int, 0, len(attempts))
	for index := range attempts {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	candidates := []*auditAttemptState{}
	for _, index := range indices {
		a := attempts[index]
		result.Attempts = append(result.Attempts, a.usageModelAuditAttempt)
		if a.responseSeen {
			candidates = append(candidates, a)
		}
	}
	if websocket || len(candidates) == 0 {
		return result, nil
	}
	result.Status = "incomplete"
	var selected *auditAttemptState
	if len(candidates) == 1 && len(attempts) == 1 {
		selected, result.Association = candidates[0], "single"
	} else {
		// Queue model names may precede alias remapping and cannot identify an
		// upstream attempt. Auth is a display label, not the log's auth_id.
		// Until an authoritative attempt identity is available, retain every
		// candidate rather than confidently selecting a coincidental model match.
		result.Association = "ambiguous"
	}
	if selected != nil {
		result.Status = selected.Status
		result.LogResponseModel = selected.ResponseModel
		result.SignatureModel = selected.SignatureModel
	}
	return result, nil
}

func auditAppend(builder *strings.Builder, text string) error {
	if builder.Len()+len(text) > auditValueLimit {
		return errors.New("upstream JSON/SSE value exceeds parser limit (8 MiB)")
	}
	builder.WriteString(text)
	return nil
}

func (b *auditBody) line(line string) error {
	if strings.HasPrefix(line, "Error:") {
		b.malformed = true
		return nil
	}
	if !b.request && (strings.HasPrefix(line, "data:") || strings.HasPrefix(line, "event:") || strings.HasPrefix(line, ":")) {
		b.sse = true
	}
	if b.sse {
		if line == "" {
			return b.event()
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimPrefix(line, "data:")
			data = strings.TrimPrefix(data, " ")
			if b.data.Len() > 0 {
				if err := auditAppend(&b.data, "\n"); err != nil {
					return err
				}
			}
			return auditAppend(&b.data, data)
		}
		if !strings.HasPrefix(line, "event:") && !strings.HasPrefix(line, ":") && !strings.HasPrefix(line, "id:") && !strings.HasPrefix(line, "retry:") {
			b.malformed = true
		}
		return nil
	}
	return auditAppend(&b.data, line+"\n")
}

func (b *auditBody) event() error {
	text := strings.TrimSpace(b.data.String())
	b.data.Reset()
	if text == "" {
		return nil
	}
	if text == "[DONE]" {
		b.terminal = true
		return nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		b.malformed = true
		return nil
	}
	b.seen = true
	if b.request {
		if model := auditString(obj, "model"); model != "" {
			b.attempt.RequestModel = &model
		}
		return nil
	}
	typeName := auditString(obj, "type")
	if _, ok := obj["error"]; ok {
		b.malformed = true
	}
	terminal := typeName == "message_stop" || typeName == "response.completed" || typeName == "response.failed" || typeName == "response.incomplete" || typeName == "error"
	if typeName == "response.failed" || typeName == "response.incomplete" || typeName == "error" {
		b.malformed = true
	}
	model := auditString(obj, "model")
	gemini := auditString(obj, "modelVersion")
	candidateObject := obj
	for _, key := range []string{"response", "message"} {
		var nested map[string]json.RawMessage
		if json.Unmarshal(obj[key], &nested) == nil {
			if value := auditString(nested, "model"); value != "" {
				model = value
			}
			if value := auditString(nested, "modelVersion"); value != "" {
				model = value
				gemini = value
				candidateObject = nested
			}
		}
	}
	if gemini != "" {
		model = gemini
	}
	if model != "" {
		if !b.modelSet[model] {
			b.modelBytes += len(model)
			if b.modelBytes > auditValueLimit {
				return errors.New("upstream model declarations exceed parser limit (8 MiB)")
			}
			b.modelSet[model] = true
			b.attempt.Models = append(b.attempt.Models, model)
		}
		// First declaration is retained, except a terminal OpenAI declaration
		// or Gemini's latest version. All differing declarations remain visible.
		if b.attempt.ResponseModel == nil || terminal || gemini != "" {
			b.attempt.ResponseModel = &model
		}
		if len(b.attempt.Models) > 1 {
			b.attempt.Conflict = true
		}
	}
	var candidates []map[string]json.RawMessage
	if json.Unmarshal(candidateObject["candidates"], &candidates) == nil {
		for _, c := range candidates {
			if auditString(c, "finishReason") != "" {
				terminal = true
			}
		}
	}
	if terminal {
		b.terminal = true
	}
	var index int
	if typeName == "content_block_delta" || typeName == "content_block_stop" {
		if err := json.Unmarshal(obj["index"], &index); err != nil || index < 0 {
			b.malformed = true
			return nil
		}
	}
	if typeName == "content_block_delta" {
		var delta map[string]json.RawMessage
		if json.Unmarshal(obj["delta"], &delta) == nil && auditString(delta, "type") == "signature_delta" {
			fragment := auditString(delta, "signature")
			if fragment == "" {
				b.attempt.SignatureStatus = "malformed"
				return nil
			}
			b.signatureBytes += len(fragment)
			if b.signatureBytes > auditValueLimit {
				return errors.New("upstream signatures exceed parser limit (8 MiB)")
			}
			b.signatures[index] += fragment
		}
	}
	if typeName == "content_block_stop" {
		b.decodeSignature(index)
	}
	// Non-stream Anthropic responses put the signature on a thinking block.
	var content []map[string]json.RawMessage
	if json.Unmarshal(obj["content"], &content) == nil {
		for _, block := range content {
			if auditString(block, "type") == "thinking" {
				if signature := auditString(block, "signature"); signature != "" {
					b.applySignature(signature)
				}
			}
		}
	}
	return nil
}

func (b *auditBody) finish() error {
	if err := b.event(); err != nil {
		return err
	}
	if b.request {
		return nil
	}
	for index := range b.signatures {
		b.decodeSignature(index)
	}
	if b.seen && !b.malformed && (!b.sse || b.terminal) {
		b.attempt.Status = "complete"
	}
	return nil
}

func auditString(obj map[string]json.RawMessage, key string) string {
	var value string
	if json.Unmarshal(obj[key], &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func auditUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func (b *auditBody) decodeSignature(index int) {
	if value, ok := b.signatures[index]; ok {
		b.applySignature(value)
		delete(b.signatures, index)
	}
}

func (b *auditBody) applySignature(signature string) {
	model, status := decodeUsageThinkingSignature(signature)
	if status != "found" {
		if b.attempt.SignatureStatus != "malformed" {
			b.attempt.SignatureStatus = status
		}
		return
	}
	b.signatureModels = auditUnique(b.signatureModels, model)
	if b.attempt.SignatureStatus == "missing" {
		b.attempt.SignatureStatus = "found"
	}
	if len(b.signatureModels) > 1 {
		b.attempt.SignatureModel = nil
		b.attempt.Conflict = true
	} else {
		b.attempt.SignatureModel = &model
	}
}

// This extracts CCH's observed protobuf path [2,1,6]; it does not verify a
// signature or attest the identity of a model. Unknown schema is unsupported.
func decodeUsageThinkingSignature(signature string) (string, string) {
	signature = strings.TrimSpace(signature)
	if !auditSignaturePattern.MatchString(signature) {
		return "", "malformed"
	}
	signature = strings.NewReplacer("-", "+", "_", "/").Replace(signature)
	var data []byte
	var err error
	if strings.Contains(signature, "=") {
		data, err = base64.StdEncoding.Strict().DecodeString(signature)
	} else {
		data, err = base64.RawStdEncoding.Strict().DecodeString(signature)
	}
	if err != nil || len(data) == 0 {
		return "", "malformed"
	}
	for _, field := range []uint64{2, 1, 6} {
		var found []byte
		for offset := 0; offset < len(data); {
			tag, n := binary.Uvarint(data[offset:])
			if n <= 0 || tag>>3 == 0 || tag>>3 > 1<<29-1 {
				return "", "malformed"
			}
			offset += n
			wire := tag & 7
			var value []byte
			switch wire {
			case 0:
				_, n = binary.Uvarint(data[offset:])
				if n <= 0 {
					return "", "malformed"
				}
				offset += n
			case 1, 5:
				size := 8
				if wire == 5 {
					size = 4
				}
				if len(data)-offset < size {
					return "", "malformed"
				}
				offset += size
			case 2:
				length, n := binary.Uvarint(data[offset:])
				if n <= 0 {
					return "", "malformed"
				}
				offset += n
				if length > uint64(len(data)-offset) {
					return "", "malformed"
				}
				value = data[offset : offset+int(length)]
				offset += int(length)
			default:
				return "", "unsupported"
			}
			if tag>>3 == field {
				if wire != 2 {
					return "", "unsupported"
				}
				if found != nil {
					return "", "unsupported"
				}
				found = value
			}
		}
		if found == nil {
			return "", "unsupported"
		}
		data = found
	}
	if !utf8.Valid(data) {
		return "", "malformed"
	}
	model := strings.TrimSpace(string(data))
	if model == "" {
		return "", "unsupported"
	}
	return model, "found"
}
