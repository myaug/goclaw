package mcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/nextlevelbuilder/goclaw/internal/providers"
	"github.com/nextlevelbuilder/goclaw/internal/store"
	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

// Embedded documents
//
// An MCP server whose settings contain "allow_embedded_documents": true may
// return PDFs in a tool result as EmbeddedResource content with a blob:
//
//	{"type": "resource",
//	 "resource": {"uri": "...", "mimeType": "application/pdf", "blob": "<base64>"}}
//
// Each accepted PDF is stored in the run's workspace uploads directory as a
// document media item and replaced, in the text the LLM sees, by a line that
// carries its media_id so the agent can call read_document(media_id=...).
// With the setting off (the default) such content is reported exactly as
// before: "[non-text content: ...]".

// maxEmbeddedDocumentsPerCall caps how many documents one tool call may store.
const maxEmbeddedDocumentsPerCall = 5

// embeddedDocumentMime is the only MIME type accepted for embedded documents.
const embeddedDocumentMime = "application/pdf"

// WithEmbeddedDocuments enables storing PDF EmbeddedResource blobs returned by
// this tool. Returns t for chaining.
func (t *BridgeTool) WithEmbeddedDocuments(allow bool) *BridgeTool {
	t.allowEmbeddedDocuments = allow
	return t
}

// AllowsEmbeddedDocuments reports whether embedded documents are enabled.
func (t *BridgeTool) AllowsEmbeddedDocuments() bool { return t.allowEmbeddedDocuments }

// convertResultContent renders a CallToolResult for the LLM. It matches
// extractTextContent except that, when embedded documents are allowed, PDF
// blobs are stored and replaced by an attachment line. Returns the text and
// the refs of the stored documents.
func (t *BridgeTool) convertResultContent(ctx context.Context, result *mcpgo.CallToolResult) (string, []providers.MediaRef) {
	if !t.allowEmbeddedDocuments {
		return extractTextContent(result), nil
	}
	if result == nil || len(result.Content) == 0 {
		return "", nil
	}

	var parts []string
	var refs []providers.MediaRef
	for _, c := range result.Content {
		switch v := c.(type) {
		case mcpgo.TextContent:
			parts = append(parts, v.Text)
		case *mcpgo.TextContent:
			parts = append(parts, v.Text)
		case mcpgo.EmbeddedResource:
			line, ref := t.storeEmbeddedDocument(ctx, v.Resource, len(refs))
			parts = append(parts, line)
			if ref != nil {
				refs = append(refs, *ref)
			}
		case *mcpgo.EmbeddedResource:
			line, ref := t.storeEmbeddedDocument(ctx, v.Resource, len(refs))
			parts = append(parts, line)
			if ref != nil {
				refs = append(refs, *ref)
			}
		default:
			parts = append(parts, fmt.Sprintf("[non-text content: %T]", c))
		}
	}
	return strings.Join(parts, "\n"), refs
}

// storeEmbeddedDocument validates and persists one embedded resource.
// stored is the number of documents already stored for this call. Returns the
// line to show the LLM and the stored ref (nil when rejected).
func (t *BridgeTool) storeEmbeddedDocument(ctx context.Context, res mcpgo.ResourceContents, stored int) (string, *providers.MediaRef) {
	var blob *mcpgo.BlobResourceContents
	switch b := res.(type) {
	case mcpgo.BlobResourceContents:
		blob = &b
	case *mcpgo.BlobResourceContents:
		blob = b
	case mcpgo.TextResourceContents:
		// Text resources are plain text — pass them through like TextContent.
		return b.Text, nil
	case *mcpgo.TextResourceContents:
		return b.Text, nil
	}
	if blob == nil {
		return fmt.Sprintf("[non-text content: %T]", res), nil
	}

	reject := func(reason string) (string, *providers.MediaRef) {
		slog.Warn("mcp.tool.embedded_document.rejected",
			"server", t.serverName, "tool", t.registeredName,
			"agent_id", store.AgentIDFromContext(ctx),
			"reason", reason)
		return "[embedded document rejected: " + reason + "]", nil
	}

	mime := strings.ToLower(strings.TrimSpace(blob.MIMEType))
	if i := strings.IndexByte(mime, ';'); i >= 0 {
		mime = strings.TrimSpace(mime[:i])
	}
	if mime != embeddedDocumentMime {
		return reject(fmt.Sprintf("unsupported mimeType %q (only %s is accepted)", blob.MIMEType, embeddedDocumentMime))
	}
	if stored >= maxEmbeddedDocumentsPerCall {
		return reject(fmt.Sprintf("limit of %d documents per tool call reached", maxEmbeddedDocumentsPerCall))
	}
	// Reject oversize payloads before decoding: base64 expands 3 bytes to 4.
	if base64.StdEncoding.DecodedLen(len(blob.Blob)) > tools.DocumentMaxBytes+3 {
		return reject(fmt.Sprintf("document too large (max %d bytes)", tools.DocumentMaxBytes))
	}
	data, err := decodeBase64Blob(blob.Blob)
	if err != nil {
		return reject("blob is not valid base64")
	}
	if len(data) > tools.DocumentMaxBytes {
		return reject(fmt.Sprintf("document too large: %d bytes (max %d)", len(data), tools.DocumentMaxBytes))
	}
	if !tools.IsPDF(data) {
		return reject("content is not a PDF (missing %PDF- signature)")
	}

	doc, err := tools.PersistToolDocument(ctx, data, embeddedDocumentName(blob), embeddedDocumentMime)
	if err != nil {
		return reject(err.Error())
	}
	slog.Info("mcp.tool.embedded_document.stored",
		"server", t.serverName, "tool", t.registeredName,
		"agent_id", store.AgentIDFromContext(ctx),
		"media_id", doc.Ref.ID, "size_bytes", doc.Size)

	line := fmt.Sprintf("[document attached: media_id=%s, name=%s, path=%s, size=%d bytes, mime=%s. Call read_document with media_id=%q to read it.]",
		doc.Ref.ID, doc.Name, doc.LogicalPath, doc.Size, embeddedDocumentMime, doc.Ref.ID)
	return line, &doc.Ref
}

// decodeBase64Blob decodes standard base64, tolerating missing padding and
// embedded line breaks that some servers emit.
func decodeBase64Blob(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', ' ', '\t':
			return -1
		}
		return r
	}, s)
	if data, err := base64.StdEncoding.DecodeString(s); err == nil {
		return data, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}

// embeddedDocumentName picks a filename hint for an embedded blob: an explicit
// _meta "filename"/"name" string, else the last path segment of the URI.
// The result is untrusted; PersistToolDocument sanitizes it.
func embeddedDocumentName(blob *mcpgo.BlobResourceContents) string {
	for _, key := range []string{"filename", "name"} {
		if v, ok := blob.Meta[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	if blob.URI == "" {
		return ""
	}
	p := blob.URI
	if u, err := url.Parse(blob.URI); err == nil {
		switch {
		case u.Path != "":
			p = u.Path
		case u.Opaque != "":
			p = u.Opaque
		}
	}
	base := path.Base(strings.TrimRight(p, "/"))
	if base == "." || base == "/" {
		return ""
	}
	return base
}
