package command

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"vibekit/internal/api"
)

// documentExts are file extensions sent as ACP document content blocks.
var documentExts = map[string]string{
	".pdf":  "application/pdf",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".doc":  "application/msword",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xls":  "application/vnd.ms-excel",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".ppt":  "application/vnd.ms-powerpoint",
	".rtf":  "application/rtf",
	".odt":  "application/vnd.oasis.opendocument.text",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".odp":  "application/vnd.oasis.opendocument.presentation",
	".csv":  "text/csv",
}

// MaxDocumentBytes caps the size of a document attachment (10 MiB).
const MaxDocumentBytes = 10 * 1024 * 1024

// BuildPromptBlocks constructs the ACP prompt content array.
func BuildPromptBlocks(text string, attachments []api.Attachment, resolve func(string) (string, error)) []map[string]any {
	blocks := []map[string]any{{keyType: contentTypeText, keyText: text}}
	for _, att := range attachments {
		ext := strings.ToLower(filepath.Ext(att.Path))
		mime, isDoc := documentExts[ext]
		displayName := filepath.Base(att.Path)
		if isDoc {
			abs, err := resolve(att.Path)
			if err != nil {
				slog.Warn("attachment: path escapes workspace",
					"path", displayName, keyError, err)
				blocks = append(blocks, map[string]any{
					keyType: contentTypeText,
					keyText: "Attached file (invalid path): " + displayName,
				})
				continue
			}
			info, err := os.Stat(abs)
			if err != nil {
				slog.Warn("attachment: stat failed", "path", displayName, keyError, err)
				blocks = append(blocks, map[string]any{
					keyType: contentTypeText,
					keyText: "Attached file (unreadable): " + displayName,
				})
				continue
			}
			if info.Size() > MaxDocumentBytes {
				slog.Warn("attachment: too large",
					"path", displayName, "size", info.Size())
				blocks = append(blocks, map[string]any{
					keyType: contentTypeText,
					keyText: "Attached file (too large for inline): " + displayName,
				})
				continue
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				slog.Warn("attachment: read failed",
					"path", displayName, keyError, err)
				blocks = append(blocks, map[string]any{
					keyType: contentTypeText,
					keyText: "Attached file (unreadable): " + displayName,
				})
				continue
			}
			blocks = append(blocks, map[string]any{
				keyType:    "document",
				keyName:    att.Name,
				"content":  data,
				"mimeType": mime,
			})
		} else {
			if _, err := resolve(att.Path); err != nil {
				slog.Warn("attachment: path escapes workspace",
					"path", displayName, keyError, err)
				blocks = append(blocks, map[string]any{
					keyType: contentTypeText,
					keyText: "Attached file (invalid path): " + displayName,
				})
				continue
			}
			blocks = append(blocks, map[string]any{
				keyType: contentTypeText,
				keyText: "Attached file: " + att.Path,
			})
		}
	}
	return blocks
}
