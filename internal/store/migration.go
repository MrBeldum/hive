package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/colonyops/hive/internal/domain/messaging"
	"github.com/colonyops/hive/internal/domain/session"
	"github.com/colonyops/hive/internal/store/db"
	"github.com/colonyops/hive/pkg/randid"
)

// SessionFile is the root JSON structure for sessions.json
type SessionFile struct {
	Sessions []session.Session `json:"sessions"`
}

// TopicFile is the root JSON structure for per-topic message files.
type TopicFile struct {
	Topic    string              `json:"topic"`
	Messages []messaging.Message `json:"messages"`
}

// MigrateFromJSON imports sessions.json and the per-topic message files into
// an empty database. It reads and parses every file before it writes, and
// writes in one transaction, so a failure leaves nothing imported and the next
// start retries.
func MigrateFromJSON(ctx context.Context, database *db.DB, dataDir string) error {
	sessionsPath := filepath.Join(dataDir, "sessions.json")
	if _, err := os.Stat(sessionsPath); os.IsNotExist(err) {
		return nil
	}

	existing, err := database.Queries().ListSessions(ctx)
	if err != nil {
		return fmt.Errorf("failed to check existing sessions: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}

	var sessions SessionFile
	if err := readJSON(sessionsPath, &sessions); err != nil {
		return fmt.Errorf("failed to migrate sessions: %w", err)
	}
	messages, err := readTopicFiles(filepath.Join(dataDir, "messages", "topics"))
	if err != nil {
		return fmt.Errorf("failed to migrate messages: %w", err)
	}

	return database.WithTx(ctx, func(q *db.Queries) error {
		for _, sess := range sessions.Sessions {
			if err := saveSession(ctx, q, sess); err != nil {
				return fmt.Errorf("failed to save session %s: %w", sess.ID, err)
			}
		}
		for _, msg := range messages {
			if msg.CreatedAt.IsZero() {
				msg.CreatedAt = time.Now()
			}
			err := q.PublishMessage(ctx, db.PublishMessageParams{
				ID:        randid.Generate(8),
				Topic:     msg.Topic,
				Payload:   msg.Payload,
				Sender:    toNullString(msg.Sender),
				SessionID: toNullString(msg.SessionID),
				CreatedAt: msg.CreatedAt.UnixNano(),
			})
			if err != nil {
				return fmt.Errorf("failed to migrate message %s: %w", msg.ID, err)
			}
		}
		return nil
	})
}

func readTopicFiles(topicsDir string) ([]messaging.Message, error) {
	entries, err := os.ReadDir(topicsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read topics directory: %w", err)
	}

	var messages []messaging.Message
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var file TopicFile
		if err := readJSON(filepath.Join(topicsDir, entry.Name()), &file); err != nil {
			return nil, fmt.Errorf("topic file %s: %w", entry.Name(), err)
		}
		messages = append(messages, file.Messages...)
	}
	return messages, nil
}

func readJSON(path string, dest any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}
