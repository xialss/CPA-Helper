package app

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"
)

func auditTestLog(request, response string) string {
	return "=== REQUEST BODY ===\n{\"model\":\"client\"}\n\n=== API REQUEST 1 ===\nAuth: provider=test, auth_id=one\nHeaders:\n<none>\n\nBody:\n" + request + "\n\n=== API RESPONSE 1 ===\nTimestamp: 2026-09-20T10:00:00+08:00\nStatus: 200\nHeaders:\nContent-Type: application/json\n\nBody:\n" + response + "\n\n=== RESPONSE ===\nStatus: 200\n\n{\"model\":\"downstream-rewritten\"}\n"
}

func auditTestSignature(model []byte) string {
	for _, field := range []uint64{6, 1, 2} {
		data := binary.AppendUvarint(nil, field<<3|2)
		data = binary.AppendUvarint(data, uint64(len(model)))
		model = append(data, model...)
	}
	return base64.StdEncoding.EncodeToString(model)
}

func TestUsageModelAuditJSONAndSSE(t *testing.T) {
	tests := []struct{ name, body, model, status string }{
		{"openai", `{"model":"gpt-upstream","choices":[]}`, "gpt-upstream", "complete"},
		{"responses", `{"response":{"model":"gpt-responses"}}`, "gpt-responses", "complete"},
		{"claude", `{"message":{"model":"claude-upstream"}}`, "claude-upstream", "complete"},
		{"gemini", `{"response":{"modelVersion":"gemini-version"}}`, "gemini-version", "complete"},
		{"no model", `{"usage":{"total_tokens":1}}`, "", "complete"},
		{"malformed", `{"model":"broken"`, "", "incomplete"},
		{"error", `{"error":{"message":"failed"}}`, "", "incomplete"},
		{"incomplete stream", "data: {\"model\":\"partial\"}\n\n", "partial", "incomplete"},
		{"multiline sse", "event: message_start\ndata: {\"message\":\ndata: {\"model\":\"claude-stream\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", "claude-stream", "complete"},
		{"chat sse", "data: {\"model\":\"chat\"}\n\ndata: [DONE]\n\n", "chat", "complete"},
		{"wrapped gemini sse", "data: {\"response\":{\"modelVersion\":\"old\"}}\n\ndata: {\"response\":{\"modelVersion\":\"new\",\"candidates\":[{\"finishReason\":\"STOP\"}]}}\n\n", "new", "complete"},
		{"invalid event", "data: invalid\n\ndata: [DONE]\n\n", "", "incomplete"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseUsageModelAudit(strings.NewReader(auditTestLog(`{"model":"requested"}`, tt.body)), UsageRecord{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tt.status || result.Association != "single" || len(result.Attempts) != 1 {
				t.Fatalf("unexpected result: %+v", result)
			}
			model := ""
			if result.LogResponseModel != nil {
				model = *result.LogResponseModel
			}
			if model != tt.model || *result.Attempts[0].RequestModel != "requested" {
				t.Fatalf("model mismatch: %+v", result)
			}
		})
	}
}

func TestUsageModelAuditRetryAssociation(t *testing.T) {
	log := strings.Replace(auditTestLog(`{"model":"first"}`, `{"model":"first-result"}`), "=== RESPONSE ===", "=== API REQUEST 2 ===\nBody:\n{\"model\":\"second\"}\n\n=== API RESPONSE 2 ===\nBody:\n{\"model\":\"second-result\"}\n\n=== RESPONSE ===", 1)
	result, err := parseUsageModelAudit(strings.NewReader(log), UsageRecord{})
	if err != nil || result.Association != "ambiguous" || result.LogResponseModel != nil || len(result.Attempts) != 2 {
		t.Fatalf("ambiguous retry: %+v %v", result, err)
	}
	model := "second"
	result, err = parseUsageModelAudit(strings.NewReader(log), UsageRecord{Model: &model})
	if err != nil || result.Association != "ambiguous" || result.LogResponseModel != nil {
		t.Fatalf("queue model coincidence must not associate a retry: %+v %v", result, err)
	}
	log = strings.Replace(log, `{"model":"first"}`, `{}`, 1)
	result, err = parseUsageModelAudit(strings.NewReader(log), UsageRecord{Model: &model})
	if err != nil || result.Association != "ambiguous" {
		t.Fatalf("missing evidence: %+v %v", result, err)
	}
}

func TestUsageModelAuditSignatureFragmentsAndConflicts(t *testing.T) {
	sig := auditTestSignature([]byte("claude-signature"))
	body := fmt.Sprintf("data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-declared\"}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":%q}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":%q}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_stop\"}\n\n", sig[:9], sig[9:])
	result, err := parseUsageModelAudit(strings.NewReader(auditTestLog(`{}`, body)), UsageRecord{})
	if err != nil || result.Status != "complete" || result.SignatureModel == nil || *result.SignatureModel != "claude-signature" || *result.LogResponseModel != "claude-declared" || result.Attempts[0].SignatureStatus != "found" {
		t.Fatalf("fragment signature: %+v %v", result, err)
	}
	body = "data: {\"type\":\"response.created\",\"response\":{\"model\":\"first\"}}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"last\"}}\n\n"
	result, err = parseUsageModelAudit(strings.NewReader(auditTestLog(`{}`, body)), UsageRecord{})
	if err != nil || !result.Attempts[0].Conflict || len(result.Attempts[0].Models) != 2 || *result.LogResponseModel != "last" {
		t.Fatalf("conflict: %+v %v", result, err)
	}
	body = fmt.Sprintf(`{"model":"claude","content":[{"type":"thinking","signature":%q},{"type":"thinking","signature":%q}]}`, sig, auditTestSignature([]byte("other")))
	result, err = parseUsageModelAudit(strings.NewReader(auditTestLog(`{}`, body)), UsageRecord{})
	if err != nil || !result.Attempts[0].Conflict || result.SignatureModel != nil {
		t.Fatalf("signature conflict: %+v %v", result, err)
	}
}

func TestUsageThinkingSignatureValidation(t *testing.T) {
	valid := auditTestSignature([]byte("claude-opus"))
	tests := []struct{ name, input, status string }{
		{"valid", valid, "found"},
		{"unpadded", strings.TrimRight(valid, "="), "found"},
		{"bad alphabet", "!!!", "malformed"},
		{"interior newline", valid[:4] + "\n" + valid[4:], "malformed"},
		{"bad bounds", base64.StdEncoding.EncodeToString([]byte{0x12, 0x7f}), "malformed"},
		{"missing path", base64.StdEncoding.EncodeToString([]byte{0x0a, 0}), "unsupported"},
		{"wrong wire", base64.StdEncoding.EncodeToString([]byte{0x10, 1}), "unsupported"},
		{"invalid utf8", auditTestSignature([]byte{0xff}), "malformed"},
		{"varint overflow", base64.StdEncoding.EncodeToString([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0}), "malformed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, status := decodeUsageThinkingSignature(tt.input)
			if status != tt.status || (status == "found" && model != "claude-opus") {
				t.Fatalf("%q %s", model, status)
			}
		})
	}
}

func TestUsageThinkingSignatureCCHFixture(t *testing.T) {
	// Public CCH thinking-signature-model.test.ts sample; no request content.
	const signature = "EqsDCmMIDhgCKkCrnWTbZMEF0r5uok/aYSgRICLVbOUhwZJhOfCxigdcVkbcTEAsm/33aCjav1PGuQPRqeZ3RAn4VTYmOZUnQHZOMg9jbGF1ZGUtb3B1cy00LTc4AEIIdGhpbmtpbmcSDH4eDs/asAFTgfDkVRoMkNlw68oKoopYj9TnIjCDgiWGjzG1woio60hvwVQRMb0ASwJyYMjZQWqCXTubppc6YpvGLIrhjtJsMfSCC/Qq9QGlGbLsHHRN4ulPmTANpxm1H83mRvzzpkYd96OGTFq/RIjHIA+CVdkiQu57eR0tj/egvnKiD0F0aYp//vOQR7dweMU75+LpNAJKuL6hIR0AwlU92NOp5EaSvO1JBIkzmcgpZyANjMKHwmTziKIqJ3nP8JRaaF/9Zi/xWKymHki7ThrD6hRbY6Kc6UXvFIo44ZmOKQOBlhtau+8ze87cKZVGWa1QyqJfFZgB0dPnD9jEjTLh6hPz9XHKPQsMEz9OZ+DYHs6oJPCms9QxssaqcTpQK4aRh04LMIU+UvkZIPCI7KEzQOXHfRLNZ2uV/EF3n0hbGzVPzRgB"
	model, status := decodeUsageThinkingSignature(signature)
	if status != "found" || model != "claude-opus-4-7" {
		t.Fatalf("%q %s", model, status)
	}
}

func TestUsageModelAuditUnsupportedAndLimits(t *testing.T) {
	for _, log := range []string{"=== RESPONSE ===\n{\"model\":\"wrong\"}", "=== API WEBSOCKET TIMELINE ===\nBody:\n{\"model\":\"wrong\"}", "=== WEBSOCKET TIMELINE ===\n" + auditTestLog(`{}`, `{"model":"http"}`)} {
		result, err := parseUsageModelAudit(strings.NewReader(log), UsageRecord{})
		if err != nil || result.Status != "unsupported" || result.LogResponseModel != nil {
			t.Fatalf("unsupported: %+v %v", result, err)
		}
	}
	_, err := parseUsageModelAudit(strings.NewReader(auditTestLog(`{}`, strings.Repeat("x", auditValueLimit+1))), UsageRecord{})
	if err == nil {
		t.Fatal("oversized line must fail explicitly")
	}
	_, err = parseUsageModelAudit(io.MultiReader(strings.NewReader(auditTestLog(`{}`, `{"model":"ok"}`)), auditBrokenReader{}), UsageRecord{})
	if err == nil {
		t.Fatal("reader failure must not return success")
	}
}

func TestUsageModelAuditIgnoresDownstreamSectionImitation(t *testing.T) {
	log := "=== RESPONSE ===\nStatus: 502\n\n=== API RESPONSE 1 ===\nBody:\n{\"model\":\"fabricated\"}\n"
	result, err := parseUsageModelAudit(strings.NewReader(log), UsageRecord{})
	if err != nil || result.Status != "unsupported" || len(result.Attempts) != 0 || result.LogResponseModel != nil {
		t.Fatalf("downstream text became upstream evidence: %+v %v", result, err)
	}
	log = auditTestLog(`{}`, `{"model":"upstream"}`) + "\n=== API RESPONSE 2 ===\nBody:\n{\"model\":\"fabricated\"}\n"
	result, err = parseUsageModelAudit(strings.NewReader(log), UsageRecord{})
	if err != nil || result.Association != "single" || len(result.Attempts) != 1 || result.LogResponseModel == nil || *result.LogResponseModel != "upstream" {
		t.Fatalf("downstream text changed upstream evidence: %+v %v", result, err)
	}
}

type auditBrokenReader struct{}

func (auditBrokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
